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
	MediaSyncInterval   time.Duration
	SyncHTTPTimeout     time.Duration
	MediaArticlesPerRun int
}

func MustLoad() Config {
	cfg := Config{
		AppPort:             getEnv("APP_PORT", "8080"),
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://fri:fri@localhost:5432/fri?sslmode=disable"),
		SourceHTMLPath:      getEnv("SOURCE_HTML_PATH", "web/source/fri-index.html"),
		WebDir:              getEnv("WEB_DIR", "web/static"),
		DBMaxConns:          int32(getEnvInt("DB_MAX_CONNS", 10)),
		AutoSyncEnabled:     getEnvBool("AUTO_SYNC_ENABLED", true),
		MediaSyncInterval:   time.Duration(getEnvInt("MEDIA_SYNC_INTERVAL_MINUTES", 360)) * time.Minute,
		SyncHTTPTimeout:     time.Duration(getEnvInt("SYNC_HTTP_TIMEOUT_SECONDS", 15)) * time.Second,
		MediaArticlesPerRun: getEnvInt("MEDIA_ARTICLES_PER_PLAYER", 3),
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
