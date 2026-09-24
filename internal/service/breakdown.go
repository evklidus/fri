package service

import (
	"context"
	"errors"

	"fri.local/football-reputation-index/internal/domain"
)

// ErrBreakdownUnavailable is returned when the store cannot assemble one.
var ErrBreakdownUnavailable = errors.New("breakdown unavailable")

type breakdownStore interface {
	PlayerBreakdown(ctx context.Context, playerID int64) (*domain.PlayerBreakdown, error)
}

// PlayerBreakdown is everything a player's FRI was built from — the raw
// season counts, career record, audience, coverage and events — with the
// weights that combine them, so a signed-in visitor can check the number
// rather than take it on trust.
func (s *Service) PlayerBreakdown(ctx context.Context, playerID int64) (*domain.PlayerBreakdown, error) {
	store, ok := s.repo.(breakdownStore)
	if !ok {
		return nil, ErrBreakdownUnavailable
	}
	out, err := store.PlayerBreakdown(ctx, playerID)
	if err != nil {
		return nil, err
	}
	out.Weights = map[string]float64{"performance": 0.40, "social": 0.25, "media": 0.20, "character": 0.15}
	out.CareerShare = careerBaselineWeight
	return out, nil
}
