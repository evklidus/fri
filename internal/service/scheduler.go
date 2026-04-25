package service

import (
	"context"
	"log"
	"time"
)

func (s *Service) StartScheduler(ctx context.Context, mediaInterval time.Duration) {
	if mediaInterval <= 0 {
		return
	}

	go func() {
		// Give the app a small delay so startup remains responsive.
		initialTimer := time.NewTimer(5 * time.Second)
		defer initialTimer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-initialTimer.C:
			if _, err := s.SyncMedia(ctx); err != nil {
				log.Printf("phase2 media sync failed: %v", err)
			}
		}

		ticker := time.NewTicker(mediaInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.SyncMedia(ctx); err != nil {
					log.Printf("phase2 media sync failed: %v", err)
				}
			}
		}
	}()
}
