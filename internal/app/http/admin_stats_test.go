package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/service"
	"github.com/gin-gonic/gin"
)

func TestTrafficSectionCountsVisitsNotAssets(t *testing.T) {
	// One visit is one page load. Counting the script, the logo and the
	// stylesheet alongside it would report four.
	skipped := []string{"/assets/live.js", "/assets/logo.png", "/assets/logo@2x.png", "/api/health"}
	for _, path := range skipped {
		if got := trafficSection(path); got != "" {
			t.Errorf("trafficSection(%q) = %q, want it skipped", path, got)
		}
	}
	pages := map[string]string{
		"/":            "home",
		"":             "home",
		"/leaderboard": "leaderboard",
		"/vote":        "vote",
		"/about":       "about",
		"/admin":       "admin",
	}
	for path, want := range pages {
		if got := trafficSection(path); got != want {
			t.Errorf("trafficSection(%q) = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"/api/players", "/api/news/feed", "/api/auth/me"} {
		if got := trafficSection(path); got != "api" {
			t.Errorf("trafficSection(%q) = %q, want api", path, got)
		}
	}
	// A scanner must not be able to add a row per invented path.
	for _, path := range []string{"/wp-login.php", "/.env", "/some/deep/thing"} {
		if got := trafficSection(path); got != "other" {
			t.Errorf("trafficSection(%q) = %q, want other", path, got)
		}
	}
}

func TestVisitorHashRotatesDailyAndKeepsNoAddress(t *testing.T) {
	day1 := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	ip := "203.0.113.7"

	a := service.TrafficVisitorHash(ip, day1)
	b := service.TrafficVisitorHash(ip, day1.Add(19*time.Hour)) // same day, later
	c := service.TrafficVisitorHash(ip, day2)

	if a == "" || a != b {
		t.Errorf("same visitor on the same day hashed differently: %q vs %q", a, b)
	}
	if a == c {
		t.Error("hash did not rotate overnight — the rows would track one person across days")
	}
	if service.TrafficVisitorHash("", day1) != "" {
		t.Error("no address should produce no hash")
	}
	// The address itself must not be recoverable by reading the value.
	if a == ip || len(a) != 32 {
		t.Errorf("hash = %q, want a 32-char digest that is not the address", a)
	}
}

func TestClientAddressLooksBehindTheProxy(t *testing.T) {
	// Caddy forwards the real address in a header. If this fell through to
	// the connection's own address, every visitor would be the proxy and the
	// dashboard would report one unique visitor a day forever.
	newCtx := func(headers map[string]string) *gin.Context {
		req := httptest.NewRequest(stdhttp.MethodGet, "/", nil)
		req.RemoteAddr = "172.18.0.1:5555" // the docker network's proxy
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		return c
	}
	if got := clientAddress(newCtx(map[string]string{"X-Forwarded-For": "203.0.113.7, 10.0.0.1"})); got != "203.0.113.7" {
		t.Errorf("got %q, want the left-most forwarded address", got)
	}
	if got := clientAddress(newCtx(map[string]string{"X-Real-IP": "203.0.113.8"})); got != "203.0.113.8" {
		t.Errorf("got %q, want the X-Real-IP address", got)
	}
	if got := clientAddress(newCtx(nil)); got == "" {
		t.Error("with no headers it should still fall back to the connection address")
	}
}

// trafficFake records what the middleware reports.
type trafficFake struct {
	fakeService
	mu    sync.Mutex
	calls []string
	stats *domain.TrafficStats
}

func (f *trafficFake) RecordView(section, visitorHash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry := section
	if visitorHash != "" {
		entry += "+visitor"
	}
	f.calls = append(f.calls, entry)
}

func (f *trafficFake) TrafficStats(context.Context) (*domain.TrafficStats, error) {
	if f.stats == nil {
		return nil, service.ErrTrafficUnavailable
	}
	return f.stats, nil
}

func (f *trafficFake) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestMiddlewareRecordsPageLoadsWithAVisitor(t *testing.T) {
	fake := &trafficFake{stats: &domain.TrafficStats{}}
	server := newServerWithFake(t, fake)
	defer server.Close()

	for _, path := range []string{"/leaderboard", "/api/players", "/assets/live.js"} {
		req, err := stdhttp.NewRequest(stdhttp.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		resp, err := stdhttp.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		resp.Body.Close()
	}

	got := fake.recorded()
	want := []string{"leaderboard+visitor", "api"}
	if len(got) != len(want) {
		t.Fatalf("recorded %v, want %v — assets must not be counted", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recorded[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAdminStatsNeedsAdmin(t *testing.T) {
	fake := &trafficFake{stats: &domain.TrafficStats{
		Users: domain.UserTotals{Total: 42, NewLast7Days: 5},
	}}
	server := newServerWithFake(t, fake)
	defer server.Close()

	// Account numbers and traffic are business information; an anonymous
	// caller must not read them.
	resp, err := stdhttp.Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusUnauthorized && resp.StatusCode != stdhttp.StatusForbidden {
		t.Errorf("anonymous status = %d, want 401 or 403", resp.StatusCode)
	}

	req, err := stdhttp.NewRequest(stdhttp.MethodGet, server.URL+"/api/stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(adminTokenHeader, testAdminToken)
	adminResp, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer adminResp.Body.Close()
	if adminResp.StatusCode != stdhttp.StatusOK {
		t.Errorf("admin status = %d, want 200", adminResp.StatusCode)
	}
}

func TestUserCountIsNotPublic(t *testing.T) {
	// How many people signed up is a business number. This endpoint answered
	// anyone who asked until 2026-09-09, and nothing on the site read it.
	fake := &trafficFake{stats: &domain.TrafficStats{}}
	server := newServerWithFake(t, fake)
	defer server.Close()

	resp, err := stdhttp.Get(server.URL + "/api/stats/users")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == stdhttp.StatusOK {
		t.Error("the account count is readable without an admin account")
	}
}
