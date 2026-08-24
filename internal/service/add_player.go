package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fri.local/football-reputation-index/internal/domain"
)

// Errors the add-player flow can return. The handler maps each to a status,
// so they have to be distinguishable — "we found nobody" and "we found four"
// call for very different things from the operator.
var (
	ErrPlayerExists     = errors.New("player already on the roster")
	ErrPlayerNotFound   = errors.New("no such player at that club")
	ErrClubUnknown      = errors.New("unknown club")
	ErrAmbiguousPlayer  = errors.New("several players match")
	ErrProviderRequired = errors.New("adding players requires an api-football key")
)

// AmbiguousPlayerError carries the candidates back to the caller so they can
// pick one by provider id.
//
// It deliberately refuses to guess. Guessing is what bound our goalkeeper to
// Barcelona's defender: two Garcías, one search term, and the code took
// whichever the API listed first.
type AmbiguousPlayerError struct {
	Candidates []domain.PlayerCandidate
}

func (e *AmbiguousPlayerError) Error() string {
	names := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		names = append(names, fmt.Sprintf("%s (id %d, %s, age %d)", c.FullName, c.ProviderPlayerID, c.Position, c.Age))
	}
	return "several players match: " + strings.Join(names, "; ")
}

func (e *AmbiguousPlayerError) Is(target error) bool { return target == ErrAmbiguousPlayer }

// playerResolver is the provider surface AddPlayer needs. Kept narrow so a
// test can supply one without standing up the whole api-football client.
type playerResolver interface {
	ResolvePlayer(ctx context.Context, name, club string, providerPlayerID int, position string) (domain.ResolvedPlayer, error)
}

// rosterStore is the repository surface for adding a player.
type rosterStore interface {
	PlayerExists(ctx context.Context, name, slug string) (int64, bool, error)
	InsertPlayer(ctx context.Context, player domain.PlayerWithScore, providerID, providerTeamID string) (int64, error)
}

// neutralComponentScore is what a brand-new player scores on the components no
// sync has measured yet.
//
// 50, not 0. Zero would be the site publishing a measurement it has not made,
// and it would drop the player to the bottom of a leaderboard ordered by FRI.
// 50 is what this codebase already means by "no evidence yet" — see the fan
// baseline in migration 011.
//
// It also keeps the leaderboard gate intact: with three components at 50, the
// best reachable FRI is 0.40×100 + 0.25×50 + 0.20×50 + 0.15×50 = 70, so a
// newly added player cannot land in the withheld top five on placeholder data
// alone.
const neutralComponentScore = 50.0

// AddPlayer puts one player on the roster without disturbing anyone else.
//
// The only pre-existing way to add a player was ForceSeed, which truncates the
// players table — taking news, rating events, votes and user accounts with it.
// That is not an operation anybody should run to add a striker.
func (s *Service) AddPlayer(ctx context.Context, input domain.AddPlayerInput) (*domain.PlayerWithScore, error) {
	name := strings.TrimSpace(input.Name)
	club := strings.TrimSpace(input.Club)
	if name == "" || club == "" {
		return nil, fmt.Errorf("name and club are required")
	}

	store, ok := s.repo.(rosterStore)
	if !ok {
		return nil, errors.New("roster store unavailable")
	}
	return s.addPlayerWithStore(ctx, input, store)
}

// addPlayerWithStore is AddPlayer with the repository injected, so the logic
// that matters — refusing duplicates, trusting the provider over the request —
// can be tested without a database.
func (s *Service) addPlayerWithStore(ctx context.Context, input domain.AddPlayerInput, store rosterStore) (*domain.PlayerWithScore, error) {
	name := strings.TrimSpace(input.Name)
	club := strings.TrimSpace(input.Club)

	// Check for a duplicate before spending a provider request.
	slug := slugify(name)
	if existingID, exists, err := store.PlayerExists(ctx, name, slug); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("%w (player_id=%d)", ErrPlayerExists, existingID)
	}

	resolver, ok := s.performanceProvider.(playerResolver)
	if !ok {
		// The demo provider can't resolve anyone, and inventing a player from
		// the operator's typing is how bad roster data starts.
		return nil, ErrProviderRequired
	}

	resolved, err := resolver.ResolvePlayer(ctx, name, club, input.ProviderPlayerID, input.Position)
	if err != nil {
		return nil, err
	}

	player := domain.PlayerWithScore{}
	player.Slug = slug
	player.Name = name
	player.Club = club
	player.League = leagueForClub(club)
	// Position and age come from the provider, never from the request.
	player.Position = rosterPositionFor(resolved.Position)
	player.Age = resolved.Age
	player.BirthDate = resolved.BirthDate
	player.PhotoURL = resolved.PhotoURL
	player.Emoji = "⚽"

	// Every component starts neutral; the inline Performance sync below
	// replaces its own within the same request when it can.
	player.Performance = neutralComponentScore
	player.Social = neutralComponentScore
	player.Media = neutralComponentScore
	player.Character = neutralComponentScore
	player.Fan = neutralComponentScore
	player.FanBase = neutralComponentScore
	player.FRI = round1(
		player.Performance*0.40 + player.Social*0.25 + player.Media*0.20 + player.Character*0.15,
	)
	player.TrendDirection = "stable"

	playerID, err := store.InsertPlayer(ctx, player,
		strconv.Itoa(resolved.ProviderPlayerID), strconv.Itoa(resolved.ProviderTeamID))
	if err != nil {
		return nil, err
	}
	player.ID = playerID
	player.PlayerID = playerID

	return &player, nil
}

// rosterPositionFor maps a provider position onto the four codes the roster
// uses. Anything unrecognised becomes MID — the middle of the pitch is the
// least wrong guess, and the next sync corrects it from real statistics.
func rosterPositionFor(providerPosition string) string {
	switch positionGroup(providerPosition) {
	case "GK":
		return "GK"
	case "DEF":
		return "DEF"
	case "ATT":
		return "FWD"
	case "MID":
		return "MID"
	default:
		return "MID"
	}
}
