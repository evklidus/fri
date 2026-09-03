package domain

import (
	"strings"
	"time"
)

type Player struct {
	ID        int64      `json:"id"`
	Slug      string     `json:"slug"`
	Name      string     `json:"name"`
	Club      string     `json:"club"`
	League    string     `json:"league"`
	Position  string     `json:"position"`
	Age       int        `json:"age"`
	BirthDate *time.Time `json:"birth_date,omitempty"`
	Emoji     string     `json:"emoji"`
	// PhotoData is the legacy base64 data URI seeded from the source HTML.
	// PhotoURL points at api-football's CDN and is preferred when set: it
	// keeps the players payload small enough to grow a roster into.
	PhotoData       string    `json:"photo_data"`
	PhotoURL        string    `json:"photo_url"`
	ThemeBackground string    `json:"theme_background"`
	SummaryEN       string    `json:"summary_en"`
	SummaryRU       string    `json:"summary_ru"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// AgeFromBirthDate returns a player's age in whole years on the given day.
// Returns 0 when the birth date is unknown, letting callers keep whatever
// stored age they already have.
func AgeFromBirthDate(birth *time.Time, on time.Time) int {
	if birth == nil || birth.IsZero() {
		return 0
	}
	years := on.Year() - birth.Year()
	// Compare month and day rather than day-of-year: a leap year shifts every
	// date after February by one, so a player born 12 July 2000 (leap) read as
	// a day "earlier" than 12 July 2026 and lost a year on their own birthday.
	if on.Month() < birth.Month() || (on.Month() == birth.Month() && on.Day() < birth.Day()) {
		years--
	}
	if years < 0 || years > 120 {
		return 0
	}
	return years
}

type Score struct {
	PlayerID             int64     `json:"player_id"`
	FRI                  float64   `json:"fri"`
	Performance          float64   `json:"performance"`
	Social               float64   `json:"social"`
	Fan                  float64   `json:"fan"`
	FanBase              float64   `json:"fan_base"`
	Media                float64   `json:"media"`
	Character            float64   `json:"character"`
	TrendValue           float64   `json:"trend_value"`
	TrendDirection       string    `json:"trend_direction"`
	CalculatedAt         time.Time `json:"calculated_at"`
	PerformanceUpdatedAt time.Time `json:"performance_updated_at"`
	SocialUpdatedAt      time.Time `json:"social_updated_at"`
	FanUpdatedAt         time.Time `json:"fan_updated_at"`
	MediaUpdatedAt       time.Time `json:"media_updated_at"`
	CharacterUpdatedAt   time.Time `json:"character_updated_at"`
}

// PlayerWithScore joins a player with their current scores. Locked marks a
// row whose contents were withheld because the caller has no account — the
// rank is still real, everything else is blank.
type PlayerWithScore struct {
	Locked bool `json:"locked,omitempty"`

	Player
	Score
}

type HistoryPoint struct {
	ID           int64     `json:"id"`
	PlayerID     int64     `json:"player_id"`
	FRI          float64   `json:"fri"`
	Delta        float64   `json:"delta"`
	CalculatedAt time.Time `json:"calculated_at"`
}

type NewsItem struct {
	// Locked marks an article about a player whose leaderboard place is
	// withheld from this caller. Everything identifying has been stripped
	// server-side; the UI renders a blurred placeholder in its place.
	Locked bool `json:"locked,omitempty"`

	ID           int64     `json:"id"`
	PlayerID     *int64    `json:"player_id"`
	PlayerName   string    `json:"player_name"`
	ImpactType   string    `json:"impact_type"`
	ImpactDelta  float64   `json:"impact_delta"`
	RelativeTime string    `json:"relative_time"`
	TitleEN      string    `json:"title_en"`
	TitleRU      string    `json:"title_ru"`
	SummaryEN    string    `json:"summary_en"`
	SummaryRU    string    `json:"summary_ru"`
	Source       string    `json:"source"`
	SourceURL    string    `json:"source_url"`
	SourceTier   float64   `json:"source_tier"`
	Sentiment    float64   `json:"sentiment"`
	PublishedAt  time.Time `json:"published_at"`
	CreatedAt    time.Time `json:"created_at"`
}

type Vote struct {
	PlayerID      int64     `json:"player_id"`
	SessionID     string    `json:"session_id"`
	RatingOverall int       `json:"rating_overall"`
	RatingHype    int       `json:"rating_hype"`
	RatingTier    int       `json:"rating_tier"`
	BehaviorScore int       `json:"behavior_score"`
	InternalScore float64   `json:"internal_score"`
	IPHash        string    `json:"ip_hash"`
	CreatedAt     time.Time `json:"created_at"`
}

type VoteInput struct {
	SessionID     string `json:"session_id"`
	RatingOverall int    `json:"rating_overall"`
	RatingHype    int    `json:"rating_hype"`
	RatingTier    int    `json:"rating_tier"`
	BehaviorScore int    `json:"behavior_score"`
}

type LegacyPlayer struct {
	Rank   int
	Emoji  string
	Name   string
	Club   string
	Pos    string
	Age    int
	FRI    float64
	Perf   float64
	Social float64
	Fan    float64
	Media  float64
	Char   float64
	Trend  string
	Dir    string
	BG     string
	Photo  string
	SumEN  string
	SumRU  string
}

type LegacyNews struct {
	Player    string
	Impact    string
	Delta     string
	Time      string
	TitleEN   string
	TitleRU   string
	SummaryEN string
	SummaryRU string
}

type PlayerSyncTarget struct {
	ID       int64
	Name     string
	Club     string
	Position string
	Age      int
	Score    Score
}

type CharacterEvent struct {
	ID          int64     `json:"id"`
	PlayerID    int64     `json:"player_id"`
	PlayerName  string    `json:"player_name,omitempty"`
	NewsItemID  *int64    `json:"news_item_id,omitempty"`
	TriggerWord string    `json:"trigger_word"`
	Delta       float64   `json:"delta"`
	Status      string    `json:"status"`
	DetectedAt  time.Time `json:"detected_at"`
}

// CharacterEventCandidate is what an event scanner emits before the repository
// deduplicates it against the unique index. Despite the name, candidates can
// target any score component — set TargetComponent to "performance" for
// performance-targeting triggers (hat-trick, drought, awards). Empty
// TargetComponent defaults to "character" for backward compatibility.
//
// The natural key depends on the source:
//   - news-derived: (PlayerID, NewsItemID, TriggerWord)
//   - other (fixture/etc.): (PlayerID, TriggerWord, SourceRef)
//
// SourceRef is a free-form fingerprint like "fixture:9482:hat_trick" used to
// keep stats-based detectors idempotent across reruns. For news events leave
// SourceRef empty — the news_item_id already provides idempotency.
//
// AutoApply (Phase 5): when true the event skips fan voting and the proposed
// delta is locked in as the final delta at insert time. Used for definitive
// triggers (doping, racism, official year awards). When false, the event
// enters voting_status='pending_vote' for a 24h window before finalization.
type CharacterEventCandidate struct {
	PlayerID        int64
	NewsItemID      int64 // 0 = no associated news article
	TriggerWord     string
	Delta           float64
	TargetComponent string // "character" (default) or "performance"
	SourceRef       string // optional idempotency key for non-news events
	AutoApply       bool   // skip voting; finalize at insert
}

// PendingEvent is a denormalized view of one character_events row that's
// currently accepting fan votes, plus the running median + total vote count.
// Returned by the /api/events/pending endpoint so the UI can render a slider
// per event without round-tripping to fetch vote details separately.
type PendingEvent struct {
	// Locked marks an event about a player whose leaderboard place is
	// withheld from this caller; the identifying fields are stripped.
	Locked bool `json:"locked,omitempty"`

	ID              int64     `json:"id"`
	PlayerID        int64     `json:"player_id"`
	PlayerName      string    `json:"player_name"`
	TriggerWord     string    `json:"trigger_word"`
	TargetComponent string    `json:"target_component"`
	ProposedDelta   float64   `json:"proposed_delta"`
	NewsItemID      *int64    `json:"news_item_id,omitempty"`
	NewsTitle       string    `json:"news_title,omitempty"`
	VotesCount      int       `json:"votes_count"`
	VotesMedian     *float64  `json:"votes_median,omitempty"` // nil when no votes yet
	DetectedAt      time.Time `json:"detected_at"`
	VotingClosesAt  time.Time `json:"voting_closes_at"`
}

// EventVoteInput is the request body for POST /api/events/{id}/vote.
// SuggestedDelta is clamped to [-5, +5] at the handler before reaching the
// repository, so a griefer can't write extreme values that drag the median.
type EventVoteInput struct {
	SuggestedDelta float64 `json:"suggested_delta"`
}

// PlayerCareerBaseline holds an aggregated snapshot of a player's career
// across the last N seasons. Used as a 40% anchor in the Performance score so
// stars don't crater during an off year. Refreshed monthly — career numbers
// move slowly.
type PlayerCareerBaseline struct {
	PlayerID            int64     `json:"player_id"`
	SeasonsPlayed       int       `json:"seasons_played"`
	SeasonsLookback     int       `json:"seasons_lookback"`
	CareerAppearances   int       `json:"career_appearances"`
	CareerMinutes       int       `json:"career_minutes"`
	CareerGoals         int       `json:"career_goals"`
	CareerAssists       int       `json:"career_assists"`
	CareerAvgRating     float64   `json:"career_avg_rating"`
	CareerTrophiesCount int       `json:"career_trophies_count"`
	BaselineScore       float64   `json:"baseline_score"`
	ComputedAt          time.Time `json:"computed_at"`
}

type ComponentUpdate struct {
	ID          int64      `json:"id"`
	Component   string     `json:"component"`
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	Message     string     `json:"message"`
	RecordsSeen int        `json:"records_seen"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type SocialSnapshot struct {
	ID               int64     `json:"id"`
	PlayerID         int64     `json:"player_id"`
	PlayerName       string    `json:"player_name,omitempty"`
	Provider         string    `json:"provider"`
	Followers        int64     `json:"followers"`
	EngagementRate   float64   `json:"engagement_rate"`
	MentionsGrowth7D float64   `json:"mentions_growth_7d"`
	YouTubeViews7D   int64     `json:"youtube_views_7d"`
	NormalizedScore  float64   `json:"normalized_score"`
	SnapshotAt       time.Time `json:"snapshot_at"`
}

type PlayerExternalIDs struct {
	PlayerID       int64     `json:"player_id"`
	Provider       string    `json:"provider"`
	ExternalID     string    `json:"external_id"`
	ExternalTeamID string    `json:"external_team_id,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type PerformanceSnapshot struct {
	ID                int64     `json:"id"`
	PlayerID          int64     `json:"player_id"`
	PlayerName        string    `json:"player_name,omitempty"`
	Provider          string    `json:"provider"`
	AverageRating     float64   `json:"average_rating"`
	GoalsAssistsPer90 float64   `json:"goals_assists_per90"`
	XGXAPer90         float64   `json:"xg_xa_per90"`
	PositionRankScore float64   `json:"position_rank_score"`
	MinutesShare      float64   `json:"minutes_share"`
	FormScore         float64   `json:"form_score"`
	Last5Goals        int       `json:"last5_goals"`
	Last5Assists      int       `json:"last5_assists"`
	Last5Rating       float64   `json:"last5_rating"`
	NormalizedScore   float64   `json:"normalized_score"`
	SnapshotAt        time.Time `json:"snapshot_at"`

	// Profile fields the provider picked up while fetching stats. They don't
	// belong to the snapshot conceptually, but the API returns them in the
	// same payload, so carrying them here refreshes a player's birth date and
	// portrait without spending a second request per player. Empty when the
	// provider has nothing to offer (the demo provider, or a mapping miss).
	BirthDate *time.Time `json:"birth_date,omitempty"`
	PhotoURL  string     `json:"photo_url,omitempty"`

	// PerformanceEvents are stats-derived rating events the provider chose
	// to emit alongside the snapshot — e.g. "5-match scoring drought" for an
	// attacker. The sync orchestrator forwards them to ApplyCharacterSync
	// (with TargetComponent="performance") so they show up in the same
	// events feed as keyword-detected ones. Empty for providers that don't
	// implement stats-based detection.
	PerformanceEvents []CharacterEventCandidate `json:"-"`
}

type MediaArticleCandidate struct {
	PlayerName  string
	Title       string
	Summary     string
	Source      string
	SourceURL   string
	PublishedAt time.Time
}

type MediaSyncArticle struct {
	PlayerID     int64
	PlayerName   string
	TitleEN      string
	TitleRU      string
	SummaryEN    string
	SummaryRU    string
	Source       string
	SourceURL    string
	SourceTier   float64
	Sentiment    float64
	ImpactType   string
	ImpactDelta  float64
	RelativeTime string
	PublishedAt  time.Time
}

type MediaSyncPlayerResult struct {
	PlayerID      int64
	PlayerName    string
	MediaScore    float64
	Articles      []MediaSyncArticle
	ArticlesCount int
}

type ComponentSyncResult struct {
	Component   string            `json:"component"`
	Provider    string            `json:"provider"`
	Status      string            `json:"status"`
	Message     string            `json:"message"`
	RecordsSeen int               `json:"records_seen"`
	StartedAt   time.Time         `json:"started_at"`
	FinishedAt  time.Time         `json:"finished_at"`
	Players     []PlayerSyncDelta `json:"players,omitempty"`
}

type PlayerSyncDelta struct {
	PlayerID    int64   `json:"player_id"`
	PlayerName  string  `json:"player_name"`
	Component   string  `json:"component"`
	OldValue    float64 `json:"old_value"`
	NewValue    float64 `json:"new_value"`
	OldFRI      float64 `json:"old_fri"`
	NewFRI      float64 `json:"new_fri"`
	ImpactDelta float64 `json:"impact_delta"`
}

// User is a registered account. The password hash never leaves the
// repository layer — it has no JSON tag on purpose, so an accidental
// marshal of this struct can't leak it.
type User struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	IsAdmin      bool       `json:"is_admin"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// Session is one logged-in browser. Token doubles as the cookie value, so it
// is omitted from JSON: the client already holds it in a cookie and echoing
// it into a response body only widens where it can leak (logs, caches).
type Session struct {
	Token     string    `json:"-"`
	UserID    int64     `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Credentials is the request body for register and login.
type Credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AddPlayerInput is the request body for POST /api/players.
//
// Name and club are all an operator should have to know: everything else —
// position, birth date, portrait — is read from the provider, which knows it
// better. Position here is a filter for disambiguation, never stored as given:
// a hand-typed "GK" is precisely what let a defender masquerade as our
// goalkeeper for weeks.
type AddPlayerInput struct {
	Name string `json:"name"`
	Club string `json:"club"`
	// ProviderPlayerID pins the choice when a club fields two players who
	// answer to the same surname. It is the only unambiguous key there is.
	ProviderPlayerID int    `json:"provider_player_id,omitempty"`
	Position         string `json:"position,omitempty"`
}

// PlayerCandidate is one possible match for an add request, returned when the
// name alone doesn't pick out a single player.
type PlayerCandidate struct {
	ProviderPlayerID int    `json:"provider_player_id"`
	Name             string `json:"name"`
	FullName         string `json:"full_name"`
	Position         string `json:"position"`
	Age              int    `json:"age"`
	PhotoURL         string `json:"photo_url"`
}

// ResolvedPlayer is a provider record confirmed to be one specific footballer.
type ResolvedPlayer struct {
	ProviderPlayerID int
	ProviderTeamID   int
	Name             string
	Position         string
	BirthDate        *time.Time
	PhotoURL         string
	Age              int
}

// CharacterBaseline is the neutral starting point for a player's Character
// score before any events have fired. 80, not 50: a footballer with nothing
// recorded against them is a good citizen, not a half-bad one. Migration 010
// recomputes every existing player on this basis, and ApplyCharacterSync
// keeps them there — so anything that creates a player has to start from the
// same number, or the new arrival carries a 30-point penalty for being new.
const CharacterBaseline = 80.0

// ArticleStats is what the Media formula needs from one article; everything
// else about it is presentation. Every news row stores both, so a player's
// Media score can be rebuilt from the database without asking the provider
// again — which is what makes deleting an article reversible.
type ArticleStats struct {
	Sentiment  float64
	SourceTier float64
}

// NewsSuppression records that a moderator removed an article from one
// player's feed. The media sync consults these before it scores, so the
// article neither reappears nor counts.
type NewsSuppression struct {
	PlayerID   int64
	ArticleKey string
}

// NewsArticleKey identifies an article across syncs. Rows are recreated with
// fresh ids twice a day, so the id is useless for this; the URL is stable,
// and the title stands in for the rare row without one.
func NewsArticleKey(sourceURL, title string) string {
	if u := strings.TrimSpace(sourceURL); u != "" {
		return u
	}
	return "title:" + strings.ToLower(strings.TrimSpace(title))
}

// NewsDeletion reports what removing an article did to the player it was
// filed under, so the moderator sees the score move rather than being told
// it did.
type NewsDeletion struct {
	NewsID     int64   `json:"news_id"`
	PlayerID   int64   `json:"player_id"`
	PlayerName string  `json:"player_name"`
	Title      string  `json:"title"`
	OldMedia   float64 `json:"old_media"`
	NewMedia   float64 `json:"new_media"`
	OldFRI     float64 `json:"old_fri"`
	NewFRI     float64 `json:"new_fri"`
	Remaining  int     `json:"remaining_articles"`
}
