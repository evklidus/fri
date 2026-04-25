package domain

import "time"

type Player struct {
	ID              int64     `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Club            string    `json:"club"`
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
	Score    Score
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
	Provider         string    `json:"provider"`
	Followers        int64     `json:"followers"`
	EngagementRate   float64   `json:"engagement_rate"`
	MentionsGrowth7D float64   `json:"mentions_growth_7d"`
	YouTubeViews7D   int64     `json:"youtube_views_7d"`
	NormalizedScore  float64   `json:"normalized_score"`
	SnapshotAt       time.Time `json:"snapshot_at"`
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
