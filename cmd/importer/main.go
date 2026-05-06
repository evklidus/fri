package main

import (
	"context"
	"log"
	"os"

	"fri.local/football-reputation-index/internal/app/config"
	"fri.local/football-reputation-index/internal/platform/db"
	"fri.local/football-reputation-index/internal/repository/postgres"
	"fri.local/football-reputation-index/internal/service"
)

func main() {
	cfg := config.MustLoad()
	sourcePath := cfg.SourceHTMLPath
	if len(os.Args) > 1 {
		sourcePath = os.Args[1]
	}

	ctx := context.Background()

	pool, err := db.NewPoolWithRetry(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	if err := db.RunMigrations(ctx, pool); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	repo := postgres.NewRepository(pool)
	mediaProvider := service.NewMediaProvider(cfg.SyncHTTPTimeout, cfg.MediaArticlesPerRun, cfg.MediaStackAPIKey, cfg.MediaStackBaseURL)
	socialProvider := service.NewSocialProvider(cfg.YouTubeAPIKey, cfg.YouTubeBaseURL, cfg.SyncHTTPTimeout)
	performanceProvider := service.NewPerformanceProvider(cfg.APIFootballKey, cfg.APIFootballBaseURL, repo, cfg.SyncHTTPTimeout)
	svc := service.New(repo, mediaProvider, socialProvider, performanceProvider)

	if err := svc.ForceSeed(ctx, sourcePath); err != nil {
		log.Fatalf("import legacy html: %v", err)
	}

	log.Printf("import completed from %s", sourcePath)
}
