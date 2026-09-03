package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	apiFootballProviderName         = "api-football"
	apiFootballFallbackProviderName = "api-football-fallback"
	defaultAPIFootballBaseURL       = "https://v3.football.api-sports.io"
	seasonCacheTTL                  = 24 * time.Hour
)

type teamSeasonInfo struct {
	Season   int
	LeagueID int
}

type seasonCacheEntry struct {
	info      teamSeasonInfo
	expiresAt time.Time
}

type topNRanks struct {
	ranks   map[int]int // external player ID -> rank position (1-based)
	total   int
	expires time.Time
}

type formSnapshot struct {
	Games   int
	Goals   int
	Assists int
	Rating  float64 // average across last games where rating was available
	Minutes float64 // average minutes per appearance
}

type formCacheEntry struct {
	form    formSnapshot
	expires time.Time
}

// externalIDsStore is the subset of the repository interface needed by the
// api-football provider — keeps the provider decoupled from the full service
// repository.
type externalIDsStore interface {
	GetExternalIDs(ctx context.Context, playerID int64, provider string) (*domain.PlayerExternalIDs, error)
	UpsertExternalIDs(ctx context.Context, ids domain.PlayerExternalIDs) error
	DeleteExternalIDs(ctx context.Context, playerID int64, provider string) error
}

type apiFootballPerformanceProvider struct {
	key      string
	baseURL  string
	client   *http.Client
	store    externalIDsStore
	fallback performanceProvider

	seasonMu     sync.Mutex
	seasonByTeam map[int]seasonCacheEntry

	topNMu       sync.Mutex
	topNByLeague map[int]map[string]topNRanks // leagueID -> positionGroup -> ranks

	formMu       sync.Mutex
	formByPlayer map[int]formCacheEntry // external player ID -> form

	// Outbound pacing. A performance sync spends ~9 requests per player with
	// no natural gaps, so 22 players emit nearly 200 calls in under 20
	// seconds — enough to trip the per-minute ceiling on a Pro plan and get
	// a whole sync answered with "Too many requests". minGap spaces calls
	// out; lastCallAt tracks when the previous one went out.
	callMu     sync.Mutex
	lastCallAt time.Time
	minGap     time.Duration
}

func newAPIFootballPerformanceProvider(key, baseURL string, store externalIDsStore, timeout time.Duration, fallback performanceProvider) performanceProvider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultAPIFootballBaseURL
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	return &apiFootballPerformanceProvider{
		key:          strings.TrimSpace(key),
		baseURL:      baseURL,
		client:       &http.Client{Timeout: timeout},
		store:        store,
		fallback:     fallback,
		seasonByTeam: make(map[int]seasonCacheEntry),
		topNByLeague: make(map[int]map[string]topNRanks),
		formByPlayer: make(map[int]formCacheEntry),
		minGap:       apiFootballMinRequestGap,
	}
}

const (
	topNCacheTTL = 6 * time.Hour
	formCacheTTL = 1 * time.Hour
	formMatches  = 5

	// How many finished fixtures a season needs before we treat its stats as
	// a fair reading of current form. Below this the sample is too thin to
	// rank anyone on — one good substitute appearance would outrank a season
	// of elite output. Five matches is roughly a month of league play.
	minPlayedFixturesForSeason = 5

	// Minimum spacing between outbound API-Football calls. The Pro plan
	// allows 300 requests/minute; 250ms sustains 240/minute, which leaves
	// room for the retry traffic a sync generates without ever reaching the
	// ceiling. A full 22-player sync costs roughly 50s of wall clock at this
	// pace — irrelevant for a job that runs twice a day, and far better than
	// a fast sync that comes back rate-limited and silently keeps stale
	// scores. Raise it if the roster grows onto an Ultra plan.
	apiFootballMinRequestGap = 250 * time.Millisecond
)

func (p *apiFootballPerformanceProvider) Name() string {
	return apiFootballProviderName
}

func (p *apiFootballPerformanceProvider) FetchPerformanceSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error) {
	if p.store != nil {
		ids, err := p.store.GetExternalIDs(ctx, player.ID, apiFootballProviderName)
		if err != nil {
			log.Printf("api-football: GetExternalIDs failed for player %d: %v — falling back to text-search", player.ID, err)
		}
		if err == nil && ids != nil && strings.TrimSpace(ids.ExternalID) != "" {
			snapshot, fetchErr := p.fetchByExternalID(ctx, player, ids)
			if fetchErr == nil {
				return snapshot, nil
			}
			log.Printf("api-football: mapping-path failed for player %d (external_id=%s): %v — retrying via text-search", player.ID, ids.ExternalID, fetchErr)
			// Self-heal: text-search may rebuild the mapping (e.g. after a transfer
			// or if the saved external_id became invalid). If text-search can't
			// match either, the demo fallback inside fetchByTextSearch kicks in.
		}
	}

	return p.fetchByTextSearch(ctx, player)
}

func (p *apiFootballPerformanceProvider) fetchByExternalID(ctx context.Context, player domain.PlayerSyncTarget, ids *domain.PlayerExternalIDs) (domain.PerformanceSnapshot, error) {
	externalPlayerID, err := strconv.Atoi(strings.TrimSpace(ids.ExternalID))
	if err != nil || externalPlayerID <= 0 {
		return domain.PerformanceSnapshot{}, fmt.Errorf("invalid external player id %q", ids.ExternalID)
	}

	knownTeamID, _ := strconv.Atoi(strings.TrimSpace(ids.ExternalTeamID))

	var info teamSeasonInfo
	if knownTeamID > 0 {
		info = p.currentSeasonForTeam(ctx, knownTeamID)
	} else {
		info = teamSeasonInfo{Season: defaultCurrentSeason()}
	}

	apiPlayer, err := p.fetchPlayerByID(ctx, externalPlayerID, info.Season)
	if err != nil {
		return domain.PerformanceSnapshot{}, err
	}

	stat, ok := selectClubStatistic(apiPlayer.Statistics, knownTeamID, info.LeagueID)
	if !ok {
		return domain.PerformanceSnapshot{}, fmt.Errorf("api-football no statistic for player id %d", externalPlayerID)
	}

	// Re-validate the saved mapping every sync. If the persisted external_id
	// turns out to point at the wrong player (e.g. a borderline match was
	// saved earlier, or the API reassigned the ID), drop the mapping so that
	// the caller's text-search self-heal path can rebuild it.
	if !passesSanityCheck(player, apiPlayer, stat) {
		if p.store != nil {
			if delErr := p.store.DeleteExternalIDs(ctx, player.ID, apiFootballProviderName); delErr != nil {
				log.Printf("api-football: DeleteExternalIDs failed for player %d after sanity-check mismatch: %v", player.ID, delErr)
			}
		}
		return domain.PerformanceSnapshot{}, fmt.Errorf("api-football mapping sanity-check failed for player %d", player.ID)
	}

	currentTeamID := stat.Team.ID
	if currentTeamID <= 0 {
		currentTeamID = knownTeamID
	}

	if stat.Team.ID > 0 && stat.Team.ID != knownTeamID && p.store != nil {
		if upsertErr := p.store.UpsertExternalIDs(ctx, domain.PlayerExternalIDs{
			PlayerID:       player.ID,
			Provider:       apiFootballProviderName,
			ExternalID:     ids.ExternalID,
			ExternalTeamID: strconv.Itoa(stat.Team.ID),
		}); upsertErr != nil {
			log.Printf("api-football: UpsertExternalIDs (transfer) failed for player %d: %v", player.ID, upsertErr)
		}
		// Refresh league info for the new team — old league might be wrong now.
		info = p.currentSeasonForTeam(ctx, stat.Team.ID)
	}

	rankPos, rankTotal := p.topNRankFor(ctx, info.LeagueID, info.Season, positionGroup(player.Position), externalPlayerID)
	form, _ := p.formFor(ctx, externalPlayerID, currentTeamID)

	snapshot := buildAPIFootballSnapshot(player, stat, rankPos, rankTotal, form)
	attachProfile(&snapshot, apiPlayer.Player)
	// Phase 4.3 MVP: stats-derived performance events. Right now this only
	// catches goal droughts for attacking positions — full per-fixture
	// hat-trick / brace detection lands once we expose per-fixture data.
	snapshot.PerformanceEvents = detectPerformanceEvents(player, form, info.Season)
	return snapshot, nil
}

func (p *apiFootballPerformanceProvider) fetchByTextSearch(ctx context.Context, player domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error) {
	team, err := p.findTeam(ctx, player.Club)
	if err != nil {
		log.Printf("api-football: text-search fallback for player %d (%s) — findTeam failed for club %q: %v",
			player.ID, player.Name, player.Club, err)
		return p.fallbackSnapshot(ctx, player)
	}

	apiPlayer, err := p.findPlayer(ctx, player, team.ID)
	if err != nil {
		log.Printf("api-football: text-search fallback for player %d (%s) — findPlayer failed in team %d: %v",
			player.ID, player.Name, team.ID, err)
		return p.fallbackSnapshot(ctx, player)
	}

	// Resolve league up-front so selectClubStatistic can use the (team,
	// league) deterministic filter rather than a minutes heuristic.
	info := p.currentSeasonForTeam(ctx, team.ID)

	// Use selectClubStatistic (not selectPlayerStatistic) so the fallback
	// inside the helper prefers club statistics over a national-team record —
	// otherwise we could persist a national-team external_team_id during an
	// international break.
	stat, ok := selectClubStatistic(apiPlayer.Statistics, team.ID, info.LeagueID)
	if !ok {
		log.Printf("api-football: text-search fallback for player %d (%s) — no usable statistic found",
			player.ID, player.Name)
		return p.fallbackSnapshot(ctx, player)
	}

	if !passesSanityCheck(player, apiPlayer, stat) {
		log.Printf("api-football: text-search fallback for player %d (%s) — sanity-check failed (api=%s/%d/%dmin, our pos=%s, age=%d)",
			player.ID, player.Name,
			apiPlayer.Player.Position, apiPlayer.Player.Age, stat.Games.Minutes,
			player.Position, player.Age)
		return p.fallbackSnapshot(ctx, player)
	}

	if p.store != nil && apiPlayer.Player.ID > 0 && !stat.Team.National && stat.Team.ID > 0 {
		if upsertErr := p.store.UpsertExternalIDs(ctx, domain.PlayerExternalIDs{
			PlayerID:       player.ID,
			Provider:       apiFootballProviderName,
			ExternalID:     strconv.Itoa(apiPlayer.Player.ID),
			ExternalTeamID: strconv.Itoa(stat.Team.ID),
		}); upsertErr != nil {
			log.Printf("api-football: UpsertExternalIDs (text-search) failed for player %d: %v", player.ID, upsertErr)
		}
	}

	// info already resolved above for selectClubStatistic; reuse it.
	rankPos, rankTotal := p.topNRankFor(ctx, info.LeagueID, info.Season, positionGroup(player.Position), apiPlayer.Player.ID)
	form, _ := p.formFor(ctx, apiPlayer.Player.ID, team.ID)

	snapshot := buildAPIFootballSnapshot(player, stat, rankPos, rankTotal, form)
	attachProfile(&snapshot, apiPlayer.Player)
	snapshot.PerformanceEvents = detectPerformanceEvents(player, form, info.Season)
	return snapshot, nil
}

func (p *apiFootballPerformanceProvider) fetchPlayerByID(ctx context.Context, externalPlayerID, season int) (apiFootballPlayerEntry, error) {
	params := url.Values{
		"id":     []string{strconv.Itoa(externalPlayerID)},
		"season": []string{strconv.Itoa(season)},
	}

	var response apiFootballPlayersResponse
	if err := p.get(ctx, "/players", params, &response); err != nil {
		return apiFootballPlayerEntry{}, err
	}
	if hasAPIFootballErrors(response.Errors) {
		return apiFootballPlayerEntry{}, fmt.Errorf("api-football players error: %s", string(response.Errors))
	}
	if len(response.Response) == 0 {
		return apiFootballPlayerEntry{}, fmt.Errorf("api-football no data for player id %d season %d", externalPlayerID, season)
	}
	return response.Response[0], nil
}

// topNRankFor returns the player's rank (1-based) within the given league and
// position group, plus the total ranked size. (0, 0) means we couldn't compute
// a rank (cache miss + fetch error or player absent from the top list).
func (p *apiFootballPerformanceProvider) topNRankFor(ctx context.Context, leagueID, season int, group string, externalPlayerID int) (int, int) {
	if leagueID <= 0 || season <= 0 || group == "" || group == "OTHER" {
		return 0, 0
	}

	now := time.Now()
	p.topNMu.Lock()
	if byGroup, ok := p.topNByLeague[leagueID]; ok {
		if entry, ok := byGroup[group]; ok && now.Before(entry.expires) {
			p.topNMu.Unlock()
			return entry.ranks[externalPlayerID], entry.total
		}
	}
	p.topNMu.Unlock()

	endpoint := topNEndpointFor(group)
	if endpoint == "" {
		return 0, 0
	}

	entries, err := p.fetchPlayerList(ctx, endpoint, leagueID, season)
	if err != nil {
		log.Printf("api-football: topN fetch failed for league=%d group=%s: %v", leagueID, group, err)
		return 0, 0
	}

	ranks := buildRanksByGroup(entries, group)

	p.topNMu.Lock()
	if _, ok := p.topNByLeague[leagueID]; !ok {
		p.topNByLeague[leagueID] = make(map[string]topNRanks)
	}
	p.topNByLeague[leagueID][group] = topNRanks{
		ranks:   ranks,
		total:   len(ranks),
		expires: now.Add(topNCacheTTL),
	}
	p.topNMu.Unlock()

	return ranks[externalPlayerID], len(ranks)
}

func (p *apiFootballPerformanceProvider) fetchPlayerList(ctx context.Context, path string, leagueID, season int) ([]apiFootballPlayerEntry, error) {
	params := url.Values{
		"league": []string{strconv.Itoa(leagueID)},
		"season": []string{strconv.Itoa(season)},
	}
	var response apiFootballPlayersResponse
	if err := p.get(ctx, path, params, &response); err != nil {
		return nil, err
	}
	if hasAPIFootballErrors(response.Errors) {
		return nil, fmt.Errorf("api-football %s error: %s", path, string(response.Errors))
	}
	return response.Response, nil
}

// formFor aggregates the player's goals/assists/rating across the team's last
// formMatches fixtures. Returns ok=false when no fixtures or no usable rating
// could be collected (start of season, lower-tier league with no ratings).
func (p *apiFootballPerformanceProvider) formFor(ctx context.Context, externalPlayerID, teamID int) (formSnapshot, bool) {
	if externalPlayerID <= 0 || teamID <= 0 {
		return formSnapshot{}, false
	}

	now := time.Now()
	p.formMu.Lock()
	if entry, ok := p.formByPlayer[externalPlayerID]; ok && now.Before(entry.expires) {
		p.formMu.Unlock()
		return entry.form, entry.form.Games > 0
	}
	p.formMu.Unlock()

	fixtures, err := p.lastFixtures(ctx, teamID)
	if err != nil {
		log.Printf("api-football: lastFixtures failed for team=%d: %v", teamID, err)
		return formSnapshot{}, false
	}

	form := aggregateFormAcrossFixtures(ctx, p, fixtures, externalPlayerID)

	p.formMu.Lock()
	p.formByPlayer[externalPlayerID] = formCacheEntry{
		form:    form,
		expires: now.Add(formCacheTTL),
	}
	p.formMu.Unlock()

	return form, form.Games > 0
}

// playedFixtures counts finished matches a team has in a season. It answers
// "does this season have any evidence in it yet?" — the question that
// separates a real current season from one that merely opened on the
// calendar. Returns -1 when the count can't be established, so callers can
// tell "no matches" apart from "couldn't ask" and leave the season alone.
//
// Pass a leagueID to count only that competition. This matters more than it
// looks: pre-season friendlies are finished fixtures too, and a club on a
// summer tour racks up half a dozen before the league kicks off. Counting
// them made Real Madrid look like it had a season underway while La Liga
// hadn't started, so its players were still being scored on an empty 2026 —
// Vinícius couldn't even be found, because the squad list for that season
// was empty.
func (p *apiFootballPerformanceProvider) playedFixtures(ctx context.Context, teamID, season, leagueID int) int {
	params := url.Values{
		"team":   []string{strconv.Itoa(teamID)},
		"season": []string{strconv.Itoa(season)},
		"status": []string{"FT"},
	}
	if leagueID > 0 {
		params.Set("league", strconv.Itoa(leagueID))
	}
	var response apiFootballFixturesResponse
	if err := p.get(ctx, "/fixtures", params, &response); err != nil {
		log.Printf("api-football: playedFixtures failed for team %d season %d: %v", teamID, season, err)
		return -1
	}
	if hasAPIFootballErrors(response.Errors) {
		log.Printf("api-football: playedFixtures error for team %d season %d: %s", teamID, season, string(response.Errors))
		return -1
	}
	return len(response.Response)
}

func (p *apiFootballPerformanceProvider) lastFixtures(ctx context.Context, teamID int) ([]int, error) {
	params := url.Values{
		"team": []string{strconv.Itoa(teamID)},
		"last": []string{strconv.Itoa(formMatches)},
	}
	var response apiFootballFixturesResponse
	if err := p.get(ctx, "/fixtures", params, &response); err != nil {
		return nil, err
	}
	if hasAPIFootballErrors(response.Errors) {
		return nil, fmt.Errorf("api-football fixtures error: %s", string(response.Errors))
	}
	ids := make([]int, 0, len(response.Response))
	for _, item := range response.Response {
		if item.Fixture.ID > 0 {
			ids = append(ids, item.Fixture.ID)
		}
	}
	return ids, nil
}

func (p *apiFootballPerformanceProvider) fixturePlayers(ctx context.Context, fixtureID int) ([]apiFootballFixtureTeamPlayers, error) {
	params := url.Values{"fixture": []string{strconv.Itoa(fixtureID)}}
	var response apiFootballFixturePlayersResponse
	if err := p.get(ctx, "/fixtures/players", params, &response); err != nil {
		return nil, err
	}
	if hasAPIFootballErrors(response.Errors) {
		return nil, fmt.Errorf("api-football fixtures/players error: %s", string(response.Errors))
	}
	return response.Response, nil
}

func aggregateFormAcrossFixtures(ctx context.Context, p *apiFootballPerformanceProvider, fixtures []int, externalPlayerID int) formSnapshot {
	form := formSnapshot{}
	if len(fixtures) == 0 {
		return form
	}
	var ratingSum, minutesSum float64
	var ratingSamples, minutesSamples int

	for _, fixtureID := range fixtures {
		teams, err := p.fixturePlayers(ctx, fixtureID)
		if err != nil {
			log.Printf("api-football: fixture-players failed for fixture=%d: %v", fixtureID, err)
			continue
		}
		stat, ok := findFixtureStat(teams, externalPlayerID)
		if !ok {
			continue
		}
		form.Games++
		form.Goals += stat.Goals.Total
		form.Assists += stat.Goals.Assists
		if stat.Games.Minutes > 0 {
			minutesSum += float64(stat.Games.Minutes)
			minutesSamples++
		}
		if rating := parseAPIFootballRating(stat.Games.Rating); rating > 0 {
			ratingSum += rating
			ratingSamples++
		}
	}

	if ratingSamples > 0 {
		form.Rating = ratingSum / float64(ratingSamples)
	}
	if minutesSamples > 0 {
		form.Minutes = minutesSum / float64(minutesSamples)
	}
	return form
}

func findFixtureStat(teams []apiFootballFixtureTeamPlayers, externalPlayerID int) (apiFootballStatistic, bool) {
	for _, team := range teams {
		for _, p := range team.Players {
			if p.Player.ID != externalPlayerID {
				continue
			}
			if len(p.Statistics) == 0 {
				return apiFootballStatistic{}, false
			}
			return p.Statistics[0], true
		}
	}
	return apiFootballStatistic{}, false
}

// topNEndpointFor maps our internal position groups onto api-football's
// ranking endpoints. ATT uses topscorers (relevant proxy by goals); MID/DEF
// use topassists, which we then re-sort by rating to derive a fairer rank.
func topNEndpointFor(group string) string {
	switch group {
	case "ATT":
		return "/players/topscorers"
	case "MID", "DEF":
		return "/players/topassists"
	default:
		return ""
	}
}

func buildRanksByGroup(entries []apiFootballPlayerEntry, group string) map[int]int {
	type ranked struct {
		playerID int
		rating   float64
		goals    int
	}
	pool := make([]ranked, 0, len(entries))
	for _, entry := range entries {
		if entry.Player.ID <= 0 {
			continue
		}
		if entry.Player.Position != "" && positionGroup(entry.Player.Position) != group {
			continue
		}
		var bestRating float64
		var totalGoals int
		for _, stat := range entry.Statistics {
			if stat.Team.National {
				continue
			}
			if r := parseAPIFootballRating(stat.Games.Rating); r > bestRating {
				bestRating = r
			}
			totalGoals += stat.Goals.Total
		}
		pool = append(pool, ranked{playerID: entry.Player.ID, rating: bestRating, goals: totalGoals})
	}

	if group == "ATT" {
		// topscorers already returns goals-sorted; preserve incoming order.
	} else {
		// MID/DEF: client-side sort by rating desc (stable on ties via goals).
		for i := 1; i < len(pool); i++ {
			for j := i; j > 0; j-- {
				if pool[j].rating > pool[j-1].rating ||
					(pool[j].rating == pool[j-1].rating && pool[j].goals > pool[j-1].goals) {
					pool[j], pool[j-1] = pool[j-1], pool[j]
					continue
				}
				break
			}
		}
	}

	ranks := make(map[int]int, len(pool))
	for idx, r := range pool {
		ranks[r.playerID] = idx + 1
	}
	return ranks
}

func (p *apiFootballPerformanceProvider) fallbackSnapshot(ctx context.Context, player domain.PlayerSyncTarget) (domain.PerformanceSnapshot, error) {
	snapshot, err := p.fallback.FetchPerformanceSnapshot(ctx, player)
	if err != nil {
		return domain.PerformanceSnapshot{}, err
	}
	snapshot.Provider = apiFootballFallbackProviderName
	return snapshot, nil
}

func (p *apiFootballPerformanceProvider) findTeam(ctx context.Context, club string) (apiFootballTeam, error) {
	// Hardcoded shortcut for top clubs: api-football's text search is noisy
	// for popular names (e.g. "Manchester City" pulls up Manchester City W
	// before the canonical men's team). For seed clubs we already know the
	// stable team_id, so skip the API call entirely. Names verified against
	// api-football documentation.
	if id, ok := canonicalTeamID(club); ok {
		return apiFootballTeam{ID: id, Name: club}, nil
	}

	// Try the canonical club name first; if api-football doesn't recognize it
	// (e.g. our DB stores "FC Barcelona" but they index it as "Barcelona"),
	// retry with a stripped/simplified variant.
	candidates := teamSearchCandidates(club)
	var lastErr error
	for _, query := range candidates {
		team, err := p.searchTeam(ctx, query, club)
		if err == nil {
			return team, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("api-football team not found: %s", club)
	}
	return apiFootballTeam{}, lastErr
}

// canonicalTeamID returns the api-football team_id for a club name we know
// well enough to hardcode. Stable values verified against the api-football
// `/teams?search=` and `/leagues?team=` endpoints. Add new entries when seed
// data introduces a new club.
//
// Why hardcode at all: text search returns multiple matches per name and the
// "junk" filter can't always recover the canonical first team — sometimes
// the women's / reserve squad is the *only* result returned for a given
// query. Hardcoding sidesteps both issues for the well-known clubs in seed.
func canonicalTeamID(club string) (int, bool) {
	id, ok := knownClubTeamIDs[normalizeClubName(club)]
	return id, ok
}

var knownClubTeamIDs = map[string]int{
	// Premier League
	"manchester city":   50,
	"man city":          50,
	"manchester united": 33,
	"man united":        33,
	"liverpool":         40,
	"arsenal":           42,
	"chelsea":           49,
	"tottenham":         47,
	"newcastle":         34,
	"aston villa":       66,
	"west ham":          48,

	// La Liga
	"real madrid":     541,
	"fc barcelona":    529,
	"barcelona":       529,
	"atletico madrid": 530,
	"real sociedad":   548,
	"villarreal":      533,

	// Bundesliga
	"bayern munich":     157,
	"bayer leverkusen":  168,
	"borussia dortmund": 165,
	"rb leipzig":        173,

	// Serie A
	"inter":       505,
	"inter milan": 505,
	"ac milan":    489,
	"juventus":    496,
	"napoli":      492,
	"roma":        497,
	"atalanta":    499,

	// Ligue 1
	"psg":                 85,
	"paris saint germain": 85,
	"marseille":           81,
	"lyon":                80,
	"monaco":              91,

	// Süper Lig
	"fenerbahce":  611,
	"galatasaray": 645,
	"besiktas":    549,
	"trabzonspor": 998, // Salah, from 2026-08-03

	// MLS
	"inter miami": 1614,

	// Saudi Pro League
	"al nassr":   2939,
	"al ittihad": 2932,
	"al hilal":   2925,
}

func (p *apiFootballPerformanceProvider) searchTeam(ctx context.Context, query, originalClub string) (apiFootballTeam, error) {
	var response apiFootballTeamsResponse
	if err := p.get(ctx, "/teams", url.Values{"search": []string{query}}, &response); err != nil {
		return apiFootballTeam{}, err
	}
	if hasAPIFootballErrors(response.Errors) {
		return apiFootballTeam{}, fmt.Errorf("api-football teams error: %s", string(response.Errors))
	}
	if len(response.Response) == 0 {
		return apiFootballTeam{}, fmt.Errorf("api-football team not found: %s", originalClub)
	}

	// Drop reserve/youth/women teams up-front so they can't win the
	// tie-break against the canonical first team. If filtering empties the
	// list (rare — e.g. seed actually targets a youth squad) we fall back to
	// the unfiltered set rather than failing outright.
	candidates := make([]apiFootballTeam, 0, len(response.Response))
	for _, item := range response.Response {
		if isJunkTeam(item.Team.Name) {
			continue
		}
		candidates = append(candidates, item.Team)
	}
	if len(candidates) == 0 {
		for _, item := range response.Response {
			candidates = append(candidates, item.Team)
		}
	}

	needle := normalizeFootballName(originalClub)
	for _, team := range candidates {
		if normalizeFootballName(team.Name) == needle {
			return team, nil
		}
	}
	// No exact match — pick the non-national candidate with the highest
	// substring-overlap score against the needle.
	best := apiFootballTeam{}
	bestScore := -1
	for _, team := range candidates {
		if team.National {
			continue
		}
		score := teamNameOverlap(needle, normalizeFootballName(team.Name))
		if score > bestScore {
			best = team
			bestScore = score
		}
	}
	if bestScore >= 0 {
		return best, nil
	}
	return candidates[0], nil
}

// isJunkTeam returns true for reserve / youth / women / academy team names
// that pollute api-football's `/teams?search=` results. Detection is based
// on suffix tokens (case-insensitive) since those rarely appear in
// canonical first-team names. Patterns covered:
//   - " W", " Women"         — women's squads
//   - " U23", " U21", ...    — academy age groups
//   - " II", " B"            — reserve teams
//   - " Reserves", " Youth", " Academy"
func isJunkTeam(rawName string) bool {
	name := strings.ToLower(strings.TrimSpace(rawName))
	if name == "" {
		return false
	}
	// Whole-word suffixes — split on space and inspect last token(s).
	tokens := strings.Fields(name)
	if len(tokens) >= 2 {
		last := tokens[len(tokens)-1]
		switch last {
		case "w", "women", "ii", "b", "reserves", "youth", "academy":
			return true
		}
		// Age-group suffix matches "u17"/"u19"/"u21"/"u23".
		if strings.HasPrefix(last, "u") && len(last) >= 2 && len(last) <= 3 {
			rest := last[1:]
			if _, err := strconv.Atoi(rest); err == nil {
				return true
			}
		}
	}
	// Some feeds embed the marker mid-name ("Manchester City Women's").
	for _, marker := range []string{" women", " reserves", " academy", " youth", " u23 ", " u21 ", " u19 ", " u17 "} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// teamSearchCandidates returns variants of the club name to try against
// api-football's `/teams?search=` endpoint. The endpoint rejects non-ASCII
// and short queries, and uses different naming (e.g. "Barcelona" vs "FC
// Barcelona"), so we generate a small ordered set of plausible queries.
func teamSearchCandidates(club string) []string {
	seen := make(map[string]struct{})
	add := func(s string) []string {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "" || len(key) < 3 {
			return nil
		}
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		return []string{s}
	}

	var result []string
	result = append(result, add(club)...)
	result = append(result, add(asciiOnly(club))...)
	result = append(result, add(stripClubPrefix(asciiOnly(club)))...)
	result = append(result, add(stripClubPrefix(club))...)
	return result
}

// stripClubPrefix removes common club-form prefixes/suffixes that prevent
// api-football's text search from matching our seed names verbatim.
func stripClubPrefix(name string) string {
	trimmed := strings.TrimSpace(name)
	prefixes := []string{"FC ", "AFC ", "CF ", "AS ", "SC ", "SK ", "1. ", "1.FC ", "FK "}
	for _, prefix := range prefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	suffixes := []string{" FC", " CF", " AFC"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(trimmed, suffix) {
			return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)])
		}
	}
	return trimmed
}

// asciiOnly converts diacritics through normalizeFootballName, then strips
// any remaining non-alphanumeric characters. api-football's `/teams?search=`
// rejects non-ASCII outright with a 400.
func asciiOnly(s string) string {
	normalized := normalizeFootballName(s)
	var b strings.Builder
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ':
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// teamNameOverlap returns how many characters of `needle` (a normalized
// search string) appear contiguously inside `candidate`. Used to break ties
// when api-football returns multiple matching teams.
func teamNameOverlap(needle, candidate string) int {
	if needle == "" || candidate == "" {
		return 0
	}
	if strings.Contains(candidate, needle) {
		return len(needle)
	}
	if strings.Contains(needle, candidate) {
		return len(candidate)
	}
	// Character-overlap fallback: count shared word tokens.
	candWords := make(map[string]struct{})
	for _, w := range strings.Fields(candidate) {
		candWords[w] = struct{}{}
	}
	overlap := 0
	for _, w := range strings.Fields(needle) {
		if _, ok := candWords[w]; ok {
			overlap += len(w)
		}
	}
	return overlap
}

// bestPlayerMatch picks the api-football entry that best corroborates one of
// our roster players, or reports that none does.
//
// The old rule — first candidate whose name contains the search term — is
// exactly how our goalkeeper "J. García" was bound to Eric García: both are
// "garcia" at Barcelona, and Eric was returned first. Two signals settle it,
// and both were being thrown away:
//
//   - the initial. Our roster writes "J. García", "E. Haaland", "M. Salah".
//     If a candidate's first name starts with a different letter, it is not
//     our player, full stop.
//   - the position, which lives in the statistics rather than on the player
//     object (api-football leaves player.position null).
//
// A candidate contradicted by either is rejected outright rather than scored
// down: a defender is not a goalkeeper who had a bad season.
func bestPlayerMatch(player domain.PlayerSyncTarget, candidates []apiFootballPlayerEntry) (apiFootballPlayerEntry, bool) {
	needle := normalizeFootballName(player.Name)
	initial := rosterInitial(player.Name)

	var best apiFootballPlayerEntry
	bestScore := 0

	for _, item := range candidates {
		firstname := normalizeFootballName(item.Player.Firstname)

		// Contradicted initial: not our player.
		if initial != "" && firstname != "" && !strings.HasPrefix(firstname, initial) {
			continue
		}

		// Contradicted position: not our player either. Read from the club
		// statistic, since that is the only place it is populated.
		if player.Position != "" {
			apiPosition := ""
			for _, stat := range item.Statistics {
				if pos := strings.TrimSpace(stat.Games.Position); pos != "" {
					apiPosition = pos
					break
				}
			}
			if positionsContradict(apiPosition, player.Position) {
				continue
			}
		}

		score := 1
		if initial != "" && strings.HasPrefix(firstname, initial) {
			score += 3
		}
		if player.Position != "" {
			apiPos := ""
			for _, stat := range item.Statistics {
				if pos := strings.TrimSpace(stat.Games.Position); pos != "" {
					apiPos = pos
					break
				}
			}
			if apiPos != "" && positionGroup(apiPos) == positionGroup(player.Position) {
				score += 2
			}
		}
		if lastname := normalizeFootballName(item.Player.Lastname); lastname != "" && strings.Contains(needle, lastname) {
			score += 3
		}
		if firstname != "" && strings.Contains(needle, firstname) {
			score += 2
		}
		if strings.Contains(normalizeFootballName(item.Player.Name), playerSearchTerm(needle)) {
			score += 2
		}
		if player.Age > 0 && item.Player.Age > 0 {
			diff := item.Player.Age - player.Age
			if diff < 0 {
				diff = -diff
			}
			if diff <= 1 {
				score += 2
			}
		}

		if score > bestScore {
			best, bestScore = item, score
		}
	}

	// A bare 1 means the candidate survived the contradiction checks but
	// nothing actually pointed at them; let the caller keep searching.
	if bestScore <= 1 {
		return apiFootballPlayerEntry{}, false
	}
	return best, true
}

// rosterInitial returns the lowercased first initial when our roster name
// carries one ("J. García" -> "j"), and "" when the name leads with a full
// word ("Vinicius Jr", "Pedri") — in that case there is no initial to check.
func rosterInitial(name string) string {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) < 2 {
		return ""
	}
	first := fields[0]
	if !strings.HasSuffix(first, ".") {
		return ""
	}
	letters := normalizeFootballName(strings.TrimSuffix(first, "."))
	if len([]rune(letters)) != 1 {
		return ""
	}
	return letters
}

func (p *apiFootballPerformanceProvider) findPlayer(ctx context.Context, player domain.PlayerSyncTarget, teamID int) (apiFootballPlayerEntry, error) {
	info := p.currentSeasonForTeam(ctx, teamID)

	var lastErr error
	for _, searchTerm := range playerSearchTerms(player.Name) {
		params := url.Values{
			"team":   []string{strconv.Itoa(teamID)},
			"season": []string{strconv.Itoa(info.Season)},
			"search": []string{searchTerm},
		}

		var response apiFootballPlayersResponse
		if err := p.get(ctx, "/players", params, &response); err != nil {
			return apiFootballPlayerEntry{}, err
		}
		if hasAPIFootballErrors(response.Errors) {
			return apiFootballPlayerEntry{}, fmt.Errorf("api-football players error: %s", string(response.Errors))
		}
		if len(response.Response) == 0 {
			lastErr = fmt.Errorf("api-football player not found: %s (search=%q)", player.Name, searchTerm)
			continue
		}

		// Score the candidates instead of taking the first one whose name
		// merely contains the search term. That rule matched "E. García"
		// against a search for "garcia" when our roster said "J. García", and
		// Eric happened to come first in the response.
		if best, ok := bestPlayerMatch(player, response.Response); ok {
			return best, nil
		}

		// Nothing corroborated the name. Fall back to the first entry only
		// when this was our best guess AND the search was unambiguous — with
		// several candidates back, picking one at random is how the wrong
		// García got in.
		if searchTerm == playerSearchTerm(player.Name) && len(response.Response) == 1 {
			return response.Response[0], nil
		}
	}

	if lastErr != nil {
		return apiFootballPlayerEntry{}, lastErr
	}
	return apiFootballPlayerEntry{}, fmt.Errorf("api-football player not found: %s", player.Name)
}

func (p *apiFootballPerformanceProvider) currentSeasonForTeam(ctx context.Context, teamID int) teamSeasonInfo {
	now := time.Now()

	p.seasonMu.Lock()
	if entry, ok := p.seasonByTeam[teamID]; ok && now.Before(entry.expiresAt) {
		p.seasonMu.Unlock()
		return entry.info
	}
	p.seasonMu.Unlock()

	info, err := p.fetchCurrentSeason(ctx, teamID)
	if err != nil || info.Season <= 0 {
		info.Season = defaultCurrentSeason()
	}

	// Season-rollover guard (2026-08).
	//
	// API-Football flips "current" to the new season the moment it opens —
	// in August it reported La Liga 2026/27 as current, ending 2027-05-30,
	// which is correct but useless: no matches had been played, so every
	// player came back with zero minutes, zero goals and no rating. The
	// snapshot builder then substituted its 5.8 default rating and produced
	// nonsense — Pedri scored 18.9 and Yamal 26.1 on a 0–100 scale.
	//
	// So don't trust the label, count the evidence: if the season has barely
	// any finished fixtures AND the one before it demonstrably has more, use
	// the older season. Requiring the second half of that test matters —
	// checking only "current looks empty" would roll a whole season back on
	// any empty-but-successful response, which is indistinguishable from a
	// genuinely fresh season. Comparing the two makes a wrong answer require
	// two wrong answers. This self-heals as the new season fills in, and both
	// calls are cached per team for the season TTL.
	if played := p.playedFixtures(ctx, teamID, info.Season, info.LeagueID); played >= 0 && played < minPlayedFixturesForSeason {
		if previous := p.playedFixtures(ctx, teamID, info.Season-1, info.LeagueID); previous > played {
			log.Printf("api-football: season %d for team %d (league %d) has %d finished fixtures vs %d in %d — using the older season",
				info.Season, teamID, info.LeagueID, played, previous, info.Season-1)
			info.Season--
		}
	}

	p.seasonMu.Lock()
	p.seasonByTeam[teamID] = seasonCacheEntry{
		info:      info,
		expiresAt: now.Add(seasonCacheTTL),
	}
	p.seasonMu.Unlock()

	return info
}

func (p *apiFootballPerformanceProvider) fetchCurrentSeason(ctx context.Context, teamID int) (teamSeasonInfo, error) {
	params := url.Values{
		"team":    []string{strconv.Itoa(teamID)},
		"current": []string{"true"},
	}

	var response apiFootballLeaguesResponse
	if err := p.get(ctx, "/leagues", params, &response); err != nil {
		return teamSeasonInfo{}, err
	}
	if hasAPIFootballErrors(response.Errors) {
		return teamSeasonInfo{}, fmt.Errorf("api-football leagues error: %s", string(response.Errors))
	}

	// Prefer League type over Cup so that topN rank uses regular-season standings.
	var fallback teamSeasonInfo
	for _, item := range response.Response {
		for _, season := range item.Seasons {
			if !season.Current || season.Year <= 0 {
				continue
			}
			info := teamSeasonInfo{Season: season.Year, LeagueID: item.League.ID}
			if strings.EqualFold(item.League.Type, "League") {
				return info, nil
			}
			if fallback.Season == 0 {
				fallback = info
			}
		}
	}
	if fallback.Season > 0 {
		return fallback, nil
	}
	return teamSeasonInfo{}, fmt.Errorf("api-football no current season for team %d", teamID)
}

// defaultCurrentSeason picks a sensible season for European football when the
// API call to resolve the active season fails. July+ → new season started,
// otherwise the previous calendar year's season is still ongoing.
func defaultCurrentSeason() int {
	now := time.Now().UTC()
	if now.Month() >= time.July {
		return now.Year()
	}
	return now.Year() - 1
}

func (p *apiFootballPerformanceProvider) get(ctx context.Context, path string, params url.Values, target any) error {
	if err := p.respectRateLimit(ctx); err != nil {
		return err
	}

	endpoint := p.baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-apisports-key", p.key)

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("api-football returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// respectRateLimit blocks until at least minGap has passed since the previous
// outbound call, so a sync paces itself instead of emptying its per-minute
// budget in one burst. It holds the lock across the wait on purpose: callers
// must queue behind each other, otherwise ten goroutines all see "the gap has
// elapsed" and fire together, which is the exact failure this prevents.
func (p *apiFootballPerformanceProvider) respectRateLimit(ctx context.Context) error {
	if p.minGap <= 0 {
		return nil
	}

	p.callMu.Lock()
	defer p.callMu.Unlock()

	if wait := time.Until(p.lastCallAt.Add(p.minGap)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.lastCallAt = time.Now()
	return nil
}

// performanceWeights groups the six signals that go into a player's
// Performance score, with one weight per signal. Weights must sum to 1.0
// within a position; see performanceWeightsFor for the actual values.
type performanceWeights struct {
	rating       float64 // match-rating from api-football
	goalsAssists float64 // (G + A) / 90, capped at position max
	xgxa         float64 // proxy from key passes + shots on target
	posRank      float64 // 1-based rank in position group within league
	minutes      float64 // minutes share over the season
	form         float64 // last-5 fixtures aggregate
}

// performanceWeightsFor returns the per-position weighting used to combine
// raw signals into the Performance score (0–100).
//
//	FWD: legacy default — goal/assist output is genuinely the job.
//	MID: heavier rating + assists, lighter goals. Creative midfielders
//	     (Pedri, Vitinha) shouldn't be punished for sub-10-goal seasons.
//	DEF: rating + minutes dominate; goal output ~5% only as a tie-breaker.
//	GK:  no goals/assists/xG channels at all; rating + minutes carry it.
//	     The goal/assist weight (normally 0.18+0.18=0.36) is reallocated
//	     to rating + minutes + form.
//
// These are the same shape as career-baseline weights (Phase 4.2); reusing
// the philosophy keeps the system consistent end-to-end.
func performanceWeightsFor(position string) performanceWeights {
	switch positionGroup(position) {
	case "ATT":
		return performanceWeights{rating: 0.30, goalsAssists: 0.18, xgxa: 0.18, posRank: 0.14, minutes: 0.10, form: 0.10}
	case "MID":
		return performanceWeights{rating: 0.40, goalsAssists: 0.10, xgxa: 0.14, posRank: 0.16, minutes: 0.10, form: 0.10}
	case "DEF":
		return performanceWeights{rating: 0.50, goalsAssists: 0.05, xgxa: 0.05, posRank: 0.15, minutes: 0.15, form: 0.10}
	case "GK":
		return performanceWeights{rating: 0.55, goalsAssists: 0.00, xgxa: 0.00, posRank: 0.15, minutes: 0.20, form: 0.10}
	default:
		// Unknown position — fall back to FWD weights so we don't accidentally
		// zero-out an unmapped player.
		return performanceWeights{rating: 0.30, goalsAssists: 0.18, xgxa: 0.18, posRank: 0.14, minutes: 0.10, form: 0.10}
	}
}

func buildAPIFootballSnapshot(player domain.PlayerSyncTarget, stat apiFootballStatistic, rankPos, rankTotal int, form formSnapshot) domain.PerformanceSnapshot {
	minutes := float64(stat.Games.Minutes)
	appearances := float64(stat.Games.Appearances)
	goals := float64(stat.Goals.Total)
	assists := float64(stat.Goals.Assists)
	keyPasses := float64(stat.Passes.Key)
	shotsOn := float64(stat.Shots.On)

	averageRating := parseAPIFootballRating(stat.Games.Rating)
	if averageRating <= 0 {
		averageRating = 5.8
	}

	goalsAssistsPer90 := per90(goals+assists, minutes)
	keyPassesPer90 := per90(keyPasses, minutes)
	shotsOnPer90 := per90(shotsOn, minutes)

	// API-Football's budget plan does not expose xG/xA; this is a transparent proxy from available attacking actions.
	xgXaProxyPer90 := round2((goalsAssistsPer90 * 0.70) + (keyPassesPer90 * 0.08) + (shotsOnPer90 * 0.05))

	// Position rank: prefer real league rank from topN endpoints; fall back to
	// a rating-based proxy when we couldn't compute the rank.
	var positionRankScore float64
	if rankPos > 0 && rankTotal > 0 {
		positionRankScore = round1(float64(rankTotal-rankPos+1) / float64(rankTotal) * 100)
	} else {
		// Tightened range 2026-05-15: rating 7.8+ already signifies a top-tier
		// performer in API-Football's distribution. The previous 9.5 ceiling
		// was theoretical — almost no full-season player ever hits it, so
		// elite players were stuck in the 50-65 range. See Performance recal.
		positionRankScore = normalizeLinear(averageRating, 5.5, 7.8)
	}

	minutesShare := normalizeLinear(minutes, 0, 3420)
	if appearances > 0 && minutesShare == 0 {
		minutesShare = normalizeLinear(appearances, 0, 38)
	}

	formScore := buildFormScore(player.Position, form)

	// Position-aware weights (Phase 4 ext-B, 2026-05-13).
	//
	// The original single weight-set treated rating + goals/assists + xG/xA as
	// the dominant signals, which made sense for forwards but punished
	// game-controlling midfielders (Pedri, Vitinha) and defenders. Web
	// reality-check against May 2026 Ballon d'Or rankings showed our system
	// had Pedri at #14 while consensus puts him as "best midfielder on the
	// planet". Per-position weights fix the bias.
	//
	// Sum must equal 1.0 within each row.
	w := performanceWeightsFor(player.Position)

	// Rating range tightened from (5.5, 9.5) to (5.5, 7.8) on 2026-05-15:
	// API-Football season-average ratings cluster 5.5-7.8 in practice. The
	// old 9.5 ceiling was a theoretical max that no real player ever reached,
	// so top performers were stuck around 50-65 normalized when they should
	// be in the 80-100 band. FIFA-style scale was the partner's request.
	normalizedScore := clampScore(
		(normalizeLinear(averageRating, 5.5, 7.8) * w.rating) +
			(normalizeLinear(goalsAssistsPer90, 0, positionGAMax(player.Position)) * w.goalsAssists) +
			(normalizeLinear(xgXaProxyPer90, 0, positionXGXAMax(player.Position)) * w.xgxa) +
			(positionRankScore * w.posRank) +
			(minutesShare * w.minutes) +
			(formScore * w.form),
	)

	return domain.PerformanceSnapshot{
		PlayerID:          player.ID,
		PlayerName:        player.Name,
		Provider:          apiFootballProviderName,
		AverageRating:     round1(averageRating),
		GoalsAssistsPer90: round2(goalsAssistsPer90),
		XGXAPer90:         xgXaProxyPer90,
		PositionRankScore: positionRankScore,
		MinutesShare:      minutesShare,
		FormScore:         formScore,
		Last5Goals:        form.Goals,
		Last5Assists:      form.Assists,
		Last5Rating:       round1(form.Rating),
		NormalizedScore:   normalizedScore,
		SnapshotAt:        time.Now().UTC(),
	}
}

// detectPerformanceEvents emits stats-derived rating events from a form
// snapshot. Phase 4.3 MVP: only goal droughts (the cheap signal we already
// have aggregated). Hat-trick / brace detection needs per-fixture goal counts
// — that lands in Phase 4.3 full, alongside a dedicated per-fixture pull.
//
// Idempotency strategy: source_ref bakes in season + ISO week so the same
// drought across re-runs of the same week is one event. When the player
// finally scores or the week rolls over, the source_ref changes and a fresh
// detection can fire.
func detectPerformanceEvents(player domain.PlayerSyncTarget, form formSnapshot, season int) []domain.CharacterEventCandidate {
	// Only attacking positions get drought-flagged. A 5-match scoreless run
	// from a centre-back isn't a story.
	if !isAttackingPosition(player.Position) {
		return nil
	}
	// Need a full 5-game window with zero goals AND zero assists. A
	// dry-on-goals creator who's still racking up assists isn't in a drought.
	if form.Games < formMatches || form.Goals != 0 || form.Assists != 0 {
		return nil
	}

	now := time.Now().UTC()
	isoYear, isoWeek := now.ISOWeek()
	sourceRef := fmt.Sprintf("drought:%d:%d-W%02d", season, isoYear, isoWeek)

	return []domain.CharacterEventCandidate{
		{
			PlayerID:        player.ID,
			TriggerWord:     "goal_drought_5_stats",
			Delta:           -1.0, // half the keyword-detected drought_5 — stats is precise, fires reliably
			TargetComponent: "performance",
			SourceRef:       sourceRef,
		},
	}
}

// isAttackingPosition returns true for the player positions where a 5-match
// goalless run is newsworthy. Defenders and goalkeepers aren't expected to
// score, so we don't flag droughts for them.
func isAttackingPosition(position string) bool {
	switch strings.ToUpper(strings.TrimSpace(position)) {
	case "FWD", "FW", "ST", "CF", "LW", "RW", "ATT", "MID", "AM", "CAM":
		return true
	}
	return false
}

// buildFormScore turns the last-N aggregate into a 0..100 normalized form
// indicator. When we have no usable form data (start of season, lower-tier
// leagues without ratings), fall back to a neutral 50.
func buildFormScore(position string, form formSnapshot) float64 {
	if form.Games == 0 {
		return 50
	}
	gaPer90 := per90(float64(form.Goals+form.Assists), form.Minutes*float64(form.Games))
	gaScore := normalizeLinear(gaPer90, 0, positionGAMax(position))
	// Same (5.5, 7.8) range as the season aggregate above — keep form on the
	// same FIFA-style scale so a 7.5-form player reads as elite, not average.
	ratingScore := normalizeLinear(form.Rating, 5.5, 7.8)
	if form.Rating <= 0 {
		// No rating data: lean on goal contribution alone.
		return clampScore(gaScore)
	}
	return clampScore((ratingScore * 0.6) + (gaScore * 0.4))
}

func selectPlayerStatistic(statistics []apiFootballStatistic, teamID int) (apiFootballStatistic, bool) {
	if len(statistics) == 0 {
		return apiFootballStatistic{}, false
	}
	for _, stat := range statistics {
		if stat.Team.ID == teamID {
			return stat, true
		}
	}
	return statistics[0], true
}

// selectClubStatistic picks the right statistic out of api-football's
// per-competition array. For Olise at Bayern we get 7 entries (Bundesliga,
// Champions League, DFB-Pokal, Audi Cup, Klub WM, Super Cup, France U21);
// only the Bundesliga one represents his "main league" production.
//
// Selection priority:
//  1. Exact match on (team, league) — this is deterministic when we know
//     both IDs (we always do for canonical clubs from `currentSeasonForTeam`).
//  2. Most minutes among entries matching knownTeamID — fallback when
//     leagueID is unknown (e.g. team_id missing from saved mapping).
//  3. Most minutes among any non-national entry — covers mid-season
//     transfers where stat.Team.ID is the new club.
//  4. statistics[0] as last resort.
func selectClubStatistic(statistics []apiFootballStatistic, knownTeamID, knownLeagueID int) (apiFootballStatistic, bool) {
	if len(statistics) == 0 {
		return apiFootballStatistic{}, false
	}

	// (1) Deterministic: exact league × team match.
	if knownTeamID > 0 && knownLeagueID > 0 {
		for _, stat := range statistics {
			if stat.Team.ID == knownTeamID && stat.League.ID == knownLeagueID {
				return stat, true
			}
		}
	}

	// (2) Same team, unknown league — pick the most-played competition.
	if knownTeamID > 0 {
		var best apiFootballStatistic
		bestMinutes := -1
		for _, stat := range statistics {
			if stat.Team.ID != knownTeamID {
				continue
			}
			if stat.Games.Minutes > bestMinutes {
				best = stat
				bestMinutes = stat.Games.Minutes
			}
		}
		if bestMinutes >= 0 {
			return best, true
		}
	}

	// (3) Mid-season transfer / unknown team — most-played non-national.
	var best apiFootballStatistic
	bestMinutes := -1
	for _, stat := range statistics {
		if stat.Team.National {
			continue
		}
		if stat.Games.Minutes > bestMinutes {
			best = stat
			bestMinutes = stat.Games.Minutes
		}
	}
	if bestMinutes >= 0 {
		return best, true
	}

	return statistics[0], true
}

// passesSanityCheck rejects obvious false-positive matches before persisting
// an external ID. Because mappings are sticky, the cost of a wrong save is
// permanent corruption — so we require at least one positive identity signal
// (matching position group OR age within ±3) plus minimal real-match activity
// (at least one full match worth of minutes).
func passesSanityCheck(player domain.PlayerSyncTarget, apiPlayer apiFootballPlayerEntry, stat apiFootballStatistic) bool {
	// Position comes from the statistic, not the player object: api-football
	// returns player.position as null, so the old comparison was always
	// between an empty string and ours and could never fail. That is how our
	// goalkeeper "J. García" was matched to Eric García, a defender at the
	// same club — the check fell through to age, 24 against 25, and passed.
	apiPosition := strings.TrimSpace(stat.Games.Position)
	if apiPosition == "" {
		apiPosition = strings.TrimSpace(apiPlayer.Player.Position)
	}

	hasPositionMatch := apiPosition != "" && player.Position != "" &&
		positionGroup(apiPosition) == positionGroup(player.Position)
	if positionsContradict(apiPosition, player.Position) {
		return false
	}

	hasAgeSignal := apiPlayer.Player.Age > 0 && player.Age > 0
	hasAgeMatch := false
	if hasAgeSignal {
		diff := apiPlayer.Player.Age - player.Age
		if diff < 0 {
			diff = -diff
		}
		if diff > 3 {
			return false
		}
		hasAgeMatch = true
	}

	// Require at least one positive identity signal — without it we'd be
	// trusting nothing more than the search-text match, which is exactly the
	// scenario this check exists to defend against.
	if !hasPositionMatch && !hasAgeMatch {
		return false
	}

	if stat.Games.Minutes < 90 {
		return false
	}

	return true
}

// positionsContradict reports whether two position labels describe people who
// cannot be the same footballer.
//
// Neither extreme works. Rejecting every mismatch would have dropped four of
// our best players onto synthetic data: api-football files Olise as a
// Midfielder in every competition while our roster says FWD, Yamal is an
// Attacker in La Liga and a Midfielder in the cups, Salah is both. Accepting
// every mismatch lets a defender stand in for a striker of the same surname.
//
// The position groups sit on a line — GK, DEF, MID, ATT — and the useful rule
// is distance along it. One step apart is ordinary disagreement about how to
// describe the same player. Two or more is a different footballer: nobody is
// an attacker here and a defender there.
//
// Goalkeeper is the exception to the arithmetic: any mismatch involving GK is
// fatal regardless of distance. Nobody keeps goal in one competition and plays
// out in another, which is exactly what separated our Joan García from
// Barcelona's Eric García — one step apart on the line, and unmistakably two
// different men.
func positionsContradict(a, b string) bool {
	rank := map[string]int{"GK": 0, "DEF": 1, "MID": 2, "ATT": 3}

	ga, gb := positionGroup(a), positionGroup(b)
	ra, okA := rank[ga]
	rb, okB := rank[gb]
	if !okA || !okB {
		// One side is unknown ("OTHER" or empty) and decides nothing.
		return false
	}
	if (ga == "GK") != (gb == "GK") {
		return true
	}
	diff := ra - rb
	if diff < 0 {
		diff = -diff
	}
	return diff >= 2
}

func positionGroup(position string) string {
	switch strings.ToUpper(strings.TrimSpace(position)) {
	case "ST", "LW", "RW", "CF", "FW", "FWD", "F", "ATT", "ATTACKER", "FORWARD", "STRIKER", "WINGER":
		return "ATT"
	case "AM", "CM", "DM", "MID", "M", "MF", "LM", "RM", "MIDFIELDER":
		return "MID"
	case "CB", "LB", "RB", "DEF", "D", "DF", "WB", "LWB", "RWB", "DEFENDER":
		return "DEF"
	case "GK", "G", "GOALKEEPER":
		return "GK"
	default:
		return "OTHER"
	}
}

func parseAPIFootballRating(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return value
}

// attachProfile copies the identity fields api-football returns alongside a
// player's statistics onto the snapshot. Age is kept as a birth date so it
// stops being wrong the day after a birthday, and the portrait travels as a
// CDN URL rather than an inlined image.
func attachProfile(snapshot *domain.PerformanceSnapshot, profile apiFootballPlayerProfile) {
	if photo := strings.TrimSpace(profile.Photo); photo != "" {
		snapshot.PhotoURL = photo
	}
	raw := strings.TrimSpace(profile.Birth.Date)
	if raw == "" {
		return
	}
	born, err := time.Parse("2006-01-02", raw)
	if err != nil {
		log.Printf("api-football: unparseable birth date %q for player %d", raw, snapshot.PlayerID)
		return
	}
	snapshot.BirthDate = &born
}

func per90(value, minutes float64) float64 {
	if minutes <= 0 {
		return 0
	}
	return value / minutes * 90
}

func positionGAMax(position string) float64 {
	return defaultPositionMetric(map[string]float64{
		"FWD": 1.25,
		"ST":  1.25,
		"LW":  1.05,
		"RW":  1.05,
		"MID": 0.75,
		"CM":  0.70,
		"DM":  0.45,
		"DEF": 0.35,
		"CB":  0.25,
		"LB":  0.35,
		"RB":  0.35,
		"GK":  0.05,
	}, strings.ToUpper(strings.TrimSpace(position)), 0.65)
}

func positionXGXAMax(position string) float64 {
	return defaultPositionMetric(map[string]float64{
		"FWD": 1.15,
		"ST":  1.15,
		"LW":  0.95,
		"RW":  0.95,
		"MID": 0.70,
		"CM":  0.65,
		"DM":  0.40,
		"DEF": 0.35,
		"CB":  0.20,
		"LB":  0.35,
		"RB":  0.35,
		"GK":  0.02,
	}, strings.ToUpper(strings.TrimSpace(position)), 0.55)
}

// playerSearchTerm picks a search query for api-football's `/players?search=`
// endpoint. Constraints from the API:
//   - must be ≥4 alphanumeric characters
//   - must be ASCII (diacritics are pre-normalized)
//
// Strategy: prefer the **last** word ≥4 chars (surname), since api-football
// indexes players by surname. "N'Golo Kanté" → "kante" finds him; picking
// "ngolo" (the longest word) does not. "Vinicius Jr" → "jr" is too short, so
// fall through to the longest word ≥4 — that gives "vinicius". Final
// fallback joins the parts so single-word names still work.
func playerSearchTerm(name string) string {
	terms := playerSearchTerms(name)
	if len(terms) == 0 {
		return asciiOnly(name)
	}
	return terms[0]
}

// playerSearchTerms returns every search term worth trying for a name, best
// guess first. One guess isn't enough because api-football's `search` matches
// the player's *display* name, and Spanish and Brazilian squads are full of
// players listed under a mononym: our roster says "Fermín López", the API
// calls him "Fermín", and his surname lives in a `lastname` field that search
// never looks at. Querying "lopez" against Barcelona returns nothing at all.
//
// So the surname guess stays first — it is right for most names — and the
// given name follows as a fallback. Callers try each in order and stop at the
// first that resolves, paying the extra request only when the first misses.
func playerSearchTerms(name string) []string {
	name = asciiOnly(name)
	parts := strings.Fields(name)
	if len(parts) == 0 {
		if name == "" {
			return nil
		}
		return []string{name}
	}

	var terms []string
	add := func(candidate string) {
		if len(candidate) < 4 {
			return
		}
		for _, existing := range terms {
			if existing == candidate {
				return
			}
		}
		terms = append(terms, candidate)
	}

	// (1) Last word — the surname for almost every player name we see.
	add(parts[len(parts)-1])
	// (2) First word — catches mononym listings like "Fermín" and "Pedri".
	add(parts[0])
	// (3) Longest word — e.g. "Vinicius Jr", where the surname slot is "jr".
	longest := ""
	for _, p := range parts {
		if len(p) > len(longest) {
			longest = p
		}
	}
	add(longest)
	// (4) Everything joined, so short single-word names still clear the API's
	//     four-character minimum.
	add(strings.Join(parts, ""))

	if len(terms) == 0 {
		return []string{parts[0]}
	}
	return terms
}

func normalizeFootballName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c", "š", "s", "ć", "c", "č", "c", "ž", "z",
		".", " ", "-", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(value)), " ")
}

type apiFootballTeamsResponse struct {
	Errors   json.RawMessage `json:"errors"`
	Response []struct {
		Team apiFootballTeam `json:"team"`
	} `json:"response"`
}

type apiFootballPlayersResponse struct {
	Errors   json.RawMessage          `json:"errors"`
	Response []apiFootballPlayerEntry `json:"response"`
}

type apiFootballLeaguesResponse struct {
	Errors   json.RawMessage          `json:"errors"`
	Response []apiFootballLeaguesItem `json:"response"`
}

type apiFootballLeaguesItem struct {
	League  apiFootballLeagueRef `json:"league"`
	Seasons []apiFootballSeason  `json:"seasons"`
}

type apiFootballLeagueRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type apiFootballSeason struct {
	Year    int  `json:"year"`
	Current bool `json:"current"`
}

type apiFootballTeam struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	National bool   `json:"national"`
}

type apiFootballPlayerEntry struct {
	Player     apiFootballPlayerProfile `json:"player"`
	Statistics []apiFootballStatistic   `json:"statistics"`
}

type apiFootballPlayerProfile struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Firstname string `json:"firstname"`
	Lastname  string `json:"lastname"`
	Age       int    `json:"age"`
	Position  string `json:"position"`
	// Birth date beats age: an age is only true until the player's next
	// birthday, and ours were seeded once in May and have drifted since.
	Birth apiFootballBirth `json:"birth"`
	// Portrait on api-football's CDN. We store the URL rather than the image
	// so /api/players stays small — see migration 015.
	Photo string `json:"photo"`
}

type apiFootballBirth struct {
	Date string `json:"date"` // YYYY-MM-DD
}

type apiFootballStatistic struct {
	Team   apiFootballTeamRef   `json:"team"`
	League apiFootballLeagueRef `json:"league"`
	Games  apiFootballGames     `json:"games"`
	Shots  apiFootballShots     `json:"shots"`
	Goals  apiFootballGoals     `json:"goals"`
	Passes apiFootballPasses    `json:"passes"`
}

type apiFootballTeamRef struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	National bool   `json:"national"`
}

type apiFootballGames struct {
	// `appearences` is the spelling returned by api-football — keep it as-is.
	Appearances int    `json:"appearences"`
	Minutes     int    `json:"minutes"`
	Rating      string `json:"rating"`
	// Position lives here, not on the player object: api-football returns
	// player.position as null. We compared the empty one for months, which is
	// how "J. García" (our goalkeeper) was matched to Eric García, a defender
	// — the single signal that would have caught it was never populated.
	Position string `json:"position"`
}

type apiFootballShots struct {
	On int `json:"on"`
}

type apiFootballGoals struct {
	Total   int `json:"total"`
	Assists int `json:"assists"`
}

type apiFootballPasses struct {
	Key int `json:"key"`
}

type apiFootballFixturesResponse struct {
	Errors   json.RawMessage           `json:"errors"`
	Response []apiFootballFixturesItem `json:"response"`
}

type apiFootballFixturesItem struct {
	Fixture apiFootballFixtureRef `json:"fixture"`
}

type apiFootballFixtureRef struct {
	ID int `json:"id"`
}

type apiFootballFixturePlayersResponse struct {
	Errors   json.RawMessage                 `json:"errors"`
	Response []apiFootballFixtureTeamPlayers `json:"response"`
}

type apiFootballFixtureTeamPlayers struct {
	Team    apiFootballTeamRef              `json:"team"`
	Players []apiFootballFixturePlayerEntry `json:"players"`
}

type apiFootballFixturePlayerEntry struct {
	Player     apiFootballPlayerProfile `json:"player"`
	Statistics []apiFootballStatistic   `json:"statistics"`
}

func hasAPIFootballErrors(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "[]" && trimmed != "{}" && trimmed != "null"
}

// ────────────────────────────────────────────────────────────────────────
//  Phase 4.2 — career baseline via api-football historical data
// ────────────────────────────────────────────────────────────────────────

// FetchCareerBaseline aggregates a player's stats over the last `lookback`
// completed seasons. The provider reuses the parent provider's external-ID
// mapping (so we don't pay a /players?search= call per season), HTTP client,
// and API key.
//
// Returns a zero-value baseline (SeasonsPlayed=0) on "no data found" — the
// caller treats that as "skip this player" rather than an error, since a new
// player legitimately has no career data on file.
func (p *apiFootballPerformanceProvider) FetchCareerBaseline(ctx context.Context, player domain.PlayerSyncTarget, lookback int) (domain.PlayerCareerBaseline, error) {
	if lookback <= 0 {
		lookback = careerBaselineDefaultLookback
	}

	// We need the api-football player ID to query historical seasons. Without
	// a mapping we'd have to do a text-search per season (5x cost), so we
	// just skip players whose mapping isn't built yet — the live performance
	// sync will populate it on its next run.
	if p.store == nil {
		return domain.PlayerCareerBaseline{}, fmt.Errorf("career-baseline: no external-id store configured")
	}
	ids, err := p.store.GetExternalIDs(ctx, player.ID, apiFootballProviderName)
	if err != nil {
		return domain.PlayerCareerBaseline{}, fmt.Errorf("career-baseline: external-id lookup: %w", err)
	}
	if ids == nil || strings.TrimSpace(ids.ExternalID) == "" {
		return domain.PlayerCareerBaseline{}, nil // not mapped yet
	}
	externalID, parseErr := strconv.Atoi(strings.TrimSpace(ids.ExternalID))
	if parseErr != nil {
		return domain.PlayerCareerBaseline{}, fmt.Errorf("career-baseline: bad external_id %q: %w", ids.ExternalID, parseErr)
	}

	// Skip the current season — that's already the live performance signal.
	// We want PAST seasons only to anchor a long-term baseline.
	currentSeason := defaultCurrentSeason()
	startSeason := currentSeason - 1
	endSeason := currentSeason - lookback // inclusive

	agg := domain.PlayerCareerBaseline{
		SeasonsLookback: lookback,
	}

	// Minute-weighted rating aggregation: sum(rating × minutes_in_season)
	// then divide by total_minutes at the end. Avoids the bias from short
	// seasons (10-game cameo with 9.0 rating shouldn't outweigh a full
	// 38-match season at 7.5).
	weightedRatingSum := 0.0
	totalMinutes := 0

	for season := startSeason; season >= endSeason; season-- {
		params := url.Values{
			"id":     []string{strconv.Itoa(externalID)},
			"season": []string{strconv.Itoa(season)},
		}
		var resp apiFootballPlayersResponse
		if err := p.get(ctx, "/players", params, &resp); err != nil {
			// One bad season shouldn't kill the whole baseline — log and continue.
			log.Printf("career-baseline: season %d fetch failed for player %d: %v", season, player.ID, err)
			continue
		}
		if hasAPIFootballErrors(resp.Errors) || len(resp.Response) == 0 {
			continue
		}

		// A player can have multiple statistics rows per season (loan + parent
		// club, transfer mid-season, national team). We want the SUM of club
		// stats — exclude national-team entries to avoid double-counting
		// minutes (a player who played all club + every international has
		// crazy inflated totals otherwise).
		seasonMinutes := 0
		seasonAppearances := 0
		seasonGoals := 0
		seasonAssists := 0
		seasonWeightedRating := 0.0
		seasonRatingDenominator := 0

		for _, stat := range resp.Response[0].Statistics {
			if stat.Team.National {
				continue
			}
			if stat.Games.Minutes <= 0 {
				continue
			}
			seasonMinutes += stat.Games.Minutes
			seasonAppearances += stat.Games.Appearances
			seasonGoals += stat.Goals.Total
			seasonAssists += stat.Goals.Assists

			if rating := parseAPIFootballRating(stat.Games.Rating); rating > 0 {
				seasonWeightedRating += rating * float64(stat.Games.Minutes)
				seasonRatingDenominator += stat.Games.Minutes
			}
		}

		if seasonMinutes == 0 {
			continue // player didn't play at the club level this season
		}

		agg.SeasonsPlayed++
		agg.CareerAppearances += seasonAppearances
		agg.CareerMinutes += seasonMinutes
		agg.CareerGoals += seasonGoals
		agg.CareerAssists += seasonAssists

		// Roll the season into the running weighted-rating sum. We only use
		// minutes that actually had a rating attached (some lower-tier
		// fixtures don't have one); falling back to all minutes would
		// underweight ratings for full-rated seasons.
		if seasonRatingDenominator > 0 {
			weightedRatingSum += seasonWeightedRating
			totalMinutes += seasonRatingDenominator
		}
	}

	if agg.SeasonsPlayed == 0 {
		// No usable historical data — emit a zero-value baseline so the
		// caller leaves any existing row untouched.
		return domain.PlayerCareerBaseline{}, nil
	}

	if totalMinutes > 0 {
		agg.CareerAvgRating = round1(weightedRatingSum / float64(totalMinutes))
	}

	// Trophies: best-effort. The Pro plan exposes /trophies?player={id}; the
	// older free plan doesn't. We just count entries with a tier of "Winner".
	trophiesAvailable := false
	trophiesCount, trophyErr := p.fetchTrophyCount(ctx, externalID)
	if trophyErr == nil {
		agg.CareerTrophiesCount = trophiesCount
		trophiesAvailable = true
	} else {
		log.Printf("career-baseline: trophy fetch failed for player %d: %v — re-normalizing without trophy weight", player.ID, trophyErr)
	}

	agg.BaselineScore = computeBaselineScore(agg, player.Position, trophiesAvailable)
	return agg, nil
}

// fetchTrophyCount calls /trophies?player={id} and counts entries marked as
// won. Pro plan only; gracefully fails on free plan ("not subscribed" comes
// back as a 200 with `errors`).
func (p *apiFootballPerformanceProvider) fetchTrophyCount(ctx context.Context, externalID int) (int, error) {
	params := url.Values{"player": []string{strconv.Itoa(externalID)}}
	var resp apiFootballTrophiesResponse
	if err := p.get(ctx, "/trophies", params, &resp); err != nil {
		return 0, err
	}
	if hasAPIFootballErrors(resp.Errors) {
		return 0, fmt.Errorf("api-football /trophies returned errors")
	}
	won := 0
	for _, t := range resp.Response {
		// Place == "1st" or Place == "Winner" both mean "won". The API isn't
		// totally consistent — older entries use the localized place,
		// newer ones use "Winner". Accept both.
		switch strings.ToLower(strings.TrimSpace(t.Place)) {
		case "winner", "1st", "champion", "champions":
			won++
		}
	}
	return won, nil
}

type apiFootballTrophiesResponse struct {
	Errors   json.RawMessage          `json:"errors"`
	Response []apiFootballTrophyEntry `json:"response"`
}

type apiFootballTrophyEntry struct {
	League  string `json:"league"`
	Country string `json:"country"`
	Season  string `json:"season"`
	Place   string `json:"place"`
}

// ResolvePlayer identifies one footballer at one club, for the add-player
// flow. It is deliberately stricter than the sync path: a sync that guesses
// wrong self-heals on the next run, but a bad identity captured here is
// written into the roster and pinned as an external id, so it persists.
//
// When the name picks out several people it returns them all rather than
// choosing. Choosing is what bound our goalkeeper to Barcelona's defender.
func (p *apiFootballPerformanceProvider) ResolvePlayer(ctx context.Context, name, club string, providerPlayerID int, position string) (domain.ResolvedPlayer, error) {
	team, err := p.findTeam(ctx, club)
	if err != nil {
		return domain.ResolvedPlayer{}, fmt.Errorf("%w: %s", ErrClubUnknown, club)
	}
	info := p.currentSeasonForTeam(ctx, team.ID)

	// Pinned by id: fetch that player directly and confirm they are at this
	// club, so a mistyped id can't attach someone from another squad.
	if providerPlayerID > 0 {
		entry, err := p.fetchPlayerByID(ctx, providerPlayerID, info.Season)
		if err != nil {
			return domain.ResolvedPlayer{}, fmt.Errorf("%w: provider id %d", ErrPlayerNotFound, providerPlayerID)
		}
		if !playsForTeam(entry, team.ID) {
			return domain.ResolvedPlayer{}, fmt.Errorf("%w: provider id %d is not at %s", ErrPlayerNotFound, providerPlayerID, club)
		}
		return resolvedFromEntry(entry, team.ID), nil
	}

	seen := make(map[int]bool)
	candidates := make([]apiFootballPlayerEntry, 0, 4)
	for _, term := range playerSearchTerms(name) {
		params := url.Values{
			"team":   []string{strconv.Itoa(team.ID)},
			"season": []string{strconv.Itoa(info.Season)},
			"search": []string{term},
		}
		var response apiFootballPlayersResponse
		if err := p.get(ctx, "/players", params, &response); err != nil {
			return domain.ResolvedPlayer{}, err
		}
		if hasAPIFootballErrors(response.Errors) {
			return domain.ResolvedPlayer{}, fmt.Errorf("api-football players error: %s", string(response.Errors))
		}
		for _, item := range response.Response {
			if item.Player.ID == 0 || seen[item.Player.ID] {
				continue
			}
			// An explicitly requested position narrows the field, but only by
			// discarding people it plainly contradicts.
			if position != "" && positionsContradict(entryPosition(item), position) {
				continue
			}
			seen[item.Player.ID] = true
			candidates = append(candidates, item)
		}
		if len(candidates) > 0 {
			// The first term that finds anybody is the most specific one;
			// widening further only adds noise.
			break
		}
	}

	switch len(candidates) {
	case 0:
		return domain.ResolvedPlayer{}, fmt.Errorf("%w: %q at %s", ErrPlayerNotFound, name, club)
	case 1:
		return resolvedFromEntry(candidates[0], team.ID), nil
	}

	out := make([]domain.PlayerCandidate, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, domain.PlayerCandidate{
			ProviderPlayerID: item.Player.ID,
			Name:             item.Player.Name,
			FullName:         strings.TrimSpace(item.Player.Firstname + " " + item.Player.Lastname),
			Position:         entryPosition(item),
			Age:              item.Player.Age,
			PhotoURL:         item.Player.Photo,
		})
	}
	return domain.ResolvedPlayer{}, &AmbiguousPlayerError{Candidates: out}
}

// entryPosition digs the position out of the statistics, where api-football
// actually puts it — player.position is null in every response we have seen.
func entryPosition(entry apiFootballPlayerEntry) string {
	for _, stat := range entry.Statistics {
		if pos := strings.TrimSpace(stat.Games.Position); pos != "" {
			return pos
		}
	}
	return strings.TrimSpace(entry.Player.Position)
}

func playsForTeam(entry apiFootballPlayerEntry, teamID int) bool {
	for _, stat := range entry.Statistics {
		if stat.Team.ID == teamID {
			return true
		}
	}
	return false
}

func resolvedFromEntry(entry apiFootballPlayerEntry, teamID int) domain.ResolvedPlayer {
	resolved := domain.ResolvedPlayer{
		ProviderPlayerID: entry.Player.ID,
		ProviderTeamID:   teamID,
		Name:             entry.Player.Name,
		Position:         entryPosition(entry),
		PhotoURL:         strings.TrimSpace(entry.Player.Photo),
		Age:              entry.Player.Age,
	}
	if raw := strings.TrimSpace(entry.Player.Birth.Date); raw != "" {
		if born, err := time.Parse("2006-01-02", raw); err == nil {
			resolved.BirthDate = &born
			// Prefer the age we can compute over the one the provider states:
			// a stored age is only true until the next birthday.
			if derived := domain.AgeFromBirthDate(&born, time.Now().UTC()); derived > 0 {
				resolved.Age = derived
			}
		}
	}
	return resolved
}
