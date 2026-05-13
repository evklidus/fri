package config

import (
	"os"
	"time"
)

type Config struct {
	AppPort             string
	DatabaseURL         string
	SourceHTMLPath      string
	WebDir              string
	DBMaxConns          int32
	AutoSyncEnabled     bool
	SyncOnStartup       bool
	MediaSyncInterval   time.Duration
	SocialSyncInterval  time.Duration
	PerformanceInterval    time.Duration
	CharacterInterval      time.Duration
	CareerBaselineInterval time.Duration
	FinalizeEventsInterval time.Duration
	SyncHTTPTimeout     time.Duration
	MediaArticlesPerRun int
	APIFootballKey      string
	APIFootballBaseURL  string
	YouTubeAPIKey       string
	YouTubeBaseURL      string
	MediaStackAPIKey    string
	MediaStackBaseURL   string
}

func MustLoad() Config {
	cfg := Config{
		AppPort:             getEnv("APP_PORT", "8080"),
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://fri:fri@localhost:5432/fri?sslmode=disable"),
		SourceHTMLPath:      getEnv("SOURCE_HTML_PATH", "web/source/fri-index.html"),
		WebDir:              getEnv("WEB_DIR", "web/static"),
		DBMaxConns:          int32(getEnvInt("DB_MAX_CONNS", 10)),
		AutoSyncEnabled:     getEnvBool("AUTO_SYNC_ENABLED", true),
		SyncOnStartup:       getEnvBool("SYNC_ON_STARTUP", false),
		MediaSyncInterval:   time.Duration(getEnvInt("MEDIA_SYNC_INTERVAL_MINUTES", 360)) * time.Minute,
		SocialSyncInterval:  time.Duration(getEnvInt("SOCIAL_SYNC_INTERVAL_MINUTES", 1440)) * time.Minute,
		PerformanceInterval:    time.Duration(getEnvInt("PERFORMANCE_SYNC_INTERVAL_MINUTES", 720)) * time.Minute,
		CharacterInterval:      time.Duration(getEnvInt("CHARACTER_SYNC_INTERVAL_MINUTES", 720)) * time.Minute,
		CareerBaselineInterval: time.Duration(getEnvInt("CAREER_BASELINE_SYNC_INTERVAL_MINUTES", 43200)) * time.Minute,
		FinalizeEventsInterval: time.Duration(getEnvInt("FINALIZE_EVENTS_INTERVAL_MINUTES", 60)) * time.Minute,
		SyncHTTPTimeout:     time.Duration(getEnvInt("SYNC_HTTP_TIMEOUT_SECONDS", 15)) * time.Second,
		MediaArticlesPerRun: getEnvInt("MEDIA_ARTICLES_PER_PLAYER", 3),
		APIFootballKey:      getEnv("API_FOOTBALL_KEY", ""),
		APIFootballBaseURL:  getEnv("API_FOOTBALL_BASE_URL", "https://v3.football.api-sports.io"),
		YouTubeAPIKey:       getEnv("YOUTUBE_API_KEY", ""),
		YouTubeBaseURL:      getEnv("YOUTUBE_BASE_URL", "https://www.googleapis.com/youtube/v3"),
		MediaStackAPIKey:    getEnv("MEDIASTACK_API_KEY", ""),
		MediaStackBaseURL:   getEnv("MEDIASTACK_BASE_URL", "http://api.mediastack.com/v1"),
	}

	return cfg
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	var parsed int
	_, err := fmtSscanf(value, &parsed)
	if err != nil {
		return fallback
	}

	return parsed
}

func getEnvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	switch value {
	case "1", "true", "TRUE", "yes", "YES":
		return true
	case "0", "false", "FALSE", "no", "NO":
		return false
	default:
		return fallback
	}
}

func fmtSscanf(value string, target *int) (int, error) {
	return fmtSscanfImpl(value, target)
}
