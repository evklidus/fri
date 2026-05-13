package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
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
	ListSyncTargets(ctx context.Context) ([]domain.PlayerSyncTarget, error)
	GetHistory(ctx context.Context, playerID int64) ([]domain.HistoryPoint, error)
	ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error)
	CreateVoteAndRefreshScore(ctx context.Context, vote domain.Vote) (*domain.Score, error)
	StartComponentUpdate(ctx context.Context, component, provider string) (int64, error)
	FinishComponentUpdate(ctx context.Context, updateID int64, status, message string, recordsSeen int) error
	ListComponentUpdates(ctx context.Context, limit int) ([]domain.ComponentUpdate, error)
	ApplySocialSync(ctx context.Context, snapshots []domain.SocialSnapshot, provider string) ([]domain.PlayerSyncDelta, error)
	ApplyPerformanceSync(ctx context.Context, snapshots []domain.PerformanceSnapshot, provider string) ([]domain.PlayerSyncDelta, error)
	ApplyMediaSync(ctx context.Context, results []domain.MediaSyncPlayerResult, provider string) ([]domain.PlayerSyncDelta, error)
	GetExternalIDs(ctx context.Context, playerID int64, provider string) (*domain.PlayerExternalIDs, error)
	UpsertExternalIDs(ctx context.Context, ids domain.PlayerExternalIDs) error
	DeleteExternalIDs(ctx context.Context, playerID int64, provider string) error
	HasRecentVote(ctx context.Context, playerID int64, ipHash string, since time.Time) (bool, error)
	ApplyCharacterSync(ctx context.Context, candidates []domain.CharacterEventCandidate, perPlayerCap float64) ([]domain.PlayerSyncDelta, error)
	GetCareerBaseline(ctx context.Context, playerID int64) (*domain.PlayerCareerBaseline, error)
	UpsertCareerBaseline(ctx context.Context, baseline domain.PlayerCareerBaseline) error
	ListPendingEvents(ctx context.Context, limit int) ([]domain.PendingEvent, error)
	ListPendingEventsForPlayer(ctx context.Context, playerID int64, limit int) ([]domain.PendingEvent, error)
	GetPendingEvent(ctx context.Context, eventID int64) (*domain.PendingEvent, error)
	SubmitEventVote(ctx context.Context, eventID int64, ipHash string, suggestedDelta float64) (bool, error)
	FinalizePendingEvents(ctx context.Context) (int, error)
}

// voteCooldown bounds how often a single IP can vote for the same player.
// Tuned so an honest fan can re-vote daily without obvious manual abuse.
const voteCooldown = 24 * time.Hour

type Service struct {
	repo                   repository
	mediaProvider          mediaProvider
	socialProvider         socialProvider
	performanceProvider    performanceProvider
	careerBaselineProvider careerBaselineProvider // optional; may be nil if no API-Football key

	// Per-component sync locks prevent overlapping scheduled and ad-hoc HTTP
	// runs of the same component. We use TryLock so a concurrent caller
	// returns immediately with status=skipped instead of queueing.
	performanceSyncMu    sync.Mutex
	socialSyncMu         sync.Mutex
	mediaSyncMu          sync.Mutex
	characterSyncMu      sync.Mutex
	careerBaselineSyncMu sync.Mutex
	finalizeEventsMu     sync.Mutex
}

func New(repo repository, mediaProvider mediaProvider, socialProvider socialProvider, performanceProvider performanceProvider) *Service {
	return &Service{
		repo:                repo,
		mediaProvider:       mediaProvider,
		socialProvider:      socialProvider,
		performanceProvider: performanceProvider,
	}
}

// WithCareerBaselineProvider wires an optional career-baseline source. Pass
// the api-football provider here when an API key is available — without one
// the SyncCareerBaseline call no-ops and the Performance score falls back to
// current-season-only.
//
// Returns the service for fluent chaining at construction time.
func (s *Service) WithCareerBaselineProvider(p careerBaselineProvider) *Service {
	s.careerBaselineProvider = p
	return s
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
				League:          leagueForClub(item.Club),
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

func (s *Service) ListComponentUpdates(ctx context.Context, limit int) ([]domain.ComponentUpdate, error) {
	return s.repo.ListComponentUpdates(ctx, limit)
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

	ipHash := hashIP(rawIP)

	// Anti-abuse: one vote per (player, IP) per 24h window. Empty/blank rawIP
	// is treated as "no IP available" and skipped to keep tests/CLI happy.
	if strings.TrimSpace(rawIP) != "" {
		windowStart := time.Now().UTC().Add(-voteCooldown)
		recent, err := s.repo.HasRecentVote(ctx, playerID, ipHash, windowStart)
		if err != nil {
			return nil, fmt.Errorf("check recent vote: %w", err)
		}
		if recent {
			return nil, fmt.Errorf("vote rate limit: already voted for this player in the last 24h")
		}
	}

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

// leagueForClub maps a canonical club name (as stored in seed data) to its
// league. Seed contains a small, known set of clubs — keeping this as a
// lookup table avoids the need for an external "leagues" reference table at
// MVP scale. Unknown clubs fall through to "Other" so the leaderboard still
// renders.
func leagueForClub(club string) string {
	normalized := normalizeClubName(club)
	if league, ok := clubLeagueIndex[normalized]; ok {
		return league
	}
	return "Other"
}

func normalizeClubName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c",
		".", " ", "-", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(value)), " ")
}

// clubLeagueIndex covers clubs present in current seed data plus common
// aliases ("Man City" / "Manchester City"). Add entries as the player roster
// grows.
var clubLeagueIndex = map[string]string{
	// Premier League
	"manchester city":   "Premier League",
	"man city":          "Premier League",
	"manchester united": "Premier League",
	"man united":        "Premier League",
	"liverpool":         "Premier League",
	"arsenal":           "Premier League",
	"chelsea":           "Premier League",
	"tottenham":         "Premier League",
	"newcastle":         "Premier League",
	"aston villa":       "Premier League",
	"west ham":          "Premier League",

	// La Liga
	"real madrid":     "La Liga",
	"fc barcelona":    "La Liga",
	"barcelona":       "La Liga",
	"atletico madrid": "La Liga",
	"real sociedad":   "La Liga",
	"villarreal":      "La Liga",

	// Bundesliga
	"bayern munich":     "Bundesliga",
	"bayer leverkusen":  "Bundesliga",
	"borussia dortmund": "Bundesliga",
	"rb leipzig":        "Bundesliga",

	// Serie A
	"inter":       "Serie A",
	"inter milan": "Serie A",
	"ac milan":    "Serie A",
	"juventus":    "Serie A",
	"napoli":      "Serie A",
	"roma":        "Serie A",
	"atalanta":    "Serie A",

	// Ligue 1
	"psg":                 "Ligue 1",
	"paris saint germain": "Ligue 1",
	"marseille":           "Ligue 1",
	"lyon":                "Ligue 1",
	"monaco":              "Ligue 1",

	// Saudi Pro League
	"al nassr":   "Saudi Pro League",
	"al ittihad": "Saudi Pro League",
	"al hilal":   "Saudi Pro League",

	// Süper Lig
	"fenerbahce":  "Süper Lig",
	"galatasaray": "Süper Lig",
	"besiktas":    "Süper Lig",

	// MLS
	"inter miami": "MLS",
	"la galaxy":   "MLS",
	"lafc":        "MLS",
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

// hashIP returns the SHA-256 hex digest of an IP address. We never store
// raw IPs — only their hashes — so the system stays PII-compliant under
// GDPR / Russian data laws. The hash is enough to correlate repeat votes
// from the same source for anti-abuse.
func hashIP(rawIP string) string {
	sum := sha256.Sum256([]byte(rawIP))
	return hex.EncodeToString(sum[:])
}
