package service

import (
	"testing"
)

// TestPerformanceWeightsSumToOne is the structural contract: each position
// must have weights summing to ~1.0, or the resulting score is silently
// scaled wrong. A drift here would show up as everyone's Performance being
// systematically too low or too high.
func TestPerformanceWeightsSumToOne(t *testing.T) {
	for _, pos := range []string{"FWD", "ATT", "MID", "DEF", "GK", "ST", "CB", "AM", "Unknown"} {
		w := performanceWeightsFor(pos)
		sum := w.rating + w.goalsAssists + w.xgxa + w.posRank + w.minutes + w.form
		if sum < 0.99 || sum > 1.01 {
			t.Errorf("position %q weights sum to %.3f, want 1.0", pos, sum)
		}
	}
}

// TestPerformanceWeightsCreativeMidfielderBoost is the Pedri regression.
// A midfielder who maxes out match rating but is light on goals should
// score higher under MID weights than the old (FWD-shaped) weights.
func TestPerformanceWeightsCreativeMidfielderBoost(t *testing.T) {
	const (
		ratingMax     = 1.0 // assume normalized to 1.0 — top of scale
		modestGoals   = 0.2 // 3 goals over 30 games for a CM
		modestXGXA    = 0.3
		strongPosRank = 0.85
		fullMinutes   = 0.9
		strongForm    = 0.7
	)

	fwdW := performanceWeightsFor("FWD")
	midW := performanceWeightsFor("MID")

	// Synthetic score under each weighting (signals * 100 = on 0..100 scale).
	score := func(w performanceWeights) float64 {
		return (ratingMax*100*w.rating +
			modestGoals*100*w.goalsAssists +
			modestXGXA*100*w.xgxa +
			strongPosRank*100*w.posRank +
			fullMinutes*100*w.minutes +
			strongForm*100*w.form)
	}

	fwdScore := score(fwdW)
	midScore := score(midW)

	// Same player, only position weighting differs. The creative midfielder
	// profile (peak rating, low goals) should score noticeably higher under
	// MID weights — the whole point of Phase 4 ext-B.
	if midScore-fwdScore < 3 {
		t.Errorf("MID weighting should lift creative midfielders: fwd=%.1f mid=%.1f (delta only %.1f)", fwdScore, midScore, midScore-fwdScore)
	}
}

// TestPerformanceWeightsGKNoGoalChannel — for a goalkeeper, the goal and xG
// weights must be zero. A GK who happens to score (rare set-piece) should
// not get an inflated Performance from a per-90 spike on a tiny sample.
func TestPerformanceWeightsGKNoGoalChannel(t *testing.T) {
	w := performanceWeightsFor("GK")
	if w.goalsAssists != 0 || w.xgxa != 0 {
		t.Errorf("GK must have zero goal/xG weight, got goals=%.2f xgxa=%.2f", w.goalsAssists, w.xgxa)
	}
	// Rating must carry most of the score — match rating is how
	// API-Football quantifies "good keeper" (saves, distribution, etc).
	if w.rating < 0.45 {
		t.Errorf("GK rating weight %.2f too low — rating is the only quality signal for keepers", w.rating)
	}
}

// TestPerformanceWeightsDefenderBalance — a defender's score shouldn't be
// dominated by goals. Rating + minutes should be the two main signals.
func TestPerformanceWeightsDefenderBalance(t *testing.T) {
	w := performanceWeightsFor("DEF")
	if w.goalsAssists+w.xgxa > 0.15 {
		t.Errorf("DEF goal+xG combined weight %.2f too high — CBs aren't paid to score", w.goalsAssists+w.xgxa)
	}
	if w.rating+w.minutes < 0.60 {
		t.Errorf("DEF rating+minutes %.2f too low — these are the two reliable signals", w.rating+w.minutes)
	}
}
