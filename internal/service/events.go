package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/repository/postgres"
)

// Phase 5: per-event fan voting service layer.
//
// The repository owns the SQL; this file owns the policy (input validation,
// rate limits, IP hashing, scheduler glue).

const (
	// eventVoteSliderMin / Max bound the suggested_delta a fan can submit.
	// Wide enough for a reasonable opinion (-5 == "this should be a major
	// negative event" / +5 == "actually a big positive"), narrow enough
	// that one griefer can't drag the median into nonsense.
	eventVoteSliderMin = -5.0
	eventVoteSliderMax = 5.0

	// eventVoteCooldown bounds how often the same IP can vote on the same
	// event. We use the same 24h window as the legacy player-poll for
	// consistency.
	eventVoteCooldown = 24 * time.Hour

	// finalizeTickInterval is how often the scheduler runs FinalizePendingEvents.
	// 1h is plenty — voting closes 24h after detection, so an event hangs in
	// 'pending_vote' state at most 1 hour past its real close time.
	finalizeTickInterval = 1 * time.Hour
)

// ListPendingEvents returns the pending-vote queue. When playerID > 0 the
// list is scoped to that player; pass 0 for the site-wide feed.
func (s *Service) ListPendingEvents(ctx context.Context, playerID int64, limit int) ([]domain.PendingEvent, error) {
	if playerID > 0 {
		return s.repo.ListPendingEventsForPlayer(ctx, playerID, limit)
	}
	return s.repo.ListPendingEvents(ctx, limit)
}

// GetPendingEvent fetches one pending event by id. Returns (nil, nil) when
// the event doesn't exist or its voting period has ended.
func (s *Service) GetPendingEvent(ctx context.Context, eventID int64) (*domain.PendingEvent, error) {
	return s.repo.GetPendingEvent(ctx, eventID)
}

// SubmitEventVote validates the suggested delta, clamps it, and forwards to
// the repository. Returns an HTTP-flavored error code via the wrapper types
// so the handler can map cleanly:
//
//   - nil          → 201/200 OK
//   - ErrEventGone → 410 Gone (event finalized or doesn't exist)
//   - other        → 500
func (s *Service) SubmitEventVote(ctx context.Context, eventID int64, suggestedDelta float64, rawIP string) error {
	// Clamp the slider value here so a malicious client can't bypass the UI
	// cap by hand-crafting an HTTP request.
	if suggestedDelta < eventVoteSliderMin {
		suggestedDelta = eventVoteSliderMin
	}
	if suggestedDelta > eventVoteSliderMax {
		suggestedDelta = eventVoteSliderMax
	}

	ipHash := hashIP(rawIP)
	if _, err := s.repo.SubmitEventVote(ctx, eventID, ipHash, suggestedDelta); err != nil {
		if errors.Is(err, postgres.ErrEventNotVotable) {
			return ErrEventGone
		}
		return fmt.Errorf("submit event vote: %w", err)
	}
	return nil
}

// ErrEventGone is returned by SubmitEventVote when the event no longer
// accepts votes (finalized, closed, or unknown id). The HTTP handler maps
// it to 410 Gone.
var ErrEventGone = errors.New("event is no longer accepting votes")

// FinalizePendingEvents runs the median-then-apply cycle for every event
// past its voting_closes_at. Idempotent — safe to invoke at any time.
//
// This is the public surface for the hourly scheduler tick.
func (s *Service) FinalizePendingEvents(ctx context.Context) (*domain.ComponentSyncResult, error) {
	if !s.finalizeEventsMu.TryLock() {
		now := time.Now().UTC()
		return &domain.ComponentSyncResult{
			Component:  "event-finalize",
			Provider:   "event-voting",
			Status:     "skipped",
			Message:    "finalize already in progress",
			StartedAt:  now,
			FinishedAt: now,
		}, nil
	}
	defer s.finalizeEventsMu.Unlock()

	startedAt := time.Now().UTC()
	updateID, err := s.repo.StartComponentUpdate(ctx, "event-finalize", "event-voting")
	if err != nil {
		return nil, err
	}

	result := &domain.ComponentSyncResult{
		Component: "event-finalize",
		Provider:  "event-voting",
		Status:    "running",
		StartedAt: startedAt,
	}

	count, finalizeErr := s.repo.FinalizePendingEvents(ctx)
	result.FinishedAt = time.Now().UTC()
	result.RecordsSeen = count

	if finalizeErr != nil {
		result.Status = "failed"
		result.Message = finalizeErr.Error()
		_ = s.repo.FinishComponentUpdate(ctx, updateID, result.Status, result.Message, count)
		return result, finalizeErr
	}

	result.Status = "completed"
	result.Message = fmt.Sprintf("finalized %d expired events", count)
	if err := s.repo.FinishComponentUpdate(ctx, updateID, result.Status, result.Message, count); err != nil {
		log.Printf("event-finalize: FinishComponentUpdate failed: %v", err)
	}
	return result, nil
}
