package domain

import "time"

type Player struct {
	ID              int64     `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Club            string    `json:"club"`
	League          string    `json:"league"`
	Position        string    `json:"position"`
	Age             int       `json:"age"`
	Emoji           string    `json:"emoji"`
	PhotoData       string    `json:"photo_data"`
	ThemeBackground string    `json:"theme_background"`
	SummaryEN       string    `json:"summary_en"`
	SummaryRU       string    `json:"summary_ru"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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

type PlayerWithScore struct {
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
