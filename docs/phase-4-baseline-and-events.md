# Phase 4 — Career baseline + Performance event detection

**Status:** in development
**Owner:** evklidus
**Partner ask (2026-05-09):**

> Хотелось бы, чтоб «база» (изначальный рейтинг) от которой дальше будут расти/падать рейтинг была взята за все время. Если это трудно реализовать, то хотя бы аспект Performance только был. Также надо чтоб всё-таки детектились события по части Performance (они часто упоминаются в медиа, типа играл уже 5 матчей не забивает и т.д.). Social я так понимаю мы подключим позже.

---

## Goals

1. **Career baseline** anchors a star's Performance score using their all-time stats, not just current season.
   - Messi during an injury season ≠ Messi during a bad season ≠ a midcard player.
   - Currently the model only sees the current season, which produces counter-intuitive ranks.
2. **Performance events** capture concrete moments that the press talks about — droughts, hat-tricks, awards.
   - Currently Performance is a continuous stat snapshot. It misses things humans notice.
3. **Social** is deferred to a later phase. Without paid Instagram/Twitter API access (~$250/mo), reliable signals don't exist.

---

## Current model (for reference)

```
Performance (35%) = weighted blend of:
    avg_rating  × 0.30
    goals+assists/90  × 0.18
    xG+xA/90  × 0.18
    position_rank_in_league  × 0.14
    minutes_share  × 0.10
    last5_form  × 0.10

Social (20%) = mostly placeholders + 10% YouTube views
Fan Poll (20%) = real votes, 24h-IP rate-limited
Media (15%) = sentiment-weighted MediaStack article scoring
Character (10%) = keyword-scan of news_items, capped ±15 per sync

Final FRI = 0.35·Perf + 0.20·Social + 0.20·Fan + 0.15·Media + 0.10·Char
```

All component values are **replaced** each sync (snapshot), then `applyFriFormula` recomputes FRI. There is no "baseline + delta" accumulation today.

---

## Plan — three sub-phases

### 4.1 — Performance event triggers (1 day, lowest risk)

Extend the existing `character_sync` keyword scanner to detect performance-flavoured news and route the delta to the **Performance** score instead of Character.

**Schema (`migration 008_target_component.sql`):**

```sql
ALTER TABLE character_events
    ADD COLUMN target_component TEXT NOT NULL DEFAULT 'character',
    ADD COLUMN source_ref TEXT;

CREATE INDEX idx_character_events_target_component
    ON character_events(target_component);

-- Idempotency guard for events without a news_item_id (e.g. fixture-derived).
CREATE UNIQUE INDEX idx_character_events_source_ref
    ON character_events(player_id, trigger_word, source_ref)
    WHERE source_ref IS NOT NULL;
```

We keep the table name `character_events` for now (a rename would require touching tests, repository methods, and history queries — not worth it for one column). When phase 5 lands and we generalise to fan-validated events, we can rename to `rating_events`.

**Code changes:**

- `characterTrigger` struct gains a `targetComponent string` field.
- New triggers tagged `targetComponent: "performance"`:

  | Concept | Delta | Words (en + ru) |
  |---|---:|---|
  | `hat_trick` | +2.0 | hat-trick, хет-трик |
  | `brace` | +1.0 | brace, дубль, two goals in |
  | `goal_drought_5` | −1.5 | scoring drought, five games without scoring, 5 матчей без гола |
  | `goal_drought_10` | −3.0 | ten games without scoring, 10 матчей без гола |
  | `player_of_month` | +3.0 | player of the month, игрок месяца |
  | `player_of_year` | +5.0 | player of the year, игрок года |
  | `ballon_dor` | +8.0 | ballon d'or, золотой мяч |
  | `goal_of_season` | +2.5 | goal of the season, гол сезона |
  | `trophy_won` | +4.0 | won the champions league, league title clinched |
  | `injury_serious` | −1.5 | long-term injury, season-ending injury |
  | `penalty_miss_key` | −1.0 | missed penalty in, missed crucial penalty |

- `ApplyCharacterSync` groups deltas by `(player_id, target_component)` instead of just player. Per-component cap of ±15. Applies to `fri_scores.character` or `fri_scores.performance` accordingly.
- Rename it to `ApplyRatingEventsSync` internally; keep a thin alias `ApplyCharacterSync` for backward compat with any external callers.

**Known limitation:** keyword detection has duplicate-event risk. If 5 different articles mention the same drought, we generate 5 events totaling −7.5. The per-sync cap (±15) limits the damage, but it's not ideal. Phase 4.3 (stats-based detector) solves this with stable `source_ref` fingerprints.

### 4.2 — Career baseline (2 days)

Compute a player-level "career score" from up to 5 past seasons of API-Football data, blend it with the current-season Performance.

**Schema (`migration 009_player_career_baseline.sql`):**

```sql
CREATE TABLE player_career_baseline (
    player_id BIGINT PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    seasons_played INT NOT NULL DEFAULT 0,
    seasons_lookback INT NOT NULL DEFAULT 5,
    career_appearances INT NOT NULL DEFAULT 0,
    career_minutes INT NOT NULL DEFAULT 0,
    career_goals INT NOT NULL DEFAULT 0,
    career_assists INT NOT NULL DEFAULT 0,
    career_avg_rating DOUBLE PRECISION NOT NULL DEFAULT 0,
    career_trophies_count INT NOT NULL DEFAULT 0,
    baseline_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Service (`internal/service/career_baseline_sync.go`):**

For each tracked player:

```
For season in [current-5 .. current-1]:        # skip current season; that's
    GET /players?id={apiFootballId}&season={s} # the live performance signal
    Aggregate: appearances, minutes, goals, assists, rating × minutes
GET /trophies?player={apiFootballId}            # if endpoint available
```

Aggregate:
- `career_appearances` = sum of appearances
- `career_minutes` = sum of minutes
- `career_goals` = sum of goals
- `career_assists` = sum of assists
- `career_avg_rating` = minute-weighted average rating (rating × minutes_in_season / total_minutes)

Compute `baseline_score ∈ [0, 100]`:

```
baseline = clamp(
    normalizeLinear(career_avg_rating, 6.0, 8.5)       × 0.45 +
    normalizeLinear(career_goals_per_90, 0, gaMax)     × 0.20 +
    normalizeLinear(career_assists_per_90, 0, gaMax)   × 0.15 +
    normalizeLog(career_minutes, 5_000, 50_000)        × 0.10 +
    normalizeLinear(career_trophies_count, 0, 10)      × 0.10
)
```

(Trophy detection is optional. If `/trophies` endpoint is unavailable or empty, we drop that 10% and re-normalize to 0.50+0.22+0.17+0.11.)

**Sync schedule:** `CAREER_BASELINE_SYNC_INTERVAL_MINUTES=43200` (30 days). The signal changes slowly — running it monthly keeps API-Football usage minimal.

**Cost estimate:** 22 players × 5 seasons = 110 requests per full refresh. The Pro plan allows 7,500/day, so this is 1.5% of the daily quota — comfortably under.

**Performance score blend:**

```go
finalPerformance := 0.6 * currentSeasonScore
if baseline.ComputedAt.IsValid() {
    finalPerformance += 0.4 * baseline.BaselineScore
}
```

If no baseline yet (new player, or pre-Phase-4.2 install), the current season score is used unblended.

**`api_football_performance.go` change:** after computing `normalizedScore` (current season), look up the baseline via repository and blend before returning.

### 4.3 — Stats-based event detector (3 days, fixture-driven)

Replace press-dependent keyword detection with deterministic fixture analysis. Same `character_events` table, same routing, but each event carries `source_ref = "fixture:{fixture_id}:{concept}"` so it's idempotent across re-runs.

**Service (`internal/service/performance_events_sync.go`):**

```
For each player:
    GET /fixtures?team={current_team_id}&season={current}&last=10
    For each fixture in date order:
        GET /fixtures/players?fixture={id}&team={team_id}
        Find this player's stats line
        Record: goals_scored, assists, minutes, position

State machine over the fixture window:
    consecutive_no_goal_streak = how many recent matches had goals=0
    consecutive_no_minutes_streak = how many recent matches the player was an unused sub

Event triggers:
    On 5th consecutive 0-goal match for an FWD/AM → emit goal_drought_5
    On 10th consecutive 0-goal match for an FWD/AM → emit goal_drought_10
    On 3+ goals in single match → emit hat_trick (also +1 for 2-goal)
    On 5+ consecutive matches as unused sub → emit benched_streak (−1)
```

Each emission is INSERTed via `INSERT … ON CONFLICT (player_id, trigger_word, source_ref) DO NOTHING`, so re-running the sync over the same fixture window is a no-op.

**Sync schedule:** every 6h, same as media (`PERFORMANCE_EVENTS_SYNC_INTERVAL_MINUTES=360`).

**Cost estimate:** 22 players × ~2 calls (fixtures list + last-fixture players-detail) = 44 calls per run, 8 runs/day = 352 calls/day. Combined with current `/players` and form-cache queries we'd be at ~400-500/day — still well under the 7,500 limit.

---

## Migration & rollout

1. Migrations 008 + 009 run on next deploy via `db.Migrate()` boot path.
2. New env vars added to `.env.prod.example` with sensible defaults — no manual edits needed.
3. Phase 4.1 ships in a single commit; works on existing news immediately.
4. Phase 4.2 requires the `career_baseline_sync` to run once to populate the table. Until then, Performance falls back to current-season-only — no breakage.
5. Phase 4.3 ships separately; can be feature-flagged off via `STATS_EVENTS_ENABLED=false` if it misfires.

## Out of scope for Phase 4

- Fan-validated per-event sliders (this is Phase 5)
- Career stats older than 5 seasons (API-Football data quality drops further back)
- Trophy detection beyond what `/trophies?player={id}` returns
- Award detection from non-press sources (would need official federation feeds)

## Acceptance criteria

- A player with 10+ year career and current-season injury keeps Performance ≥ 60.
- "5 матчей без гола" mention in a news headline reduces the player's Performance (visible in `/api/sync/updates` log).
- A re-run of `sync/performance-events` over the same fixtures produces zero new rows in `character_events`.
- `go test ./... -race` stays green.
