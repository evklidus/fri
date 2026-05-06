package service

import (
	"context"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

func makeNews(id int64, playerID int64, title, summary string, publishedAt time.Time) domain.NewsItem {
	pid := playerID
	return domain.NewsItem{
		ID:          id,
		PlayerID:    &pid,
		TitleEN:     title,
		SummaryEN:   summary,
		PublishedAt: publishedAt,
	}
}

func TestScanFiresOnNegativeKeyword(t *testing.T) {
	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	news := []domain.NewsItem{
		makeNews(1, 7, "Player banned for doping after failed drug test", "Investigation finds traces", time.Now().UTC()),
	}

	got := scanNewsForCharacterTriggers(news, cutoff)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].PlayerID != 7 || got[0].NewsItemID != 1 {
		t.Errorf("candidate ids = %d/%d, want 7/1", got[0].PlayerID, got[0].NewsItemID)
	}
	if got[0].TriggerWord != "doping" {
		t.Errorf("trigger = %q, want doping", got[0].TriggerWord)
	}
	if got[0].Delta >= 0 {
		t.Errorf("delta = %v, want negative for doping", got[0].Delta)
	}
}

func TestScanIgnoresOldArticles(t *testing.T) {
	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	old := time.Now().UTC().AddDate(0, 0, -45)
	news := []domain.NewsItem{makeNews(1, 7, "Player sent off after red card", "", old)}

	if got := scanNewsForCharacterTriggers(news, cutoff); len(got) != 0 {
		t.Errorf("expected 0 candidates for stale article, got %d", len(got))
	}
}

func TestScanSkipsNegatorVictim(t *testing.T) {
	// "Vinicius racism victim" — player is the target, not the perpetrator.
	news := []domain.NewsItem{makeNews(1, 9, "Vinicius racism victim", "Fans chant racist abuse", time.Now().UTC())}

	if got := scanNewsForCharacterTriggers(news, time.Time{}); len(got) != 0 {
		t.Errorf("negator should block trigger, got %d candidates: %+v", len(got), got)
	}
}

func TestScanSkipsCondemnsNegator(t *testing.T) {
	news := []domain.NewsItem{makeNews(1, 9, "Mbappé condemns racism in football", "Speaks out against abuse", time.Now().UTC())}

	if got := scanNewsForCharacterTriggers(news, time.Time{}); len(got) != 0 {
		t.Errorf("negator 'condemns' should block trigger, got %d", len(got))
	}
}

func TestScanFiresOncePerConcept(t *testing.T) {
	// Multiple keywords for the same concept ("doping" + "failed drug test")
	// in one article must register exactly one event.
	news := []domain.NewsItem{makeNews(1, 7, "Player banned for doping after failed drug test", "Doping confirmed", time.Now().UTC())}

	got := scanNewsForCharacterTriggers(news, time.Time{})
	if len(got) != 1 {
		t.Errorf("expected 1 candidate (single concept), got %d: %+v", len(got), got)
	}
}

func TestScanFiresPositiveTrigger(t *testing.T) {
	news := []domain.NewsItem{makeNews(1, 7, "Player wins fair play award at FIFA gala", "", time.Now().UTC())}

	got := scanNewsForCharacterTriggers(news, time.Time{})
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	if got[0].Delta <= 0 {
		t.Errorf("delta = %v, want positive for fair play award", got[0].Delta)
	}
}

func TestScanSkipsArticlesWithoutPlayerID(t *testing.T) {
	news := []domain.NewsItem{
		{TitleEN: "Player sent off", PublishedAt: time.Now().UTC()}, // PlayerID is nil
	}

	if got := scanNewsForCharacterTriggers(news, time.Time{}); len(got) != 0 {
		t.Errorf("article without player_id must be skipped, got %d", len(got))
	}
}

func TestScanSkipsArticlesWithZeroPlayerID(t *testing.T) {
	zero := int64(0)
	news := []domain.NewsItem{
		{PlayerID: &zero, TitleEN: "Player sent off", PublishedAt: time.Now().UTC()},
	}

	if got := scanNewsForCharacterTriggers(news, time.Time{}); len(got) != 0 {
		t.Errorf("article with player_id=0 must be skipped, got %d", len(got))
	}
}

func TestSyncCharacterSkipsWhenAlreadyRunning(t *testing.T) {
	svc := New(&mockRepo{}, nil, nil, nil)
	svc.characterSyncMu.Lock()
	defer svc.characterSyncMu.Unlock()

	result, err := svc.SyncCharacter(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "skipped" {
		t.Errorf("status = %q, want skipped", result.Status)
	}
	if result.Component != "character" {
		t.Errorf("component = %q, want character", result.Component)
	}
}

func TestSyncCharacterAggregatesAndPersists(t *testing.T) {
	pid := int64(7)
	repo := &mockRepo{
		listNewsFn: func(_ context.Context, _ *int64) ([]domain.NewsItem, error) {
			return []domain.NewsItem{
				{ID: 1, PlayerID: &pid, TitleEN: "Player sent off after red card", PublishedAt: time.Now().UTC()},
				{ID: 2, PlayerID: &pid, TitleEN: "Match ban announced", PublishedAt: time.Now().UTC()},
			}, nil
		},
		applyCharacterSyncFn: func(_ context.Context, candidates []domain.CharacterEventCandidate, cap float64) ([]domain.PlayerSyncDelta, error) {
			if len(candidates) != 2 {
				t.Errorf("expected 2 candidates, got %d", len(candidates))
			}
			if cap != characterPerSyncCap {
				t.Errorf("cap = %v, want %v", cap, characterPerSyncCap)
			}
			return []domain.PlayerSyncDelta{{PlayerID: pid, Component: "character", OldValue: 80, NewValue: 76, ImpactDelta: -0.4}}, nil
		},
	}
	svc := New(repo, nil, nil, nil)

	result, err := svc.SyncCharacter(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.RecordsSeen != 2 {
		t.Errorf("records seen = %d, want 2", result.RecordsSeen)
	}
	if len(result.Players) != 1 {
		t.Errorf("expected 1 player delta, got %d", len(result.Players))
	}
}

func TestSyncCharacterPropagatesListNewsError(t *testing.T) {
	repo := &mockRepo{
		listNewsFn: func(context.Context, *int64) ([]domain.NewsItem, error) {
			return nil, &fakeErr{msg: "db down"}
		},
	}
	svc := New(repo, nil, nil, nil)

	result, err := svc.SyncCharacter(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
