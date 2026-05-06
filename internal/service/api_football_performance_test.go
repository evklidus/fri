package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

type fakeStore struct {
	mu      sync.Mutex
	entries map[string]domain.PlayerExternalIDs
	getErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{entries: make(map[string]domain.PlayerExternalIDs)}
}

func (s *fakeStore) key(playerID int64, provider string) string {
	return fmt.Sprintf("%d:%s", playerID, provider)
}

func (s *fakeStore) GetExternalIDs(_ context.Context, playerID int64, provider string) (*domain.PlayerExternalIDs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	if entry, ok := s.entries[s.key(playerID, provider)]; ok {
		copyEntry := entry
		return &copyEntry, nil
	}
	return nil, nil
}

func (s *fakeStore) UpsertExternalIDs(_ context.Context, ids domain.PlayerExternalIDs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids.UpdatedAt = time.Now().UTC()
	s.entries[s.key(ids.PlayerID, ids.Provider)] = ids
	return nil
}

func (s *fakeStore) DeleteExternalIDs(_ context.Context, playerID int64, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, s.key(playerID, provider))
	return nil
}

func (s *fakeStore) seed(ids domain.PlayerExternalIDs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[s.key(ids.PlayerID, ids.Provider)] = ids
}

func (s *fakeStore) get(playerID int64, provider string) (domain.PlayerExternalIDs, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[s.key(playerID, provider)]
	return entry, ok
}

type recordingHandler struct {
	t        *testing.T
	mu       sync.Mutex
	hits     map[string]int
	handlers map[string]http.HandlerFunc
}

func newRecordingHandler(t *testing.T) *recordingHandler {
	return &recordingHandler{
		t:        t,
		hits:     make(map[string]int),
		handlers: make(map[string]http.HandlerFunc),
	}
}

func (h *recordingHandler) on(path string, fn http.HandlerFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[path] = fn
}

func (h *recordingHandler) hitCount(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[path]
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.hits[r.URL.Path]++
	fn, ok := h.handlers[r.URL.Path]
	h.mu.Unlock()
	if !ok {
		// Unregistered endpoints return an empty success response. The provider
		// treats empty responses as "no enrichment data" and keeps the test
		// focused on the behavior under examination. Tests that need strict
		// assertions register an explicit handler that fails (or count via
		// hitCount + handler registration).
		writeJSON(w, map[string]any{"errors": []any{}, "response": []any{}})
		return
	}
	fn(w, r)
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func newTestProvider(server *httptest.Server, store externalIDsStore) *apiFootballPerformanceProvider {
	p := newAPIFootballPerformanceProvider("test-key", server.URL, store, time.Second, demoPerformanceProvider{})
	return p.(*apiFootballPerformanceProvider)
}

func TestAPIFootballMappingPathUsesExternalID(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       7,
		Provider:       apiFootballProviderName,
		ExternalID:     "521",
		ExternalTeamID: "541",
	})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("team"); got != "541" {
			t.Errorf("/leagues team param = %q, want 541", got)
		}
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"seasons": []any{map[string]any{"year": 2025, "current": true}},
				},
			},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "521" {
			t.Errorf("/players id param = %q, want 521", got)
		}
		if got := r.URL.Query().Get("season"); got != "2025" {
			t.Errorf("/players season param = %q, want 2025", got)
		}
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{
						"id": 521, "name": "Lionel Messi", "firstname": "Lionel", "lastname": "Messi",
						"age": 38, "position": "Attacker",
					},
					"statistics": []any{
						map[string]any{
							"team":   map[string]any{"id": 541, "name": "Inter Miami", "national": false},
							"games":  map[string]any{"appearences": 28, "minutes": 2400, "rating": "8.1"},
							"shots":  map[string]any{"on": 60},
							"goals":  map[string]any{"total": 22, "assists": 11},
							"passes": map[string]any{"key": 75},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 7, Name: "Lionel Messi", Club: "Inter Miami", Position: "RW", Age: 38}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}

	if snapshot.Provider != apiFootballProviderName {
		t.Errorf("provider = %q, want %q", snapshot.Provider, apiFootballProviderName)
	}
	if snapshot.AverageRating != 8.1 {
		t.Errorf("average rating = %v, want 8.1", snapshot.AverageRating)
	}
	if handler.hitCount("/teams") != 0 {
		t.Errorf("/teams should not be called on mapping path, got %d hits", handler.hitCount("/teams"))
	}
}

func TestAPIFootballMappingPathDetectsTeamChange(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       7,
		Provider:       apiFootballProviderName,
		ExternalID:     "521",
		ExternalTeamID: "541",
	})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"seasons": []any{map[string]any{"year": 2025, "current": true}},
				},
			},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 521, "name": "Lionel Messi", "lastname": "Messi", "age": 38, "position": "Attacker"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 999, "name": "New Club FC", "national": false},
							"games": map[string]any{"appearences": 12, "minutes": 1080, "rating": "7.6"},
							"goals": map[string]any{"total": 5, "assists": 4},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 7, Name: "Lionel Messi", Club: "Inter Miami", Position: "RW", Age: 38}

	if _, err := provider.FetchPerformanceSnapshot(context.Background(), target); err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}

	stored, ok := store.get(7, apiFootballProviderName)
	if !ok {
		t.Fatalf("expected mapping to remain present")
	}
	if stored.ExternalTeamID != "999" {
		t.Errorf("ExternalTeamID = %q, want 999 after transfer", stored.ExternalTeamID)
	}
	if stored.ExternalID != "521" {
		t.Errorf("ExternalID = %q, want 521 unchanged", stored.ExternalID)
	}
}

func TestAPIFootballMappingPathFallsBackOnEmptyResponse(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       7,
		Provider:       apiFootballProviderName,
		ExternalID:     "521",
		ExternalTeamID: "541",
	})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"seasons": []any{map[string]any{"year": 2025, "current": true}},
				},
			},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"errors": []any{}, "response": []any{}})
	})
	// Self-heal will try text-search; both /teams and /players with no data
	// → demo fallback. Mapping stays untouched.
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"errors": []any{}, "response": []any{}})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 7, Name: "Lionel Messi", Club: "Inter Miami", Position: "RW", Age: 38}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if snapshot.Provider != apiFootballFallbackProviderName {
		t.Errorf("provider = %q, want fallback %q", snapshot.Provider, apiFootballFallbackProviderName)
	}

	stored, ok := store.get(7, apiFootballProviderName)
	if !ok {
		t.Fatalf("mapping should still be present after empty response (no sanity-check failure)")
	}
	if stored.ExternalID != "521" {
		t.Errorf("mapping changed unexpectedly: %+v", stored)
	}
}

func TestAPIFootballMappingPathDeletesOnSanityFailure(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       7,
		Provider:       apiFootballProviderName,
		ExternalID:     "521",
		ExternalTeamID: "541",
	})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})
	// API returns a defender, but the saved mapping was for a striker — sanity-check
	// must reject and delete the mapping.
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 521, "lastname": "Wrong", "age": 30, "position": "Defender"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 541, "name": "Inter Miami", "national": false},
							"games": map[string]any{"appearences": 25, "minutes": 2200, "rating": "7.0"},
						},
					},
				},
			},
		})
	})
	// Self-heal text-search: same wrong data, /teams returns empty so it gives up
	// to demo fallback.
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"errors": []any{}, "response": []any{}})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 7, Name: "Lionel Messi", Club: "Inter Miami", Position: "RW", Age: 38}

	snapshot, _ := provider.FetchPerformanceSnapshot(context.Background(), target)
	if snapshot.Provider != apiFootballFallbackProviderName {
		t.Errorf("provider = %q, want fallback after sanity-check failure", snapshot.Provider)
	}
	if _, ok := store.get(7, apiFootballProviderName); ok {
		t.Errorf("mapping must be deleted when mapping-path sanity-check fails")
	}
}

func TestAPIFootballSelfHealRebuildsMappingViaTextSearch(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       11,
		Provider:       apiFootballProviderName,
		ExternalID:     "404",
		ExternalTeamID: "999",
	})

	playerCalls := 0
	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"team": map[string]any{"id": 50, "name": "Manchester City", "national": false}}},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, r *http.Request) {
		playerCalls++
		// First call: id=404 → empty (stale mapping). Subsequent calls: text-search returns the real player.
		if r.URL.Query().Get("id") == "404" {
			writeJSON(w, map[string]any{"errors": []any{}, "response": []any{}})
			return
		}
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 909, "lastname": "Haaland", "age": 25, "position": "Attacker"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "name": "Manchester City", "national": false},
							"games": map[string]any{"appearences": 30, "minutes": 2700, "rating": "8.4"},
							"goals": map[string]any{"total": 28, "assists": 6},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if snapshot.Provider != apiFootballProviderName {
		t.Errorf("provider = %q, want %q after self-heal", snapshot.Provider, apiFootballProviderName)
	}

	stored, ok := store.get(11, apiFootballProviderName)
	if !ok {
		t.Fatalf("expected mapping to be rebuilt by self-heal text-search")
	}
	if stored.ExternalID != "909" || stored.ExternalTeamID != "50" {
		t.Errorf("mapping after self-heal = %+v, want ExternalID=909, ExternalTeamID=50", stored)
	}
}

func TestAPIFootballSanityCheckRejectsWhenNoIdentitySignals(t *testing.T) {
	store := newFakeStore()
	handler := newRecordingHandler(t)
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"team": map[string]any{"id": 50, "name": "Manchester City", "national": false}}},
		})
	})
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})
	// API returns a player with no position and no age → no identity signal.
	// Sanity-check must reject to avoid trusting only the search-text match.
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 808, "lastname": "Haaland"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "name": "Manchester City", "national": false},
							"games": map[string]any{"appearences": 30, "minutes": 2700, "rating": "8.0"},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, _ := provider.FetchPerformanceSnapshot(context.Background(), target)
	if snapshot.Provider != apiFootballFallbackProviderName {
		t.Errorf("provider = %q, want fallback when no identity signal", snapshot.Provider)
	}
	if _, ok := store.get(11, apiFootballProviderName); ok {
		t.Errorf("mapping must NOT be saved when there's no identity signal")
	}
}

func TestAPIFootballTextSearchSkipsMappingForNationalTeamStat(t *testing.T) {
	store := newFakeStore()
	handler := newRecordingHandler(t)
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"team": map[string]any{"id": 50, "name": "Manchester City", "national": false}}},
		})
	})
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})
	// Player matched, but only a national-team stat is returned (international break).
	// selectClubStatistic prefers club-stats; since none exist, it falls back to the
	// first record (national). The text-search path should refuse to persist a
	// national-team external_team_id.
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 909, "lastname": "Haaland", "age": 25, "position": "Attacker"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 100, "name": "Norway", "national": true},
							"games": map[string]any{"appearences": 4, "minutes": 360, "rating": "7.8"},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	if _, err := provider.FetchPerformanceSnapshot(context.Background(), target); err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}

	if _, ok := store.get(11, apiFootballProviderName); ok {
		t.Errorf("mapping must NOT be saved when only national-team statistic is available")
	}
}

func TestAPIFootballTextSearchSavesMappingOnSuccess(t *testing.T) {
	store := newFakeStore()
	handler := newRecordingHandler(t)
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{"team": map[string]any{"id": 50, "name": "Manchester City", "national": false}},
			},
		})
	})
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}},
			},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{
						"id": 909, "name": "Erling Haaland", "lastname": "Haaland",
						"age": 25, "position": "Attacker",
					},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "name": "Manchester City", "national": false},
							"games": map[string]any{"appearences": 30, "minutes": 2700, "rating": "8.4"},
							"goals": map[string]any{"total": 28, "assists": 6},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if snapshot.Provider != apiFootballProviderName {
		t.Errorf("provider = %q, want %q", snapshot.Provider, apiFootballProviderName)
	}

	stored, ok := store.get(11, apiFootballProviderName)
	if !ok {
		t.Fatalf("expected mapping to be saved after successful text search")
	}
	if stored.ExternalID != "909" || stored.ExternalTeamID != "50" {
		t.Errorf("stored mapping = %+v, want ExternalID=909, ExternalTeamID=50", stored)
	}
}

func TestAPIFootballSanityCheckBlocksFalsePositive(t *testing.T) {
	store := newFakeStore()
	handler := newRecordingHandler(t)
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{"team": map[string]any{"id": 50, "name": "Manchester City", "national": false}},
			},
		})
	})
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})
	// API returns a defender with the same surname as our striker target — must reject.
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{
						"id": 808, "name": "Other Haaland", "lastname": "Haaland",
						"age": 24, "position": "Defender",
					},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "name": "Manchester City", "national": false},
							"games": map[string]any{"appearences": 20, "minutes": 1800, "rating": "7.0"},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if snapshot.Provider != apiFootballFallbackProviderName {
		t.Errorf("provider = %q, want fallback when sanity-check fails", snapshot.Provider)
	}
	if _, ok := store.get(11, apiFootballProviderName); ok {
		t.Errorf("mapping must NOT be saved when sanity-check fails")
	}
}

func TestAPIFootballSanityCheckBlocksLargeAgeGap(t *testing.T) {
	store := newFakeStore()
	handler := newRecordingHandler(t)
	handler.on("/teams", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"team": map[string]any{"id": 50, "name": "Manchester City", "national": false}}},
		})
	})
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 808, "lastname": "Haaland", "age": 18, "position": "Attacker"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "national": false},
							"games": map[string]any{"appearences": 15, "minutes": 1300, "rating": "7.2"},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, _ := provider.FetchPerformanceSnapshot(context.Background(), target)
	if snapshot.Provider != apiFootballFallbackProviderName {
		t.Errorf("provider = %q, want fallback for age gap > 3", snapshot.Provider)
	}
	if _, ok := store.get(11, apiFootballProviderName); ok {
		t.Errorf("mapping must NOT be saved when age gap is too big")
	}
}

func TestSelectClubStatisticPrefersClubOverNational(t *testing.T) {
	stats := []apiFootballStatistic{
		{
			Team:  apiFootballTeamRef{ID: 100, Name: "Argentina", National: true},
			Games: apiFootballGames{Appearances: 5, Minutes: 450, Rating: "7.5"},
		},
		{
			Team:  apiFootballTeamRef{ID: 541, Name: "Inter Miami", National: false},
			Games: apiFootballGames{Appearances: 28, Minutes: 2400, Rating: "8.1"},
		},
	}

	stat, ok := selectClubStatistic(stats, 0, 0)
	if !ok {
		t.Fatalf("expected statistic to be selected")
	}
	if stat.Team.National {
		t.Errorf("selected national statistic, expected club")
	}
	if stat.Team.ID != 541 {
		t.Errorf("selected team id = %d, want 541", stat.Team.ID)
	}
}

func TestSelectClubStatisticDeterministicByLeagueID(t *testing.T) {
	// Real-world api-football response shape: same team, multiple
	// competitions. We want exact (team, league) match — never guess by
	// minutes (e.g. early-season CL > league wouldn't fool us).
	stats := []apiFootballStatistic{
		// DFB Pokal (id=81) — first in array, was previously winning
		{
			Team:   apiFootballTeamRef{ID: 157, Name: "Bayern München", National: false},
			League: apiFootballLeagueRef{ID: 81, Name: "DFB Pokal", Type: "Cup"},
			Games:  apiFootballGames{Appearances: 4, Minutes: 315, Rating: "7.27"},
			Goals:  apiFootballGoals{Total: 1, Assists: 0},
		},
		// Some friendly
		{
			Team:   apiFootballTeamRef{ID: 157, Name: "Bayern München", National: false},
			League: apiFootballLeagueRef{ID: 564, Name: "Audi Cup", Type: "Cup"},
			Games:  apiFootballGames{Appearances: 1, Minutes: 89, Rating: "6.6"},
		},
		// Bundesliga (id=78) — this is what we should pick
		{
			Team:   apiFootballTeamRef{ID: 157, Name: "Bayern München", National: false},
			League: apiFootballLeagueRef{ID: 78, Name: "Bundesliga", Type: "League"},
			Games:  apiFootballGames{Appearances: 30, Minutes: 2198, Rating: "7.88"},
			Goals:  apiFootballGoals{Total: 14, Assists: 19},
		},
		// Champions League — substantial, but wrong league
		{
			Team:   apiFootballTeamRef{ID: 157, Name: "Bayern München", National: false},
			League: apiFootballLeagueRef{ID: 2, Name: "UEFA Champions League", Type: "Cup"},
			Games:  apiFootballGames{Appearances: 12, Minutes: 991, Rating: "7.58"},
			Goals:  apiFootballGoals{Total: 5, Assists: 6},
		},
	}

	stat, ok := selectClubStatistic(stats, 157, 78)
	if !ok {
		t.Fatalf("expected statistic to be selected")
	}
	if stat.League.ID != 78 {
		t.Errorf("selected league = %d, want 78 (Bundesliga)", stat.League.ID)
	}
	if stat.Games.Minutes != 2198 || stat.Goals.Total != 14 {
		t.Errorf("got %d min / %d goals; want 2198 min / 14 goals (Bundesliga)", stat.Games.Minutes, stat.Goals.Total)
	}
}

func TestSelectClubStatisticEarlySeasonNotFooledByCLMinutes(t *testing.T) {
	// Counter-example for the old "max minutes" heuristic: in August a
	// player has 180 CL minutes (2 group games) but only 90 league minutes
	// (1 weekend match). Max-minutes would pick CL — wrong.
	stats := []apiFootballStatistic{
		{
			Team:   apiFootballTeamRef{ID: 157, National: false},
			League: apiFootballLeagueRef{ID: 2, Name: "UEFA Champions League"},
			Games:  apiFootballGames{Appearances: 2, Minutes: 180, Rating: "7.6"},
		},
		{
			Team:   apiFootballTeamRef{ID: 157, National: false},
			League: apiFootballLeagueRef{ID: 78, Name: "Bundesliga"},
			Games:  apiFootballGames{Appearances: 1, Minutes: 90, Rating: "7.2"},
		},
	}

	stat, _ := selectClubStatistic(stats, 157, 78)
	if stat.League.ID != 78 {
		t.Errorf("selected league = %d, want 78 even with fewer minutes than CL", stat.League.ID)
	}
}

func TestSelectClubStatisticFallsBackToMaxMinutesWhenLeagueUnknown(t *testing.T) {
	// Caller didn't know leagueID (e.g. /leagues lookup failed). Fall back
	// to most-minutes among same-team entries.
	stats := []apiFootballStatistic{
		{Team: apiFootballTeamRef{ID: 157}, League: apiFootballLeagueRef{ID: 81}, Games: apiFootballGames{Minutes: 315}},
		{Team: apiFootballTeamRef{ID: 157}, League: apiFootballLeagueRef{ID: 78}, Games: apiFootballGames{Minutes: 2198}},
	}
	stat, _ := selectClubStatistic(stats, 157, 0)
	if stat.Games.Minutes != 2198 {
		t.Errorf("got %d minutes, want 2198 (most-played fallback)", stat.Games.Minutes)
	}
}

func TestPositionGroupHandlesAPIFootballValues(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Attacker", "ATT"},
		{"Defender", "DEF"},
		{"Midfielder", "MID"},
		{"Goalkeeper", "GK"},
		{"ST", "ATT"},
		{"CB", "DEF"},
		{"CM", "MID"},
		{"GK", "GK"},
		{"unknown", "OTHER"},
	}
	for _, tc := range cases {
		if got := positionGroup(tc.input); got != tc.want {
			t.Errorf("positionGroup(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestStripClubPrefixHandlesCommonForms(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"FC Barcelona", "Barcelona"},
		{"AFC Bournemouth", "Bournemouth"},
		{"1. FC Köln", "FC Köln"}, // strips "1. " only; "FC " stripped on next pass via asciiOnly chain
		{"Real Madrid", "Real Madrid"},
		{"Manchester United", "Manchester United"},
		{"FK Crvena zvezda", "Crvena zvezda"},
		{"Brighton FC", "Brighton"},
	}
	for _, tc := range cases {
		if got := stripClubPrefix(tc.in); got != tc.want {
			t.Errorf("stripClubPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAsciiOnlyDropsDiacriticsAndPunct(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Fenerbahçe", "fenerbahce"},
		{"FC Köln", "fc koln"},
		{"N'Golo Kanté", "ngolo kante"},
		{"Real Sociedad", "real sociedad"},
	}
	for _, tc := range cases {
		if got := asciiOnly(tc.in); got != tc.want {
			t.Errorf("asciiOnly(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTeamSearchCandidatesProducesUniqueOrderedSet(t *testing.T) {
	got := teamSearchCandidates("FC Barcelona")
	if len(got) < 2 {
		t.Fatalf("expected multiple candidates for 'FC Barcelona', got %v", got)
	}
	// First candidate should be the original input.
	if got[0] != "FC Barcelona" {
		t.Errorf("first candidate = %q, want original 'FC Barcelona'", got[0])
	}
	// Stripped variant must be present.
	hasStripped := false
	for _, c := range got {
		if strings.EqualFold(c, "Barcelona") || strings.EqualFold(c, "barcelona") {
			hasStripped = true
		}
	}
	if !hasStripped {
		t.Errorf("expected stripped variant 'Barcelona' in candidates, got %v", got)
	}
}

func TestPlayerSearchTermPrefersSurname(t *testing.T) {
	// API-Football indexes players by surname, so prefer the last word ≥4
	// chars. Falls through to longest if last is too short; falls through to
	// joined parts if both are too short.
	cases := []struct {
		in, want string
	}{
		{"N'Golo Kanté", "kante"},   // critical: surname, not "ngolo"
		{"Lionel Messi", "messi"},   // surname over first name
		{"E. Haaland", "haaland"},   // initial dropped, surname kept
		{"Vinicius Jr", "vinicius"}, // "jr" too short → longest fallback
		{"V. van Dijk", "dijk"},     // last word ≥4, even with compound surname
		{"Pedri", "pedri"},          // single-word name
		{"Jr", "jr"},                // pathological: < 4 chars on every branch
	}
	for _, tc := range cases {
		if got := playerSearchTerm(tc.in); got != tc.want {
			t.Errorf("playerSearchTerm(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalTeamIDShortCircuitsFindTeam(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "should not be called for hardcoded clubs", http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := newTestProvider(server, newFakeStore())
	cases := []struct {
		club   string
		wantID int
	}{
		{"Manchester City", 50},
		{"Bayern Munich", 157},
		{"Real Madrid", 541},
		{"FC Barcelona", 529},
		{"PSG", 85},
		{"Inter Miami", 1614},
		{"Liverpool", 40},
		{"Arsenal", 42},
	}
	for _, tc := range cases {
		team, err := provider.findTeam(context.Background(), tc.club)
		if err != nil {
			t.Errorf("findTeam(%q) failed: %v", tc.club, err)
			continue
		}
		if team.ID != tc.wantID {
			t.Errorf("findTeam(%q) id = %d, want %d", tc.club, team.ID, tc.wantID)
		}
	}
	if calls != 0 {
		t.Errorf("hardcoded shortcut should not hit the network, got %d API calls", calls)
	}
}

func TestCanonicalTeamIDFallsThroughToSearchForUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[],"response":[
			{"team":{"id":9999,"name":"Some Smaller Club","national":false}}
		]}`))
	}))
	defer server.Close()

	provider := newTestProvider(server, newFakeStore())
	team, err := provider.findTeam(context.Background(), "Some Smaller Club")
	if err != nil {
		t.Fatalf("findTeam: %v", err)
	}
	if team.ID != 9999 {
		t.Errorf("team id = %d, want 9999 (search path for unknown club)", team.ID)
	}
}

func TestIsJunkTeamCatchesReserveYouthWomen(t *testing.T) {
	junk := []string{
		"Manchester City W",
		"Manchester City Women",
		"Bayern München II",
		"Bayern Munich B",
		"Real Madrid U21",
		"Real Madrid U19",
		"Liverpool U17",
		"Arsenal Reserves",
		"Chelsea Academy",
		"Atletico Madrid Youth",
		"Manchester City Women's",
	}
	for _, name := range junk {
		if !isJunkTeam(name) {
			t.Errorf("isJunkTeam(%q) = false, want true", name)
		}
	}

	canonical := []string{
		"Manchester City",
		"Bayern Munich",
		"Real Madrid",
		"FC Barcelona",
		"Inter",
		"PSG",
		"Newcastle United",
		"West Ham United",
		"VfB Stuttgart",
	}
	for _, name := range canonical {
		if isJunkTeam(name) {
			t.Errorf("isJunkTeam(%q) = true, want false (canonical first team)", name)
		}
	}
}

func TestFindTeamSkipsWomenAndPicksFirstTeam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Both a women's team and the canonical first team — the women's
		// entry comes first, so without filtering the tie-break would win it.
		// Using a non-hardcoded club name so the search path runs.
		_, _ = w.Write([]byte(`{"errors":[],"response":[
			{"team":{"id":99001,"name":"Sample Town W","national":false}},
			{"team":{"id":99002,"name":"Sample Town","national":false}}
		]}`))
	}))
	defer server.Close()

	provider := newTestProvider(server, newFakeStore())
	team, err := provider.findTeam(context.Background(), "Sample Town")
	if err != nil {
		t.Fatalf("findTeam: %v", err)
	}
	if team.ID != 99002 {
		t.Errorf("team id = %d, want 99002 (canonical, not 99001 women)", team.ID)
	}
}

func TestFindTeamFallsBackWhenAllAreJunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Pretend every result is a youth/women team. We still want a
		// non-error result instead of an outright miss — sometimes seed data
		// genuinely targets a non-canonical squad.
		_, _ = w.Write([]byte(`{"errors":[],"response":[
			{"team":{"id":99001,"name":"Sample United W","national":false}}
		]}`))
	}))
	defer server.Close()

	provider := newTestProvider(server, newFakeStore())
	team, err := provider.findTeam(context.Background(), "Sample United")
	if err != nil {
		t.Fatalf("findTeam: %v", err)
	}
	if team.ID != 99001 {
		t.Errorf("team id = %d, want fallback to 99001 when no canonical option exists", team.ID)
	}
}

func TestFindTeamRetriesWithStrippedPrefix(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("search")
		calls = append(calls, query)
		w.Header().Set("Content-Type", "application/json")
		// Use a non-hardcoded club so the search path runs. Empty for the
		// original "FC Sample United"; success for stripped "Sample United".
		if strings.EqualFold(query, "FC Sample United") {
			_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
			return
		}
		if strings.Contains(strings.ToLower(query), "sample united") {
			_, _ = w.Write([]byte(`{"errors":[],"response":[
				{"team":{"id":99003,"name":"Sample United","national":false}}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{"errors":[],"response":[]}`))
	}))
	defer server.Close()

	provider := newTestProvider(server, newFakeStore())
	team, err := provider.findTeam(context.Background(), "FC Sample United")
	if err != nil {
		t.Fatalf("findTeam: %v", err)
	}
	if team.ID != 99003 {
		t.Errorf("team id = %d, want 99003 (Sample United)", team.ID)
	}
	if len(calls) < 2 {
		t.Errorf("expected ≥2 search calls (original + stripped), got %v", calls)
	}
}

func TestDefaultCurrentSeasonHeuristic(t *testing.T) {
	got := defaultCurrentSeason()
	now := time.Now().UTC()
	want := now.Year() - 1
	if now.Month() >= time.July {
		want = now.Year()
	}
	if got != want {
		t.Errorf("defaultCurrentSeason() = %d, want %d", got, want)
	}
}

func TestAPIFootballFormUsesLastFixturesAndCaches(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       11,
		Provider:       apiFootballProviderName,
		ExternalID:     "909",
		ExternalTeamID: "50",
	})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{map[string]any{
				"league":  map[string]any{"id": 39, "name": "Premier League", "type": "League"},
				"seasons": []any{map[string]any{"year": 2025, "current": true}},
			}},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 909, "lastname": "Haaland", "age": 25, "position": "Attacker"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "national": false},
							"games": map[string]any{"appearences": 30, "minutes": 2700, "rating": "8.4"},
							"goals": map[string]any{"total": 28, "assists": 6},
						},
					},
				},
			},
		})
	})
	handler.on("/fixtures", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{"fixture": map[string]any{"id": 1001}},
				map[string]any{"fixture": map[string]any{"id": 1002}},
			},
		})
	})
	handler.on("/fixtures/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"team": map[string]any{"id": 50},
					"players": []any{
						map[string]any{
							"player": map[string]any{"id": 909, "name": "Haaland"},
							"statistics": []any{map[string]any{
								"games": map[string]any{"minutes": 90, "rating": "8.5"},
								"goals": map[string]any{"total": 2, "assists": 0},
							}},
						},
					},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}

	if snapshot.Last5Goals != 4 {
		t.Errorf("last5 goals = %d, want 4 (2 goals × 2 fixtures)", snapshot.Last5Goals)
	}
	if snapshot.Last5Rating < 8.4 || snapshot.Last5Rating > 8.6 {
		t.Errorf("last5 rating = %v, want ~8.5", snapshot.Last5Rating)
	}
	if snapshot.FormScore <= 50 {
		t.Errorf("form_score = %v, want > 50 for hot streak", snapshot.FormScore)
	}

	// Second fetch should hit cache, no extra /fixtures or /fixtures/players calls.
	if _, err := provider.FetchPerformanceSnapshot(context.Background(), target); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := handler.hitCount("/fixtures"); got != 1 {
		t.Errorf("/fixtures should be called once due to form cache, got %d", got)
	}
	if got := handler.hitCount("/fixtures/players"); got != 2 {
		t.Errorf("/fixtures/players should be called 2 times once and reused (form cache), got %d", got)
	}
}

func TestAPIFootballTopNRankUsesTopscorersForAttackers(t *testing.T) {
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{
		PlayerID:       11,
		Provider:       apiFootballProviderName,
		ExternalID:     "909",
		ExternalTeamID: "50",
	})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{map[string]any{
				"league":  map[string]any{"id": 39, "name": "Premier League", "type": "League"},
				"seasons": []any{map[string]any{"year": 2025, "current": true}},
			}},
		})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player": map[string]any{"id": 909, "lastname": "Haaland", "age": 25, "position": "Attacker"},
					"statistics": []any{
						map[string]any{
							"team":  map[string]any{"id": 50, "national": false},
							"games": map[string]any{"appearences": 30, "minutes": 2700, "rating": "8.4"},
							"goals": map[string]any{"total": 28, "assists": 6},
						},
					},
				},
			},
		})
	})
	handler.on("/players/topscorers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors": []any{},
			"response": []any{
				map[string]any{
					"player":     map[string]any{"id": 909, "position": "Attacker"},
					"statistics": []any{map[string]any{"team": map[string]any{"national": false}, "games": map[string]any{"rating": "8.4"}, "goals": map[string]any{"total": 28}}},
				},
				map[string]any{
					"player":     map[string]any{"id": 100, "position": "Attacker"},
					"statistics": []any{map[string]any{"team": map[string]any{"national": false}, "games": map[string]any{"rating": "7.8"}, "goals": map[string]any{"total": 20}}},
				},
				map[string]any{
					"player":     map[string]any{"id": 200, "position": "Attacker"},
					"statistics": []any{map[string]any{"team": map[string]any{"national": false}, "games": map[string]any{"rating": "7.4"}, "goals": map[string]any{"total": 15}}},
				},
			},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	target := domain.PlayerSyncTarget{ID: 11, Name: "Erling Haaland", Club: "Manchester City", Position: "ST", Age: 25}

	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// Player is rank 1 of 3 → positionRankScore = 100.
	if snapshot.PositionRankScore != 100 {
		t.Errorf("position rank score = %v, want 100 (rank 1 of 3)", snapshot.PositionRankScore)
	}
	if got := handler.hitCount("/players/topassists"); got != 0 {
		t.Errorf("/players/topassists should not be called for attacker, got %d", got)
	}
}

func TestSeasonCacheReusesEntries(t *testing.T) {
	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"errors":   []any{},
			"response": []any{map[string]any{"seasons": []any{map[string]any{"year": 2025, "current": true}}}},
		})
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, newFakeStore())

	for i := 0; i < 3; i++ {
		if info := provider.currentSeasonForTeam(context.Background(), 541); info.Season != 2025 {
			t.Errorf("iter %d: season = %d, want 2025", i, info.Season)
		}
	}

	if got := handler.hitCount("/leagues"); got != 1 {
		t.Errorf("/leagues should be called once due to cache, got %d", got)
	}
}
