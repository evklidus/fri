package config

import "os"

type Config struct {
	AppPort        string
	DatabaseURL    string
	SourceHTMLPath string
	WebDir         string
	DBMaxConns     int32
}

func MustLoad() Config {
	cfg := Config{
		AppPort:        getEnv("APP_PORT", "8080"),
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://fri:fri@localhost:5432/fri?sslmode=disable"),
		SourceHTMLPath: getEnv("SOURCE_HTML_PATH", "web/source/fri-index.html"),
		WebDir:         getEnv("WEB_DIR", "web/static"),
		DBMaxConns:     int32(getEnvInt("DB_MAX_CONNS", 10)),
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

func fmtSscanf(value string, target *int) (int, error) {
	return fmtSscanfImpl(value, target)
}
