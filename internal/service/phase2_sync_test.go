package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"fri.local/football-reputation-index/internal/domain"
)

func TestSyncPerformanceSkipsWhenAlreadyRunning(t *testing.T) {
	s := New(nil, nil, nil, demoPerformanceProvider{})
	s.performanceSyncMu.Lock()
	defer s.performanceSyncMu.Unlock()

	result, err := s.SyncPerformance(context.Background())
	if err != nil {
		t.Fatalf("unexpected error from skipped sync: %v", err)
	}
	if result == nil {
		t.Fatalf("expected non-nil result")
	}
	if result.Status != "skipped" {
		t.Errorf("status = %q, want skipped", result.Status)
	}
	if result.Component != "performance" {
		t.Errorf("component = %q, want performance", result.Component)
	}
}

func TestDemoSocialProviderIsDeterministicAndClamped(t *testing.T) {
	provider := NewSocialProvider("", "", 0)
	player := domain.PlayerSyncTarget{ID: 1, Name: "Lionel Messi", Club: "Inter Miami", Position: "RW"}

	first, err := provider.FetchSocialSnapshot(context.Background(), player)
	if err != nil {
		t.Fatalf("fetch first social snapshot: %v", err)
	}
	second, err := provider.FetchSocialSnapshot(context.Background(), player)
	if err != nil {
		t.Fatalf("fetch second social snapshot: %v", err)
	}

	if first.Followers != second.Followers ||
		first.EngagementRate != second.EngagementRate ||
		first.MentionsGrowth7D != second.MentionsGrowth7D ||
		first.YouTubeViews7D != second.YouTubeViews7D ||
		first.NormalizedScore != second.NormalizedScore {
		t.Fatalf("expected deterministic social snapshot, got %#v and %#v", first, second)
	}

	assertScoreRange(t, first.NormalizedScore)
}

func TestDemoPerformanceProviderIsDeterministicAndClamped(t *testing.T) {
	provider := NewPerformanceProvider("", "", nil, 0)
	player := domain.PlayerSyncTarget{ID: 1, Name: "Erling Haaland", Club: "Manchester City", Position: "ST"}

	first, err := provider.FetchPerformanceSnapshot(context.Background(), player)
	if err != nil {
		t.Fatalf("fetch first performance snapshot: %v", err)
	}
	second, err := provider.FetchPerformanceSnapshot(context.Background(), player)
	if err != nil {
		t.Fatalf("fetch second performance snapshot: %v", err)
	}

	if first.AverageRating != second.AverageRating ||
		first.GoalsAssistsPer90 != second.GoalsAssistsPer90 ||
		first.XGXAPer90 != second.XGXAPer90 ||
		first.PositionRankScore != second.PositionRankScore ||
		first.MinutesShare != second.MinutesShare ||
		first.NormalizedScore != second.NormalizedScore {
		t.Fatalf("expected deterministic performance snapshot, got %#v and %#v", first, second)
	}

	assertScoreRange(t, first.NormalizedScore)
}

func assertScoreRange(t *testing.T, score float64) {
	t.Helper()
	if score < 0 || score > 100 {
		t.Fatalf("expected score in 0..100, got %.1f", score)
	}
}

func TestEngagementIsJudgedAgainstAccountSize(t *testing.T) {
	// Before: Mbappé (130M followers, 3.8% engagement) and Cubarsí (6M, 7%)
	// both scored 68.8 on Social. A flat 1–8% engagement scale read the
	// biggest audience in football as half-asleep, when 3.8% at that size is
	// exceptional and 7% at six million is merely good.
	ctx := context.Background()
	mbappe, err := demoSocialProvider{}.FetchSocialSnapshot(ctx, domain.PlayerSyncTarget{ID: 1, Name: "K. Mbappé"})
	if err != nil {
		t.Fatal(err)
	}
	cubarsi, err := demoSocialProvider{}.FetchSocialSnapshot(ctx, domain.PlayerSyncTarget{ID: 2, Name: "P. Cubarsí"})
	if err != nil {
		t.Fatal(err)
	}
	if mbappe.NormalizedScore <= cubarsi.NormalizedScore+10 {
		t.Errorf("Mbappé %.1f vs Cubarsí %.1f — 130M followers should clearly outscore 6M", mbappe.NormalizedScore, cubarsi.NormalizedScore)
	}
	// The norm itself: engagement falls as audiences grow.
	if expectedEngagementRate(100_000_000) >= expectedEngagementRate(1_000_000) {
		t.Error("expected engagement should fall with audience size")
	}
}

func TestMeasuredFollowersAreNotPunishedForUnmeasuredEngagement(t *testing.T) {
	// The Ballon d'Or nominees were added with real follower counts and
	// nothing else. Scoring the two missing signals as zero would have made
	// a measured 22M-follower account rank below a guessed 3M one — the same
	// failure as the hash fallback, arrived at from the other direction.
	ctx := context.Background()
	dembele, err := demoSocialProvider{}.FetchSocialSnapshot(ctx, domain.PlayerSyncTarget{ID: 26, Name: "O. Dembélé"})
	if err != nil {
		t.Fatal(err)
	}
	cherki, err := demoSocialProvider{}.FetchSocialSnapshot(ctx, domain.PlayerSyncTarget{ID: 12, Name: "Rayan Cherki"})
	if err != nil {
		t.Fatal(err)
	}
	if dembele.Followers != 22_400_000 {
		t.Errorf("followers = %d, want the measured 22.4M", dembele.Followers)
	}
	if dembele.NormalizedScore <= cherki.NormalizedScore {
		t.Errorf("Dembélé %.1f (22.4M, engagement unmeasured) scored at or below Cherki %.1f (3M, fully specified)",
			dembele.NormalizedScore, cherki.NormalizedScore)
	}

	// The medians come from the players somebody measured, and sit inside
	// the range of those values rather than at either end.
	eng, mentions := socialMedians()
	if eng < 3.2 || eng > 7.5 {
		t.Errorf("median engagement %.2f is outside the measured range", eng)
	}
	if mentions < 38 || mentions > 88 {
		t.Errorf("median mentions %.1f is outside the measured range", mentions)
	}
}

func TestMedianOf(t *testing.T) {
	if got := medianOf([]float64{1, 2, 3}, 0); got != 2 {
		t.Errorf("odd-length median = %v, want 2", got)
	}
	if got := medianOf([]float64{1, 2, 3, 4}, 0); got != 2.5 {
		t.Errorf("even-length median = %v, want 2.5", got)
	}
	if got := medianOf(nil, 9); got != 9 {
		t.Errorf("empty median = %v, want the fallback 9", got)
	}
	// Order must not matter.
	if medianOf([]float64{3, 1, 2}, 0) != medianOf([]float64{1, 2, 3}, 0) {
		t.Error("median depends on input order")
	}
}

func TestRodriIsNotGivenSomeoneElsesAccount(t *testing.T) {
	// He has no social media at all. @rodrigo belongs to a Brazilian video
	// editor; attaching it here would put a stranger's audience inside a
	// Ballon d'Or nominee's rating.
	if _, listed := realSocialOverrides["Rodri"]; listed {
		t.Error("Rodri has a follower count — he has no account; check where the number came from")
	}
	snapshot, err := demoSocialProvider{}.FetchSocialSnapshot(context.Background(),
		domain.PlayerSyncTarget{ID: 40, Name: "Rodri"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Followers != 0 || snapshot.NormalizedScore != neutralComponentScore {
		t.Errorf("Rodri scored %+v, want no followers and the neutral score", snapshot)
	}
}

type unavailablePerformance struct{}

func (unavailablePerformance) Name() string { return apiFootballProviderName }
func (unavailablePerformance) FetchPerformanceSnapshot(_ context.Context, p domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error) {
	return domain.PerformanceSnapshot{}, fmt.Errorf("%w for %s", ErrNoRealPerformance, p.Name)
}

func TestLapsedProviderLeavesScoresAloneAndSaysSo(t *testing.T) {
	// 2026-09-17: the API-Football plan lapsed to Free, which refuses the
	// current season. The sync used to fill every player with demo numbers
	// and report "completed". It must now write nothing and report failure.
	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return []domain.PlayerSyncTarget{{ID: 8, Name: "L. Yamal"}, {ID: 17, Name: "Pedri"}}, nil
		},
	}
	svc := New(repo, nil, nil, unavailablePerformance{})
	result, err := svc.SyncPerformance(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != "failed" {
		t.Errorf("status = %q, want failed — a provider that measured nobody is an outage, not a sync", result.Status)
	}
	if !strings.Contains(result.Message, "subscription") {
		t.Errorf("message %q should point at the likely cause", result.Message)
	}
}
