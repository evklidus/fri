package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	socialProviderName      = "demo-social-provider"
	performanceProviderName = "demo-performance-provider"
)

type socialProvider interface {
	Name() string
	FetchSocialSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.SocialSnapshot, error)
}

type performanceProvider interface {
	Name() string
	FetchPerformanceSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error)
}

type demoSocialProvider struct{}

type demoPerformanceProvider struct{}

func NewSocialProvider(youTubeAPIKey, youTubeBaseURL string, timeout time.Duration) socialProvider {
	demo := demoSocialProvider{}
	if strings.TrimSpace(youTubeAPIKey) != "" {
		return newYouTubeSocialProvider(youTubeAPIKey, youTubeBaseURL, timeout, demo)
	}
	return demo
}

func NewPerformanceProvider(apiFootballKey, apiFootballBaseURL string, store externalIDsStore, timeout time.Duration) performanceProvider {
	if strings.TrimSpace(apiFootballKey) != "" {
		return newAPIFootballPerformanceProvider(apiFootballKey, apiFootballBaseURL, store, timeout, demoPerformanceProvider{})
	}
	return demoPerformanceProvider{}
}

// NewCareerBaselineProvider returns the api-football career-baseline provider
// when a key is available, or nil otherwise (career baseline is optional —
// the rest of the system runs fine without it, just without the long-term
// anchor on Performance).
//
// The returned value type-asserts the api-football performance provider that
// would have been created by NewPerformanceProvider — they share state
// (external-id store, HTTP client, season cache), so we accept that
// provider as input rather than rebuilding from scratch.
func NewCareerBaselineProvider(perf performanceProvider) careerBaselineProvider {
	if p, ok := perf.(*apiFootballPerformanceProvider); ok {
		return p
	}
	return nil
}

func (demoSocialProvider) Name() string {
	return socialProviderName
}

func (demoPerformanceProvider) Name() string {
	return performanceProviderName
}

func (demoSocialProvider) FetchSocialSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.SocialSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.SocialSnapshot{}, err
	}

	popularity := deterministicPercent(player.Name + ":social:popularity")
	engagementSeed := deterministicPercent(player.Name + ":social:engagement")
	mentionsSeed := deterministicPercent(player.Name + ":social:mentions")

	followers := int64(math.Round(math.Pow(10, 4.7+(popularity/100*3.9))))
	engagementRate := round1(1.5 + (engagementSeed / 100 * 5.5))
	mentionsGrowth := round1(mentionsSeed)
	youtubeViews := int64(math.Round(float64(followers)*0.08 + math.Pow(mentionsSeed+10, 3.15)))

	followersNormalized := normalizeLog(float64(followers), 50_000, 500_000_000)
	engagementNormalized := normalizeLinear(engagementRate, 1, 8)
	youtubeNormalized := normalizeLog(float64(youtubeViews), 1_000, 80_000_000)

	normalizedScore := clampScore(
		(followersNormalized * 0.40) +
			(engagementNormalized * 0.30) +
			(mentionsGrowth * 0.20) +
			(youtubeNormalized * 0.10),
	)

	return domain.SocialSnapshot{
		PlayerID:         player.ID,
		PlayerName:       player.Name,
		Provider:         socialProviderName,
		Followers:        followers,
		EngagementRate:   engagementRate,
		MentionsGrowth7D: mentionsGrowth,
		YouTubeViews7D:   youtubeViews,
		NormalizedScore:  normalizedScore,
		SnapshotAt:       time.Now().UTC(),
	}, nil
}

func (demoPerformanceProvider) FetchPerformanceSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.PerformanceSnapshot{}, err
	}

	skill := deterministicPercent(player.Name + ":performance:skill")
	availability := deterministicPercent(player.Name + ":performance:minutes")
	position := strings.ToUpper(strings.TrimSpace(player.Position))

	goalsAssistsMax := map[string]float64{
		"ST": 1.25,
		"LW": 1.05,
		"RW": 1.05,
		"AM": 0.95,
		"CM": 0.70,
		"DM": 0.45,
		"CB": 0.25,
		"LB": 0.35,
		"RB": 0.35,
		"GK": 0.05,
	}
	xgxaMax := map[string]float64{
		"ST": 1.15,
		"LW": 0.95,
		"RW": 0.95,
		"AM": 0.90,
		"CM": 0.65,
		"DM": 0.40,
		"CB": 0.20,
		"LB": 0.35,
		"RB": 0.35,
		"GK": 0.02,
	}

	gaMax := defaultPositionMetric(goalsAssistsMax, position, 0.65)
	xgxaPositionMax := defaultPositionMetric(xgxaMax, position, 0.55)

	averageRating := round1(5.8 + (skill / 100 * 3.5))
	goalsAssistsPer90 := round2((skill / 100) * gaMax)
	xgxaPer90 := round2((skill / 100) * xgxaPositionMax)
	positionRankScore := clampScore(55 + (skill * 0.45))
	minutesShare := round1(55 + (availability / 100 * 43))

	ratingScore := normalizeLinear(averageRating, 5.5, 9.5)
	gaScore := normalizeLinear(goalsAssistsPer90, 0, gaMax)
	xgxaScore := normalizeLinear(xgxaPer90, 0, xgxaPositionMax)

	normalizedScore := clampScore(
		(ratingScore * 0.35) +
			(gaScore * 0.20) +
			(xgxaScore * 0.20) +
			(positionRankScore * 0.15) +
			(minutesShare * 0.10),
	)

	return domain.PerformanceSnapshot{
		PlayerID:          player.ID,
		PlayerName:        player.Name,
		Provider:          performanceProviderName,
		AverageRating:     averageRating,
		GoalsAssistsPer90: goalsAssistsPer90,
		XGXAPer90:         xgxaPer90,
		PositionRankScore: positionRankScore,
		MinutesShare:      minutesShare,
		NormalizedScore:   normalizedScore,
		SnapshotAt:        time.Now().UTC(),
	}, nil
}

func (s *Service) SyncSocial(ctx context.Context) (*domain.ComponentSyncResult, error) {
	if !s.socialSyncMu.TryLock() {
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "social",
			Provider:   s.socialProvider.Name(),
			Status:     "skipped",
			Message:    "social sync already in progress",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}
	defer s.socialSyncMu.Unlock()

	startedAt := time.Now().UTC()
	providerName := s.socialProvider.Name()
	updateID, err := s.repo.StartComponentUpdate(ctx, "social", providerName)
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "social",
		Provider:  providerName,
		Status:    "running",
		StartedAt: startedAt,
	}

	finish := func(status, message string, records int, deltas []domain.PlayerSyncDelta, err error) (*domain.ComponentSyncResult, error) {
		result.Status = status
		result.Message = message
		result.RecordsSeen = records
		result.Players = deltas
		result.FinishedAt = time.Now().UTC()
		if finishErr := s.repo.FinishComponentUpdate(ctx, updateID, status, message, records); finishErr != nil && err == nil {
			err = finishErr
		}
		return result, err
	}

	targets, err := s.repo.ListSyncTargets(ctx)
	if err != nil {
		return finish("failed", err.Error(), 0, nil, err)
	}

	snapshots := make([]domain.SocialSnapshot, 0, len(targets))
	for _, player := range targets {
		snapshot, fetchErr := s.socialProvider.FetchSocialSnapshot(ctx, player)
		if fetchErr != nil {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}

	if len(snapshots) == 0 {
		return finish("completed", "no social snapshots produced", 0, nil, nil)
	}

	deltas, err := s.repo.ApplySocialSync(ctx, snapshots, providerName)
	if err != nil {
		return finish("failed", err.Error(), len(snapshots), nil, err)
	}

	return finish("completed", fmt.Sprintf("social sync completed for %d players", len(snapshots)), len(snapshots), deltas, nil)
}

func (s *Service) SyncPerformance(ctx context.Context) (*domain.ComponentSyncResult, error) {
	if !s.performanceSyncMu.TryLock() {
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "performance",
			Provider:   s.performanceProvider.Name(),
			Status:     "skipped",
			Message:    "performance sync already in progress",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}
	defer s.performanceSyncMu.Unlock()

	startedAt := time.Now().UTC()
	providerName := s.performanceProvider.Name()
	updateID, err := s.repo.StartComponentUpdate(ctx, "performance", providerName)
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "performance",
		Provider:  providerName,
		Status:    "running",
		StartedAt: startedAt,
	}

	finish := func(status, message string, records int, deltas []domain.PlayerSyncDelta, err error) (*domain.ComponentSyncResult, error) {
		result.Status = status
		result.Message = message
		result.RecordsSeen = records
		result.Players = deltas
		result.FinishedAt = time.Now().UTC()
		if finishErr := s.repo.FinishComponentUpdate(ctx, updateID, status, message, records); finishErr != nil && err == nil {
			err = finishErr
		}
		return result, err
	}

	targets, err := s.repo.ListSyncTargets(ctx)
	if err != nil {
		return finish("failed", err.Error(), 0, nil, err)
	}

	snapshots := make([]domain.PerformanceSnapshot, 0, len(targets))
	var statsEvents []domain.CharacterEventCandidate
	for _, player := range targets {
		snapshot, fetchErr := s.performanceProvider.FetchPerformanceSnapshot(ctx, player)
		if fetchErr != nil {
			continue
		}
		// Anchor with career baseline (Phase 4.2). If no baseline exists yet
		// the snapshot is left unchanged — the very first run before the
		// baseline sync has populated the table.
		snapshot = s.blendBaselineIntoPerformance(ctx, snapshot)
		// Phase 4.3: harvest stats-derived events (e.g. goal drought). We
		// store them off to the side and apply them after the main
		// performance snapshot so the drought-driven delta sits on top of
		// the freshly-written current-season score.
		if len(snapshot.PerformanceEvents) > 0 {
			statsEvents = append(statsEvents, snapshot.PerformanceEvents...)
			snapshot.PerformanceEvents = nil // don't persist via PerformanceSnapshot path
		}
		snapshots = append(snapshots, snapshot)
	}

	if len(snapshots) == 0 {
		return finish("completed", "no performance snapshots produced", 0, nil, nil)
	}

	deltas, err := s.repo.ApplyPerformanceSync(ctx, snapshots, providerName)
	if err == nil && len(statsEvents) > 0 {
		// Stats events ride through the same event-routing path as
		// keyword-detected ones — ApplyCharacterSync routes by
		// TargetComponent="performance" to fri_scores.performance. The
		// per-component cap (±15) bounds runaway penalties.
		if _, applyErr := s.repo.ApplyCharacterSync(ctx, statsEvents, characterPerSyncCap); applyErr != nil {
			// Log but don't fail the whole sync — performance scores were
			// already written; missing stats events isn't critical.
			result.Message += fmt.Sprintf(" (stats events failed: %v)", applyErr)
		}
	}
	if err != nil {
		return finish("failed", err.Error(), len(snapshots), nil, err)
	}

	return finish("completed", fmt.Sprintf("performance sync completed for %d players", len(snapshots)), len(snapshots), deltas, nil)
}

func (s *Service) SyncAll(ctx context.Context) ([]domain.ComponentSyncResult, error) {
	var firstErr error
	results := make([]domain.ComponentSyncResult, 0, 5)

	// Order matters:
	//  - SyncCareerBaseline runs FIRST so the table is populated before
	//    SyncPerformance does its blend lookup. On reruns this is a no-op
	//    (idempotent upsert), so no extra cost.
	//  - SyncCharacter runs LAST because it scans news_items written by
	//    SyncMedia.
	for _, syncFn := range []func(context.Context) (*domain.ComponentSyncResult, error){
		s.SyncCareerBaseline,
		s.SyncPerformance,
		s.SyncSocial,
		s.SyncMedia,
		s.SyncCharacter,
	} {
		result, err := syncFn(ctx)
		if result != nil {
			results = append(results, *result)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return results, firstErr
}

func defaultPositionMetric(values map[string]float64, position string, fallback float64) float64 {
	if value, ok := values[position]; ok {
		return value
	}
	return fallback
}

func normalizeLinear(value, minValue, maxValue float64) float64 {
	if maxValue <= minValue {
		return 0
	}
	normalized := ((value - minValue) / (maxValue - minValue)) * 100
	if normalized < 0 {
		return 0
	}
	if normalized > 100 {
		return 100
	}
	return round1(normalized)
}

func normalizeLog(value, minValue, maxValue float64) float64 {
	if value <= 0 || minValue <= 0 || maxValue <= minValue {
		return 0
	}
	return normalizeLinear(math.Log10(value), math.Log10(minValue), math.Log10(maxValue))
}

func deterministicPercent(seed string) float64 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.ToLower(seed)))
	return round1(float64(hash.Sum32()%1000) / 10)
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}
