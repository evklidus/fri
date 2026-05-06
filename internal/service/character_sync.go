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

// characterTrigger encodes a single keyword pattern that can shift the
// character score. Multiple language variants for the same concept get the
// same delta — we count one trigger per (article × concept), not per word.
type characterTrigger struct {
	concept string  // grouping key for de-dup within an article
	delta   float64 // points added/subtracted from Character
	words   []string
}

// characterTriggers is intentionally short and conservative. False positives
// permanently corrupt a player's reputation in this minimal MVP pipeline (no
// moderation), so we only ship triggers with high precision in football
// headlines.
var characterTriggers = []characterTrigger{
	// Negative — severe
	{concept: "doping", delta: -8, words: []string{"doping", "failed drug test", "banned for doping", "допинг", "провалил тест на допинг"}},
	{concept: "racism", delta: -6, words: []string{"racism", "racist abuse", "racist remark", "расизм", "расистск"}},
	{concept: "criminal", delta: -7, words: []string{" arrested", "criminal charges", "criminal probe", "арестован", "уголовн"}},
	{concept: "scandal", delta: -3, words: []string{"scandal", "controversy erupt", "скандал"}},

	// Negative — moderate
	{concept: "red_card", delta: -1.5, words: []string{"red card", "sent off", "красная карточка", "удалён с поля"}},
	{concept: "ban", delta: -2.5, words: []string{"two-match ban", "three-match ban", "match ban", "дисквалифицирован"}},
	{concept: "fine", delta: -0.5, words: []string{"fined", "штраф"}},

	// Positive
	{concept: "fair_play", delta: 1.5, words: []string{"fair play award", "награда за fair play"}},
	{concept: "charity", delta: 0.8, words: []string{"charity donation", "philanthrop", "благотворительн"}},
	{concept: "captain", delta: 0.5, words: []string{"named captain", "captaincy", "назначен капитаном"}},
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
				PlayerID:    *item.PlayerID,
				NewsItemID:  item.ID,
				TriggerWord: trig.concept,
				Delta:       trig.delta,
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
