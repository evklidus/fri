package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	mediaStackProviderName = "mediastack"
	// Free tier requires HTTP. Paid plans serve HTTPS — override via
	// MEDIASTACK_BASE_URL when upgrading.
	mediaStackDefaultBaseURL = "http://api.mediastack.com/v1"
	mediaStackDefaultTimeout = 30 * time.Second
	mediaStackPageSize       = 25
	mediaStackLookbackDays   = 30
	// 500ms keeps us well under any documented per-second cap on Standard/Pro
	// plans while still surviving free-tier throttling (where retry kicks in).
	mediaStackDefaultMinGap   = 500 * time.Millisecond
	mediaStack429BackoffStart = 5 * time.Second
	mediaStackMaxRetries      = 1
)

type mediaStackMediaProvider struct {
	apiKey            string
	baseURL           string
	client            *http.Client
	articlesPerPlayer int
	minGap            time.Duration

	callMu     sync.Mutex
	lastCallAt time.Time
}

func newMediaStackMediaProvider(apiKey, baseURL string, timeout time.Duration, articlesPerPlayer int) mediaProvider {
	if articlesPerPlayer <= 0 {
		articlesPerPlayer = 5
	}
	if timeout <= 0 {
		timeout = mediaStackDefaultTimeout
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = mediaStackDefaultBaseURL
	}
	return &mediaStackMediaProvider{
		apiKey:            strings.TrimSpace(apiKey),
		baseURL:           strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:            &http.Client{Timeout: timeout},
		articlesPerPlayer: articlesPerPlayer,
		minGap:            mediaStackDefaultMinGap,
	}
}

func (p *mediaStackMediaProvider) Name() string { return mediaStackProviderName }

func (p *mediaStackMediaProvider) FetchPlayerArticles(ctx context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error) {
	now := time.Now().UTC()
	fromDate := now.AddDate(0, 0, -mediaStackLookbackDays).Format("2006-01-02")
	toDate := now.Format("2006-01-02")

	candidates := make([]domain.MediaArticleCandidate, 0)

	if err := p.respectRateLimit(ctx); err != nil {
		return nil, err
	}
	enArticles, err := p.fetch(ctx, player.Name, "en", fromDate, toDate)
	if err != nil {
		log.Printf("mediastack: en fetch failed for %q: %v", player.Name, err)
	}
	candidates = append(candidates, enArticles...)

	if err := p.respectRateLimit(ctx); err != nil {
		return candidates, nil
	}
	ruArticles, err := p.fetch(ctx, player.Name, "ru", fromDate, toDate)
	if err != nil {
		log.Printf("mediastack: ru fetch failed for %q: %v", player.Name, err)
	}
	candidates = append(candidates, ruArticles...)

	candidates = applyDomainDenylist(candidates)
	candidates = filterTitleMentionsPlayer(candidates, player.Name)
	candidates = filterFootballContext(candidates)
	candidates = dedupeArticles(candidates)

	if len(candidates) > p.articlesPerPlayer {
		candidates = candidates[:p.articlesPerPlayer]
	}
	return candidates, nil
}

// filterTitleMentionsPlayer drops articles where the player's name (full or
// surname) doesn't appear in the title. MediaStack matches on body text too,
// which produces false positives — e.g. an Anthony Gordon transfer article
// that mentions "Raphinha not interested in Saudi" gets attributed to
// Raphinha. Title-only filtering is a strict but cheap heuristic that cuts
// the obvious off-topic mentions while keeping articles where the player is
// the actual subject.
//
// Both sides of the substring check are run through asciiOnly so accented
// Latin characters match plain ASCII surnames. Otherwise "Cubarsí dazzles"
// (title) doesn't contain "cubarsi" (asciiOnly surname) — same for López,
// Kanté, and any other diacritic-bearing name.
func filterTitleMentionsPlayer(items []domain.MediaArticleCandidate, playerName string) []domain.MediaArticleCandidate {
	fullNormalized := asciiOnly(playerName)
	if fullNormalized == "" {
		return items
	}
	surname := playerSearchTerm(playerName) // ascii-normalized surname (≥4 chars when possible)
	out := make([]domain.MediaArticleCandidate, 0, len(items))
	for _, item := range items {
		// Cyrillic titles can't be matched against an ASCII surname directly
		// (e.g. "Месси" vs "Messi"). MediaStack already filters by latin
		// keyword, so cyrillic results are statistically clean — let them
		// through without title-check.
		if cyrillicRatio(item.Title) >= 0.3 {
			out = append(out, item)
			continue
		}
		titleNormalized := asciiOnly(item.Title)
		if strings.Contains(titleNormalized, fullNormalized) {
			out = append(out, item)
			continue
		}
		// Match on surname only when it's specific enough (≥4 chars) — short
		// names like "Jr" would let everything through.
		if len(surname) >= 4 && strings.Contains(titleNormalized, surname) {
			out = append(out, item)
			continue
		}
	}
	return out
}

// footballContextWords are terms that virtually never appear in non-football
// articles. The list is intentionally narrow — broad words like "season" or
// "team" match too many false positives (TV seasons, baseball teams). We
// keep canonical league names, position labels, and football-specific
// vocabulary that doesn't bleed into other domains.
//
// Both English and Russian are included so MediaStack's RU feed also passes
// the check.
var footballContextWords = []string{
	// English — football-specific verbs/nouns
	"football", "soccer", "fc ", " fc", "f.c.",
	"goal ", " goals", "scored", "scoring", "header", "assist", "hat-trick", "hat trick",
	"midfielder", "defender", "striker", "winger", "goalkeeper", "forward",
	"transfer", "signing", " signed for", "loan deal", "contract extension",
	"manager said", "coach said", "head coach", "boss said",
	"premier league", "champions league", "europa league", "la liga", "bundesliga",
	"serie a", "ligue 1", "süper lig", "saudi pro league", "mls",
	"world cup", "uefa", "fifa", "euros 2", "copa america", "afcon",
	"red card", "yellow card", "penalty", "free kick", "offside", "stoppage time",
	"el clasico", "derby", "matchday", "starting xi", "lineup", "squad",
	"barcelona", "real madrid", "manchester", "liverpool", "arsenal", "chelsea",
	"bayern", "psg", "juventus", "milan", "atletico",
	// Russian
	"футбол", "матч", "клуб", "лига", "лиги чемпионов", "лч",
	"трансфер", "трансфера", "забил", "гол ", "голы", "голов",
	"тренер ", "защитник", "полузащит", "нападающ", "вратарь",
	"премьер-лиг", "ла лига", "бундеслиг", "серия а",
	"чемпионат мира", "уефа", "фифа",
	"красная карточк", "жёлтая карточк", "пенальти", "штрафная",
	"барселон", "реал мадрид", "манчестер", "ливерпул",
}

// filterFootballContext drops articles that pass the surname check but read
// like non-football noise. MediaStack search returns headlines for any
// matching surname, so "Kane Biotech Presents Data", "Sergio Garcia's Wife",
// or "Bellingham Hill Park" all get through. We require the title OR summary
// to mention at least one football-context word — a cheap whitelist that
// catches the obvious off-topic hits without an ML pipeline.
func filterFootballContext(items []domain.MediaArticleCandidate) []domain.MediaArticleCandidate {
	out := make([]domain.MediaArticleCandidate, 0, len(items))
	for _, item := range items {
		text := strings.ToLower(item.Title + " " + item.Summary)
		// Cyrillic articles get the cyrillic football vocabulary check below
		// for free — both sets are in footballContextWords.
		matched := false
		for _, w := range footballContextWords {
			if strings.Contains(text, w) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, item)
		}
	}
	return out
}

func (p *mediaStackMediaProvider) respectRateLimit(ctx context.Context) error {
	if p.minGap <= 0 {
		return nil
	}
	p.callMu.Lock()
	wait := time.Until(p.lastCallAt.Add(p.minGap))
	p.callMu.Unlock()
	if wait <= 0 {
		p.markCalled()
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	p.markCalled()
	return nil
}

func (p *mediaStackMediaProvider) markCalled() {
	p.callMu.Lock()
	p.lastCallAt = time.Now()
	p.callMu.Unlock()
}

// fetch hits the MediaStack /news endpoint for one language. The keyword is
// the player's *surname* in quotes — MediaStack does exact-phrase matching,
// and journalists never write our seed-style "E. Haaland" (they use
// "Haaland" or "Erling Haaland"). Searching by surname returns articles for
// every well-known player; the title-mention filter downstream still
// guarantees the surname appears in the headline before we attribute it.
//
// Retries once on rate-limit errors (HTTP 429 or `error.code` =
// rate_limit_reached) — free-tier buckets refill in a few seconds.
func (p *mediaStackMediaProvider) fetch(ctx context.Context, playerName, language, fromDate, toDate string) ([]domain.MediaArticleCandidate, error) {
	keyword := mediaStackKeywordFor(playerName)
	params := url.Values{
		"access_key": []string{p.apiKey},
		"keywords":   []string{`"` + keyword + `"`},
		"languages":  []string{language},
		"date":       []string{fromDate + "," + toDate},
		"limit":      []string{strconv.Itoa(mediaStackPageSize)},
		"sort":       []string{"published_desc"},
		// Categories filter cuts the non-sports noise on MediaStack's side:
		// "Kane Biotech presents data", "Anneka Rice TV joke", "Bellingham
		// Hill Park redesign" and similar namesake matches in entertainment /
		// politics / health verticals never come back to us. Other sports
		// (golf, baseball) still pass — those get caught by the downstream
		// football-context whitelist in filterFootballContext.
		//
		// MediaStack accepts a comma-separated category list. We pick only
		// "sports" since that's what the system is for; expand later if we
		// ever want general media coverage of a player (e.g. fashion mags).
		"categories": []string{"sports"},
	}
	endpoint := p.baseURL + "/news?" + params.Encode()

	for attempt := 0; attempt <= mediaStackMaxRetries; attempt++ {
		candidates, retryAfter, err := p.doRequest(ctx, endpoint, playerName)
		if err == nil {
			return candidates, nil
		}
		if retryAfter == 0 || attempt == mediaStackMaxRetries {
			return nil, err
		}
		log.Printf("mediastack: rate limited for %q (lang=%s), backing off %s before retry", playerName, language, retryAfter)
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("mediastack: retries exhausted for %q (lang=%s)", playerName, language)
}

// doRequest performs one MediaStack call. Returns retryAfter > 0 for any
// rate-limit signal so the caller can sleep and retry.
func (p *mediaStackMediaProvider) doRequest(ctx context.Context, endpoint, playerName string) ([]domain.MediaArticleCandidate, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "FRI-Bot/1.0 (+https://localhost)")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, mediaStack429BackoffStart, fmt.Errorf("mediastack returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, 0, fmt.Errorf("mediastack returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var response mediaStackResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, 0, err
	}
	// MediaStack returns 200 OK with an `error` object on quota/auth issues.
	if response.Error.Code != "" {
		// Rate-limit error embedded in 200 response: retry like HTTP 429.
		if response.Error.Code == "rate_limit_reached" {
			return nil, mediaStack429BackoffStart, fmt.Errorf("mediastack error %s: %s", response.Error.Code, response.Error.Message)
		}
		return nil, 0, fmt.Errorf("mediastack error %s: %s", response.Error.Code, response.Error.Message)
	}

	candidates := make([]domain.MediaArticleCandidate, 0, len(response.Data))
	for _, art := range response.Data {
		title := strings.TrimSpace(art.Title)
		if title == "" {
			continue
		}
		summary := strings.TrimSpace(art.Description)
		if summary == "" {
			summary = title
		}
		candidates = append(candidates, domain.MediaArticleCandidate{
			PlayerName:  playerName,
			Title:       title,
			Summary:     summary,
			Source:      strings.TrimSpace(art.Source),
			SourceURL:   strings.TrimSpace(art.URL),
			PublishedAt: parseMediaStackDate(art.PublishedAt),
		})
	}
	return candidates, 0, nil
}

// mediaStackKeywordFor extracts the search term we send to MediaStack from a
// seed-style player name. Strategy:
//  1. Drop initials ("E.", "M.").
//  2. Drop short suffixes ("Jr", "Sr").
//  3. Drop tokens containing punctuation that breaks MediaStack's tokenizer
//     (apostrophes, hyphens). For "N'Golo Kanté" this drops "N'Golo" — the
//     asciiOnly form "ngolo" won't match MediaStack's index (which sees
//     "n'golo" tokenized into ["n", "golo"]). Surname alone matches reliably.
//  4. Run the result through asciiOnly. MediaStack's exact-phrase matcher is
//     case-insensitive but inconsistent on diacritics: "Mbappé" works,
//     "Cubarsí" returns zero. Stripping diacritics fixes it across the board.
//
// Empirical results from /v1/news with quoted keyword:
//
//	"Cubarsí" → 0   |  "cubarsi"      → 5
//	"Kanté"   → 0   |  "kante"        → 10
//	"Fermín López" → works either way (we still ascii-fold for consistency)
func mediaStackKeywordFor(playerName string) string {
	parts := strings.Fields(strings.TrimSpace(playerName))
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		// Skip initials like "E.", "K.", "M." — they're not in headlines.
		if strings.HasSuffix(p, ".") && len(p) <= 2 {
			continue
		}
		// Skip stray short tokens ("Jr", "Sr") that match too many articles.
		if len(p) < 3 {
			continue
		}
		// Skip tokens with apostrophes/hyphens — MediaStack tokenizes on
		// those and the asciiOnly form ("ngolo") won't match the indexed
		// tokens ("n", "golo"). Better to drop the first-name part entirely
		// and search by surname only ("kante" → 10 articles).
		if strings.ContainsAny(p, "'’’‘-") {
			continue
		}
		cleaned = append(cleaned, p)
	}
	var phrase string
	if len(cleaned) == 0 {
		// Pathological: every token was filtered. Fall back to the raw name
		// so we at least try to match something.
		phrase = strings.TrimSpace(playerName)
	} else {
		phrase = strings.Join(cleaned, " ")
	}
	// asciiOnly lowercases and strips diacritics — both safe for MediaStack.
	if normalized := asciiOnly(phrase); normalized != "" {
		return normalized
	}
	return phrase
}

func parseMediaStackDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	// MediaStack returns "2026-05-05T10:00:00+00:00" (RFC3339).
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC()
	}
	return time.Now().UTC()
}

type mediaStackResponse struct {
	Pagination mediaStackPagination `json:"pagination"`
	Data       []mediaStackArticle  `json:"data"`
	Error      mediaStackError      `json:"error"`
}

type mediaStackPagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Count  int `json:"count"`
	Total  int `json:"total"`
}

type mediaStackArticle struct {
	Author      string `json:"author"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Source      string `json:"source"`
	Image       string `json:"image"`
	Category    string `json:"category"`
	Language    string `json:"language"`
	Country     string `json:"country"`
	PublishedAt string `json:"published_at"`
}

type mediaStackError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
