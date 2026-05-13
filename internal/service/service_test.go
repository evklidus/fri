package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// mockRepo is a programmable in-memory implementation of the repository
// interface used by Service. Each method is overridable per test through a
// function field; nil fields fall back to a sensible default.
type mockRepo struct {
	playerCount             int
	playerCountErr          error
	replaceAllErr           error
	createVoteFn            func(ctx context.Context, vote domain.Vote) (*domain.Score, error)
	hasRecentVoteFn         func() (bool, error)
	listSyncTargetsFn       func(ctx context.Context) ([]domain.PlayerSyncTarget, error)
	startComponentUpdateFn  func(ctx context.Context, component, provider string) (int64, error)
	finishComponentUpdateFn func(ctx context.Context, updateID int64, status, message string, recordsSeen int) error
	applyMediaSyncFn        func(ctx context.Context, results []domain.MediaSyncPlayerResult, provider string) ([]domain.PlayerSyncDelta, error)
	applyCharacterSyncFn    func(ctx context.Context, candidates []domain.CharacterEventCandidate, perPlayerCap float64) ([]domain.PlayerSyncDelta, error)
	getCareerBaselineFn     func(ctx context.Context, playerID int64) (*domain.PlayerCareerBaseline, error)
	upsertCareerBaselineFn  func(ctx context.Context, baseline domain.PlayerCareerBaseline) error
	listNewsFn              func(ctx context.Context, playerID *int64) ([]domain.NewsItem, error)

	// captures
	replacePlayers []domain.PlayerWithScore
	replaceHistory map[string][]domain.HistoryPoint
	replaceNews    []domain.NewsItem
	createdVote    *domain.Vote
}

func (m *mockRepo) PlayerCount(context.Context) (int, error) { return m.playerCount, m.playerCountErr }

func (m *mockRepo) ReplaceAllSeedData(_ context.Context, players []domain.PlayerWithScore, history map[string][]domain.HistoryPoint, news []domain.NewsItem) error {
	m.replacePlayers = players
	m.replaceHistory = history
	m.replaceNews = news
	return m.replaceAllErr
}

func (m *mockRepo) ListPlayers(context.Context, string, string, string) ([]domain.PlayerWithScore, error) {
	return nil, nil
}
func (m *mockRepo) GetPlayer(context.Context, int64) (*domain.PlayerWithScore, error) {
	return nil, nil
}
func (m *mockRepo) ListSyncTargets(ctx context.Context) ([]domain.PlayerSyncTarget, error) {
	if m.listSyncTargetsFn != nil {
		return m.listSyncTargetsFn(ctx)
	}
	return nil, nil
}
func (m *mockRepo) GetHistory(context.Context, int64) ([]domain.HistoryPoint, error) { return nil, nil }
func (m *mockRepo) ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error) {
	if m.listNewsFn != nil {
		return m.listNewsFn(ctx, playerID)
	}
	return nil, nil
}
func (m *mockRepo) CreateVoteAndRefreshScore(ctx context.Context, vote domain.Vote) (*domain.Score, error) {
	m.createdVote = &vote
	if m.createVoteFn != nil {
		return m.createVoteFn(ctx, vote)
	}
	return &domain.Score{PlayerID: vote.PlayerID, FRI: 80}, nil
}
func (m *mockRepo) StartComponentUpdate(ctx context.Context, component, provider string) (int64, error) {
	if m.startComponentUpdateFn != nil {
		return m.startComponentUpdateFn(ctx, component, provider)
	}
	return 1, nil
}
func (m *mockRepo) FinishComponentUpdate(ctx context.Context, updateID int64, status, message string, recordsSeen int) error {
	if m.finishComponentUpdateFn != nil {
		return m.finishComponentUpdateFn(ctx, updateID, status, message, recordsSeen)
	}
	return nil
}
func (m *mockRepo) ListComponentUpdates(context.Context, int) ([]domain.ComponentUpdate, error) {
	return nil, nil
}
func (m *mockRepo) ApplySocialSync(context.Context, []domain.SocialSnapshot, string) ([]domain.PlayerSyncDelta, error) {
	return nil, nil
}
func (m *mockRepo) ApplyPerformanceSync(context.Context, []domain.PerformanceSnapshot, string) ([]domain.PlayerSyncDelta, error) {
	return nil, nil
}
func (m *mockRepo) ApplyMediaSync(ctx context.Context, results []domain.MediaSyncPlayerResult, provider string) ([]domain.PlayerSyncDelta, error) {
	if m.applyMediaSyncFn != nil {
		return m.applyMediaSyncFn(ctx, results, provider)
	}
	return nil, nil
}
func (m *mockRepo) GetExternalIDs(context.Context, int64, string) (*domain.PlayerExternalIDs, error) {
	return nil, nil
}
func (m *mockRepo) UpsertExternalIDs(context.Context, domain.PlayerExternalIDs) error { return nil }
func (m *mockRepo) DeleteExternalIDs(context.Context, int64, string) error            { return nil }
func (m *mockRepo) HasRecentVote(_ context.Context, _ int64, _ string, _ time.Time) (bool, error) {
	if m.hasRecentVoteFn != nil {
		return m.hasRecentVoteFn()
	}
	return false, nil
}
func (m *mockRepo) ApplyCharacterSync(ctx context.Context, candidates []domain.CharacterEventCandidate, perPlayerCap float64) ([]domain.PlayerSyncDelta, error) {
	if m.applyCharacterSyncFn != nil {
		return m.applyCharacterSyncFn(ctx, candidates, perPlayerCap)
	}
	return nil, nil
}
func (m *mockRepo) GetCareerBaseline(ctx context.Context, playerID int64) (*domain.PlayerCareerBaseline, error) {
	if m.getCareerBaselineFn != nil {
		return m.getCareerBaselineFn(ctx, playerID)
	}
	return nil, nil
}
func (m *mockRepo) UpsertCareerBaseline(ctx context.Context, baseline domain.PlayerCareerBaseline) error {
	if m.upsertCareerBaselineFn != nil {
		return m.upsertCareerBaselineFn(ctx, baseline)
	}
	return nil
}
func (m *mockRepo) ListPendingEvents(ctx context.Context, limit int) ([]domain.PendingEvent, error) {
	return nil, nil
}
func (m *mockRepo) ListPendingEventsForPlayer(ctx context.Context, playerID int64, limit int) ([]domain.PendingEvent, error) {
	return nil, nil
}
func (m *mockRepo) GetPendingEvent(ctx context.Context, eventID int64) (*domain.PendingEvent, error) {
	return nil, nil
}
func (m *mockRepo) SubmitEventVote(ctx context.Context, eventID int64, ipHash string, suggestedDelta float64) (bool, error) {
	return true, nil
}
func (m *mockRepo) FinalizePendingEvents(ctx context.Context) (int, error) {
	return 0, nil
}

func newServiceWithRepo(repo *mockRepo) *Service {
	return New(repo, nil, nil, nil)
}

// ----- SubmitVote -----

func TestSubmitVoteValidatesRanges(t *testing.T) {
	repo := &mockRepo{}
	svc := newServiceWithRepo(repo)

	cases := []struct {
		name string
		in   domain.VoteInput
	}{
		{"overall_too_high", domain.VoteInput{RatingOverall: 6, RatingHype: 5, RatingTier: 50, BehaviorScore: 50}},
		{"overall_too_low", domain.VoteInput{RatingOverall: 0, RatingHype: 5, RatingTier: 50, BehaviorScore: 50}},
		{"hype_too_high", domain.VoteInput{RatingOverall: 5, RatingHype: 11, RatingTier: 50, BehaviorScore: 50}},
		{"tier_too_high", domain.VoteInput{RatingOverall: 5, RatingHype: 5, RatingTier: 101, BehaviorScore: 50}},
		{"behavior_too_high", domain.VoteInput{RatingOverall: 5, RatingHype: 5, RatingTier: 50, BehaviorScore: 101}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SubmitVote(context.Background(), 1, tc.in, "1.2.3.4")
			if err == nil {
				t.Errorf("expected validation error for %+v", tc.in)
			}
		})
	}
}

func TestSubmitVoteHashesIPAndComputesInternalScore(t *testing.T) {
	repo := &mockRepo{}
	svc := newServiceWithRepo(repo)

	score, err := svc.SubmitVote(context.Background(), 7, domain.VoteInput{
		SessionID:     "session-x",
		RatingOverall: 5,
		RatingHype:    8,
		RatingTier:    80,
		BehaviorScore: 70,
	}, "10.0.0.1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if score == nil {
		t.Fatal("expected score, got nil")
	}
	if repo.createdVote == nil {
		t.Fatal("vote not forwarded to repo")
	}
	if repo.createdVote.PlayerID != 7 {
		t.Errorf("forwarded player id = %d, want 7", repo.createdVote.PlayerID)
	}
	if len(repo.createdVote.IPHash) != 64 {
		t.Errorf("IP hash length = %d, want sha256 hex length 64", len(repo.createdVote.IPHash))
	}
	if repo.createdVote.IPHash == "10.0.0.1" {
		t.Errorf("raw IP must not be persisted")
	}
	// (5*20)*0.4 + (8*10)*0.3 + 80*0.2 + 70*0.1 = 40 + 24 + 16 + 7 = 87
	if repo.createdVote.InternalScore != 87 {
		t.Errorf("internal score = %v, want 87", repo.createdVote.InternalScore)
	}
}

func TestSubmitVoteAutoFillsSessionID(t *testing.T) {
	repo := &mockRepo{}
	svc := newServiceWithRepo(repo)

	_, err := svc.SubmitVote(context.Background(), 1, domain.VoteInput{
		RatingOverall: 4, RatingHype: 7, RatingTier: 60, BehaviorScore: 60,
	}, "1.1.1.1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if repo.createdVote == nil {
		t.Fatal("no vote captured")
	}
	if !strings.HasPrefix(repo.createdVote.SessionID, "session-") {
		t.Errorf("auto session id should start with 'session-', got %q", repo.createdVote.SessionID)
	}
}

func TestSubmitVoteRejectsWhenRecentVoteExists(t *testing.T) {
	repo := &mockRepo{
		hasRecentVoteFn: func() (bool, error) { return true, nil },
	}
	svc := newServiceWithRepo(repo)

	_, err := svc.SubmitVote(context.Background(), 1, domain.VoteInput{
		RatingOverall: 5, RatingHype: 5, RatingTier: 50, BehaviorScore: 50,
	}, "1.2.3.4")
	if err == nil {
		t.Fatal("expected rate-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error should mention rate limit, got: %v", err)
	}
	if repo.createdVote != nil {
		t.Errorf("vote should NOT have been persisted on rate-limit reject")
	}
}

func TestSubmitVoteAllowedWhenNoRecentVote(t *testing.T) {
	repo := &mockRepo{
		hasRecentVoteFn: func() (bool, error) { return false, nil },
	}
	svc := newServiceWithRepo(repo)

	if _, err := svc.SubmitVote(context.Background(), 1, domain.VoteInput{
		RatingOverall: 5, RatingHype: 5, RatingTier: 50, BehaviorScore: 50,
	}, "1.2.3.4"); err != nil {
		t.Fatalf("unexpected error when no recent vote: %v", err)
	}
	if repo.createdVote == nil {
		t.Errorf("vote should have been persisted")
	}
}

func TestSubmitVoteSkipsRateLimitWhenIPMissing(t *testing.T) {
	repo := &mockRepo{
		hasRecentVoteFn: func() (bool, error) {
			t.Errorf("HasRecentVote should not be called for empty IP")
			return false, nil
		},
	}
	svc := newServiceWithRepo(repo)

	if _, err := svc.SubmitVote(context.Background(), 1, domain.VoteInput{
		RatingOverall: 5, RatingHype: 5, RatingTier: 50, BehaviorScore: 50,
	}, ""); err != nil {
		t.Fatalf("unexpected error with empty IP: %v", err)
	}
}

func TestSubmitVotePropagatesRepoError(t *testing.T) {
	repo := &mockRepo{
		createVoteFn: func(context.Context, domain.Vote) (*domain.Score, error) {
			return nil, errors.New("db locked")
		},
	}
	svc := newServiceWithRepo(repo)

	_, err := svc.SubmitVote(context.Background(), 1, domain.VoteInput{
		RatingOverall: 5, RatingHype: 5, RatingTier: 50, BehaviorScore: 50,
	}, "1.1.1.1")
	if err == nil {
		t.Error("expected repo error to propagate")
	}
}

// ----- SeedIfEmpty / ForceSeed -----

func writeMinimalLegacyHTML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fri-index.html")
	body := `<html><body>
<script>
const players = [
  { rank: 1, emoji: "🇦🇷", name: "Lionel Messi", club: "Inter Miami", pos: "RW", age: 38, fri: 91.5, perf: 90, social: 95, fan: 92, media: 89, char: 88, trend: "0.4", dir: "up", bg: "", photo: "", sumEN: "GOAT", sumRU: "ГОАТ" },
  { rank: 2, emoji: "🇳🇴", name: "Erling Haaland", club: "Manchester City", pos: "ST", age: 25, fri: 90.0, perf: 95, social: 85, fan: 88, media: 82, char: 80, trend: "0.2", dir: "stable", bg: "", photo: "", sumEN: "Goal machine", sumRU: "Машина голов" }
];
const news = [
  { player: "Lionel Messi", impact: "pos", delta: "+0.5", time: "2h", titleEN: "Messi scored", titleRU: "Месси забил", summaryEN: "Brilliant goal", summaryRU: "Великолепный гол" }
];
</script>
</body></html>`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestSeedIfEmptySkipsWhenNotEmpty(t *testing.T) {
	repo := &mockRepo{playerCount: 27}
	svc := newServiceWithRepo(repo)

	if err := svc.SeedIfEmpty(context.Background(), "/nonexistent.html"); err != nil {
		t.Fatalf("seed if empty: %v", err)
	}
	if repo.replacePlayers != nil {
		t.Errorf("ReplaceAllSeedData should not be called when DB is non-empty")
	}
}

func TestSeedIfEmptyPropagatesPlayerCountError(t *testing.T) {
	repo := &mockRepo{playerCountErr: errors.New("db down")}
	svc := newServiceWithRepo(repo)

	if err := svc.SeedIfEmpty(context.Background(), "/nope"); err == nil {
		t.Error("expected error to propagate from PlayerCount")
	}
}

func TestForceSeedParsesAndForwardsLegacyHTML(t *testing.T) {
	path := writeMinimalLegacyHTML(t)
	repo := &mockRepo{}
	svc := newServiceWithRepo(repo)

	if err := svc.ForceSeed(context.Background(), path); err != nil {
		t.Fatalf("force seed: %v", err)
	}

	if got := len(repo.replacePlayers); got != 2 {
		t.Fatalf("seeded players = %d, want 2", got)
	}
	if repo.replacePlayers[0].Name != "Lionel Messi" {
		t.Errorf("first player = %q, want Lionel Messi", repo.replacePlayers[0].Name)
	}
	if repo.replacePlayers[0].FRI != 91.5 {
		t.Errorf("FRI = %v, want 91.5", repo.replacePlayers[0].FRI)
	}
	// History per player: 30d ago, 7d ago, now.
	if got := len(repo.replaceHistory["Lionel Messi"]); got != 3 {
		t.Errorf("history points = %d, want 3", got)
	}
	if got := len(repo.replaceNews); got != 1 {
		t.Errorf("seeded news = %d, want 1", got)
	}
}

func TestSeedIfEmptyTriggersForceSeedWhenEmpty(t *testing.T) {
	path := writeMinimalLegacyHTML(t)
	repo := &mockRepo{playerCount: 0}
	svc := newServiceWithRepo(repo)

	if err := svc.SeedIfEmpty(context.Background(), path); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(repo.replacePlayers) != 2 {
		t.Errorf("expected 2 players seeded, got %d", len(repo.replacePlayers))
	}
}
