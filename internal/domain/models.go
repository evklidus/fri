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
	PlayerID       int64     `json:"player_id"`
	FRI            float64   `json:"fri"`
	Performance    float64   `json:"performance"`
	Social         float64   `json:"social"`
	Fan            float64   `json:"fan"`
	FanBase        float64   `json:"fan_base"`
	Media          float64   `json:"media"`
	Character      float64   `json:"character"`
	TrendValue     float64   `json:"trend_value"`
	TrendDirection string    `json:"trend_direction"`
	CalculatedAt   time.Time `json:"calculated_at"`
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
