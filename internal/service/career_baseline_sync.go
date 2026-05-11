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

// blendBaselineIntoPerformance mixes the persistent career baseline (40%)
// into the snapshot's current-season normalized score (60%). When no
// baseline exists yet — first run, or a player we just added — the snapshot
// passes through unchanged.
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
	const baselineWeight = 0.40
	blended := (1-baselineWeight)*snapshot.NormalizedScore + baselineWeight*baseline.BaselineScore
	snapshot.NormalizedScore = clampScore(round1(blended))
	return snapshot
}

// computeBaselineScore turns raw career aggregates into a 0–100 score using
// the same normalization helpers the live performance pipeline uses. Pulled
// out as a free function so api-football implementation and tests share it.
//
// Weights (sum 1.0):
//
//	rating × 0.45       — the single best signal of "is this a good player"
//	goals/90 × 0.20     — attacking output, capped at position max
//	assists/90 × 0.15   — creative output, same cap
//	minutes log × 0.10  — anti-fluke filter; rewards consistently-selected
//	                      players over those with a few cameo seasons
//	trophies × 0.10     — résumé length; 0 when /trophies returned nothing
//
// When trophy data is missing (trophiesCount = 0 and the provider couldn't
// reach /trophies), the trophy weight is reallocated proportionally to the
// other four signals — we don't want to under-rank everyone just because the
// API call timed out.
func computeBaselineScore(b domain.PlayerCareerBaseline, position string, trophiesAvailable bool) float64 {
	if b.SeasonsPlayed == 0 || b.CareerMinutes == 0 {
		return 0
	}

	minutes := float64(b.CareerMinutes)
	goalsPer90 := per90(float64(b.CareerGoals), minutes)
	assistsPer90 := per90(float64(b.CareerAssists), minutes)

	gaMax := positionGAMax(position)
	if gaMax <= 0 {
		gaMax = 0.6
	}

	ratingScore := normalizeLinear(b.CareerAvgRating, 6.0, 8.5)
	goalsScore := normalizeLinear(goalsPer90, 0, gaMax)
	assistsScore := normalizeLinear(assistsPer90, 0, gaMax)
	minutesScore := normalizeLog(minutes, 5_000, 50_000)

	score := ratingScore*0.45 +
		goalsScore*0.20 +
		assistsScore*0.15 +
		minutesScore*0.10

	if trophiesAvailable {
		trophiesScore := normalizeLinear(float64(b.CareerTrophiesCount), 0, 10)
		score += trophiesScore * 0.10
	} else {
		// Reallocate the 0.10 trophy weight proportionally to the other
		// signals (they sum to 0.90, so multiply by 1/0.90).
		score = score / 0.90
	}

	return clampScore(round1(score))
}
