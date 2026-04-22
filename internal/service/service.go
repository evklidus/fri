package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/platform/legacyhtml"
)

type repository interface {
	PlayerCount(ctx context.Context) (int, error)
	ReplaceAllSeedData(ctx context.Context, players []domain.PlayerWithScore, history map[string][]domain.HistoryPoint, news []domain.NewsItem) error
	ListPlayers(ctx context.Context, search, position, club string) ([]domain.PlayerWithScore, error)
	GetPlayer(ctx context.Context, id int64) (*domain.PlayerWithScore, error)
	GetHistory(ctx context.Context, playerID int64) ([]domain.HistoryPoint, error)
	ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error)
	CreateVoteAndRefreshScore(ctx context.Context, vote domain.Vote) (*domain.Score, error)
}

type Service struct {
	repo repository
}

func New(repo repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) SeedIfEmpty(ctx context.Context, sourceHTMLPath string) error {
	count, err := s.repo.PlayerCount(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return s.ForceSeed(ctx, sourceHTMLPath)
}

func (s *Service) ForceSeed(ctx context.Context, sourceHTMLPath string) error {
	parsed, err := legacyhtml.ParseFile(sourceHTMLPath)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	players := make([]domain.PlayerWithScore, 0, len(parsed.Players))
	history := make(map[string][]domain.HistoryPoint, len(parsed.Players))

	for _, item := range parsed.Players {
		trendValue := parseSignedTrend(item.Trend, item.Dir)
		player := domain.PlayerWithScore{
			Player: domain.Player{
				Slug:            slugify(item.Name),
				Name:            item.Name,
				Club:            item.Club,
				Position:        item.Pos,
				Age:             item.Age,
				Emoji:           item.Emoji,
				PhotoData:       item.Photo,
				ThemeBackground: item.BG,
				SummaryEN:       item.SumEN,
				SummaryRU:       item.SumRU,
			},
			Score: domain.Score{
				FRI:            round1(item.FRI),
				Performance:    round1(item.Perf),
				Social:         round1(item.Social),
				Fan:            round1(item.Fan),
				FanBase:        round1(item.Fan),
				Media:          round1(item.Media),
				Character:      round1(item.Char),
				TrendValue:     round1(math.Abs(trendValue)),
				TrendDirection: normalizeDirection(item.Dir),
				CalculatedAt:   now,
			},
		}

		players = append(players, player)

		sevenDaysAgo := round1(item.FRI - trendValue)
		thirtyDaysAgo := round1(item.FRI - trendValue*1.8)

		history[item.Name] = []domain.HistoryPoint{
			{FRI: clampScore(thirtyDaysAgo), Delta: 0, CalculatedAt: now.AddDate(0, 0, -30)},
			{FRI: clampScore(sevenDaysAgo), Delta: round1(sevenDaysAgo - thirtyDaysAgo), CalculatedAt: now.AddDate(0, 0, -7)},
			{FRI: round1(item.FRI), Delta: round1(item.FRI - sevenDaysAgo), CalculatedAt: now},
		}
	}

	news := make([]domain.NewsItem, 0, len(parsed.News))
	for _, item := range parsed.News {
		news = append(news, domain.NewsItem{
			PlayerName:   item.Player,
			ImpactType:   item.Impact,
			ImpactDelta:  parseSignedDelta(item.Delta),
			RelativeTime: item.Time,
			TitleEN:      item.TitleEN,
			TitleRU:      item.TitleRU,
			SummaryEN:    item.SummaryEN,
			SummaryRU:    item.SummaryRU,
			Source:       "legacy-html",
			PublishedAt:  parseRelativeTime(now, item.Time),
		})
	}

	return s.repo.ReplaceAllSeedData(ctx, players, history, news)
}

func (s *Service) ListPlayers(ctx context.Context, search, position, club string) ([]domain.PlayerWithScore, error) {
	return s.repo.ListPlayers(ctx, search, position, club)
}

func (s *Service) GetPlayer(ctx context.Context, id int64) (*domain.PlayerWithScore, error) {
	return s.repo.GetPlayer(ctx, id)
}

func (s *Service) GetHistory(ctx context.Context, playerID int64) ([]domain.HistoryPoint, error) {
	return s.repo.GetHistory(ctx, playerID)
}

func (s *Service) ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error) {
	return s.repo.ListNews(ctx, playerID)
}

func (s *Service) SubmitVote(ctx context.Context, playerID int64, input domain.VoteInput, rawIP string) (*domain.Score, error) {
	if input.RatingOverall < 1 || input.RatingOverall > 5 {
		return nil, fmt.Errorf("rating_overall must be between 1 and 5")
	}
	if input.RatingHype < 1 || input.RatingHype > 10 {
		return nil, fmt.Errorf("rating_hype must be between 1 and 10")
	}
	if input.RatingTier < 0 || input.RatingTier > 100 {
		return nil, fmt.Errorf("rating_tier must be between 0 and 100")
	}
	if input.BehaviorScore < 0 || input.BehaviorScore > 100 {
		return nil, fmt.Errorf("behavior_score must be between 0 and 100")
	}
	if strings.TrimSpace(input.SessionID) == "" {
		input.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())
	}

	internalScore := round1(
		(float64(input.RatingOverall*20) * 0.40) +
			(float64(input.RatingHype*10) * 0.30) +
			(float64(input.RatingTier) * 0.20) +
			(float64(input.BehaviorScore) * 0.10),
	)

	sum := sha256.Sum256([]byte(rawIP))
	ipHash := hex.EncodeToString(sum[:])

	return s.repo.CreateVoteAndRefreshScore(ctx, domain.Vote{
		PlayerID:      playerID,
		SessionID:     input.SessionID,
		RatingOverall: input.RatingOverall,
		RatingHype:    input.RatingHype,
		RatingTier:    input.RatingTier,
		BehaviorScore: input.BehaviorScore,
		InternalScore: internalScore,
		IPHash:        ipHash,
	})
}

func normalizeDirection(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "up":
		return "up"
	case "down":
		return "down"
	default:
		return "stable"
	}
}

func parseSignedTrend(raw, dir string) float64 {
	value, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	switch normalizeDirection(dir) {
	case "up":
		return math.Abs(value)
	case "down":
		return -math.Abs(value)
	default:
		return 0
	}
}

func parseSignedDelta(raw string) float64 {
	value, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return value
}

func parseRelativeTime(now time.Time, raw string) time.Time {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if len(raw) < 2 {
		return now
	}

	unit := raw[len(raw)-1]
	value, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil {
		return now
	}

	switch unit {
	case 'h':
		return now.Add(-time.Duration(value) * time.Hour)
	case 'd':
		return now.AddDate(0, 0, -value)
	default:
		return now
	}
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false

	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}

	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "player"
	}
	return slug
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

func clampScore(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return round1(value)
}
