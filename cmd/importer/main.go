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
	svc := service.New(repo)

	if err := svc.ForceSeed(ctx, sourcePath); err != nil {
		log.Fatalf("import legacy html: %v", err)
	}

	log.Printf("import completed from %s", sourcePath)
}
