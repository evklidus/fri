package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/app/config"
	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/service"
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
	syncCareerBaselineFn   func(context.Context) (*domain.ComponentSyncResult, error)
	syncAllFn              func(context.Context) ([]domain.ComponentSyncResult, error)
	deleteNewsFn           func(context.Context, int64) (*domain.NewsDeletion, error)

	// Accounts. sessionUser, when set, is the user a request carrying the
	// session cookie resolves to — enough to exercise the signed-in branch of
	// the gates without a database.
	sessionUser *domain.User
}

// The fake implements NewsAdminService so the moderation handler can be
// exercised without a database; tests that leave deleteNewsFn nil get the
// "not found" answer, the same as a row a sync already rotated out.
func (f *fakeService) DeleteNewsItem(ctx context.Context, id int64) (*domain.NewsDeletion, error) {
	if f.deleteNewsFn == nil {
		return nil, service.ErrNewsNotFound
	}
	return f.deleteNewsFn(ctx, id)
}

// The fake implements AuthService so the handlers can tell a signed-in caller
// from an anonymous one. Only the session lookup is exercised here; register
// and login have their own coverage against the real service.
func (f *fakeService) AuthEnabled() bool { return true }

func (f *fakeService) Register(context.Context, string, string) (domain.User, string, time.Time, error) {
	return domain.User{}, "", time.Time{}, errors.New("not implemented in fake")
}

func (f *fakeService) Login(context.Context, string, string) (domain.User, string, time.Time, error) {
	return domain.User{}, "", time.Time{}, errors.New("not implemented in fake")
}

func (f *fakeService) Logout(context.Context, string) error { return nil }

func (f *fakeService) UserBySession(_ context.Context, token string) (domain.User, error) {
	if f.sessionUser == nil || token == "" {
		return domain.User{}, errors.New("no session")
	}
	return *f.sessionUser, nil
}

func (f *fakeService) CountUsers(context.Context) (int64, error) { return 0, nil }

// ListPlayers tolerates an unset stub. The leaderboard gate calls it from
// handlers that have nothing to do with listing players — it needs the ordered
// roster to decide whether a requested id is withheld — so tests about, say,
// player history would otherwise have to stub a function they never assert on.
func (f *fakeService) ListPlayers(ctx context.Context, search, position, club string) ([]domain.PlayerWithScore, error) {
	if f.listPlayersFn == nil {
		return nil, nil
	}
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
func (f *fakeService) SyncCareerBaseline(ctx context.Context) (*domain.ComponentSyncResult, error) {
	if f.syncCareerBaselineFn != nil {
		return f.syncCareerBaselineFn(ctx)
	}
	// Default to a benign "skipped" so existing tests that don't care about
	// the new endpoint don't have to wire up a stub.
	now := time.Now().UTC()
	return &domain.ComponentSyncResult{
		Component:  "career-baseline",
		Provider:   "career-baseline",
		Status:     "skipped",
		Message:    "no fake configured",
		StartedAt:  now,
		FinishedAt: now,
	}, nil
}

// Phase 5 stubs — default to empty/no-op so existing tests stay passing.
func (f *fakeService) ListPendingEvents(ctx context.Context, playerID int64, limit int) ([]domain.PendingEvent, error) {
	return nil, nil
}
func (f *fakeService) GetPendingEvent(ctx context.Context, eventID int64) (*domain.PendingEvent, error) {
	return nil, nil
}
func (f *fakeService) SubmitEventVote(ctx context.Context, eventID int64, suggestedDelta float64, rawIP string) error {
	return nil
}
func (f *fakeService) FinalizePendingEvents(ctx context.Context) (*domain.ComponentSyncResult, error) {
	now := time.Now().UTC()
	return &domain.ComponentSyncResult{
		Component:  "event-finalize",
		Provider:   "event-voting",
		Status:     "completed",
		Message:    "no events",
		StartedAt:  now,
		FinishedAt: now,
	}, nil
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

// testAdminToken authorises the admin-only routes in tests. The sync
// endpoints moved behind requireAdmin once accounts landed — they trigger
// metered API calls, so leaving them open to the internet was a standing
// invitation to burn someone else's quota.
const testAdminToken = "test-admin-token"

func newServerWithFake(t *testing.T, fake *fakeService) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{WebDir: ".", AdminAPIToken: testAdminToken}, fake)
	return httptest.NewServer(router)
}

// postAdmin issues an authenticated POST against an admin route.
func postAdmin(t *testing.T, url string) *stdhttp.Response {
	t.Helper()
	req, err := stdhttp.NewRequest(stdhttp.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set(adminTokenHeader, testAdminToken)
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
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
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			// The gate consults the roster to decide whether the requested id
			// is withheld, and an empty roster now fails closed by design.
			var top domain.PlayerWithScore
			top.ID = 1
			top.Name = "Someone Else"
			top.FRI = 99
			return []domain.PlayerWithScore{top}, nil
		},
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
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			// The vote endpoint now checks whether the target is withheld, so
			// the fake needs a roster; an empty one fails closed by design.
			var other domain.PlayerWithScore
			other.ID = 999
			other.Name = "Someone Else"
			other.FRI = 99
			return []domain.PlayerWithScore{other}, nil
		},
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
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			// The vote endpoint now checks whether the target is withheld, so
			// the fake needs a roster; an empty one fails closed by design.
			var other domain.PlayerWithScore
			other.ID = 999
			other.Name = "Someone Else"
			other.FRI = 99
			return []domain.PlayerWithScore{other}, nil
		},
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
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			// The vote endpoint now checks whether the target is withheld, so
			// the fake needs a roster; an empty one fails closed by design.
			var other domain.PlayerWithScore
			other.ID = 999
			other.Name = "Someone Else"
			other.FRI = 99
			return []domain.PlayerWithScore{other}, nil
		},
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
		resp := postAdmin(t, server.URL+path)
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

	resp := postAdmin(t, server.URL+"/api/sync/media")
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
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			// The per-player gate needs a roster to decide what is withheld;
			// an empty one now fails closed by design.
			var top domain.PlayerWithScore
			top.ID = 999
			top.Name = "Someone Else"
			top.FRI = 99
			return []domain.PlayerWithScore{top}, nil
		},
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

func TestAdminRoutesRejectAnonymousCallers(t *testing.T) {
	// Before accounts existed these were open to anyone who knew the path.
	// A stranger could trigger a full sync — which spends metered
	// API-Football and MediaStack quota — or, once deletion landed, empty
	// the news feed. Anonymous callers must be turned away.
	fake := &fakeService{
		syncMediaFn: func(context.Context) (*domain.ComponentSyncResult, error) {
			t.Error("sync ran for an anonymous caller")
			return nil, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	for _, path := range []string{
		"/api/sync/media", "/api/sync/all", "/api/sync/performance",
		"/api/sync/social", "/api/sync/character", "/api/sync/career-baseline",
	} {
		resp, err := stdhttp.Post(server.URL+path, "application/json", nil)
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != stdhttp.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestAdminRoutesRejectWrongToken(t *testing.T) {
	server := newServerWithFake(t, &fakeService{})
	defer server.Close()

	req, err := stdhttp.NewRequest(stdhttp.MethodPost, server.URL+"/api/sync/media", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set(adminTokenHeader, testAdminToken+"-wrong")
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestLeaderboardWithholdsTopPlacesFromAnonymousCallers(t *testing.T) {
	// The gate has to be real. Blurring in CSS would leave the names and
	// scores in the JSON for anyone who opens devtools, so the API itself
	// must not send them.
	players := make([]domain.PlayerWithScore, 0, 8)
	for i := 0; i < 8; i++ {
		var p domain.PlayerWithScore
		p.ID = int64(i + 1)
		p.Name = "Player " + strconv.Itoa(i+1)
		p.Club = "Club"
		p.FRI = float64(90 - i)
		p.Performance = 80
		players = append(players, p)
	}
	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return players, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/players")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := readBody(resp)

	var payload struct {
		Data []domain.PlayerWithScore `json:"data"`
		Meta struct {
			LockedTop int  `json:"locked_top"`
			SignedIn  bool `json:"signed_in"`
		} `json:"meta"`
	}
	decode(t, body, &payload)

	if len(payload.Data) != len(players) {
		t.Fatalf("got %d rows, want %d — locked places must still occupy the table", len(payload.Data), len(players))
	}
	if payload.Meta.LockedTop != lockedTopN || payload.Meta.SignedIn {
		t.Errorf("meta = %+v, want locked_top=%d signed_in=false", payload.Meta, lockedTopN)
	}

	for i, p := range payload.Data {
		locked := i < lockedTopN
		if p.Locked != locked {
			t.Errorf("row %d: locked = %v, want %v", i, p.Locked, locked)
		}
		if !locked {
			if p.Name == "" || p.FRI == 0 {
				t.Errorf("row %d: visible row lost its data (name=%q fri=%v)", i, p.Name, p.FRI)
			}
			continue
		}
		if p.Name != "" || p.Club != "" || p.FRI != 0 || p.Performance != 0 {
			t.Errorf("row %d leaked withheld data: name=%q club=%q fri=%v perf=%v",
				i, p.Name, p.Club, p.FRI, p.Performance)
		}
	}

	// The raw bytes must not carry a hidden player's name anywhere.
	for i := 0; i < lockedTopN; i++ {
		if bytes.Contains(body, []byte(players[i].Name)) {
			t.Errorf("response body still contains %q", players[i].Name)
		}
	}
}

func TestNewsFeedWithholdsArticlesAboutLockedPlayers(t *testing.T) {
	// The leaderboard gate is only as strong as its weakest surface. The news
	// feed carries player names, photos and headlines — an anonymous visitor
	// reading "Yamal signs new deal" learns exactly who tops a table whose
	// rows we blanked.
	players := make([]domain.PlayerWithScore, 0, 8)
	for i := 0; i < 8; i++ {
		var p domain.PlayerWithScore
		p.ID = int64(i + 1)
		p.Name = "Player " + strconv.Itoa(i+1)
		p.FRI = float64(90 - i)
		players = append(players, p)
	}

	lockedID := int64(1)  // rank 1 — withheld
	visibleID := int64(7) // rank 7 — freely visible
	news := []domain.NewsItem{
		{ID: 10, PlayerID: &lockedID, PlayerName: "Player 1", TitleEN: "Player 1 signs new deal",
			SummaryEN: "Details of the Player 1 contract", SourceURL: "https://example.com/player-1", ImpactDelta: 2.5},
		{ID: 11, PlayerID: &visibleID, PlayerName: "Player 7", TitleEN: "Player 7 scores twice",
			SummaryEN: "Two goals for Player 7", SourceURL: "https://example.com/player-7", ImpactDelta: 1.5},
	}

	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return players, nil
		},
		listNewsFn: func(context.Context, *int64) ([]domain.NewsItem, error) { return news, nil },
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/news/feed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := readBody(resp)

	var payload struct {
		Data []domain.NewsItem `json:"data"`
	}
	decode(t, body, &payload)

	if len(payload.Data) != 2 {
		t.Fatalf("got %d articles, want 2 — masked articles stay in the feed", len(payload.Data))
	}

	locked, visible := payload.Data[0], payload.Data[1]
	if !locked.Locked {
		t.Error("article about a withheld player is not marked locked")
	}
	if locked.PlayerName != "" || locked.TitleEN != "" || locked.SummaryEN != "" || locked.SourceURL != "" {
		t.Errorf("locked article leaked: name=%q title=%q summary=%q url=%q",
			locked.PlayerName, locked.TitleEN, locked.SummaryEN, locked.SourceURL)
	}
	if locked.PlayerID != nil {
		t.Error("locked article still carries player_id — the roster maps it straight back to a name")
	}
	if locked.ImpactDelta == 0 {
		t.Error("impact delta was dropped; it identifies nobody and it is the reason to sign up")
	}

	if visible.Locked || visible.PlayerName != "Player 7" || visible.TitleEN == "" {
		t.Errorf("article about a visible player was masked: %+v", visible)
	}

	// Nothing about the withheld player may survive anywhere in the bytes.
	for _, needle := range []string{"Player 1", "signs new deal", "player-1"} {
		if bytes.Contains(body, []byte(needle)) {
			t.Errorf("news feed body still contains %q", needle)
		}
	}
}

func TestNewsFeedIsUnmaskedForSignedInCallers(t *testing.T) {
	var p domain.PlayerWithScore
	p.ID = 1
	p.Name = "Player 1"
	p.FRI = 90
	id := int64(1)
	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return []domain.PlayerWithScore{p}, nil
		},
		listNewsFn: func(context.Context, *int64) ([]domain.NewsItem, error) {
			return []domain.NewsItem{{ID: 10, PlayerID: &id, PlayerName: "Player 1", TitleEN: "Headline"}}, nil
		},
		sessionUser: &domain.User{ID: 1, Email: "a@b.c"},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	req, err := stdhttp.NewRequest(stdhttp.MethodGet, server.URL+"/api/news/feed", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(&stdhttp.Cookie{Name: sessionCookieName, Value: "any-token"})
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := readBody(resp)
	if !bytes.Contains(body, []byte("Player 1")) {
		t.Errorf("signed-in caller lost the player name: %s", body)
	}
}

func TestLockedPlayerIsUnreachableByID(t *testing.T) {
	// The leaderboard withholds five rows, but there are only twenty-two
	// players — guessing an id is trivial. Every per-player endpoint has to
	// refuse, or the gate is a suggestion.
	players := make([]domain.PlayerWithScore, 0, 8)
	for i := 0; i < 8; i++ {
		var p domain.PlayerWithScore
		p.ID = int64(i + 1)
		p.Name = "Player " + strconv.Itoa(i+1)
		p.FRI = float64(90 - i)
		players = append(players, p)
	}

	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return players, nil
		},
		getPlayerFn: func(_ context.Context, id int64) (*domain.PlayerWithScore, error) {
			for i := range players {
				if players[i].ID == id {
					return &players[i], nil
				}
			}
			return nil, errors.New("not found")
		},
		getHistoryFn: func(context.Context, int64) ([]domain.HistoryPoint, error) {
			return []domain.HistoryPoint{{FRI: 88}}, nil
		},
		listNewsFn: func(context.Context, *int64) ([]domain.NewsItem, error) {
			return []domain.NewsItem{{ID: 1, PlayerName: "Player 1", TitleEN: "Player 1 headline"}}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	// Rank 1 is withheld: every route about them must 404 for an anonymous caller.
	for _, path := range []string{"/api/players/1", "/api/players/1/history", "/api/players/1/news"} {
		resp, err := stdhttp.Get(server.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		body, _ := readBody(resp)
		if resp.StatusCode != stdhttp.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body: %s)", path, resp.StatusCode, body)
		}
		if bytes.Contains(body, []byte("Player 1")) {
			t.Errorf("%s leaked the withheld player's name", path)
		}
	}

	// Rank 7 is freely visible and must stay that way.
	for _, path := range []string{"/api/players/7", "/api/players/7/history", "/api/players/7/news"} {
		resp, err := stdhttp.Get(server.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != stdhttp.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
		}
	}

	// With a session, the withheld player opens up.
	fake.sessionUser = &domain.User{ID: 1, Email: "a@b.c"}
	req, err := stdhttp.NewRequest(stdhttp.MethodGet, server.URL+"/api/players/1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(&stdhttp.Cookie{Name: sessionCookieName, Value: "token"})
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := readBody(resp)
	if resp.StatusCode != stdhttp.StatusOK || !bytes.Contains(body, []byte("Player 1")) {
		t.Errorf("signed-in caller was refused the top player: status=%d body=%s", resp.StatusCode, body)
	}
}

func TestMaskedRowCarriesNothingIdentifying(t *testing.T) {
	// The first version of the mask cleared a hand-written list of fields and
	// missed Slug — which is derived from the name, so "l-yamal" sat in the
	// response naming the player it was meant to withhold. The row is now
	// rebuilt from an empty struct; this asserts that nothing but the flag
	// survives, so a field added to the model later is withheld by default.
	var p domain.PlayerWithScore
	p.ID = 42
	p.Slug = "l-yamal"
	p.Name = "L. Yamal"
	p.Club = "FC Barcelona"
	p.ThemeBackground = "linear-gradient(...)"
	p.PhotoURL = "https://media.example.com/762.png"
	p.FRI = 85.2

	masked := maskLockedPlayers([]domain.PlayerWithScore{p}, map[int64]bool{p.ID: true}, false, false)
	got := masked[0]

	if !got.Locked {
		t.Fatal("row is not marked locked")
	}
	if got.ID != 0 || got.Slug != "" || got.Name != "" || got.Club != "" ||
		got.ThemeBackground != "" || got.PhotoURL != "" || got.FRI != 0 {
		t.Errorf("masked row still carries data: %+v", got)
	}
}

func TestFiltersAreNotAnOracleForWithheldPlayers(t *testing.T) {
	// Masking by index into a *filtered* result made the filters an oracle:
	// ?club=Real+Madrid returned three rows where only one Real Madrid player
	// was nameable, so exactly two of the withheld five played there, and
	// sweeping clubs reconstructed the hidden slots. A filtered response must
	// therefore be identical whether or not its matches happen to be withheld.
	all := make([]domain.PlayerWithScore, 0, 8)
	for i := 0; i < 8; i++ {
		var p domain.PlayerWithScore
		p.ID = int64(i + 1)
		p.Name = "Player " + strconv.Itoa(i+1)
		p.Club = "Visible FC"
		p.FRI = float64(90 - i)
		all = append(all, p)
	}
	// Ranks 1 and 2 (withheld) plus rank 8 (visible) share a club.
	all[0].Club, all[1].Club, all[7].Club = "Secret FC", "Secret FC", "Secret FC"

	fake := &fakeService{
		listPlayersFn: func(_ context.Context, search, position, club string) ([]domain.PlayerWithScore, error) {
			if club == "" && search == "" && position == "" {
				return all, nil
			}
			out := make([]domain.PlayerWithScore, 0, len(all))
			for _, p := range all {
				if club != "" && p.Club != club {
					continue
				}
				if search != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(search)) {
					continue
				}
				out = append(out, p)
			}
			return out, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	get := func(query string) []domain.PlayerWithScore {
		resp, err := stdhttp.Get(server.URL + "/api/players" + query)
		if err != nil {
			t.Fatalf("get %s: %v", query, err)
		}
		body, _ := readBody(resp)
		var payload struct {
			Data []domain.PlayerWithScore `json:"data"`
		}
		decode(t, body, &payload)
		for _, p := range payload.Data {
			if p.Locked {
				t.Errorf("%s: a filtered response returned a placeholder row — its presence still counts the withheld matches", query)
			}
		}
		return payload.Data
	}

	// Three players are at Secret FC but two of them are withheld: the
	// anonymous caller must see exactly the one they are allowed to see.
	if got := get("?club=Secret+FC"); len(got) != 1 || got[0].Name != "Player 8" {
		names := make([]string, 0, len(got))
		for _, p := range got {
			names = append(names, p.Name)
		}
		t.Errorf("club filter returned %v, want just [Player 8]", names)
	}

	// Searching a withheld player by name must not confirm they exist.
	if got := get("?search=Player+1"); len(got) != 0 {
		t.Errorf("search for a withheld player returned %d rows, want 0 — a non-empty answer confirms membership", len(got))
	}

	// The unfiltered leaderboard still shows the ranks, as placeholders.
	resp, err := stdhttp.Get(server.URL + "/api/players")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := readBody(resp)
	var payload struct {
		Data []domain.PlayerWithScore `json:"data"`
	}
	decode(t, body, &payload)
	if len(payload.Data) != len(all) {
		t.Errorf("unfiltered response has %d rows, want %d — the withheld ranks stay visible as placeholders", len(payload.Data), len(all))
	}
}

func TestVoteIsWriteOnlyAndGated(t *testing.T) {
	// The legacy vote endpoint answered with the recomputed score, so a single
	// throwaway vote read back a withheld player's full breakdown.
	players := []domain.PlayerWithScore{}
	for i := 0; i < 8; i++ {
		var p domain.PlayerWithScore
		p.ID = int64(i + 1)
		p.Name = "Player " + strconv.Itoa(i+1)
		p.FRI = float64(90 - i)
		players = append(players, p)
	}
	voted := 0
	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return players, nil
		},
		submitVoteFn: func(context.Context, int64, domain.VoteInput, string) (*domain.Score, error) {
			voted++
			return &domain.Score{PlayerID: 1, FRI: 91, Performance: 93}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	body := `{"rating_overall":5,"rating_hype":8,"opinion":"world_class","behavior":"role_model"}`

	// Withheld player: refused, and the write must not happen either.
	resp, err := stdhttp.Post(server.URL+"/api/players/1/vote", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusNotFound {
		t.Errorf("vote on a withheld player: status = %d, want 404", resp.StatusCode)
	}
	if voted != 0 {
		t.Error("a vote against a withheld player was recorded — an anonymous caller can nudge a hidden score")
	}

	// Visible player: accepted, but the response carries no score.
	resp, err = stdhttp.Post(server.URL+"/api/players/7/vote", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	respBody, _ := readBody(resp)
	if resp.StatusCode != stdhttp.StatusCreated {
		t.Fatalf("vote on a visible player: status = %d, want 201 (%s)", resp.StatusCode, respBody)
	}
	for _, leaked := range []string{"\"fri\"", "\"performance\"", "91", "93"} {
		if bytes.Contains(respBody, []byte(leaked)) {
			t.Errorf("vote response echoes the score (%s): %s", leaked, respBody)
		}
	}
}

func TestNewsMaskCatchesArticlesWithoutAPlayerID(t *testing.T) {
	// news_items keeps a denormalized player_name beside the foreign key, and
	// the seed only sets the key on an exact name match — so an article can
	// name a withheld player with player_id NULL. A mask keyed on the id alone
	// waves those straight through.
	var top domain.PlayerWithScore
	top.ID = 1
	top.Name = "K. Mbappé"
	top.FRI = 91

	fake := &fakeService{
		listPlayersFn: func(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
			return []domain.PlayerWithScore{top}, nil
		},
		listNewsFn: func(context.Context, *int64) ([]domain.NewsItem, error) {
			return []domain.NewsItem{{
				ID:         11,
				PlayerID:   nil, // no foreign key — only the name says who this is
				PlayerName: "K. Mbappé",
				TitleEN:    "Mbappé scores hat-trick",
				SourceURL:  "https://espn.com/k-mbappe-hat-trick",
			}}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/news/feed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := readBody(resp)
	for _, needle := range []string{"Mbappé", "Mbappe", "hat-trick", "k-mbappe"} {
		if bytes.Contains(body, []byte(needle)) {
			t.Errorf("news feed leaked %q for an article with a null player_id: %s", needle, body)
		}
	}
}

func deleteAsAdmin(t *testing.T, url string) *stdhttp.Response {
	t.Helper()
	req, err := stdhttp.NewRequest(stdhttp.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set(adminTokenHeader, testAdminToken)
	resp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete %s: %v", url, err)
	}
	return resp
}

func TestDeletingAnArticleReportsWhatItDidToTheScore(t *testing.T) {
	// The partner deleted articles and watched nothing move. The handler now
	// hands back the rescored player, so the UI can show the change instead
	// of asking the moderator to trust that it happened.
	var askedFor int64
	fake := &fakeService{
		deleteNewsFn: func(_ context.Context, id int64) (*domain.NewsDeletion, error) {
			askedFor = id
			return &domain.NewsDeletion{
				NewsID: id, PlayerID: 25, PlayerName: "B. Barcola", Title: "Transfer news LIVE",
				OldMedia: 74.8, NewMedia: 61.2, OldFRI: 62.9, NewFRI: 60.2, Remaining: 2,
			}, nil
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp := deleteAsAdmin(t, server.URL+"/api/news/4242")
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if askedFor != 4242 {
		t.Errorf("deleted id %d, want 4242", askedFor)
	}
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Data domain.NewsDeletion `json:"data"`
	}
	decode(t, body, &payload)
	if payload.Data.PlayerName != "B. Barcola" || payload.Data.NewFRI != 60.2 || payload.Data.OldMedia != 74.8 {
		t.Errorf("payload lost the score change: %+v", payload.Data)
	}
}

func TestDeletingAnArticleThatIsAlreadyGoneIs404(t *testing.T) {
	// Syncs rotate the feed twice a day; an id from a stale tab may be gone.
	// That is a 404, not a 500 — nothing broke.
	fake := &fakeService{
		deleteNewsFn: func(context.Context, int64) (*domain.NewsDeletion, error) {
			return nil, service.ErrNewsNotFound
		},
	}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp := deleteAsAdmin(t, server.URL+"/api/news/1")
	defer resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
