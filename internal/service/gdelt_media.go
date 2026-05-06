package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	gdeltMediaProviderName = "gdelt"
	gdeltDefaultBaseURL    = "https://api.gdeltproject.org/api/v2/doc/doc"
	gdeltDefaultTimespan   = "1month"
	// GDELT throttles aggressively. Empirically 1.1s/req still triggered 429
	// + TLS handshake timeouts on the first real run; 3s gives the server
	// breathing room and keeps us under their soft limit.
	gdeltDefaultMinGap   = 3 * time.Second
	gdeltDefaultTimeout  = 30 * time.Second
	gdelt429BackoffStart = 5 * time.Second
	gdeltMaxRetries      = 1
)

type gdeltMediaProvider struct {
	client            *http.Client
	baseURL           string
	articlesPerPlayer int
	minGap            time.Duration

	callMu     sync.Mutex
	lastCallAt time.Time
}

func newGDELTMediaProvider(timeout time.Duration, articlesPerPlayer int, minGap time.Duration) mediaProvider {
	if articlesPerPlayer <= 0 {
		articlesPerPlayer = 5
	}
	// GDELT TLS handshake alone can take 10–15s during their daily traffic
	// spikes — fall back to a generous default when no explicit timeout is set.
	if timeout <= 0 {
		timeout = gdeltDefaultTimeout
	}
	return &gdeltMediaProvider{
		client:            &http.Client{Timeout: timeout},
		baseURL:           gdeltDefaultBaseURL,
		articlesPerPlayer: articlesPerPlayer,
		minGap:            minGap,
	}
}

func (p *gdeltMediaProvider) Name() string { return gdeltMediaProviderName }

func (p *gdeltMediaProvider) FetchPlayerArticles(ctx context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error) {
	if err := p.respectRateLimit(ctx); err != nil {
		return nil, err
	}

	candidates, err := p.fetchLanguage(ctx, player.Name, "eng")
	if err != nil {
		log.Printf("gdelt: english query failed for %q: %v", player.Name, err)
	}

	// Russian dictionary covers Cyrillic-language coverage when present.
	if err := p.respectRateLimit(ctx); err != nil {
		return candidates, nil
	}
	ruCandidates, err := p.fetchLanguage(ctx, player.Name, "rus")
	if err != nil {
		log.Printf("gdelt: russian query failed for %q: %v", player.Name, err)
	}
	candidates = append(candidates, ruCandidates...)

	candidates = applyDomainDenylist(candidates)
	candidates = dedupeArticles(candidates)

	if len(candidates) > p.articlesPerPlayer {
		candidates = candidates[:p.articlesPerPlayer]
	}
	return candidates, nil
}

// fetchLanguage hits the GDELT DOC 2.0 API for one language. Builds a
// quoted-name query anchored on football vocabulary so we don't pick up
// "David Silva" as a Hollywood actor or "Ronaldo" as a fashion influencer.
// Retries once with backoff on HTTP 429 since GDELT throttling is bursty.
func (p *gdeltMediaProvider) fetchLanguage(ctx context.Context, playerName, lang string) ([]domain.MediaArticleCandidate, error) {
	query := fmt.Sprintf(`"%s" (football OR soccer) sourcelang:%s`, playerName, lang)
	params := url.Values{
		"query":      []string{query},
		"mode":       []string{"ArtList"},
		"format":     []string{"json"},
		"maxrecords": []string{"20"},
		"timespan":   []string{gdeltDefaultTimespan},
		"sort":       []string{"DateDesc"},
	}
	endpoint := p.baseURL + "?" + params.Encode()

	backoff := gdelt429BackoffStart
	for attempt := 0; attempt <= gdeltMaxRetries; attempt++ {
		candidates, retryAfter, err := p.doRequest(ctx, endpoint, playerName)
		if err == nil {
			return candidates, nil
		}
		if retryAfter == 0 || attempt == gdeltMaxRetries {
			return nil, err
		}
		log.Printf("gdelt: 429 for %q (lang=%s), backing off %s before retry", playerName, lang, retryAfter)
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
	return nil, fmt.Errorf("gdelt: retries exhausted for %q (lang=%s)", playerName, lang)
}

// doRequest performs one GDELT request and returns either a candidate slice
// or, on HTTP 429, a non-zero retryAfter so the caller can sleep and retry.
func (p *gdeltMediaProvider) doRequest(ctx context.Context, endpoint, playerName string) ([]domain.MediaArticleCandidate, time.Duration, error) {
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

	switch {
	case resp.StatusCode == http.StatusOK:
		// happy path
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, gdelt429BackoffStart, fmt.Errorf("gdelt rate limited (HTTP 429)")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, 0, fmt.Errorf("gdelt returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Some empty-result responses come back as text/plain "no results found";
	// swallow them quietly.
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return nil, 0, nil
	}

	var response gdeltResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, 0, err
	}

	candidates := make([]domain.MediaArticleCandidate, 0, len(response.Articles))
	for _, art := range response.Articles {
		title := strings.TrimSpace(art.Title)
		if title == "" {
			continue
		}
		candidates = append(candidates, domain.MediaArticleCandidate{
			PlayerName:  playerName,
			Title:       title,
			Summary:     title, // GDELT doesn't return body/snippet
			Source:      strings.TrimSpace(art.Domain),
			SourceURL:   strings.TrimSpace(art.URL),
			PublishedAt: parseGDELTDate(art.SeenDate),
		})
	}
	return candidates, 0, nil
}

func (p *gdeltMediaProvider) respectRateLimit(ctx context.Context) error {
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

func (p *gdeltMediaProvider) markCalled() {
	p.callMu.Lock()
	p.lastCallAt = time.Now()
	p.callMu.Unlock()
}

// applyDomainDenylist filters out content farms and obvious junk that GDELT
// surfaces in long-tail searches. The list is conservative — easy to extend
// when a new offender appears in production.
func applyDomainDenylist(items []domain.MediaArticleCandidate) []domain.MediaArticleCandidate {
	deny := map[string]struct{}{
		"el-balad.com":                    {},
		"easternriverinachronicle.com.au": {},
	}
	filtered := make([]domain.MediaArticleCandidate, 0, len(items))
	for _, item := range items {
		domain := strings.ToLower(strings.TrimSpace(item.Source))
		if _, blocked := deny[domain]; blocked {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func parseGDELTDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	if parsed, err := time.Parse("20060102T150405Z", raw); err == nil {
		return parsed.UTC()
	}
	return time.Now().UTC()
}

type gdeltResponse struct {
	Articles []gdeltArticle `json:"articles"`
}

type gdeltArticle struct {
	URL           string `json:"url"`
	URLMobile     string `json:"url_mobile"`
	Title         string `json:"title"`
	SeenDate      string `json:"seendate"`
	SocialImage   string `json:"socialimage"`
	Domain        string `json:"domain"`
	Language      string `json:"language"`
	SourceCountry string `json:"sourcecountry"`
}
