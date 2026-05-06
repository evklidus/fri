package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	youTubeProviderName         = "youtube-data-api"
	youTubeFallbackProviderName = "youtube-fallback"
	youTubeDefaultBaseURL       = "https://www.googleapis.com/youtube/v3"
	youTubeViewsLookbackDays    = 7
	youTubeSearchPageSize       = 20
	youTubeViewsMin             = 10_000
	youTubeViewsMax             = 50_000_000
)

type youTubeSocialProvider struct {
	apiKey   string
	baseURL  string
	client   *http.Client
	fallback socialProvider
}

func newYouTubeSocialProvider(apiKey, baseURL string, timeout time.Duration, fallback socialProvider) socialProvider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = youTubeDefaultBaseURL
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &youTubeSocialProvider{
		apiKey:   strings.TrimSpace(apiKey),
		baseURL:  baseURL,
		client:   &http.Client{Timeout: timeout},
		fallback: fallback,
	}
}

func (p *youTubeSocialProvider) Name() string { return youTubeProviderName }

// FetchSocialSnapshot is a hybrid: it pulls real YouTube views_7d from the
// Data API, then layers it on top of the demo provider's deterministic
// followers/engagement/mentions (until we wire real Instagram/Twitter
// providers). This makes the social score directional even with one real
// signal, instead of zeroed-out followers.
func (p *youTubeSocialProvider) FetchSocialSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.SocialSnapshot, error) {
	base, err := p.fallback.FetchSocialSnapshot(ctx, player)
	if err != nil {
		return domain.SocialSnapshot{}, err
	}

	views, err := p.viewsLast7Days(ctx, player)
	if err != nil {
		base.Provider = youTubeFallbackProviderName
		return base, nil
	}

	base.Provider = youTubeProviderName
	base.YouTubeViews7D = views

	youtubeNormalized := normalizeLog(float64(views), youTubeViewsMin, youTubeViewsMax)
	followersNormalized := normalizeLog(float64(base.Followers), 50_000, 500_000_000)
	engagementNormalized := normalizeLinear(base.EngagementRate, 1, 8)

	base.NormalizedScore = clampScore(
		(followersNormalized * 0.40) +
			(engagementNormalized * 0.30) +
			(base.MentionsGrowth7D * 0.20) +
			(youtubeNormalized * 0.10),
	)
	base.SnapshotAt = time.Now().UTC()
	return base, nil
}

func (p *youTubeSocialProvider) viewsLast7Days(ctx context.Context, player domain.PlayerSyncTarget) (int64, error) {
	publishedAfter := time.Now().UTC().Add(-time.Duration(youTubeViewsLookbackDays) * 24 * time.Hour).Format(time.RFC3339)
	q := player.Name
	if club := strings.TrimSpace(player.Club); club != "" {
		q += " " + club
	} else {
		q += " football"
	}

	videoIDs, err := p.searchVideoIDs(ctx, q, publishedAfter)
	if err != nil {
		return 0, err
	}
	if len(videoIDs) == 0 {
		return 0, nil
	}
	return p.sumViewCounts(ctx, videoIDs)
}

func (p *youTubeSocialProvider) searchVideoIDs(ctx context.Context, query, publishedAfter string) ([]string, error) {
	params := url.Values{
		"part":           []string{"snippet"},
		"q":              []string{query},
		"type":           []string{"video"},
		"order":          []string{"viewCount"},
		"publishedAfter": []string{publishedAfter},
		"maxResults":     []string{strconv.Itoa(youTubeSearchPageSize)},
		"key":            []string{p.apiKey},
	}
	var response youTubeSearchResponse
	if err := p.get(ctx, "/search", params, &response); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(response.Items))
	for _, item := range response.Items {
		if id := strings.TrimSpace(item.ID.VideoID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (p *youTubeSocialProvider) sumViewCounts(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	params := url.Values{
		"part": []string{"statistics"},
		"id":   []string{strings.Join(ids, ",")},
		"key":  []string{p.apiKey},
	}
	var response youTubeVideosResponse
	if err := p.get(ctx, "/videos", params, &response); err != nil {
		return 0, err
	}

	var total int64
	for _, item := range response.Items {
		raw := strings.TrimSpace(item.Statistics.ViewCount)
		if raw == "" {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		total += v
	}
	if total < 0 {
		total = math.MaxInt64 // overflow guard, very unlikely for views
	}
	return total, nil
}

func (p *youTubeSocialProvider) get(ctx context.Context, path string, params url.Values, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("youtube returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

type youTubeSearchResponse struct {
	Items []youTubeSearchItem `json:"items"`
}

type youTubeSearchItem struct {
	ID youTubeSearchItemID `json:"id"`
}

type youTubeSearchItemID struct {
	VideoID string `json:"videoId"`
}

type youTubeVideosResponse struct {
	Items []youTubeVideoItem `json:"items"`
}

type youTubeVideoItem struct {
	Statistics youTubeStatistics `json:"statistics"`
}

type youTubeStatistics struct {
	ViewCount string `json:"viewCount"`
}
