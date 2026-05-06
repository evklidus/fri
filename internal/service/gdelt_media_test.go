package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

func TestGDELTFetchesAndDedupes(t *testing.T) {
	var mu sync.Mutex
	var queries []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		lang := "en"
		if strings.Contains(r.URL.Query().Get("query"), "sourcelang:rus") {
			lang = "ru"
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if lang == "ru" {
			_, _ = w.Write([]byte(`{"articles":[
				{"url":"https://example.ru/a","title":"Месси забил исторический гол","domain":"example.ru","seendate":"20260501T120000Z","language":"Russian"}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"articles":[
			{"url":"https://bbc.com/a","title":"Messi scored a brilliant goal","domain":"bbc.com","seendate":"20260501T100000Z","language":"English"},
			{"url":"https://other.com/dupe","title":"Messi scored a brilliant goal","domain":"bbc.com","seendate":"20260501T110000Z","language":"English"},
			{"url":"https://el-balad.com/junk","title":"Junk article","domain":"el-balad.com","seendate":"20260501T120000Z","language":"English"}
		]}`))
	}))
	defer server.Close()

	provider := newGDELTMediaProvider(time.Second, 5, 0).(*gdeltMediaProvider)
	provider.baseURL = server.URL

	target := domain.PlayerSyncTarget{Name: "Lionel Messi"}
	candidates, err := provider.FetchPlayerArticles(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// 1 from EN (the duplicate filtered out), 1 from RU, junk domain dropped.
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2; got %#v", len(candidates), candidates)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Errorf("expected 2 GDELT queries (EN + RU), got %d", len(queries))
	}
	hasEN := false
	hasRU := false
	for _, q := range queries {
		if strings.Contains(q, "sourcelang%3Aeng") {
			hasEN = true
		}
		if strings.Contains(q, "sourcelang%3Arus") {
			hasRU = true
		}
	}
	if !hasEN || !hasRU {
		t.Errorf("expected EN and RU queries, got: %v", queries)
	}
}

func TestGDELTHonoursRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"articles":[]}`))
	}))
	defer server.Close()

	provider := newGDELTMediaProvider(time.Second, 5, 80*time.Millisecond).(*gdeltMediaProvider)
	provider.baseURL = server.URL

	start := time.Now()
	for i := 0; i < 3; i++ {
		// Three rate-limited calls in a row should wait at least ~160ms.
		if err := provider.respectRateLimit(context.Background()); err != nil {
			t.Fatalf("respectRateLimit: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 160*time.Millisecond {
		t.Errorf("expected at least 160ms across 3 rate-limited calls, got %v", elapsed)
	}
}

func TestGDELTRetriesOn429ThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// First call rate-limited, second succeeds.
		if current == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"articles":[
			{"url":"https://bbc.com/a","title":"Real article","domain":"bbc.com","seendate":"20260501T100000Z"}
		]}`))
	}))
	defer server.Close()

	provider := newGDELTMediaProvider(time.Second, 5, 0).(*gdeltMediaProvider)
	provider.baseURL = server.URL

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	candidates, err := provider.fetchLanguage(ctx, "Lionel Messi", "eng")
	if err != nil {
		t.Fatalf("fetchLanguage: %v", err)
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

func TestGDELTReturnsErrorAfterMaxRetries(t *testing.T) {
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider := newGDELTMediaProvider(time.Second, 5, 0).(*gdeltMediaProvider)
	provider.baseURL = server.URL

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := provider.fetchLanguage(ctx, "Lionel Messi", "eng"); err == nil {
		t.Error("expected error after exhausted retries")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != gdeltMaxRetries+1 {
		t.Errorf("expected %d total calls, got %d", gdeltMaxRetries+1, calls)
	}
}

func TestGDELTSkipsNonJSONResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("no results found"))
	}))
	defer server.Close()

	provider := newGDELTMediaProvider(time.Second, 5, 0).(*gdeltMediaProvider)
	provider.baseURL = server.URL

	candidates, err := provider.FetchPlayerArticles(context.Background(), domain.PlayerSyncTarget{Name: "Nobody"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates for non-JSON response, got %d", len(candidates))
	}
}
