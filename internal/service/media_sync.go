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

const mediaProviderName = "google-news-rss"

type mediaProvider interface {
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

func NewMediaProvider(timeout time.Duration, articlesPerPlayer int) mediaProvider {
	return newGoogleNewsRSSProvider(timeout, articlesPerPlayer)
}

func (p *googleNewsRSSProvider) FetchPlayerArticles(ctx context.Context, player domain.PlayerSyncTarget) ([]domain.MediaArticleCandidate, error) {
	query := fmt.Sprintf(`"%s" football`, player.Name)
	feedURL := "https://news.google.com/rss/search?q=" + url.QueryEscape(query) + "&hl=en-US&gl=US&ceid=US:en"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "FRI-Bot/1.0 (+https://localhost)")

	resp, err := p.client.Do(req)
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
	startedAt := time.Now().UTC()
	updateID, err := s.repo.StartComponentUpdate(ctx, "media", mediaProviderName)
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "media",
		Provider:  mediaProviderName,
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

	deltas, err := s.repo.ApplyMediaSync(ctx, syncResults, mediaProviderName)
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
			Source:       mediaProviderName,
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

func sentimentScore(text string) float64 {
	text = strings.ToLower(text)
	positive := []string{
		"goal", "assist", "winner", "wins", "won", "hat-trick", "best", "record", "motm", "man of the match",
		"brace", "masterclass", "award", "ovation", "historic", "praised", "excellent", "clean sheet", "comeback",
	}
	negative := []string{
		"injury", "ban", "banned", "controversy", "backlash", "criticism", "suspended", "red card", "charged",
		"arrest", "scandal", "problem", "poor", "misses", "miss", "negative", "abuse", "racism", "doping",
	}

	score := 0.0
	for _, token := range positive {
		if strings.Contains(text, token) {
			score += 1
		}
	}
	for _, token := range negative {
		if strings.Contains(text, token) {
			score -= 1
		}
	}
	return score
}

func normalizeSentiment(value float64) float64 {
	normalized := 50 + (value * 12)
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

func sentimentImpactType(value float64) string {
	if value > 0.25 {
		return "pos"
	}
	if value < -0.25 {
		return "neg"
	}
	return "neu"
}

func sentimentImpactDelta(value float64) float64 {
	delta := round1(value * 0.6)
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
