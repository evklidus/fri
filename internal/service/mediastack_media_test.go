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
					{"title":"Messi leads Inter Miami to win","description":"Late winner from a midfielder cross","url":"https://espn.com/b","source":"ESPN","language":"en","published_at":"2026-05-04T18:00:00+00:00"}
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
		// Keyword is asciiOnly'd (lowercased, diacritics stripped) before
		// being quoted — see mediaStackKeywordFor.
		if got := q.Get("keywords"); got != `"lionel messi"` {
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

func TestFilterTitleMentionsPlayerDropsBodyOnlyMatches(t *testing.T) {
	items := []domain.MediaArticleCandidate{
		// Real article about the player — title contains the name
		{Title: "Raphinha hits out at lies over transfer", PlayerName: "Raphinha"},
		// False-positive: Anthony Gordon transfer article that *also* mentions
		// Raphinha somewhere in the body. Title-only check should drop it.
		{Title: "Bayern Munich News: FC Bayern, Newcastle United make contact on Gordon", PlayerName: "Raphinha"},
		// Surname-only mention in title is acceptable.
		{Title: "Olise wonder-strike caps Bayern win", PlayerName: "M. Olise"},
		// Short last token "Jr" should NOT trigger surname match — needs full
		// name in title in that case.
		{Title: "Manchester City eye Brazilian winger transfer", PlayerName: "Vinicius Jr"},
		// Same player, full name in title — kept.
		{Title: "Vinicius Jr scores stunner for Real Madrid", PlayerName: "Vinicius Jr"},
		// Diacritic in headline: "Cubarsí" must still match surname "cubarsi"
		// after asciiOnly normalization on both sides. Before the fix, the
		// title-filter dropped every Cubarsí article because strings.Contains
		// compared "cubarsí stunner" against "cubarsi" and got false.
		{Title: "Cubarsí stunner caps Barcelona win at the Bernabéu", PlayerName: "P. Cubarsí"},
		// Same regression for López — accented "ó" in title vs ascii "lopez".
		{Title: "Fermín López strikes late for Barça", PlayerName: "Fermín López"},
		// And for Kanté — accented "é" in title vs ascii "kante".
		{Title: "Kanté returns to form in Saudi clash", PlayerName: "N'Golo Kanté"},
	}

	cases := map[string]int{
		"Raphinha":     1, // first kept, second dropped
		"M. Olise":     1, // surname match
		"Vinicius Jr":  1, // only the title with full name
		"P. Cubarsí":   1, // diacritic in title matches asciiOnly surname
		"Fermín López": 1, // accented surname matches asciiOnly form
		"N'Golo Kanté": 1, // accented Kanté matches asciiOnly "kante"
	}
	for player, wantCount := range cases {
		var input []domain.MediaArticleCandidate
		for _, it := range items {
			if it.PlayerName == player {
				input = append(input, it)
			}
		}
		got := filterTitleMentionsPlayer(input, player)
		if len(got) != wantCount {
			t.Errorf("player %q: got %d articles, want %d. Kept titles: %v",
				player, len(got), wantCount, titles(got))
		}
	}
}

func titles(items []domain.MediaArticleCandidate) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Title
	}
	return out
}

// TestFilterFootballContextDropsNonFootballNoise is the partner-driven
// regression (2026-05-14): "Kane Biotech", "Sergio Garcia's Wife", "Anneka
// Rice TV joke" and similar off-topic surname matches were leaking through
// to the news feed even after the title-mention filter. We require at least
// one football-domain word in title+summary.
func TestFilterFootballContextDropsNonFootballNoise(t *testing.T) {
	cases := []struct {
		title   string
		summary string
		keep    bool
	}{
		// Drops — no football context anywhere.
		{"Kane Biotech Presents Data To Global Wound Care Community", "Biotech firm shares Q4 results.", false},
		{"Country Thunder Florida Delivers Epic Waterfront Weekend With Kane Brown", "Concert headliner announced.", false},
		{"Who Is Sergio Garcia's Wife? Everything You Need To Know", "Golf legend's wife profiled.", false},
		{"Anneka Rice makes a very risque joke on Amandaland", "TV personality returns to comedy.", false},
		{"City Announces Plans to Redesign Bellingham Hill Park", "Local park renovation in Bellingham, WA.", false},

		// Keeps — clear football context.
		{"Kane scores hat-trick as Bayern destroy Wolfsburg", "Bundesliga top scorer hits 50 goals.", true},
		{"Sergio Garcia wins Masters", "Garcia clinches green jacket.", false}, // golf — no football word
		{"Bellingham Real Madrid contract extension talks", "Midfielder linked with new deal.", true},
		{"Vinicius scores in Champions League quarter-final", "Real Madrid forward decides tie.", true},
		// Russian football
		{"Холанд забил гол в матче чемпионата", "Норвежец продлевает голевую серию", true},
	}

	for _, tc := range cases {
		out := filterFootballContext([]domain.MediaArticleCandidate{
			{Title: tc.title, Summary: tc.summary},
		})
		got := len(out) > 0
		if got != tc.keep {
			t.Errorf("keep=%v for %q (summary %q) want %v", got, tc.title, tc.summary, tc.keep)
		}
	}
}

func TestMediaStackKeywordForExtractsSurname(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Initial dropped, surname kept and ASCII-folded.
		{"E. Haaland", "haaland"},
		{"M. Salah", "salah"},
		{"K. Mbappé", "mbappe"},
		{"H. Kane", "kane"},
		{"J. Bellingham", "bellingham"},
		// Compound surname kept intact ("van" passes the ≥3-char filter).
		{"V. van Dijk", "van dijk"},
		// "Jr" dropped (too short).
		{"Vinicius Jr", "vinicius"},
		// Single-name players ASCII-fold cleanly.
		{"Pedri", "pedri"},
		{"Raphinha", "raphinha"},
		{"Vitinha", "vitinha"},
		// Apostrophe-bearing first name dropped — MediaStack quoted match
		// for "ngolo kante" returns 0 because the index has "n'golo"
		// tokenized as ["n", "golo"]. Surname alone returns 10.
		{"N'Golo Kanté", "kante"},
		// Diacritic stripped: full-name search with both words is more
		// specific than just "lopez" (which would match every Lopez).
		{"Fermín López", "fermin lopez"},
		// Direct regression for the bug we're fixing here.
		{"P. Cubarsí", "cubarsi"},
		// Hyphenated first name should also be dropped.
		{"J.-M. Pirlo", "pirlo"},
	}
	for _, tc := range cases {
		if got := mediaStackKeywordFor(tc.in); got != tc.want {
			t.Errorf("mediaStackKeywordFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
