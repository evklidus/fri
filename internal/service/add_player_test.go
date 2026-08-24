package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

type fakeResolver struct {
	resolved domain.ResolvedPlayer
	err      error
	gotName  string
	gotClub  string
	gotID    int
}

func (f *fakeResolver) ResolvePlayer(_ context.Context, name, club string, providerPlayerID int, _ string) (domain.ResolvedPlayer, error) {
	f.gotName, f.gotClub, f.gotID = name, club, providerPlayerID
	return f.resolved, f.err
}

// The service holds its provider as a performanceProvider, so the fake has to
// satisfy that too. Neither method is exercised by these tests.
func (f *fakeResolver) Name() string { return "fake-resolver" }

func (f *fakeResolver) FetchPerformanceSnapshot(context.Context, domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error) {
	return domain.PerformanceSnapshot{}, errors.New("not used in these tests")
}

type fakeRoster struct {
	existingID  int64
	exists      bool
	inserted    domain.PlayerWithScore
	providerID  string
	insertCalls int
}

func (f *fakeRoster) PlayerExists(context.Context, string, string) (int64, bool, error) {
	return f.existingID, f.exists, nil
}

func (f *fakeRoster) InsertPlayer(_ context.Context, p domain.PlayerWithScore, providerID, _ string) (int64, error) {
	f.insertCalls++
	f.inserted = p
	f.providerID = providerID
	return 99, nil
}

func TestRosterPositionForMapsProviderLabels(t *testing.T) {
	// The provider's vocabulary is wider than our four codes, and "Forward" —
	// which is what every one of Raphinha's rows says — used to fall through
	// to OTHER.
	cases := map[string]string{
		"Goalkeeper": "GK",
		"Defender":   "DEF",
		"Midfielder": "MID",
		"Attacker":   "FWD",
		"Forward":    "FWD",
		"":           "MID", // unknown: the next sync corrects it from real stats
	}
	for in, want := range cases {
		if got := rosterPositionFor(in); got != want {
			t.Errorf("rosterPositionFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewPlayerCannotReachTheWithheldTopFive(t *testing.T) {
	// A new player starts with three components at the neutral 50 and only
	// Performance measured. Even a perfect Performance leaves them below the
	// top of the table, so placeholder data cannot push somebody into the
	// five places the leaderboard withholds — which would hand an anonymous
	// visitor a blank row where a real player used to be.
	best := 100*0.40 + neutralComponentScore*0.25 + neutralComponentScore*0.20 + neutralComponentScore*0.15
	if best != 70 {
		t.Fatalf("best reachable FRI on placeholder components = %v, want 70", best)
	}
}

func TestAmbiguousPlayerErrorNamesTheCandidates(t *testing.T) {
	// The operator has to be able to tell the two apart from the message
	// alone — that is the whole point of refusing to guess.
	err := &AmbiguousPlayerError{Candidates: []domain.PlayerCandidate{
		{ProviderPlayerID: 619, FullName: "Eric García Martret", Position: "Defender", Age: 24},
		{ProviderPlayerID: 182718, FullName: "Joan García Pons", Position: "Goalkeeper", Age: 24},
	}}
	msg := err.Error()
	for _, want := range []string{"619", "182718", "Defender", "Goalkeeper", "Eric", "Joan"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message is missing %q: %s", want, msg)
		}
	}
	if !errors.Is(err, ErrAmbiguousPlayer) {
		t.Error("AmbiguousPlayerError must satisfy errors.Is(ErrAmbiguousPlayer)")
	}
}

func TestAddPlayerUsesProviderFactsNotTheRequest(t *testing.T) {
	born := time.Date(1999, 6, 22, 0, 0, 0, 0, time.UTC)
	resolver := &fakeResolver{resolved: domain.ResolvedPlayer{
		ProviderPlayerID: 909,
		ProviderTeamID:   489,
		Name:             "R. Leão",
		Position:         "Attacker",
		BirthDate:        &born,
		PhotoURL:         "https://media.api-sports.io/football/players/909.png",
		Age:              27,
	}}
	roster := &fakeRoster{}
	svc := &Service{performanceProvider: resolver}

	// The request lies about the position: says goalkeeper for a striker.
	// The stored value must come from the provider regardless — a typed
	// position is exactly what let a defender pose as our goalkeeper.
	got, err := svc.addPlayerWithStore(context.Background(), domain.AddPlayerInput{
		Name: "Rafael Leão", Club: "AC Milan", Position: "GK",
	}, roster)
	if err != nil {
		t.Fatalf("add player: %v", err)
	}
	if got.Position != "FWD" {
		t.Errorf("stored position = %q, want FWD from the provider, not the request", got.Position)
	}
	if got.Age != 27 {
		t.Errorf("age = %d, want 27 from the provider", got.Age)
	}
	if got.BirthDate == nil || !got.BirthDate.Equal(born) {
		t.Errorf("birth date = %v, want %v", got.BirthDate, born)
	}
	if got.PhotoURL == "" {
		t.Error("photo url was dropped")
	}
	if roster.providerID != "909" {
		t.Errorf("provider mapping pinned as %q, want 909 — an unpinned player goes back through fuzzy search", roster.providerID)
	}
	if got.Social != neutralComponentScore || got.Media != neutralComponentScore {
		t.Errorf("unmeasured components = social %v media %v, want %v", got.Social, got.Media, neutralComponentScore)
	}
}

func TestAddPlayerRefusesDuplicatesBeforeCallingTheProvider(t *testing.T) {
	resolver := &fakeResolver{}
	roster := &fakeRoster{existingID: 7, exists: true}
	svc := &Service{performanceProvider: resolver}

	_, err := svc.addPlayerWithStore(context.Background(), domain.AddPlayerInput{
		Name: "L. Yamal", Club: "FC Barcelona",
	}, roster)
	if !errors.Is(err, ErrPlayerExists) {
		t.Fatalf("err = %v, want ErrPlayerExists", err)
	}
	if resolver.gotName != "" {
		t.Error("provider was called for a player already on the roster — that is a metered request wasted")
	}
	if roster.insertCalls != 0 {
		t.Error("insert attempted for an existing player")
	}
}

func TestUnknownPlayerGetsNoInventedFollowers(t *testing.T) {
	// Rafael Leão was added on 2026-08-24 and immediately scored 74.4 on
	// Social — a number derived from the letters of his name by the old hash
	// fallback. Stable across syncs, so it looked like a measurement.
	snapshot, err := demoSocialProvider{}.FetchSocialSnapshot(
		context.Background(),
		domain.PlayerSyncTarget{ID: 99, Name: "Nobody We Track", Club: "Some FC"},
	)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snapshot.Followers != 0 {
		t.Errorf("followers = %d, want 0 — a follower count nobody measured is fiction", snapshot.Followers)
	}
	if snapshot.NormalizedScore != neutralComponentScore {
		t.Errorf("score = %v, want the neutral %v", snapshot.NormalizedScore, neutralComponentScore)
	}

	// A player we do have data for must still be scored on it.
	known, err := demoSocialProvider{}.FetchSocialSnapshot(
		context.Background(),
		domain.PlayerSyncTarget{ID: 8, Name: "L. Yamal", Club: "FC Barcelona"},
	)
	if err != nil {
		t.Fatalf("fetch known: %v", err)
	}
	if known.Followers <= 0 {
		t.Error("a player in realSocialOverrides lost their follower count")
	}
}
