package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// trafficRepo is a mockRepo that also records traffic, so the collector's
// store assertion succeeds.
type trafficRepo struct {
	mockRepo
	mu      sync.Mutex
	flushes []trafficFlush
}

type trafficFlush struct {
	day      time.Time
	views    map[domain.EntryPoint]int64
	visitors []string
}

func (r *trafficRepo) FlushTraffic(_ context.Context, views map[domain.EntryPoint]int64, visitors []string, day time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes = append(r.flushes, trafficFlush{day: day, views: views, visitors: visitors})
	return nil
}
func (r *trafficRepo) TrafficDays(context.Context, int) ([]domain.TrafficDay, error) {
	return nil, nil
}
func (r *trafficRepo) EntryPoints(context.Context, int) ([]domain.EntryPoint, error) { return nil, nil }
func (r *trafficRepo) SignupDays(context.Context, int) ([]domain.SignupDay, error)   { return nil, nil }
func (r *trafficRepo) UserTotals(context.Context) (domain.UserTotals, error) {
	return domain.UserTotals{Total: 3}, nil
}
func (r *trafficRepo) ContentTotals(context.Context) (domain.ContentTotals, error) {
	return domain.ContentTotals{Players: 25}, nil
}
func (r *trafficRepo) written() []trafficFlush {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]trafficFlush(nil), r.flushes...)
}

func TestViewsAggregateInMemoryAndFlushOnce(t *testing.T) {
	// The point of the buffer: a thousand requests must cost one small
	// transaction, not a thousand. Requests only touch memory.
	repo := &trafficRepo{}
	svc := New(repo, nil, nil, nil)

	for i := 0; i < 500; i++ {
		svc.RecordView("leaderboard", "visitor-a")
	}
	for i := 0; i < 300; i++ {
		svc.RecordView("home", "visitor-b")
	}
	svc.RecordView("api", "") // API calls carry no visitor

	if len(repo.written()) != 0 {
		t.Fatalf("wrote %d times before a flush — the buffer is not buffering", len(repo.written()))
	}

	svc.flushTraffic(context.Background())

	flushes := repo.written()
	if len(flushes) != 1 {
		t.Fatalf("flushes = %d, want 1", len(flushes))
	}
	got := flushes[0]
	if got.views[domain.EntryPoint{Section: "leaderboard"}] != 500 {
		t.Errorf("leaderboard views = %d, want 500", got.views[domain.EntryPoint{Section: "leaderboard"}])
	}
	if got.views[domain.EntryPoint{Section: "home"}] != 300 {
		t.Errorf("home views = %d, want 300", got.views[domain.EntryPoint{Section: "home"}])
	}
	if got.views[domain.EntryPoint{Section: "api"}] != 1 {
		t.Errorf("api views = %d, want 1", got.views[domain.EntryPoint{Section: "api"}])
	}
	// Two people, eight hundred requests.
	if len(got.visitors) != 2 {
		t.Errorf("visitors = %v, want 2 distinct", got.visitors)
	}

	// A second flush with nothing buffered must not write an empty row.
	svc.flushTraffic(context.Background())
	if len(repo.written()) != 1 {
		t.Errorf("an empty flush wrote anyway: %d", len(repo.written()))
	}
}

func TestMidnightFilesYesterdayUnderYesterday(t *testing.T) {
	// A quiet night used to be the failure here: requests buffered before
	// midnight would be written with tomorrow's date, moving traffic between
	// days.
	repo := &trafficRepo{}
	svc := New(repo, nil, nil, nil)

	svc.RecordView("home", "visitor-a")
	yesterday := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	svc.traffic.mu.Lock()
	svc.traffic.day = yesterday // pretend the buffer was filled yesterday
	svc.traffic.mu.Unlock()

	svc.RecordView("home", "visitor-b") // first request after midnight

	flushes := repo.written()
	if len(flushes) != 1 {
		t.Fatalf("crossing midnight flushed %d times, want 1", len(flushes))
	}
	if !flushes[0].day.Equal(yesterday) {
		t.Errorf("yesterday's traffic was filed under %s, want %s",
			flushes[0].day.Format("2006-01-02"), yesterday.Format("2006-01-02"))
	}

	svc.flushTraffic(context.Background())
	today := repo.written()[1]
	if !today.day.Equal(time.Now().UTC().Truncate(24 * time.Hour)) {
		t.Errorf("today's traffic filed under %s", today.day.Format("2006-01-02"))
	}
	if today.views[domain.EntryPoint{Section: "home"}] != 1 {
		t.Error("the request that crossed midnight was lost or double-counted")
	}
}

func TestTrafficStatsFlushesBeforeReading(t *testing.T) {
	// An admin who reloads right after a visit should see that visit, not
	// wonder whether the counter works.
	repo := &trafficRepo{}
	svc := New(repo, nil, nil, nil)
	svc.RecordView("admin", "visitor-a")

	stats, err := svc.TrafficStats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(repo.written()) != 1 {
		t.Error("reading the dashboard did not flush what was buffered")
	}
	if stats.Users.Total != 3 || stats.Content.Players != 25 {
		t.Errorf("stats did not come from the store: %+v", stats)
	}
	if stats.WindowDays != trafficStatsWindow {
		t.Errorf("window = %d, want %d", stats.WindowDays, trafficStatsWindow)
	}
}

func TestTrafficStatsUnavailableWithoutAStore(t *testing.T) {
	// A repository that does not record traffic must say so rather than
	// reporting zeroes as though nobody visited.
	svc := New(&mockRepo{}, nil, nil, nil)
	svc.RecordView("home", "visitor-a") // must not panic
	if _, err := svc.TrafficStats(context.Background()); err == nil {
		t.Error("want ErrTrafficUnavailable, got no error")
	}
}
