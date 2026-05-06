package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

func TestMediaStackFetchesEnAndRu(t *testing.T) {
	var mu sync.Mutex
	var calls []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Query())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("languages") {
		case "en":
			_, _ = w.Write([]byte(`{
				"pagination":{"limit":25,"offset":0,"count":2,"total":2},
				"data":[
					{"title":"Messi scored a brilliant goal","description":"Masterclass display","url":"https://bbc.com/a","source":"BBC","language":"en","published_at":"2026-05-05T10:00:00+00:00"},
					{"title":"Inter Miami win","description":"Late winner","url":"https://espn.com/b","source":"ESPN","language":"en","published_at":"2026-05-04T18:00:00+00:00"}
				],
				"error":{}
			}`))
		case "ru":
			_, _ = w.Write([]byte(`{
				"pagination":{"limit":25,"offset":0,"count":1,"total":1},
				"data":[
					{"title":"Месси забил исторический гол","description":"Великолепная игра","url":"https://example.ru/a","source":"Sport Express","language":"ru","published_at":"2026-05-05T12:00:00+00:00"}
				],
				"error":{}
			}`))
		}
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 0
	candidates, err := provider.FetchPlayerArticles(context.Background(), domain.PlayerSyncTarget{Name: "Lionel Messi"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(candidates) != 3 {
		t.Errorf("got %d candidates, want 3 (2 EN + 1 RU)", len(candidates))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Errorf("expected 2 calls (EN + RU), got %d", len(calls))
	}
	hasEN, hasRU := false, false
	for _, q := range calls {
		switch q.Get("languages") {
		case "en":
			hasEN = true
		case "ru":
			hasRU = true
		}
		if got := q.Get("access_key"); got != "test-key" {
			t.Errorf("access_key = %q, want test-key", got)
		}
		if got := q.Get("keywords"); got != `"Lionel Messi"` {
			t.Errorf("keywords = %q, want quoted phrase", got)
		}
	}
	if !hasEN || !hasRU {
		t.Errorf("expected both EN and RU calls, got %v", calls)
	}
}

func TestMediaStackReturnsErrorOn200WithErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data":[],
			"error":{"code":"usage_limit_reached","message":"Your monthly usage limit has been reached."}
		}`))
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 0
	if _, err := provider.fetch(context.Background(), "Lionel Messi", "en", "2026-04-05", "2026-05-05"); err == nil {
		t.Error("expected error from 200+error payload")
	}
}

func TestMediaStackHandlesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 0
	if _, err := provider.fetch(context.Background(), "Lionel Messi", "en", "2026-04-05", "2026-05-05"); err == nil {
		t.Error("expected error from non-200 response")
	}
}

func TestMediaStackRetriesOn429ThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if current == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"title":"Real article","url":"https://bbc.com/a","source":"BBC","language":"en","published_at":"2026-05-05T10:00:00+00:00"}],"error":{}}`))
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 0

	// Override global backoff via package-level constant — instead use a tight
	// context to force the timer to fire quickly. Backoff is 5s; bump deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	candidates, err := provider.fetch(ctx, "Lionel Messi", "en", "2026-04-05", "2026-05-05")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(candidates) != 1 {
		t.Errorf("expected 1 article after retry, got %d", len(candidates))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("expected 2 calls (initial 429 + retry), got %d", calls)
	}
}

func TestMediaStackRetriesOnEmbeddedRateLimitError(t *testing.T) {
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if current == 1 {
			// MediaStack quirk: 200 OK with embedded error.code=rate_limit_reached.
			_, _ = w.Write([]byte(`{"data":[],"error":{"code":"rate_limit_reached","message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"title":"After retry","url":"https://x.com/a","source":"X","language":"en","published_at":"2026-05-05T10:00:00+00:00"}],"error":{}}`))
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 0

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	candidates, err := provider.fetch(ctx, "Lionel Messi", "en", "2026-04-05", "2026-05-05")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(candidates) != 1 {
		t.Errorf("expected 1 article after retry, got %d", len(candidates))
	}
}

func TestMediaStackHonoursRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"error":{}}`))
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 80 * time.Millisecond

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := provider.respectRateLimit(context.Background()); err != nil {
			t.Fatalf("respectRateLimit: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 160*time.Millisecond {
		t.Errorf("expected at least 160ms across 3 rate-limited calls, got %v", elapsed)
	}
}

func TestMediaStackDedupAcrossLanguages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Same article returned for both languages — must dedupe.
		_, _ = w.Write([]byte(`{
			"data":[
				{"title":"Messi scored a goal","description":"Goal","url":"https://bbc.com/a","source":"BBC","language":"en","published_at":"2026-05-05T10:00:00+00:00"}
			],
			"error":{}
		}`))
	}))
	defer server.Close()

	provider := newMediaStackMediaProvider("test-key", server.URL, time.Second, 5).(*mediaStackMediaProvider)
	provider.minGap = 0
	candidates, err := provider.FetchPlayerArticles(context.Background(), domain.PlayerSyncTarget{Name: "Messi"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(candidates) != 1 {
		t.Errorf("got %d candidates, want 1 (deduplicated by title+source)", len(candidates))
	}
}

func TestMediaStackProviderName(t *testing.T) {
	provider := newMediaStackMediaProvider("k", "", time.Second, 5)
	if provider.Name() != mediaStackProviderName {
		t.Errorf("Name() = %q, want %q", provider.Name(), mediaStackProviderName)
	}
}

func TestNewMediaProviderFactoryUsesMediaStackWhenKeySet(t *testing.T) {
	p := NewMediaProvider(0, 5, "some-key", "http://example.com/v1")
	if p.Name() != mediaStackProviderName {
		t.Errorf("provider = %q, want mediastack", p.Name())
	}
}

func TestNewMediaProviderFactoryFallsBackToGDELTWithoutKey(t *testing.T) {
	p := NewMediaProvider(0, 5, "", "")
	if p.Name() != gdeltMediaProviderName {
		t.Errorf("provider = %q, want gdelt fallback", p.Name())
	}
}
