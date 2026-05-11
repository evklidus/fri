package service

import (
	"testing"

	"fri.local/football-reputation-index/internal/domain"
)

// TestComputeBaselineScoreSensibleRange checks the score lands in [0, 100]
// for plausible inputs and reflects the relative ordering we expect: a
// 5-year star outranks a 5-year midcard.
func TestComputeBaselineScoreSensibleRange(t *testing.T) {
	star := domain.PlayerCareerBaseline{
		SeasonsPlayed:       5,
		CareerAppearances:   180,
		CareerMinutes:       15_500,
		CareerGoals:         95,
		CareerAssists:       50,
		CareerAvgRating:     8.0,
		CareerTrophiesCount: 4,
	}
	midcard := domain.PlayerCareerBaseline{
		SeasonsPlayed:       5,
		CareerAppearances:   140,
		CareerMinutes:       11_000,
		CareerGoals:         12,
		CareerAssists:       15,
		CareerAvgRating:     6.8,
		CareerTrophiesCount: 0,
	}

	starScore := computeBaselineScore(star, "FWD", true)
	midScore := computeBaselineScore(midcard, "FWD", true)

	if starScore <= midScore {
		t.Errorf("star baseline %.1f should beat midcard %.1f", starScore, midScore)
	}
	if starScore < 0 || starScore > 100 {
		t.Errorf("star baseline %.1f out of range", starScore)
	}
	if midScore < 0 || midScore > 100 {
		t.Errorf("midcard baseline %.1f out of range", midScore)
	}
}

// TestComputeBaselineScoreNoTrophyData verifies that when /trophies wasn't
// available, the formula re-normalizes so all-zero trophy counts don't drag
// the whole score down.
func TestComputeBaselineScoreNoTrophyData(t *testing.T) {
	player := domain.PlayerCareerBaseline{
		SeasonsPlayed:     5,
		CareerAppearances: 180,
		CareerMinutes:     15_500,
		CareerGoals:       60,
		CareerAssists:     40,
		CareerAvgRating:   7.4,
	}

	withTrophyAvailable := computeBaselineScore(player, "FWD", true)
	withoutTrophyData := computeBaselineScore(player, "FWD", false)

	// Without trophy weight, the remaining 90% gets re-scaled — score should
	// rise relative to the version where trophies==0 but the weight is
	// still applied.
	if withoutTrophyData <= withTrophyAvailable {
		t.Errorf("expected re-normalized score (no trophy data) %.1f > trophy-applied-with-0 %.1f",
			withoutTrophyData, withTrophyAvailable)
	}
}

// TestComputeBaselineScoreZeroSeasons returns 0 — protects callers from
// dividing by zero when an upstream call returned no data.
func TestComputeBaselineScoreZeroSeasons(t *testing.T) {
	empty := domain.PlayerCareerBaseline{}
	if got := computeBaselineScore(empty, "FWD", true); got != 0 {
		t.Errorf("zero baseline should return 0, got %.1f", got)
	}
}

// TestComputeBaselineScorePositionAwareGoalMax is a regression: per-90 goal
// stats are normalized against a position-specific max so a CB with 5 goals
// doesn't score lower than a striker with the same total.
func TestComputeBaselineScorePositionAwareGoalMax(t *testing.T) {
	// Same numbers — only position differs.
	stats := domain.PlayerCareerBaseline{
		SeasonsPlayed:     5,
		CareerAppearances: 180,
		CareerMinutes:     15_500,
		CareerGoals:       10,
		CareerAssists:     5,
		CareerAvgRating:   7.4,
	}
	cbScore := computeBaselineScore(stats, "DEF", false)
	fwdScore := computeBaselineScore(stats, "FWD", false)

	// A defender hitting 10 goals over 5 seasons is impressive (high
	// relative to position max); a striker with same is mediocre (low
	// relative to forward max). The defender should outscore here on the
	// goal sub-signal.
	if cbScore <= fwdScore {
		t.Errorf("defender goal-rate should rank higher vs DEF baseline (%.1f) than FWD baseline (%.1f)",
			cbScore, fwdScore)
	}
}
