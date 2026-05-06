package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/app/config"
	"fri.local/football-reputation-index/internal/domain"
	"github.com/gin-gonic/gin"
)

// fakeService is a hand-rolled mock — programmable per-method via function
// fields. Keeps tests readable without dragging in a mocking framework.
type fakeService struct {
	listPlayersFn          func(context.Context, string, string, string) ([]domain.PlayerWithScore, error)
	getPlayerFn            func(context.Context, int64) (*domain.PlayerWithScore, error)
	getHistoryFn           func(context.Context, int64) ([]domain.HistoryPoint, error)
	listNewsFn             func(context.Context, *int64) ([]domain.NewsItem, error)
	submitVoteFn           func(context.Context, int64, domain.VoteInput, string) (*domain.Score, error)
	listComponentUpdatesFn func(context.Context, int) ([]domain.ComponentUpdate, error)
	syncMediaFn            func(context.Context) (*domain.ComponentSyncResult, error)
	syncSocialFn           func(context.Context) (*domain.ComponentSyncResult, error)
	syncPerformanceFn      func(context.Context) (*domain.ComponentSyncResult, error)
	syncCharacterFn        func(context.Context) (*domain.ComponentSyncResult, error)
	syncAllFn              func(context.Context) ([]domain.ComponentSyncResult, error)
}

func (f *fakeService) ListPlayers(ctx context.Context, search, position, club string) ([]domain.PlayerWithScore, error) {
	return f.listPlayersFn(ctx, search, position, club)
}
func (f *fakeService) GetPlayer(ctx context.Context, id int64) (*domain.PlayerWithScore, error) {
	return f.getPlayerFn(ctx, id)
}
func (f *fakeService) GetHistory(ctx context.Context, playerID int64) ([]domain.HistoryPoint, error) {
	return f.getHistoryFn(ctx, playerID)
}
func (f *fakeService) ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error) {
	return f.listNewsFn(ctx, playerID)
}
func (f *fakeService) SubmitVote(ctx context.Context, playerID int64, input domain.VoteInput, rawIP string) (*domain.Score, error) {
	return f.submitVoteFn(ctx, playerID, input, rawIP)
}
func (f *fakeService) ListComponentUpdates(ctx context.Context, limit int) ([]domain.ComponentUpdate, error) {
	return f.listComponentUpdatesFn(ctx, limit)
}
func (f *fakeService) SyncMedia(ctx context.Context) (*domain.ComponentSyncResult, error) {
	return f.syncMediaFn(ctx)
}
func (f *fakeService) SyncSocial(ctx context.Context) (*domain.ComponentSyncResult, error) {
	return f.syncSocialFn(ctx)
}
func (f *fakeService) SyncPerformance(ctx context.Context) (*domain.ComponentSyncResult, error) {
	return f.syncPerformanceFn(ctx)
}
func (f *fakeService) SyncCharacter(ctx context.Context) (*domain.ComponentSyncResult, error) {
	return f.syncCharacterFn(ctx)
}
func (f *fakeService) SyncAll(ctx context.Context) ([]domain.ComponentSyncResult, error) {
	return f.syncAllFn(ctx)
}

func newServerWithFake(t *testing.T, fake *fakeService) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{WebDir: "."}, fake)
	return httptest.NewServer(router)
}

func decode(t *testing.T, body []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, string(body))
	}
}

func TestHealthEndpoint(t *testing.T) {
	server := newServerWithFake(t, &fakeService{})
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestListPlayersForwardsQueryParams(t *testing.T) {
	var gotSearch, gotPosition, gotClub string
	fake := &fakeService{
		listPlayersFn: func(_ context.Context, search, position, club string) ([]domain.PlayerWithScore, error) {
			gotSearch, gotPosition, gotClub = search, position, club
			return []domain.PlayerWithScore{{Player: domain.Player{ID: 1, Name: "Messi"}}}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/players?search=mes&position=RW&club=miami")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotSearch != "mes" || gotPosition != "RW" || gotClub != "miami" {
		t.Errorf("forwarded params = %q/%q/%q, want mes/RW/miami", gotSearch, gotPosition, gotClub)
	}
}

func TestListPlayersReturns500OnError(t *testing.T) {
	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return nil, errors.New("db down")
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/players")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestGetPlayer404OnNotFound(t *testing.T) {
	fake := &fakeService{
		getPlayerFn: func(_ context.Context, id int64) (*domain.PlayerWithScore, error) {
			return nil, errors.New("not found")
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/players/99")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestParseIDRejectsNonNumeric(t *testing.T) {
	server := newServerWithFake(t, &fakeService{})
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/players/abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSubmitVoteHappyPath(t *testing.T) {
	var gotPlayerID int64
	var gotInput domain.VoteInput
	fake := &fakeService{
		submitVoteFn: func(_ context.Context, id int64, input domain.VoteInput, _ string) (*domain.Score, error) {
			gotPlayerID = id
			gotInput = input
			return &domain.Score{PlayerID: id, FRI: 87.3}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	body := `{"session_id":"s1","rating_overall":5,"rating_hype":9,"rating_tier":80,"behavior_score":70}`
	resp, err := stdhttp.Post(server.URL+"/api/players/42/vote", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if gotPlayerID != 42 {
		t.Errorf("player id = %d, want 42", gotPlayerID)
	}
	if gotInput.RatingOverall != 5 || gotInput.RatingHype != 9 || gotInput.SessionID != "s1" {
		t.Errorf("input not forwarded correctly: %+v", gotInput)
	}
}

func TestSubmitVote400OnInvalidJSON(t *testing.T) {
	server := newServerWithFake(t, &fakeService{})
	defer server.Close()

	resp, err := stdhttp.Post(server.URL+"/api/players/1/vote", "application/json", bytes.NewReader([]byte(`not json`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSubmitVote429OnRateLimitError(t *testing.T) {
	fake := &fakeService{
		submitVoteFn: func(context.Context, int64, domain.VoteInput, string) (*domain.Score, error) {
			return nil, errors.New("vote rate limit: already voted for this player in the last 24h")
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	body := `{"rating_overall":5,"rating_hype":5,"rating_tier":50,"behavior_score":50}`
	resp, err := stdhttp.Post(server.URL+"/api/players/1/vote", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
}

func TestSubmitVote400OnServiceValidationError(t *testing.T) {
	fake := &fakeService{
		submitVoteFn: func(context.Context, int64, domain.VoteInput, string) (*domain.Score, error) {
			return nil, errors.New("rating_overall must be between 1 and 5")
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	body := `{"rating_overall":99}`
	resp, err := stdhttp.Post(server.URL+"/api/players/1/vote", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSyncEndpointsReturnResultJSON(t *testing.T) {
	completed := &domain.ComponentSyncResult{
		Component:   "media",
		Provider:    "gdelt",
		Status:      "completed",
		RecordsSeen: 5,
		StartedAt:   time.Now().UTC(),
		FinishedAt:  time.Now().UTC(),
	}
	fake := &fakeService{
		syncMediaFn:       func(context.Context) (*domain.ComponentSyncResult, error) { return completed, nil },
		syncSocialFn:      func(context.Context) (*domain.ComponentSyncResult, error) { return completed, nil },
		syncPerformanceFn: func(context.Context) (*domain.ComponentSyncResult, error) { return completed, nil },
		syncAllFn: func(context.Context) ([]domain.ComponentSyncResult, error) {
			return []domain.ComponentSyncResult{*completed}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	for _, path := range []string{"/api/sync/media", "/api/sync/social", "/api/sync/performance"} {
		resp, err := stdhttp.Post(server.URL+path, "application/json", nil)
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		if resp.StatusCode != stdhttp.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
		}
		body, _ := readBody(resp)
		var wrap struct {
			Data domain.ComponentSyncResult `json:"data"`
		}
		decode(t, body, &wrap)
		if wrap.Data.Status != "completed" {
			t.Errorf("%s: status field = %q, want completed", path, wrap.Data.Status)
		}
	}
}

func TestSyncEndpointReturns500OnError(t *testing.T) {
	fake := &fakeService{
		syncMediaFn: func(context.Context) (*domain.ComponentSyncResult, error) {
			return nil, errors.New("provider down")
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Post(server.URL+"/api/sync/media", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestListNewsFeedPassesNilPlayerID(t *testing.T) {
	var gotPlayerID *int64
	fake := &fakeService{
		listNewsFn: func(_ context.Context, playerID *int64) ([]domain.NewsItem, error) {
			gotPlayerID = playerID
			return []domain.NewsItem{}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/news/feed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if gotPlayerID != nil {
		t.Errorf("player id should be nil for global feed, got %v", *gotPlayerID)
	}
}

func TestPlayerNewsScopedToPlayer(t *testing.T) {
	var gotPlayerID *int64
	fake := &fakeService{
		listNewsFn: func(_ context.Context, playerID *int64) ([]domain.NewsItem, error) {
			gotPlayerID = playerID
			return []domain.NewsItem{}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/players/7/news")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if gotPlayerID == nil || *gotPlayerID != 7 {
		t.Errorf("player id = %v, want pointer to 7", gotPlayerID)
	}
}

func readBody(resp *stdhttp.Response) ([]byte, error) {
	defer resp.Body.Close()
	buf := bytes.NewBuffer(nil)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
