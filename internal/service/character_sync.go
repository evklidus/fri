package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	characterProviderName    = "news-keyword-scan"
	characterPerSyncCap      = 15.0
	characterScanLookbackDay = 30
)

// characterTrigger encodes a single keyword pattern that can shift a score
// component. Multiple language variants for the same concept get the same
// delta — we count one trigger per (article × concept), not per word.
//
// `target` routes the delta to a specific FRI component. Empty == "character"
// for backward compat with the original Phase 3 triggers.
//
// `autoApply` (Phase 5 — 2026-05-13) controls whether the event skips fan
// voting. Set to true for definitive, non-controversial cases (doping,
// official awards) where community override would be inappropriate.
// Defaults to false: most triggers go through the 24h voting window.
type characterTrigger struct {
	concept   string  // grouping key for de-dup within an article
	delta     float64 // points added/subtracted from the target component
	target    string  // "character" (default) or "performance"
	autoApply bool    // skip fan voting; finalize at insert time
	words     []string
}

// characterTriggers covers two flavours of reputation event:
//
//   - Character-targeting: behaviour/integrity (doping, racism, fair play).
//     The original Phase 3 list. delta applies to fri_scores.character.
//   - Performance-targeting: sporting outcomes (hat-trick, drought, awards).
//     Added in Phase 4.1. delta applies to fri_scores.performance.
//
// Triggers are conservative — false positives permanently shift a player's
// score without moderation. We only ship phrases with high precision in
// football headlines.
var characterTriggers = []characterTrigger{
	// ── Character: severe negatives — DEFINITIVE, NO FAN OVERRIDE ──────
	{concept: "doping", delta: -8, autoApply: true, words: []string{"doping", "failed drug test", "banned for doping", "допинг", "провалил тест на допинг"}},
	{concept: "racism", delta: -6, autoApply: true, words: []string{"racism", "racist abuse", "racist remark", "расизм", "расистск"}},
	{concept: "criminal", delta: -7, autoApply: true, words: []string{" arrested", "criminal charges", "criminal probe", "арестован", "уголовн"}},
	// Scandal severity varies — community decides.
	{concept: "scandal", delta: -3, words: []string{"scandal", "controversy erupt", "скандал"}},

	// ── Character: moderate negatives — fans decide if it's fair ───────
	{concept: "red_card", delta: -1.5, words: []string{"red card", "sent off", "красная карточка", "удалён с поля"}},
	{concept: "ban", delta: -2.5, words: []string{"two-match ban", "three-match ban", "match ban", "дисквалифицирован"}},
	{concept: "fine", delta: -0.5, words: []string{"fined", "штраф"}},

	// ── Character: positives — context matters, fans vote ──────────────
	{concept: "fair_play", delta: 1.5, words: []string{"fair play award", "награда за fair play"}},
	{concept: "charity", delta: 0.8, words: []string{"charity donation", "philanthrop", "благотворительн"}},
	{concept: "captain", delta: 0.5, words: []string{"named captain", "captaincy", "назначен капитаном"}},

	// ── Performance: positives — context (stakes, opponent) matters ────
	{concept: "hat_trick", delta: 2.0, target: "performance", words: []string{"hat-trick", "hat trick", "хет-трик"}},
	{concept: "brace", delta: 1.0, target: "performance", words: []string{"brace against", "scored a brace", "scored twice", "two goals against", "дубль"}},
	{concept: "player_of_month", delta: 3.0, target: "performance", words: []string{"player of the month", "игрок месяца", "лучший игрок месяца"}},
	// Official year awards — definitive, no community override needed.
	{concept: "player_of_year", delta: 5.0, target: "performance", autoApply: true, words: []string{"player of the year", "игрок года", "лучший игрок года"}},
	{concept: "ballon_dor", delta: 8.0, target: "performance", autoApply: true, words: []string{"ballon d'or", "ballon d or", "золотой мяч"}},
	{concept: "goal_of_season", delta: 2.5, target: "performance", words: []string{"goal of the season", "goal of the year", "гол сезона", "гол года"}},
	{concept: "trophy_won", delta: 4.0, target: "performance", words: []string{"won the champions league", "champions league trophy", "league title clinched", "won the league", "выиграл лигу чемпионов", "выиграл чемпионат"}},

	// ── Performance: negatives — press framing varies, fans vote ───────
	{concept: "goal_drought_5", delta: -1.5, target: "performance", words: []string{"five games without scoring", "5 games without scoring", "5-game scoring drought", "scoring drought", "fifth game without", "5 матчей без гола", "пять матчей без гола"}},
	{concept: "goal_drought_10", delta: -3.0, target: "performance", words: []string{"ten games without scoring", "10 games without scoring", "10-game drought", "tenth game without", "10 матчей без гола", "десять матчей без гола"}},
	// Injury is objective (player can't perform). Auto-apply.
	{concept: "injury_serious", delta: -1.5, target: "performance", autoApply: true, words: []string{"long-term injury", "season-ending injury", "out for the season", "out for several months", "тяжёлая травма", "выбыл до конца сезона"}},
	{concept: "penalty_miss_key", delta: -1.0, target: "performance", words: []string{"missed crucial penalty", "missed a penalty in the", "penalty miss costs", "промазал решающий пенальти"}},
}

// negators block trigger detection when one of these phrases appears in the
// article title — it usually means the player is the *target* of the event,
// not the perpetrator. ("Vinicius racism victim" must not lower his
// Character score.)
var characterNegators = []string{
	"victim of",
	"target of",
	"victim",
	"speaks out against",
	"spoke out against",
	"condemns",
	"condemned",
	"defends ",
	"supports ",
	"reacts to",
	"жертва",
	"осудил",
	"высказался против",
}

// SyncCharacter scans recent news_items for reputation triggers and applies
// per-player aggregated deltas (capped) to fri_scores.character. Each fired
// trigger is persisted in character_events for an audit trail / future
// moderation UI (phase 4).
func (s *Service) SyncCharacter(ctx context.Context) (*domain.ComponentSyncResult, error) {
	if !s.characterSyncMu.TryLock() {
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "character",
			Provider:   characterProviderName,
			Status:     "skipped",
			Message:    "character sync already in progress",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}
	defer s.characterSyncMu.Unlock()

	startedAt := time.Now().UTC()
	updateID, err := s.repo.StartComponentUpdate(ctx, "character", characterProviderName)
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "character",
		Provider:  characterProviderName,
		Status:    "running",
		StartedAt: startedAt,
	}

	finish := func(status, message string, records int, deltas []domain.PlayerSyncDelta, err error) (*domain.ComponentSyncResult, error) {
		result.Status = status
		result.Message = message
		result.RecordsSeen = records
		result.Players = deltas
		result.FinishedAt = time.Now().UTC()
		if finishErr := s.repo.FinishComponentUpdate(ctx, updateID, status, message, records); finishErr != nil && err == nil {
			err = finishErr
		}
		return result, err
	}

	news, err := s.repo.ListNews(ctx, nil)
	if err != nil {
		return finish("failed", err.Error(), 0, nil, err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -characterScanLookbackDay)
	candidates := scanNewsForCharacterTriggers(news, cutoff)

	deltas, err := s.repo.ApplyCharacterSync(ctx, candidates, characterPerSyncCap)
	if err != nil {
		return finish("failed", err.Error(), len(candidates), nil, err)
	}

	return finish(
		"completed",
		fmt.Sprintf("character sync scanned %d articles, fired %d events, moved %d players", len(news), len(candidates), len(deltas)),
		len(candidates),
		deltas,
		nil,
	)
}

// scanNewsForCharacterTriggers walks each news item, checking the article
// against the trigger table. Articles older than `cutoff` or with a negator
// in the title are skipped. Returns one candidate per (player, article,
// trigger-concept).
func scanNewsForCharacterTriggers(news []domain.NewsItem, cutoff time.Time) []domain.CharacterEventCandidate {
	out := make([]domain.CharacterEventCandidate, 0)
	for _, item := range news {
		if item.PlayerID == nil || *item.PlayerID == 0 {
			continue
		}
		if !item.PublishedAt.IsZero() && item.PublishedAt.Before(cutoff) {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(item.TitleEN + " " + item.TitleRU + " " + item.SummaryEN + " " + item.SummaryRU))
		titleLower := strings.ToLower(item.TitleEN + " " + item.TitleRU)
		if hasNegator(titleLower) {
			continue
		}
		seenConcepts := make(map[string]struct{})
		for _, trig := range characterTriggers {
			if _, fired := seenConcepts[trig.concept]; fired {
				continue
			}
			if !triggerMatches(trig, text) {
				continue
			}
			seenConcepts[trig.concept] = struct{}{}
			out = append(out, domain.CharacterEventCandidate{
				PlayerID:        *item.PlayerID,
				NewsItemID:      item.ID,
				TriggerWord:     trig.concept,
				Delta:           trig.delta,
				TargetComponent: trig.target, // "" defaults to "character" in repo
				AutoApply:       trig.autoApply,
			})
		}
	}
	return out
}

func triggerMatches(trig characterTrigger, lowercaseText string) bool {
	for _, w := range trig.words {
		if strings.Contains(lowercaseText, w) {
			return true
		}
	}
	return false
}

func hasNegator(lowercaseTitle string) bool {
	for _, n := range characterNegators {
		if strings.Contains(lowercaseTitle, n) {
			return true
		}
	}
	return false
}
