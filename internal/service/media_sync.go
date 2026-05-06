package service

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	mediaProviderName  = "gdelt"
	mediaSyncBatchSize = 25
)

type mediaProvider interface {
	Name() string
	FetchPlayerArticles(ctx context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error)
}

type googleNewsRSSProvider struct {
	client            *http.Client
	articlesPerPlayer int
}

func newGoogleNewsRSSProvider(timeout time.Duration, articlesPerPlayer int) mediaProvider {
	if articlesPerPlayer <= 0 {
		articlesPerPlayer = 3
	}

	return &googleNewsRSSProvider{
		client: &http.Client{
			Timeout: timeout,
		},
		articlesPerPlayer: articlesPerPlayer,
	}
}

func NewMediaProvider(timeout time.Duration, articlesPerPlayer int, mediaStackAPIKey, mediaStackBaseURL string) mediaProvider {
	if strings.TrimSpace(mediaStackAPIKey) != "" {
		return newMediaStackMediaProvider(mediaStackAPIKey, mediaStackBaseURL, timeout, articlesPerPlayer)
	}
	// Fallback to GDELT (no key required) for development without a paid
	// MediaStack subscription. GDELT throttles aggressively, so bump timeout.
	if timeout < gdeltDefaultTimeout {
		timeout = gdeltDefaultTimeout
	}
	return newGDELTMediaProvider(timeout, articlesPerPlayer, gdeltDefaultMinGap)
}

func (p *googleNewsRSSProvider) Name() string { return "google-news-rss" }

func (p *googleNewsRSSProvider) FetchPlayerArticles(ctx context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error) {
	query := fmt.Sprintf(`"%s" football`, player.Name)
	feedURL := "https://news.google.com/rss/search?q=" + url.QueryEscape(query) + "&hl=en-US&gl=US&ceid=US:en"

	resp, err := p.getWithRetry(ctx, feedURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("rss provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var feed rssFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, err
	}

	candidates := make([]domain.MediaArticleCandidate, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		publishedAt := parseRSSDate(item.PubDate)
		summary := strings.TrimSpace(stripHTML(item.Description))
		if summary == "" {
			summary = item.Title
		}

		candidates = append(candidates, domain.MediaArticleCandidate{
			PlayerName:  player.Name,
			Title:       strings.TrimSpace(item.Title),
			Summary:     summary,
			Source:      extractSourceName(item.Source, item.Title),
			SourceURL:   strings.TrimSpace(item.Link),
			PublishedAt: publishedAt,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].PublishedAt.After(candidates[j].PublishedAt)
	})

	if len(candidates) > p.articlesPerPlayer {
		candidates = candidates[:p.articlesPerPlayer]
	}

	return dedupeArticles(candidates), nil
}

func (p *googleNewsRSSProvider) getWithRetry(ctx context.Context, feedURL string) (*http.Response, error) {
	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "FRI-Bot/1.0 (+https://localhost)")

		resp, err := p.client.Do(req)
		if err == nil && resp.StatusCode < http.StatusInternalServerError && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("rss provider returned retryable status %d", resp.StatusCode)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}

	return nil, lastErr
}

type rssFeed struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			Source      string `xml:"source"`
		} `xml:"item"`
	} `xml:"channel"`
}

func dedupeArticles(items []domain.MediaArticleCandidate) []domain.MediaArticleCandidate {
	seen := make(map[string]struct{}, len(items))
	result := make([]domain.MediaArticleCandidate, 0, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Title)) + "::" + strings.ToLower(strings.TrimSpace(item.Source))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func parseRSSDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func stripHTML(value string) string {
	replacer := strings.NewReplacer(
		"<br>", " ",
		"<br/>", " ",
		"<br />", " ",
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
	)
	cleaned := replacer.Replace(value)
	for {
		start := strings.Index(cleaned, "<")
		if start == -1 {
			break
		}
		end := strings.Index(cleaned[start:], ">")
		if end == -1 {
			break
		}
		cleaned = cleaned[:start] + cleaned[start+end+1:]
	}
	return strings.Join(strings.Fields(cleaned), " ")
}

// topByFRI returns up to `limit` targets sorted by current FRI descending.
// Used to bound expensive sync passes (media) to the most-watched players.
// Returns the original slice unchanged when no truncation is needed.
func topByFRI(targets []domain.PlayerSyncTarget, limit int) []domain.PlayerSyncTarget {
	if limit <= 0 || len(targets) <= limit {
		return targets
	}
	sorted := make([]domain.PlayerSyncTarget, len(targets))
	copy(sorted, targets)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Score.FRI > sorted[j].Score.FRI
	})
	return sorted[:limit]
}

func extractSourceName(source, title string) string {
	source = strings.TrimSpace(source)
	if source != "" {
		return source
	}
	if parts := strings.Split(title, " - "); len(parts) > 1 {
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return "Google News"
}

func (s *Service) SyncMedia(ctx context.Context) (*domain.ComponentSyncResult, error) {
	providerName := s.mediaProvider.Name()
	if !s.mediaSyncMu.TryLock() {
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "media",
			Provider:   providerName,
			Status:     "skipped",
			Message:    "media sync already in progress",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}
	defer s.mediaSyncMu.Unlock()

	startedAt := time.Now().UTC()
	updateID, err := s.repo.StartComponentUpdate(ctx, "media", providerName)
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "media",
		Provider:  providerName,
		Status:    "running",
		StartedAt: startedAt,
	}

	finish := func(status, message string, records int, deltas []domain.PlayerSyncDelta, err error) (*domain.ComponentSyncResult, error) {
		result.Status = status
		result.Message = message
		result.RecordsSeen = records
		result.Players = deltas
		result.FinishedAt = time.Now().UTC()
		if finishErr := s.repo.FinishComponentUpdate(ctx, updateID, status, message, records); finishErr != nil && err == nil {
			err = finishErr
		}
		return result, err
	}

	targets, err := s.repo.ListSyncTargets(ctx)
	if err != nil {
		return finish("failed", err.Error(), 0, nil, err)
	}

	// Media sync is heavily rate-limited (GDELT ≈3s/req × EN+RU per player =
	// 6s/player). Cap each run to the top-N by FRI to keep wall-clock under a
	// few minutes; less popular players get refreshed on subsequent runs.
	targets = topByFRI(targets, mediaSyncBatchSize)

	var syncResults []domain.MediaSyncPlayerResult
	var articlesSeen int

	for _, player := range targets {
		articles, fetchErr := s.mediaProvider.FetchPlayerArticles(ctx, player)
		if fetchErr != nil {
			continue
		}

		syncResult := s.buildMediaSyncResult(player, articles)
		articlesSeen += syncResult.ArticlesCount
		syncResults = append(syncResults, syncResult)
	}

	if len(syncResults) == 0 {
		return finish("completed", "no external media articles found", 0, nil, nil)
	}

	deltas, err := s.repo.ApplyMediaSync(ctx, syncResults, providerName)
	if err != nil {
		return finish("failed", err.Error(), articlesSeen, nil, err)
	}

	return finish("completed", fmt.Sprintf("media sync completed for %d players", len(syncResults)), articlesSeen, deltas, nil)
}

func (s *Service) buildMediaSyncResult(player domain.PlayerSyncTarget, articles []domain.MediaArticleCandidate) domain.MediaSyncPlayerResult {
	if len(articles) == 0 {
		return domain.MediaSyncPlayerResult{
			PlayerID:      player.ID,
			PlayerName:    player.Name,
			MediaScore:    player.Score.Media,
			ArticlesCount: 0,
		}
	}

	var sentimentSum float64
	var tierSum float64
	syncArticles := make([]domain.MediaSyncArticle, 0, len(articles))

	for _, article := range articles {
		sentiment := sentimentScore(article.Title + " " + article.Summary)
		tier := sourceTier(article.Source)
		sentimentSum += sentiment
		tierSum += tier

		syncArticles = append(syncArticles, domain.MediaSyncArticle{
			PlayerID:     player.ID,
			PlayerName:   player.Name,
			TitleEN:      article.Title,
			TitleRU:      article.Title,
			SummaryEN:    article.Summary,
			SummaryRU:    article.Summary,
			Source:       s.mediaProvider.Name(),
			SourceURL:    article.SourceURL,
			SourceTier:   tier,
			Sentiment:    sentiment,
			ImpactType:   sentimentImpactType(sentiment),
			ImpactDelta:  sentimentImpactDelta(sentiment),
			RelativeTime: relativeTime(article.PublishedAt, time.Now().UTC()),
			PublishedAt:  article.PublishedAt,
		})
	}

	mentionVolume := math.Min(100, float64(len(syncArticles))*25)
	avgSentiment := normalizeSentiment(sentimentSum / float64(len(syncArticles)))
	avgTier := tierSum / float64(len(syncArticles))

	mediaScore := round1((mentionVolume * 0.4) + (avgSentiment * 0.4) + (avgTier * 0.2))
	return domain.MediaSyncPlayerResult{
		PlayerID:      player.ID,
		PlayerName:    player.Name,
		MediaScore:    mediaScore,
		Articles:      syncArticles,
		ArticlesCount: len(syncArticles),
	}
}

// sentimentScore wraps the package-level analyzer so that the rest of
// media_sync continues to consume a single function. Returns a score in
// [-1, +1] (compatible with VADER's compound output).
func sentimentScore(text string) float64 {
	return analyzeSentiment(text)
}

// normalizeSentiment maps a [-1, +1] polarity score onto the [0, 100] media
// component scale: -1 → 0, 0 → 50, +1 → 100.
func normalizeSentiment(value float64) float64 {
	normalized := 50 + (value * 50)
	if normalized < 0 {
		return 0
	}
	if normalized > 100 {
		return 100
	}
	return round1(normalized)
}

func sourceTier(source string) float64 {
	source = strings.ToLower(strings.TrimSpace(source))
	tier90 := []string{"bbc", "sky sports", "espn", "reuters", "the athletic", "guardian", "uefa", "fifa"}
	tier75 := []string{"goal.com", "marca", "as", "sport", "l'équipe", "football london", "90min", "givemesport"}

	for _, item := range tier90 {
		if strings.Contains(source, item) {
			return 90
		}
	}
	for _, item := range tier75 {
		if strings.Contains(source, item) {
			return 75
		}
	}
	return 60
}

// sentimentImpactType buckets a [-1, +1] polarity score into pos/neg/neu.
// Threshold 0.15 follows VADER's standard cutoff for short text (with a bit
// of slack: short football headlines rarely cross 0.5).
func sentimentImpactType(value float64) string {
	if value > 0.15 {
		return "pos"
	}
	if value < -0.15 {
		return "neg"
	}
	return "neu"
}

// sentimentImpactDelta scales a [-1, +1] polarity score onto the FRI delta
// budget of [-3, +3].
func sentimentImpactDelta(value float64) float64 {
	delta := round1(value * 3)
	if delta > 3 {
		return 3
	}
	if delta < -3 {
		return -3
	}
	return delta
}

func relativeTime(publishedAt, now time.Time) string {
	if now.Before(publishedAt) {
		return "0h"
	}

	diff := now.Sub(publishedAt)
	if diff < 24*time.Hour {
		return fmt.Sprintf("%dh", int(diff.Hours()))
	}
	return fmt.Sprintf("%dd", int(diff.Hours()/24))
}
