package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// competition is a tournament we count toward Performance and how much a
// minute in it is worth relative to a minute in a big domestic league.
type competition struct {
	name   string
	weight float64
}

// competitionWeights is the allowlist. Keyed by api-football league id:
// stat.League.Type cannot stand in, because it says "Cup" for the Champions
// League and the Audi Cup alike.
//
// Until 2026-09 Performance read one row — the domestic league — and threw
// the rest away. Álvarez's ten Champions League goals in fifteen games at a
// 7.55 rating counted for nothing; his 54 was an honest score for his La
// Liga season and no score at all for his season.
//
// The weights are deliberately small. The Champions League's league phase
// fields opposition about 44 Elo above an Atlético's La Liga schedule,
// roughly six points of expected result; nothing published supports more
// than ~1.15, and the same club plays Europe with its strongest eleven,
// which pulls the other way. The order below the UCL follows UEFA's own
// coefficient bonuses. Domestic cups sit lowest: lower-division opposition
// inflates output. Domestic leagues all sit at 1.00 — the two best public
// strength estimates disagree on their order, and an effect smaller than
// the method noise is not worth encoding.
//
// National teams count. A reputation index that ignored the World Cup
// would be measuring the wrong thing; every competitive international
// match is in at 1.00, with no attempt to rank a World Cup semi-final
// against a qualifier — the domestic league mixes top and bottom too.
// Rows are taken from the same api-football season as the club rows being
// scored, so a summer tournament joins the season that follows it.
//
// One-match competitions — super cups, the Intercontinental Cup — are
// listed rather than excluded: the credibility ramp already gives ninety
// minutes no say in the rating, and listing them keeps "everything counts"
// true without exceptions to explain. Friendlies are the exception: not
// competitive, never counted. Anything not listed is not counted either —
// the sync fails closed and says so once in the log, so a new competition
// (or a youth one) gets noticed instead of silently scored.
var competitionWeights = map[int]competition{
	39:  {"Premier League", 1.00},
	140: {"La Liga", 1.00},
	135: {"Serie A", 1.00},
	78:  {"Bundesliga", 1.00},
	61:  {"Ligue 1", 1.00},
	203: {"Süper Lig", 1.00},

	2:   {"Champions League", 1.10},
	3:   {"Europa League", 0.95},
	848: {"Conference League", 0.85},

	45:  {"FA Cup", 0.85},
	48:  {"League Cup", 0.85},
	143: {"Copa del Rey", 0.85},
	137: {"Coppa Italia", 0.85},
	81:  {"DFB Pokal", 0.85},
	66:  {"Coupe de France", 0.85},
	206: {"Türkiye Kupası", 0.85},

	// One-off club competitions. Sample size is the ramp's problem.
	15:   {"Club World Cup", 1.00},
	1168: {"Intercontinental Cup", 1.00},
	531:  {"UEFA Super Cup", 1.00},
	556:  {"Supercopa de España", 1.00},
	547:  {"Supercoppa Italiana", 1.00},
	526:  {"Trophée des Champions", 1.00},
	528:  {"Community Shield", 1.00},
	529:  {"DFL-Supercup", 1.00},

	// National teams — competitive matches only.
	1:   {"World Cup", 1.00},
	4:   {"Euro Championship", 1.00},
	5:   {"Nations League", 1.00},
	6:   {"Africa Cup of Nations", 1.00},
	7:   {"Asian Cup", 1.00},
	9:   {"Copa América", 1.00},
	22:  {"Gold Cup", 1.00},
	29:  {"World Cup qualification (Africa)", 1.00},
	30:  {"World Cup qualification (Asia)", 1.00},
	31:  {"World Cup qualification (CONCACAF)", 1.00},
	32:  {"World Cup qualification (Europe)", 1.00},
	33:  {"World Cup qualification (Oceania)", 1.00},
	34:  {"World Cup qualification (South America)", 1.00},
	36:  {"AFCON qualification", 1.00},
	37:  {"World Cup play-offs", 1.00},
	960: {"Euro qualification", 1.00},
}

// domesticSeasonMinutes is one full league season, 38 matches of 90. It was
// the only availability yardstick before competitions pooled, and it is
// still the floor when the fixture lookup fails.
const domesticSeasonMinutes = 3420.0

// credibility ramps a competition's say in the rating and the per-90 rates
// from nothing at three full matches to everything at seven — a complete
// Champions League league phase. Below the floor the minutes still count
// as playing time, but the numbers attached to them are not yet worth
// listening to: 200 continental minutes at 8.5 must not lift anyone, and a
// cliff at 199 vs 201 would be an invitation to game. Between the two the
// sample is discounted twice, once by its minutes and once here, on
// purpose: small samples should shrink harder than their size alone says.
func credibility(minutes int) float64 {
	k := (float64(minutes) - 270) / 360
	if k < 0 {
		return 0
	}
	if k > 1 {
		return 1
	}
	return k
}

// competitionContribution is one counted row, kept for the log and for
// working out how many fixtures were available.
type competitionContribution struct {
	LeagueID    int
	Name        string
	TeamID      int
	Minutes     int
	Appearances int
	Rating      float64
	Weight      float64
	Credibility float64
}

// pooledStats is a player's season across every counted competition.
type pooledStats struct {
	// Availability: every counted minute, including rows below the
	// credibility floor. Playing time is playing time.
	RawMinutes     int
	RawAppearances int
	// Measures: minutes-weighted across competitions. Rating is 0 when no
	// counted row carried one.
	Rating            float64
	GoalsAssistsPer90 float64
	KeyPassesPer90    float64
	ShotsOnPer90      float64
	Competitions      []competitionContribution
}

// poolClubStatistics folds api-football's per-competition rows into one
// season. Each row's effective minutes are minutes × competition weight ×
// credibility, and every measure is the effective-minutes-weighted mean of
// the per-row value — a weighted mean of RATES, never weighted counts over
// weighted minutes. That is UEFA's own rule: a competition's weight changes
// how much it counts, not how good the output inside it looks.
//
// No team filter: after a mid-season move both clubs' rows pool at their
// competition weights, a national team is just another row, and
// api-football already splits rows per (team, league), so nothing is
// double-counted.
func poolClubStatistics(statistics []apiFootballStatistic) pooledStats {
	type sums struct{ rating, ratingDen, ga, kp, so, den float64 }
	var out pooledStats
	var gated, ungated sums

	for _, stat := range statistics {
		if stat.Games.Minutes <= 0 {
			continue
		}
		comp, ok := competitionWeights[stat.League.ID]
		if !ok {
			noteUncountedCompetition(stat.League)
			continue
		}
		minutes := float64(stat.Games.Minutes)
		k := credibility(stat.Games.Minutes)
		rating := parseAPIFootballRating(stat.Games.Rating)

		out.RawMinutes += stat.Games.Minutes
		out.RawAppearances += stat.Games.Appearances
		out.Competitions = append(out.Competitions, competitionContribution{
			LeagueID: stat.League.ID, Name: comp.name, TeamID: stat.Team.ID,
			Minutes: stat.Games.Minutes, Appearances: stat.Games.Appearances,
			Rating: rating, Weight: comp.weight, Credibility: k,
		})

		ga := per90(float64(stat.Goals.Total+stat.Goals.Assists), minutes)
		kp := per90(float64(stat.Passes.Key), minutes)
		so := per90(float64(stat.Shots.On), minutes)
		for _, s := range []struct {
			acc *sums
			eff float64
		}{{&gated, minutes * comp.weight * k}, {&ungated, minutes * comp.weight}} {
			if s.eff <= 0 {
				continue
			}
			s.acc.den += s.eff
			s.acc.ga += ga * s.eff
			s.acc.kp += kp * s.eff
			s.acc.so += so * s.eff
			if rating > 0 {
				s.acc.rating += rating * s.eff
				s.acc.ratingDen += s.eff
			}
		}
	}

	// Every row below the credibility floor — a squad player in September —
	// leaves the gated sums empty. Plain minutes-weighting is then the best
	// available reading, and it is what the single-row formula did anyway.
	use := gated
	if use.den == 0 {
		use = ungated
	}
	if use.den > 0 {
		out.GoalsAssistsPer90 = use.ga / use.den
		out.KeyPassesPer90 = use.kp / use.den
		out.ShotsOnPer90 = use.so / use.den
	}
	if use.ratingDen > 0 {
		out.Rating = use.rating / use.ratingDen
	}
	return out
}

// poolFromAnchor measures a player from one row, allowlist or not. It is the
// pre-pooling behaviour, kept for rows that carry no league id (older
// fixtures, the demo path) or a club whose competitions we do not count.
func poolFromAnchor(stat apiFootballStatistic) pooledStats {
	minutes := float64(stat.Games.Minutes)
	return pooledStats{
		RawMinutes:        stat.Games.Minutes,
		RawAppearances:    stat.Games.Appearances,
		Rating:            parseAPIFootballRating(stat.Games.Rating),
		GoalsAssistsPer90: per90(float64(stat.Goals.Total+stat.Goals.Assists), minutes),
		KeyPassesPer90:    per90(float64(stat.Passes.Key), minutes),
		ShotsOnPer90:      per90(float64(stat.Shots.On), minutes),
	}
}

var uncountedCompetitions sync.Map

// noteUncountedCompetition logs a competition we are skipping, once per
// process. Rows with no league id at all are silent: they are how the
// fallback and demo paths look, not something to act on.
func noteUncountedCompetition(league apiFootballLeagueRef) {
	if league.ID <= 0 {
		return
	}
	if _, seen := uncountedCompetitions.LoadOrStore(league.ID, struct{}{}); !seen {
		log.Printf("api-football: competition %d (%s) is not in the weight table — not counted toward Performance", league.ID, league.Name)
	}
}

type fixturesCacheEntry struct {
	count   int
	expires time.Time
}

// playedFixturesCached is playedFixtures behind a six-hour cache keyed by
// (team, season, league). Pooling asks once per competition per player, and
// teammates share the answer.
func (p *apiFootballPerformanceProvider) playedFixturesCached(ctx context.Context, teamID, season, leagueID int) int {
	key := fmt.Sprintf("%d/%d/%d", teamID, season, leagueID)
	now := time.Now()
	p.fixturesMu.Lock()
	if entry, ok := p.fixturesByKey[key]; ok && now.Before(entry.expires) {
		p.fixturesMu.Unlock()
		return entry.count
	}
	p.fixturesMu.Unlock()

	count := p.playedFixtures(ctx, teamID, season, leagueID)
	if count >= 0 {
		p.fixturesMu.Lock()
		p.fixturesByKey[key] = fixturesCacheEntry{count: count, expires: now.Add(topNCacheTTL)}
		p.fixturesMu.Unlock()
	}
	return count
}

// fixtureMinutesFor is the playing time that was available to this player:
// ninety minutes for every finished fixture the club played in each
// competition being counted.
//
// Minutes used to be judged against a fixed domestic season, and that is
// exactly where pooling would have gone wrong: pooled into 3,420, Álvarez
// reads 92% availability — rewarded for playing more competitions rather
// than more of them — while a teammate with no European football is capped
// at his league. Against the fixtures actually played he reads 66%: more
// than his league alone, because his workload genuinely was larger.
//
// Any failed lookup falls back to the domestic-season figure and never
// lower, so an API hiccup cannot inflate anyone's share.
func (p *apiFootballPerformanceProvider) fixtureMinutesFor(ctx context.Context, pooled pooledStats, season int) float64 {
	total := 0
	for _, c := range pooled.Competitions {
		if c.TeamID <= 0 || c.LeagueID <= 0 {
			return domesticSeasonMinutes
		}
		played := p.playedFixturesCached(ctx, c.TeamID, season, c.LeagueID)
		if played < 0 {
			return domesticSeasonMinutes
		}
		total += played
	}
	if total <= 0 {
		return domesticSeasonMinutes
	}
	return float64(total * 90)
}

// logPooledCompetitions writes one line per player so a score can be read
// back to the competitions behind it.
func logPooledCompetitions(player domain.PlayerSyncTarget, pooled pooledStats, fixtureMinutes float64) {
	if len(pooled.Competitions) == 0 {
		return
	}
	parts := make([]string, 0, len(pooled.Competitions))
	for _, c := range pooled.Competitions {
		parts = append(parts, fmt.Sprintf("%s %d′×%.2f k=%.1f", c.Name, c.Minutes, c.Weight, c.Credibility))
	}
	log.Printf("api-football: %s pooled: %s → rating %.2f, availability %d/%.0f",
		player.Name, strings.Join(parts, " · "), pooled.Rating, pooled.RawMinutes, fixtureMinutes)
}
