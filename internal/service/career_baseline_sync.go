package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	careerBaselineProviderName = "career-baseline"
	// 5 seasons is the sweet spot for API-Football data quality. Going
	// further back than that drops out lower-tier league stats and we
	// start hitting "no data" rows that dilute the average.
	careerBaselineDefaultLookback = 5
	// Skip players whose baseline was computed less than this ago. SyncAll
	// can be pressed many times in a demo — without this, every press would
	// hit API-Football for 22×5=110 extra calls. The scheduler can override
	// by skipping the freshness check.
	careerBaselineFreshness = 7 * 24 * time.Hour
)

// careerBaselineProvider knows how to assemble a multi-season aggregate for
// one player. The api-football implementation lives in
// api_football_performance.go (same file as the rest of the API client) so it
// can reuse the existing external-id lookup and the season-cache logic.
type careerBaselineProvider interface {
	Name() string
	// FetchCareerBaseline pulls the last `lookback` completed seasons for a
	// player (the current season is excluded — that's already in the live
	// performance feed) and returns the aggregated snapshot. A return of
	// (zero-value, nil) means the provider couldn't find any data; the
	// caller should treat that as "no baseline available" rather than an
	// error.
	FetchCareerBaseline(ctx context.Context, player domain.PlayerSyncTarget, lookback int) (domain.PlayerCareerBaseline, error)
}

// SyncCareerBaseline refreshes the career baseline row for every tracked
// player. Idempotent: re-running the same day overwrites with the same
// numbers. Designed to run on a slow cadence (monthly) — career data drifts
// at most a few percent per season.
func (s *Service) SyncCareerBaseline(ctx context.Context) (*domain.ComponentSyncResult, error) {
	if s.careerBaselineProvider == nil {
		// No provider wired in (e.g. running without an API-Football key).
		// Don't fail the SyncAll chain — just skip silently.
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "career-baseline",
			Provider:   careerBaselineProviderName,
			Status:     "skipped",
			Message:    "career baseline provider not configured",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}

	if !s.careerBaselineSyncMu.TryLock() {
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "career-baseline",
			Provider:   s.careerBaselineProvider.Name(),
			Status:     "skipped",
			Message:    "career baseline sync already in progress",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}
	defer s.careerBaselineSyncMu.Unlock()

	startedAt := time.Now().UTC()
	providerName := s.careerBaselineProvider.Name()
	updateID, err := s.repo.StartComponentUpdate(ctx, "career-baseline", providerName)
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "career-baseline",
		Provider:  providerName,
		Status:    "running",
		StartedAt: startedAt,
	}

	finish := func(status, message string, records int, err error) (*domain.ComponentSyncResult, error) {
		result.Status = status
		result.Message = message
		result.RecordsSeen = records
		result.FinishedAt = time.Now().UTC()
		if finishErr := s.repo.FinishComponentUpdate(ctx, updateID, status, message, records); finishErr != nil && err == nil {
			err = finishErr
		}
		return result, err
	}

	targets, err := s.repo.ListSyncTargets(ctx)
	if err != nil {
		return finish("failed", err.Error(), 0, err)
	}

	cutoff := time.Now().UTC().Add(-careerBaselineFreshness)
	updated, skippedFresh := 0, 0
	for _, player := range targets {
		// Skip players whose baseline is still fresh — saves API budget on
		// repeated SyncAll presses during demos. The scheduler can override
		// via a longer interval anyway.
		if existing, err := s.repo.GetCareerBaseline(ctx, player.ID); err == nil && existing != nil {
			if existing.ComputedAt.After(cutoff) {
				skippedFresh++
				continue
			}
		}

		baseline, fetchErr := s.careerBaselineProvider.FetchCareerBaseline(ctx, player, careerBaselineDefaultLookback)
		if fetchErr != nil {
			log.Printf("career-baseline: fetch failed for %q: %v", player.Name, fetchErr)
			continue
		}
		if baseline.SeasonsPlayed == 0 {
			// API returned nothing — leave any existing row in place rather
			// than overwriting with zeros. New players land here on first run.
			continue
		}
		baseline.PlayerID = player.ID
		baseline.SeasonsLookback = careerBaselineDefaultLookback
		if upsertErr := s.repo.UpsertCareerBaseline(ctx, baseline); upsertErr != nil {
			log.Printf("career-baseline: upsert failed for %q: %v", player.Name, upsertErr)
			continue
		}
		updated++
	}

	return finish(
		"completed",
		fmt.Sprintf("career baseline: %d refreshed, %d fresh, %d total players", updated, skippedFresh, len(targets)),
		updated,
		nil,
	)
}

// blendBaselineIntoPerformance mixes the persistent career baseline (25%)
// into the snapshot's current-season normalized score (75%). When no
// baseline exists yet — first run, or a player we just added — the snapshot
// passes through unchanged.
//
// Weight dropped 0.40 → 0.25 on 2026-05-15: the 40% blend was pulling top
// in-form players down toward their career average, capping FRI scores in
// the high-60s when partner/investor expectations were 85+. Career remains
// a meaningful drag against one-hit-wonder seasons but no longer dominates.
//
// We intentionally only blend NormalizedScore (the final 0–100 score the
// repository writes to fri_scores.performance). The raw stat fields are
// left alone so dashboards that show "this season: 8 goals, last 5 form X"
// continue to reflect the live season truth, not a career average.
func (s *Service) blendBaselineIntoPerformance(ctx context.Context, snapshot domain.PerformanceSnapshot) domain.PerformanceSnapshot {
	if s.repo == nil {
		return snapshot
	}
	baseline, err := s.repo.GetCareerBaseline(ctx, snapshot.PlayerID)
	if err != nil {
		log.Printf("career-baseline: lookup failed for player %d: %v", snapshot.PlayerID, err)
		return snapshot
	}
	if baseline == nil || baseline.BaselineScore <= 0 {
		return snapshot
	}
	const baselineWeight = 0.25
	blended := (1-baselineWeight)*snapshot.NormalizedScore + baselineWeight*baseline.BaselineScore
	snapshot.NormalizedScore = clampScore(round1(blended))
	return snapshot
}

// baselineWeights groups the per-position weights for the five baseline
// signals. Sum must equal 1.0. The weights are heavier on rating + minutes
// for positions where goals/assists are not the main job (GK, DEF), so a
// rock-solid centre-back or shotstopper isn't penalised against a striker.
type baselineWeights struct {
	rating   float64
	goals    float64
	assists  float64
	minutes  float64
	trophies float64
}

// weightsFor returns position-tuned weights. A GK with rating 7.5 and lots of
// minutes should land around the same baseline as an FWD with rating 7.5 and
// matching goals — both are top-of-position performers.
//
//	FWD: emphasise output (goals + assists)
//	MID: balance creativity (assists) and rating
//	DEF: defenders rarely score; weight rating + minutes hardest
//	GK:  no goal/assist channels at all; rating + minutes carry the score
//
// All four sets sum to 1.0 before trophy re-normalization.
func weightsFor(position string) baselineWeights {
	// positionGroup returns "ATT" for forwards, "MID", "DEF", or "GK".
	switch positionGroup(position) {
	case "ATT":
		return baselineWeights{rating: 0.45, goals: 0.20, assists: 0.15, minutes: 0.10, trophies: 0.10}
	case "MID":
		return baselineWeights{rating: 0.50, goals: 0.10, assists: 0.20, minutes: 0.10, trophies: 0.10}
	case "DEF":
		return baselineWeights{rating: 0.55, goals: 0.05, assists: 0.05, minutes: 0.25, trophies: 0.10}
	case "GK":
		// No goals/assists channels — a 0 there is the norm, not a penalty.
		// Heavy weight on rating (which API-Football derives from saves +
		// distribution + clean sheets) and minutes (consistent #1 keeper).
		return baselineWeights{rating: 0.60, goals: 0, assists: 0, minutes: 0.30, trophies: 0.10}
	default:
		// Unknown position — fall back to the FWD weighting so we don't
		// accidentally zero-out an unmapped player.
		return baselineWeights{rating: 0.45, goals: 0.20, assists: 0.15, minutes: 0.10, trophies: 0.10}
	}
}

// computeBaselineScore turns raw career aggregates into a 0–100 score, scoped
// by player position. The position-aware weighting is critical: a GK with 0
// goals over 5 seasons should still score in the 60s if their rating and
// minutes are elite — without per-position weights, the formula punished
// them down to ~28.
//
// When trophy data is missing (trophiesCount = 0 and the /trophies endpoint
// failed or returned nothing), the trophy weight is reallocated proportionally
// to the other signals — we don't want to under-rank everyone just because
// the API call timed out.
func computeBaselineScore(b domain.PlayerCareerBaseline, position string, trophiesAvailable bool) float64 {
	if b.SeasonsPlayed == 0 || b.CareerMinutes == 0 {
		return 0
	}

	w := weightsFor(position)
	minutes := float64(b.CareerMinutes)
	goalsPer90 := per90(float64(b.CareerGoals), minutes)
	assistsPer90 := per90(float64(b.CareerAssists), minutes)

	gaMax := positionGAMax(position)
	if gaMax <= 0 {
		gaMax = 0.6
	}

	// Range tightened 8.5 → 7.8 on 2026-05-15 to match the new Performance
	// scale. Career averages 7.5+ are rare and unambiguously elite.
	ratingScore := normalizeLinear(b.CareerAvgRating, 6.0, 7.8)
	goalsScore := normalizeLinear(goalsPer90, 0, gaMax)
	assistsScore := normalizeLinear(assistsPer90, 0, gaMax)
	minutesScore := normalizeLog(minutes, 5_000, 50_000)

	score := ratingScore*w.rating +
		goalsScore*w.goals +
		assistsScore*w.assists +
		minutesScore*w.minutes

	if trophiesAvailable {
		trophiesScore := normalizeLinear(float64(b.CareerTrophiesCount), 0, 10)
		score += trophiesScore * w.trophies
	} else {
		// Reallocate the trophies weight proportionally to the remaining
		// signals. Without re-norm the score would be artificially capped at
		// (1 - w.trophies) of its real max for every player.
		denominator := 1.0 - w.trophies
		if denominator > 0 {
			score = score / denominator
		}
	}

	return clampScore(round1(score))
}
