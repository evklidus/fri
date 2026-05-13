package service

import (
	"context"
	"log"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

type ScheduleConfig struct {
	MediaInterval          time.Duration
	SocialInterval         time.Duration
	PerformanceInterval    time.Duration
	CharacterInterval      time.Duration
	CareerBaselineInterval time.Duration
	// FinalizeEventsInterval picks up expired pending-vote events and
	// applies their median to the player score. Phase 5 — runs hourly.
	FinalizeEventsInterval time.Duration
	RunOnStartup           bool
}

func (s *Service) StartScheduler(ctx context.Context, cfg ScheduleConfig) {
	if cfg.MediaInterval <= 0 && cfg.SocialInterval <= 0 && cfg.PerformanceInterval <= 0 && cfg.CharacterInterval <= 0 && cfg.CareerBaselineInterval <= 0 && cfg.FinalizeEventsInterval <= 0 {
		return
	}

	// Career baseline ticks first because the initial run populates the
	// baseline table that SyncPerformance reads. The 1s startup delay is
	// well before the 15s performance delay below.
	s.startScheduledJob(ctx, "career-baseline", cfg.CareerBaselineInterval, 1*time.Second, cfg.RunOnStartup, s.SyncCareerBaseline)
	s.startScheduledJob(ctx, "media", cfg.MediaInterval, 5*time.Second, cfg.RunOnStartup, s.SyncMedia)
	s.startScheduledJob(ctx, "social", cfg.SocialInterval, 10*time.Second, cfg.RunOnStartup, s.SyncSocial)
	s.startScheduledJob(ctx, "performance", cfg.PerformanceInterval, 15*time.Second, cfg.RunOnStartup, s.SyncPerformance)
	// Character runs after media on first boot so news_items are populated
	// before the keyword scan kicks in.
	s.startScheduledJob(ctx, "character", cfg.CharacterInterval, 30*time.Second, cfg.RunOnStartup, s.SyncCharacter)
	// Event finalize: pick up expired pending-vote events. Runs after the
	// character tick so events created in this very same boot cycle have a
	// chance to gather votes before being finalized in the same instant.
	s.startScheduledJob(ctx, "event-finalize", cfg.FinalizeEventsInterval, 45*time.Second, cfg.RunOnStartup, s.FinalizePendingEvents)
}

func (s *Service) startScheduledJob(
	ctx context.Context,
	component string,
	interval time.Duration,
	initialDelay time.Duration,
	runOnStartup bool,
	syncFn func(context.Context) (*domain.ComponentSyncResult, error),
) {
	if interval <= 0 {
		return
	}

	go func() {
		if runOnStartup {
			initialTimer := time.NewTimer(initialDelay)
			select {
			case <-ctx.Done():
				initialTimer.Stop()
				return
			case <-initialTimer.C:
				if _, err := syncFn(ctx); err != nil {
					log.Printf("phase2 %s sync failed: %v", component, err)
				}
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := syncFn(ctx); err != nil {
					log.Printf("phase2 %s sync failed: %v", component, err)
				}
			}
		}
	}()
}
