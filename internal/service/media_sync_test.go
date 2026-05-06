package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// stubMediaProvider lets us bypass GDELT entirely for SyncMedia flow tests
// where we want to assert orchestration without HTTP plumbing.
type stubMediaProvider struct {
	name             string
	articlesByPlayer map[int64][]domain.MediaArticleCandidate
	err              error
}

func (s *stubMediaProvider) Name() string {
	if s.name == "" {
		return "stub"
	}
	return s.name
}

func (s *stubMediaProvider) FetchPlayerArticles(_ context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.articlesByPlayer[player.ID], nil
}

func newServiceWithMedia(repo *mockRepo, media mediaProvider) *Service {
	return New(repo, media, nil, nil)
}

func TestSyncMediaSkipsWhenAlreadyRunning(t *testing.T) {
	svc := newServiceWithMedia(&mockRepo{}, &stubMediaProvider{})
	svc.mediaSyncMu.Lock()
	defer svc.mediaSyncMu.Unlock()

	result, err := svc.SyncMedia(context.Background())
	if err != nil {
		t.Fatalf("expected no error from skipped sync: %v", err)
	}
	if result.Status != "skipped" {
		t.Errorf("status = %q, want skipped", result.Status)
	}
}

func TestSyncMediaPropagatesListSyncTargetsError(t *testing.T) {
	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return nil, errors.New("db down")
		},
	}
	svc := newServiceWithMedia(repo, &stubMediaProvider{})

	result, err := svc.SyncMedia(context.Background())
	if err == nil {
		t.Fatal("expected error from sync")
	}
	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
}

func TestSyncMediaCompletesWithNoArticles(t *testing.T) {
	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return []domain.PlayerSyncTarget{{ID: 1, Name: "Player"}}, nil
		},
	}
	svc := newServiceWithMedia(repo, &stubMediaProvider{})

	result, err := svc.SyncMedia(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.RecordsSeen != 0 {
		t.Errorf("records = %d, want 0", result.RecordsSeen)
	}
}

func TestSyncMediaAppliesArticlesAndAggregatesScore(t *testing.T) {
	var captured []domain.MediaSyncPlayerResult
	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return []domain.PlayerSyncTarget{
				{ID: 1, Name: "Lionel Messi"},
				{ID: 2, Name: "No News Player"},
			}, nil
		},
		applyMediaSyncFn: func(_ context.Context, results []domain.MediaSyncPlayerResult, _ string) ([]domain.PlayerSyncDelta, error) {
			captured = results
			return []domain.PlayerSyncDelta{}, nil
		},
	}
	media := &stubMediaProvider{
		articlesByPlayer: map[int64][]domain.MediaArticleCandidate{
			1: {
				{
					PlayerName:  "Lionel Messi",
					Title:       "Messi scores brilliant goal",
					Summary:     "Masterclass display from the captain",
					Source:      "bbc.com",
					SourceURL:   "https://bbc.com/a",
					PublishedAt: time.Now().UTC(),
				},
				{
					PlayerName:  "Lionel Messi",
					Title:       "Inter Miami secures victory",
					Summary:     "Late winner from Messi",
					Source:      "espn.com",
					SourceURL:   "https://espn.com/b",
					PublishedAt: time.Now().UTC(),
				},
			},
		},
	}

	svc := newServiceWithMedia(repo, media)
	result, err := svc.SyncMedia(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.RecordsSeen != 2 {
		t.Errorf("records seen = %d, want 2 articles", result.RecordsSeen)
	}
	if len(captured) != 2 {
		t.Fatalf("captured player results = %d, want 2 (incl no-news fallback)", len(captured))
	}

	var messiResult domain.MediaSyncPlayerResult
	for _, r := range captured {
		if r.PlayerID == 1 {
			messiResult = r
		}
	}
	if messiResult.ArticlesCount != 2 {
		t.Errorf("messi articles = %d, want 2", messiResult.ArticlesCount)
	}
	if messiResult.MediaScore <= 0 || messiResult.MediaScore > 100 {
		t.Errorf("messi media score out of range: %v", messiResult.MediaScore)
	}
}

func TestSyncMediaContinuesPastIndividualProviderErrors(t *testing.T) {
	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return []domain.PlayerSyncTarget{
				{ID: 1, Name: "Failing Player"},
				{ID: 2, Name: "OK Player"},
			}, nil
		},
	}
	media := &stubMediaProvider{
		err: errors.New("provider rate limit"),
	}
	svc := newServiceWithMedia(repo, media)

	result, err := svc.SyncMedia(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Both players failed → no articles → completes with "no external media articles found".
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
}

func TestSyncMediaCapsBatchToTopByFRI(t *testing.T) {
	// Build mediaSyncBatchSize+5 targets with descending FRI; only the top
	// mediaSyncBatchSize should be hit by the provider.
	targets := make([]domain.PlayerSyncTarget, mediaSyncBatchSize+5)
	for i := range targets {
		targets[i] = domain.PlayerSyncTarget{
			ID:    int64(i + 1),
			Name:  fmt.Sprintf("Player %d", i+1),
			Score: domain.Score{FRI: float64(100 - i)},
		}
	}

	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return targets, nil
		},
	}

	var mu sync.Mutex
	var hitPlayers []string
	media := &stubMediaProvider{
		articlesByPlayer: nil,
	}
	// Wrap with a counting fetch so we can assert *which* players were hit.
	wrapper := &countingMediaProvider{
		inner: media,
		onHit: func(name string) {
			mu.Lock()
			hitPlayers = append(hitPlayers, name)
			mu.Unlock()
		},
	}

	svc := newServiceWithMedia(repo, wrapper)
	if _, err := svc.SyncMedia(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hitPlayers) != mediaSyncBatchSize {
		t.Errorf("provider called for %d players, want %d (top-N cap)", len(hitPlayers), mediaSyncBatchSize)
	}
	// The first hit should be the highest-FRI player ("Player 1" with FRI=100).
	if len(hitPlayers) > 0 && hitPlayers[0] != "Player 1" {
		t.Errorf("first hit = %q, want Player 1 (highest FRI)", hitPlayers[0])
	}
}

type countingMediaProvider struct {
	inner mediaProvider
	onHit func(string)
}

func (c *countingMediaProvider) Name() string { return c.inner.Name() }

func (c *countingMediaProvider) FetchPlayerArticles(ctx context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error) {
	c.onHit(player.Name)
	return c.inner.FetchPlayerArticles(ctx, player)
}

func TestTopByFRIReturnsHighestFirst(t *testing.T) {
	targets := []domain.PlayerSyncTarget{
		{ID: 1, Score: domain.Score{FRI: 70}},
		{ID: 2, Score: domain.Score{FRI: 95}},
		{ID: 3, Score: domain.Score{FRI: 80}},
	}
	got := topByFRI(targets, 2)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].ID != 2 || got[1].ID != 3 {
		t.Errorf("order = [%d,%d], want [2,3]", got[0].ID, got[1].ID)
	}
}

func TestTopByFRIReturnsAllWhenLimitExceedsLength(t *testing.T) {
	targets := []domain.PlayerSyncTarget{{ID: 1}, {ID: 2}}
	if got := topByFRI(targets, 100); len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestSyncMediaEndToEndWithGDELT(t *testing.T) {
	gdelt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"articles":[
			{"url":"https://bbc.com/a","title":"Messi scored a brilliant goal","domain":"bbc.com","seendate":"20260501T100000Z"}
		]}`))
	}))
	defer gdelt.Close()

	mediaProv := newGDELTMediaProvider(time.Second, 5, 0).(*gdeltMediaProvider)
	mediaProv.baseURL = gdelt.URL

	var applyCalled bool
	repo := &mockRepo{
		listSyncTargetsFn: func(context.Context) ([]domain.PlayerSyncTarget, error) {
			return []domain.PlayerSyncTarget{{ID: 1, Name: "Lionel Messi", Score: domain.Score{Media: 75}}}, nil
		},
		applyMediaSyncFn: func(_ context.Context, results []domain.MediaSyncPlayerResult, provider string) ([]domain.PlayerSyncDelta, error) {
			applyCalled = true
			if provider != mediaProviderName {
				t.Errorf("provider = %q, want %q", provider, mediaProviderName)
			}
			if len(results) != 1 || results[0].ArticlesCount == 0 {
				t.Errorf("expected at least one article, got %+v", results)
			}
			return nil, nil
		},
	}

	svc := newServiceWithMedia(repo, mediaProv)
	result, err := svc.SyncMedia(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if !applyCalled {
		t.Error("ApplyMediaSync not called — pipeline broke before persistence")
	}
}
