package service

import (
	"context"
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
