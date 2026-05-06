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

func TestYouTubeProviderAggregatesViewCounts(t *testing.T) {
	var mu sync.Mutex
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			if got := r.URL.Query().Get("type"); got != "video" {
				t.Errorf("search type = %q, want video", got)
			}
			_, _ = w.Write([]byte(`{"items":[
				{"id":{"videoId":"V1"}},
				{"id":{"videoId":"V2"}}
			]}`))
		case "/videos":
			if got := r.URL.Query().Get("id"); got != "V1,V2" {
				t.Errorf("videos id = %q, want V1,V2", got)
			}
			_, _ = w.Write([]byte(`{"items":[
				{"statistics":{"viewCount":"5000000"}},
				{"statistics":{"viewCount":"2500000"}}
			]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := newYouTubeSocialProvider("test-key", server.URL, time.Second, demoSocialProvider{})
	target := domain.PlayerSyncTarget{ID: 1, Name: "Lionel Messi", Club: "Inter Miami", Position: "RW"}

	snapshot, err := provider.FetchSocialSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snapshot.Provider != youTubeProviderName {
		t.Errorf("provider = %q, want %q", snapshot.Provider, youTubeProviderName)
	}
	if snapshot.YouTubeViews7D != 7_500_000 {
		t.Errorf("views_7d = %d, want 7,500,000", snapshot.YouTubeViews7D)
	}
	if snapshot.NormalizedScore < 0 || snapshot.NormalizedScore > 100 {
		t.Errorf("score out of range: %v", snapshot.NormalizedScore)
	}
	if snapshot.Followers <= 0 {
		t.Errorf("followers should be inherited from demo provider, got 0")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 {
		t.Errorf("expected 2 calls (search + videos), got %d (%v)", len(paths), paths)
	}
}

func TestYouTubeProviderFallsBackOnHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "quota exceeded", http.StatusForbidden)
	}))
	defer server.Close()

	provider := newYouTubeSocialProvider("test-key", server.URL, time.Second, demoSocialProvider{})
	snapshot, err := provider.FetchSocialSnapshot(context.Background(), domain.PlayerSyncTarget{ID: 1, Name: "Player"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snapshot.Provider != youTubeFallbackProviderName {
		t.Errorf("provider = %q, want fallback %q", snapshot.Provider, youTubeFallbackProviderName)
	}
}

func TestYouTubeProviderFallbackOnEmptySearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "search") {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		t.Errorf("videos.list should not be called when search returns empty")
	}))
	defer server.Close()

	provider := newYouTubeSocialProvider("test-key", server.URL, time.Second, demoSocialProvider{})
	snapshot, err := provider.FetchSocialSnapshot(context.Background(), domain.PlayerSyncTarget{ID: 1, Name: "Obscure Player"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snapshot.Provider != youTubeProviderName {
		t.Errorf("provider = %q, want %q (empty search is success with 0 views)", snapshot.Provider, youTubeProviderName)
	}
	if snapshot.YouTubeViews7D != 0 {
		t.Errorf("views_7d = %d, want 0", snapshot.YouTubeViews7D)
	}
}

func TestNewSocialProviderReturnsDemoWithoutKey(t *testing.T) {
	provider := NewSocialProvider("", "", 0)
	if provider.Name() != socialProviderName {
		t.Errorf("with empty key, provider = %q, want demo %q", provider.Name(), socialProviderName)
	}
}
