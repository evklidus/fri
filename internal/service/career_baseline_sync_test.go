package service

import (
	"os"
	"path/filepath"
	"strings"
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

// TestComputeBaselineScoreElitedGKNotPenalisedForZeroGoals is the regression
// for the prod bug we hit on 2026-05-11: Courtois (32y, 5 seasons, rating
// 7.1, 0 goals/assists by definition) got baseline 28.9 because the formula
// treated his 0 goals as a 0-score on a 0.35 combined weight. The
// position-aware fix gives GKs no goals/assists channels at all — score
// flows through rating + minutes + trophies, which is the right signal for
// a shotstopper.
func TestComputeBaselineScoreElitedGKNotPenalisedForZeroGoals(t *testing.T) {
	courtoisLike := domain.PlayerCareerBaseline{
		SeasonsPlayed:     5,
		CareerAppearances: 150,
		CareerMinutes:     13_500,
		CareerGoals:       0,
		CareerAssists:     0,
		CareerAvgRating:   7.4,
	}
	gkScore := computeBaselineScore(courtoisLike, "GK", false)
	fwdScore := computeBaselineScore(courtoisLike, "FWD", false)

	// Same stats — but for a GK they're elite, for an FWD they're terrible
	// (no goals, no assists). The GK score must be substantially higher.
	if gkScore-fwdScore < 15 {
		t.Errorf("GK with rating 7.4 and 0 goals should score much higher than an FWD with same stats; gk=%.1f fwd=%.1f", gkScore, fwdScore)
	}
	// And the GK score itself should land in the "this is a good player"
	// range — at least 50 for a top keeper. Before the fix it was ~28.
	if gkScore < 50 {
		t.Errorf("elite GK baseline %.1f too low — formula still penalising for goals/assists", gkScore)
	}
}

// TestComputeBaselineScorePureDefenderAlsoNotPenalised — same regression as
// above but for centre-backs. A CB who plays every game with a solid rating
// shouldn't drop below baseline 40 just because they have 2 career goals.
func TestComputeBaselineScorePureDefenderAlsoNotPenalised(t *testing.T) {
	vanDijkLike := domain.PlayerCareerBaseline{
		SeasonsPlayed:     5,
		CareerAppearances: 175,
		CareerMinutes:     15_400,
		CareerGoals:       8,
		CareerAssists:     3,
		CareerAvgRating:   7.5,
	}
	defScore := computeBaselineScore(vanDijkLike, "DEF", false)
	if defScore < 50 {
		t.Errorf("solid defender baseline %.1f too low — rating + minutes weight may be insufficient", defScore)
	}
}

// TestCareerBaselineSitsOnTheSeasonScale is the regression for what got the
// career weight cut in May. Mbappé's stored career — 23,321 minutes, 244
// goals, 63 assists, rating 7.7, 29 trophies — scored 77 while his season
// scored 89, so blending the two read as a penalty. On one scale, a career
// like that is elite and says so.
func TestCareerBaselineSitsOnTheSeasonScale(t *testing.T) {
	mbappe := domain.PlayerCareerBaseline{
		SeasonsPlayed: 5, CareerMinutes: 23_321, CareerGoals: 244, CareerAssists: 63,
		CareerAvgRating: 7.7, CareerTrophiesCount: 29,
	}
	if got := computeBaselineScore(mbappe, "FWD", true); got < 90 {
		t.Errorf("Mbappé's career = %.1f, want an elite 90+ — the scale is still harsher than the season's", got)
	}
	salah := domain.PlayerCareerBaseline{
		SeasonsPlayed: 5, CareerMinutes: 24_056, CareerGoals: 158, CareerAssists: 85,
		CareerAvgRating: 7.4, CareerTrophiesCount: 22,
	}
	if got := computeBaselineScore(salah, "FWD", true); got < 78 {
		t.Errorf("Salah's career = %.1f, want 78+ — a star in an off year should hold in the 70s", got)
	}
}

// TestAssistsCountAsOutputNotAgainstAGoalsCeiling: a playmaker who splits
// output between goals and assists is worth the same as a scorer with the
// same total. Judging each channel against the combined ceiling made the
// playmaker structurally worse.
func TestAssistsCountAsOutputNotAgainstAGoalsCeiling(t *testing.T) {
	scorer := domain.PlayerCareerBaseline{SeasonsPlayed: 5, CareerMinutes: 18_000, CareerGoals: 120, CareerAssists: 0, CareerAvgRating: 7.3}
	playmaker := domain.PlayerCareerBaseline{SeasonsPlayed: 5, CareerMinutes: 18_000, CareerGoals: 60, CareerAssists: 60, CareerAvgRating: 7.3}
	a, b := computeBaselineScore(scorer, "FWD", false), computeBaselineScore(playmaker, "FWD", false)
	if a != b {
		t.Errorf("same output, different scores: scorer %.1f playmaker %.1f", a, b)
	}
}

// TestAboutPagePromiseMatchesTheCode: the About page tells visitors
// Performance is "blended 60/40 with a career baseline". For four months the
// code did 75/25. Whoever changes one has to change the other.
func TestAboutPagePromiseMatchesTheCode(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "index.html"))
	if err != nil {
		t.Skipf("about page not available here: %v", err)
	}
	if !strings.Contains(string(page), "60/40") {
		t.Fatal("the About page no longer promises a 60/40 blend — update this test and the constant together")
	}
	if careerBaselineWeight != 0.40 {
		t.Errorf("careerBaselineWeight = %v, but the About page promises 60/40", careerBaselineWeight)
	}
}
