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
	candidates = dedupeArticles(candidates)

	if len(candidates) > p.articlesPerPlayer {
		candidates = candidates[:p.articlesPerPlayer]
	}
	return candidates, nil
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

// fetch hits the MediaStack /news endpoint for one language. Uses a quoted
// player name to anchor on the exact phrase; MediaStack's keyword filter is
// substring-style so unquoted "Lionel Messi" would match "Lionel" alone.
// Retries once on rate-limit errors (HTTP 429 or `error.code` =
// rate_limit_reached) — free-tier buckets refill in a few seconds.
func (p *mediaStackMediaProvider) fetch(ctx context.Context, playerName, language, fromDate, toDate string) ([]domain.MediaArticleCandidate, error) {
	params := url.Values{
		"access_key": []string{p.apiKey},
		"keywords":   []string{`"` + playerName + `"`},
		"languages":  []string{language},
		"date":       []string{fromDate + "," + toDate},
		"limit":      []string{strconv.Itoa(mediaStackPageSize)},
		"sort":       []string{"published_desc"},
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
