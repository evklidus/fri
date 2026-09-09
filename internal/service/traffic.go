package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sync"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// trafficFlushInterval is how long counters sit in memory before being
// written. Half a minute keeps the write rate flat no matter how busy the
// site gets — a thousand requests become one small transaction — and the
// dashboard is a daily instrument, so half a minute of lag is invisible.
const trafficFlushInterval = 30 * time.Second

// trafficStatsWindow is how many days the dashboard shows.
const trafficStatsWindow = 14

// trafficStore is the persistence the collector needs. Declared here so the
// service keeps working — minus the dashboard — against a repository that
// does not implement it, which is what the router's fakes do.
type trafficStore interface {
	FlushTraffic(ctx context.Context, views map[domain.EntryPoint]int64, visitors []string, day time.Time) error
	TrafficDays(ctx context.Context, days int) ([]domain.TrafficDay, error)
	EntryPoints(ctx context.Context, days int) ([]domain.EntryPoint, error)
	SignupDays(ctx context.Context, days int) ([]domain.SignupDay, error)
	UserTotals(ctx context.Context) (domain.UserTotals, error)
	ContentTotals(ctx context.Context) (domain.ContentTotals, error)
}

// trafficBuffer accumulates counts between flushes. Requests only ever
// touch memory under a mutex, so recording a view cannot slow a response
// down or fail it — a counter is not worth an error page.
type trafficBuffer struct {
	mu       sync.Mutex
	day      time.Time
	views    map[domain.EntryPoint]int64
	visitors map[string]struct{}
}

// RecordView notes one served request. section is the part of the site it
// belongs to; visitorHash identifies the caller for the day and may be
// empty, in which case only the view is counted.
//
// The buffer is keyed by day, and a request that arrives after midnight
// flushes yesterday before starting today — otherwise a quiet night would
// file the previous day's last few requests under the new date.
func (s *Service) RecordView(section, visitorHash string) {
	if section == "" {
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)

	s.traffic.mu.Lock()
	if !s.traffic.day.Equal(today) {
		stale, staleDay := s.traffic.drainLocked()
		s.traffic.day = today
		s.traffic.mu.Unlock()
		s.writeTraffic(context.Background(), stale, staleDay)
		s.traffic.mu.Lock()
	}
	if s.traffic.views == nil {
		s.traffic.views = make(map[domain.EntryPoint]int64)
		s.traffic.visitors = make(map[string]struct{})
	}
	s.traffic.views[domain.EntryPoint{Section: section}]++
	if visitorHash != "" {
		s.traffic.visitors[visitorHash] = struct{}{}
	}
	s.traffic.mu.Unlock()
}

// drainedTraffic is one flush's worth of counters.
type drainedTraffic struct {
	views    map[domain.EntryPoint]int64
	visitors []string
}

func (b *trafficBuffer) drainLocked() (drainedTraffic, time.Time) {
	out := drainedTraffic{views: b.views}
	for hash := range b.visitors {
		out.visitors = append(out.visitors, hash)
	}
	b.views, b.visitors = nil, nil
	return out, b.day
}

func (s *Service) writeTraffic(ctx context.Context, batch drainedTraffic, day time.Time) {
	if len(batch.views) == 0 && len(batch.visitors) == 0 {
		return
	}
	store, ok := s.repo.(trafficStore)
	if !ok {
		return
	}
	if err := store.FlushTraffic(ctx, batch.views, batch.visitors, day); err != nil {
		// Counters are not worth retrying: the next flush carries on, and a
		// lost half-minute of a visitor count is not worth the complexity of
		// a queue. Logged so a persistent failure is visible.
		log.Printf("traffic: flush failed (%d sections, %d visitors): %v", len(batch.views), len(batch.visitors), err)
	}
}

// StartTrafficCollector flushes buffered counters on a timer until ctx is
// cancelled, then writes whatever is left. Without the final flush every
// restart would drop up to half a minute of traffic, and deploys are the
// times we most want the numbers to stay straight.
func (s *Service) StartTrafficCollector(ctx context.Context) {
	if _, ok := s.repo.(trafficStore); !ok {
		log.Print("traffic: repository does not record traffic — the admin dashboard will be empty")
		return
	}
	go func() {
		ticker := time.NewTicker(trafficFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.flushTraffic(ctx)
			case <-ctx.Done():
				// A fresh context: the cancelled one cannot run a query.
				flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				s.flushTraffic(flushCtx)
				cancel()
				return
			}
		}
	}()
}

func (s *Service) flushTraffic(ctx context.Context) {
	s.traffic.mu.Lock()
	batch, day := s.traffic.drainLocked()
	s.traffic.mu.Unlock()
	s.writeTraffic(ctx, batch, day)
}

// TrafficVisitorHash identifies a visitor for one day without keeping their
// address. The day is mixed into the digest, so the same person hashes
// differently tomorrow: enough to count today's uniques, not enough to
// follow anyone across days. Raw addresses are never stored, the rule votes
// already follow.
//
// Honest about what this is not: SHA-256 over an address space this small is
// brute-forceable by anyone holding the database, so this is data hygiene
// rather than anonymity. It buys the property that matters here — nobody can
// build a history of one person out of these rows.
func TrafficVisitorHash(rawIP string, day time.Time) string {
	if rawIP == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawIP + "|" + day.UTC().Format("2006-01-02")))
	return hex.EncodeToString(sum[:16])
}

// TrafficStats assembles the admin dashboard. Anything the buffer is still
// holding is written first, so an admin who reloads right after a visit sees
// that visit instead of wondering whether the counter works.
func (s *Service) TrafficStats(ctx context.Context) (*domain.TrafficStats, error) {
	store, ok := s.repo.(trafficStore)
	if !ok {
		return nil, ErrTrafficUnavailable
	}
	s.flushTraffic(ctx)

	users, err := store.UserTotals(ctx)
	if err != nil {
		return nil, err
	}
	content, err := store.ContentTotals(ctx)
	if err != nil {
		return nil, err
	}
	days, err := store.TrafficDays(ctx, trafficStatsWindow)
	if err != nil {
		return nil, err
	}
	entries, err := store.EntryPoints(ctx, trafficStatsWindow)
	if err != nil {
		return nil, err
	}
	signups, err := store.SignupDays(ctx, trafficStatsWindow)
	if err != nil {
		return nil, err
	}
	// Sync health belongs on the same screen: "no traffic" and "the data
	// stopped updating" look identical from the outside otherwise.
	syncs, err := s.repo.ListComponentUpdates(ctx, 12)
	if err != nil {
		return nil, err
	}

	return &domain.TrafficStats{
		Users:       users,
		Content:     content,
		Days:        days,
		EntryPoints: entries,
		Signups:     signups,
		Syncs:       syncs,
		WindowDays:  trafficStatsWindow,
		GeneratedAt: time.Now().UTC(),
	}, nil
}
