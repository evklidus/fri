package service

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"fri.local/football-reputation-index/internal/domain"
)

func compStat(leagueID int, leagueName string, teamID, minutes, apps int, rating string, goals, assists, keyPasses, shotsOn int) apiFootballStatistic {
	return apiFootballStatistic{
		Team:   apiFootballTeamRef{ID: teamID, Name: "Club"},
		League: apiFootballLeagueRef{ID: leagueID, Name: leagueName, Type: "League"},
		Games:  apiFootballGames{Appearances: apps, Minutes: minutes, Rating: rating},
		Goals:  apiFootballGoals{Total: goals, Assists: assists},
		Passes: apiFootballPasses{Key: keyPasses},
		Shots:  apiFootballShots{On: shotsOn},
	}
}

func TestPoolingIsMinutesWeightedAcrossCompetitions(t *testing.T) {
	// Álvarez 2025/26: La Liga 29 apps, 1902′, 6.97, 8+4; Champions League
	// 15 apps, 1240′, 7.55, 10+4. Only the first row used to count.
	laLiga := compStat(140, "La Liga", 530, 1902, 29, "6.97", 8, 4, 40, 50)
	ucl := compStat(2, "Champions League", 530, 1240, 15, "7.55", 10, 4, 30, 40)

	pooled := poolClubStatistics([]apiFootballStatistic{laLiga, ucl})

	if pooled.RawMinutes != 3142 || pooled.RawAppearances != 44 {
		t.Errorf("availability = %d′ / %d apps, want 3142 / 44", pooled.RawMinutes, pooled.RawAppearances)
	}
	// Effective minutes: 1902×1.00 and 1240×1.10 = 1364.
	wantRating := (6.97*1902 + 7.55*1364) / (1902 + 1364)
	if math.Abs(pooled.Rating-wantRating) > 0.001 {
		t.Errorf("rating = %.3f, want %.3f (minutes-weighted, UCL ×1.10)", pooled.Rating, wantRating)
	}
	// A weighted mean of per-competition RATES, not weighted counts over
	// weighted minutes: the coefficient changes how much the UCL counts,
	// never how good the output inside it looks.
	gaLiga, gaUCL := 12.0/1902*90, 14.0/1240*90
	wantGA := (gaLiga*1902 + gaUCL*1364) / (1902 + 1364)
	if math.Abs(pooled.GoalsAssistsPer90-wantGA) > 0.001 {
		t.Errorf("G+A/90 = %.3f, want %.3f", pooled.GoalsAssistsPer90, wantGA)
	}
	if pooled.GoalsAssistsPer90 <= gaLiga {
		t.Error("ten Champions League goals moved nothing")
	}
}

func TestSmallContinentalSampleDoesNotLiftAnyone(t *testing.T) {
	// 200 continental minutes at 8.5 against 2000 league minutes at 6.8:
	// the rating must stay exactly 6.8, while the minutes still count as
	// playing time.
	league := compStat(39, "Premier League", 42, 2000, 23, "6.8", 4, 2, 20, 15)
	cameo := compStat(2, "Champions League", 42, 200, 4, "8.5", 3, 1, 5, 6)

	pooled := poolClubStatistics([]apiFootballStatistic{league, cameo})
	if pooled.Rating != 6.8 {
		t.Errorf("rating = %.3f, want 6.8 — a 200-minute sample lifted the season", pooled.Rating)
	}
	if pooled.RawMinutes != 2200 {
		t.Errorf("availability = %d′, want 2200 — small samples still count as playing time", pooled.RawMinutes)
	}
	if pooled.GoalsAssistsPer90 != 6.0/2000*90 {
		t.Errorf("G+A/90 = %.3f, want the league rate alone", pooled.GoalsAssistsPer90)
	}
}

func TestPlayerWithOneCompetitionIsUnchanged(t *testing.T) {
	// Half of the big-five clubs are out of Europe in a given season. Their
	// players must come out of pooling with exactly the numbers the single
	// row carried.
	only := compStat(135, "Serie A", 489, 1859, 29, "6.91", 9, 3, 33, 41)
	pooled := poolClubStatistics([]apiFootballStatistic{only})
	single := poolFromAnchor(only)
	if pooled.Rating != single.Rating || pooled.GoalsAssistsPer90 != single.GoalsAssistsPer90 ||
		pooled.KeyPassesPer90 != single.KeyPassesPer90 || pooled.ShotsOnPer90 != single.ShotsOnPer90 ||
		pooled.RawMinutes != single.RawMinutes || pooled.RawAppearances != single.RawAppearances {
		t.Errorf("one competition should pool to itself:\n pooled %+v\n single %+v", pooled, single)
	}
}

func TestOnlyCountedCompetitionsCount(t *testing.T) {
	league := compStat(78, "Bundesliga", 157, 2500, 30, "7.9", 20, 15, 60, 70)
	worldCup := compStat(1, "World Cup", 2, 630, 7, "8.2", 5, 2, 12, 15)
	worldCup.Team.National = true
	superCup := compStat(529, "DFL-Supercup", 157, 90, 1, "9.5", 2, 0, 3, 4)
	rows := []apiFootballStatistic{
		league, worldCup, superCup,
		compStat(10, "Friendlies", 2, 270, 3, "9.0", 3, 1, 6, 7),          // not competitive
		compStat(667, "Friendlies Clubs", 157, 270, 3, "9.0", 3, 1, 6, 7), // not competitive
		compStat(9999, "Audi Cup", 157, 180, 2, "9.9", 3, 0, 2, 2),        // unknown: fail closed
	}
	national := compStat(850, "UEFA U21 Championship - Qualification", 3, 450, 5, "8.8", 4, 1, 8, 9)
	national.Team.National = true
	rows = append(rows, national) // youth level: not listed, not counted

	pooled := poolClubStatistics(rows)
	counted := map[int]bool{}
	for _, c := range pooled.Competitions {
		counted[c.LeagueID] = true
	}
	for _, want := range []int{78, 1, 529} {
		if !counted[want] {
			t.Errorf("competition %d should count", want)
		}
	}
	for _, reject := range []int{10, 667, 9999, 850} {
		if counted[reject] {
			t.Errorf("competition %d must not count", reject)
		}
	}
	// A World Cup is a national-team row and it counts like any other.
	if pooled.Rating <= 7.9 {
		t.Errorf("rating = %.3f — the World Cup run moved nothing", pooled.Rating)
	}
	// Ninety minutes of super cup are playing time but have no say in the
	// rating: the ramp, not an exclusion list, is what keeps it quiet.
	if pooled.RawMinutes != 2500+630+90 {
		t.Errorf("availability = %d′, want %d", pooled.RawMinutes, 2500+630+90)
	}
	wantRating := (7.9*2500 + 8.2*630) / (2500 + 630) // super cup k=0
	if math.Abs(pooled.Rating-wantRating) > 0.001 {
		t.Errorf("rating = %.3f, want %.3f (super cup silent)", pooled.Rating, wantRating)
	}
}

func TestCredibilityRamp(t *testing.T) {
	cases := map[int]float64{0: 0, 269: 0, 270: 0, 450: 0.5, 630: 1, 1240: 1}
	for minutes, want := range cases {
		if got := credibility(minutes); math.Abs(got-want) > 1e-9 {
			t.Errorf("credibility(%d) = %v, want %v", minutes, got, want)
		}
	}
}

func TestAvailabilityIsJudgedAgainstFixturesActuallyPlayed(t *testing.T) {
	// Pooled into a fixed 3,420-minute season, 3142′ across two competitions
	// reads 92% — rewarded for playing more competitions. Against the 38 + 12
	// fixtures the club actually played it reads 3142/4500 = 69.8%.
	store := newFakeStore()
	store.seed(domain.PlayerExternalIDs{PlayerID: 24, Provider: apiFootballProviderName, ExternalID: "6009", ExternalTeamID: "530"})

	handler := newRecordingHandler(t)
	handler.on("/leagues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"errors": []any{}, "response": []any{
			map[string]any{
				"league":  map[string]any{"id": 140, "name": "La Liga", "type": "League"},
				"seasons": []any{map[string]any{"year": 2025, "current": true}},
			},
		}})
	})
	handler.on("/fixtures", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("last") != "" { // form lookup
			writeJSON(w, map[string]any{"errors": []any{}, "response": []any{}})
			return
		}
		n := map[string]int{"140": 38, "2": 12}[r.URL.Query().Get("league")]
		items := make([]any, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, map[string]any{"fixture": map[string]any{"id": 1000 + i}})
		}
		writeJSON(w, map[string]any{"errors": []any{}, "response": items})
	})
	handler.on("/players", func(w http.ResponseWriter, _ *http.Request) {
		row := func(league int, name string, minutes, apps int, rating string, goals, assists int) map[string]any {
			return map[string]any{
				"team":   map[string]any{"id": 530, "name": "Atletico Madrid", "national": false},
				"league": map[string]any{"id": league, "name": name, "type": "Cup"},
				"games":  map[string]any{"appearences": apps, "minutes": minutes, "rating": rating, "position": "Attacker"},
				"goals":  map[string]any{"total": goals, "assists": assists},
				"passes": map[string]any{"key": 30}, "shots": map[string]any{"on": 40},
			}
		}
		writeJSON(w, map[string]any{"errors": []any{}, "response": []any{
			map[string]any{
				"player": map[string]any{"id": 6009, "name": "J. Álvarez", "lastname": "Álvarez", "age": 26, "position": "Attacker"},
				"statistics": []any{
					row(140, "La Liga", 1902, 29, "6.97", 8, 4),
					row(2, "UEFA Champions League", 1240, 15, "7.55", 10, 4),
				},
			},
		}})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	provider := newTestProvider(server, store)
	snapshot, err := provider.FetchPerformanceSnapshot(context.Background(),
		domain.PlayerSyncTarget{ID: 24, Name: "J. Álvarez", Club: "Atletico Madrid", Position: "FWD", Age: 26})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snapshot.Provider != apiFootballProviderName {
		t.Fatalf("provider = %q, want the real one", snapshot.Provider)
	}
	if snapshot.MinutesShare != 69.8 {
		t.Errorf("minutes share = %v, want 69.8 (3142 of 50 fixtures × 90)", snapshot.MinutesShare)
	}
	if snapshot.AverageRating != 7.2 {
		t.Errorf("average rating = %v, want 7.2 pooled across both competitions", snapshot.AverageRating)
	}
}
