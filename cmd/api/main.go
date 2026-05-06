package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"fri.local/football-reputation-index/internal/app/config"
	apphttp "fri.local/football-reputation-index/internal/app/http"
	"fri.local/football-reputation-index/internal/platform/db"
	"fri.local/football-reputation-index/internal/repository/postgres"
	"fri.local/football-reputation-index/internal/service"
)

func main() {
	cfg := config.MustLoad()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

	if err := svc.SeedIfEmpty(ctx, cfg.SourceHTMLPath); err != nil {
		log.Fatalf("seed database: %v", err)
	}

	if cfg.AutoSyncEnabled {
		svc.StartScheduler(ctx, service.ScheduleConfig{
			MediaInterval:       cfg.MediaSyncInterval,
			SocialInterval:      cfg.SocialSyncInterval,
			PerformanceInterval: cfg.PerformanceInterval,
			CharacterInterval:   cfg.CharacterInterval,
			RunOnStartup:        cfg.SyncOnStartup,
		})
	}

	router := apphttp.NewRouter(cfg, svc)

	server := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("FRI API listening on :%s", cfg.AppPort)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
