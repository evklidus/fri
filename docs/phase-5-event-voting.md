# Phase 5 — Per-event fan voting

**Status:** in development
**Owner:** evklidus
**Partner ask (2026-05-09):**

> Оставить четыре компонента (Field Performance, Social Influence, Media Score, Character Index) без фанатского голоса. Допустим по конкретному игроку будут изменения −1 за игру, +2 за положительный отклик в соц сетях, +4 за положительное упоминание в медиа, −3 за характер, например нагрубил тренеру во время перерыва. А потом уже выносить на обсуждение справедливо ли наказание/поощрение по баллам. Например ползунок стоит за игру −1, но фанат может перетянуть его и сделать либо ещё меньше, либо вообще в положительную сторону. Так мне кажется вовлечённость фанатов будет больше и рейтинг перестанет быть субъективным, а станет объективным. Допустим соберём данные по результатам фанатов и средние цифры применим как окончательное и финальное изменение. Понятно будут не все выносится на обсуждение, например очевидно плохое/положительное не будет учитывать мнение фанатов, а выставляться нами.

---

## Goals

1. **Drop "Fan Poll" as a 5th FRI component.** Fans no longer rate the player overall on 4 abstract sliders. The 20% they used to control is redistributed across the 4 remaining components.
2. **Every detected rating event now has a proposed delta and a final delta.** The algorithm proposes (e.g. red card → −1.5); the community votes on whether that's fair; the median of fan votes becomes the **final** delta that actually moves the score.
3. **Some events skip voting.** Obvious cases (doping, racism, Ballon d'Or win) are auto-applied at the proposed delta — no community override possible. We (FRI editorial) decide which triggers are obvious; this is hardcoded in the trigger table.
4. **24-hour voting window per event.** After that, the event is finalized (median or proposed if no votes) and the score is updated.
5. **Fan participation = real influence on real events**, not abstract sliders. Engagement story for investors: "we don't ask fans to rate a player, we ask them to validate a specific incident".

---

## Architecture

### Lifecycle of an event

```
                          DETECTED
                              │
                ┌─────────────┴─────────────┐
                │                           │
         autoApply=true              autoApply=false
                │                           │
        proposed_delta                proposed_delta
        immediately set               set; voting_status='pending'
        as final_delta                voting_closes_at = now+24h
                │                           │
        component score                fan votes accumulate
        updated NOW                    (one per IP per event)
                │                           │
                │                           ▼
                │                    finalize-events cron
                │                    (runs hourly)
                │                           │
                │                    voting_closes_at < now?
                │                           │
                │                    final_delta = median(votes)
                │                                    or proposed_delta if 0 votes
                │                           │
                │                    component score updated
                │                           │
                └───────────► EVENT FINALIZED ◄──────┘
```

### What's "obvious" — auto_apply classification

| Trigger | Delta | autoApply | Why |
|---|---:|---|---|
| `doping` | −8 | ✅ | Definitively wrong, no fan vote can excuse |
| `racism` | −6 | ✅ | Same |
| `criminal` | −7 | ✅ | Same |
| `ballon_dor` | +8 | ✅ | Official award, no override needed |
| `player_of_year` | +5 | ✅ | Same |
| `injury_serious` | −1.5 | ✅ | Not the player's fault but objective signal |
| `goal_drought_5_stats` | −1.0 | ✅ | Deterministic stats-derived |
| `red_card` | −1.5 | ❌ | May be unfair refereeing — community decides |
| `ban` | −2.5 | ❌ | Context-dependent |
| `fine` | −0.5 | ❌ | Often petty — fans may dismiss |
| `scandal` | −3 | ❌ | Severity varies wildly |
| `fair_play` | +1.5 | ❌ | Impact varies |
| `charity` | +0.8 | ❌ | Context |
| `captain` | +0.5 | ❌ | Minor |
| `hat_trick` | +2.0 | ❌ | Stakes vary (CL final vs friendly) |
| `brace` | +1.0 | ❌ | Same |
| `player_of_month` | +3.0 | ❌ | League quality matters |
| `goal_of_season` | +2.5 | ❌ | Subjective beauty |
| `trophy_won` | +4.0 | ❌ | Trophy weight varies (CL vs cup) |
| `penalty_miss_key` | −1.0 | ❌ | Pressure context |
| `goal_drought_5` (keyword) | −1.5 | ❌ | Press framing varies |
| `goal_drought_10` (keyword) | −3.0 | ❌ | Same |

### Schema

**Migration 012 — extend `character_events` + new `event_votes`:**

```sql
ALTER TABLE character_events
    ADD COLUMN proposed_delta DOUBLE PRECISION,
    ADD COLUMN final_delta DOUBLE PRECISION,
    ADD COLUMN voting_status TEXT NOT NULL DEFAULT 'auto_applied',
    ADD COLUMN voting_closes_at TIMESTAMPTZ,
    ADD COLUMN auto_apply BOOLEAN NOT NULL DEFAULT true;

-- voting_status:
--   'auto_applied'   — finalized at insert time, no voting
--   'pending_vote'   — accepting votes until voting_closes_at
--   'finalized'      — voting closed, final_delta locked in

-- Backfill: every existing event was applied immediately under the old
-- (no-voting) model. Treat them all as auto_applied with delta == final_delta.
UPDATE character_events
SET proposed_delta = delta,
    final_delta    = delta,
    voting_status  = 'auto_applied',
    auto_apply     = true
WHERE proposed_delta IS NULL;

CREATE INDEX idx_character_events_voting_pending
    ON character_events(voting_status, voting_closes_at)
    WHERE voting_status = 'pending_vote';

CREATE TABLE event_votes (
    id BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES character_events(id) ON DELETE CASCADE,
    ip_hash TEXT NOT NULL,
    suggested_delta DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (event_id, ip_hash)
);

CREATE INDEX idx_event_votes_event ON event_votes(event_id);
```

**Migration 013 — drop Fan from FRI formula:**

```sql
-- Recompute FRI without the Fan component. The 0.20 weight previously held
-- by Fan is redistributed proportionally to the four remaining components.
--
-- Old: P 0.35  S 0.20  Fan 0.20  M 0.15  C 0.10  (sum 1.00)
-- New: P 0.40  S 0.25  M 0.20  C 0.15           (sum 1.00)
-- Δ:   +0.05  +0.05   −0.20    +0.05  +0.05
UPDATE fri_scores
SET fri = ROUND(
    (performance * 0.40 +
     social      * 0.25 +
     media       * 0.20 +
     character   * 0.15
    )::numeric, 1),
    calculated_at = NOW();
-- fri_scores.fan column is kept for backward compat (read by the legacy
-- vote endpoint until that's removed), but contributes 0 to FRI now.
```

The Go `applyFriFormula` function is updated to match. `fri_scores.fan` stays as a column so we don't break any consumer reading it; it just doesn't participate in the FRI calculation.

### Voting math

When event finalizes:

```
votes = SELECT suggested_delta FROM event_votes WHERE event_id = $1
if len(votes) == 0:
    final_delta = proposed_delta
else:
    final_delta = median(votes)
```

We use **median**, not mean — median is robust to outliers (bot spam, troll votes at extremes).

### Apply semantics

After `final_delta` is set, the component score recomputes:

- **Character**: `clamp(80 + Σ(final_delta of all character events for this player), 0, 100)` — same as Phase 4 ext-A, but using `final_delta` instead of `delta`.
- **Performance**: additive — the previous Performance value + `final_delta`, clamped. The current Performance snapshot from API-Football still overwrites on each performance sync; events shift on top of that. (Same as current.)

### Vote constraints

- One vote per (event_id, ip_hash). Re-vote on the same event from the same IP → update existing row (the UI doesn't currently support re-voting, but the schema allows for it via DELETE+INSERT if we ever add it).
- Vote can only be submitted while `voting_status = 'pending_vote'` AND `voting_closes_at > now`. Otherwise rejected with HTTP 410 Gone.
- Suggested delta clamped to [−5, +5] to prevent griefing with extreme values that distort the median.

### API endpoints

```
GET /api/events/pending?player_id={id}
    → list of pending events for that player, with current vote count
       and median-so-far. Used to populate the Events Feed in the UI.

GET /api/events/{id}
    → event details: trigger, proposed_delta, current votes, time left.

POST /api/events/{id}/vote
    body: { "suggested_delta": -1.5 }
    headers: X-Forwarded-For (real IP, hashed server-side)
    → 201 if new vote, 200 if updated, 410 if voting closed, 429 if rate
       limit hit on the IP.
```

### Scheduler

New tick: `finalize-events` every hour. Picks up all `voting_status='pending_vote' AND voting_closes_at < now` rows, computes median, sets `final_delta` and `voting_status='finalized'`, then refreshes the affected players' component scores.

### Frontend

**Remove from UI:**
- Old Fan Poll widget (5 abstract sliders): "Rate Mbappé", star rating, opinion buttons, behavior buttons. All gone.
- Fan Poll column from leaderboard.

**Add to UI:**
- "Events Feed" section on each player's modal — shows recent finalized events with proposed vs final delta side-by-side ("FRI thought −1.5, fans voted −0.8")
- "Pending decisions" callout on player modal — shows events still accepting votes, with a slider to vote.
- Site-wide "Vote on today's events" page that lists all pending events across all players (encourages broader engagement).

### Out of scope for this phase

- Comment threads on events (just vote sliders for now).
- Different vote weighting by user reputation (everyone equal).
- Vote decay (old votes don't lose weight over time).
- Geolocation per vote (we keep IP hash but don't try to break down by country).

---

## Acceptance criteria

1. A new red-card event detected via news creates a row with `voting_status='pending_vote'`, `voting_closes_at` 24h ahead, `final_delta` NULL.
2. `POST /api/events/{id}/vote` with `suggested_delta=-0.5` from a fresh IP returns 201.
3. The same IP re-voting on the same event returns 200 (idempotent update).
4. A different event past `voting_closes_at` is processed by the cron: `final_delta` = median(votes), `voting_status='finalized'`.
5. A new player's Character starts at 80; after a single auto_applied doping event (−8), Character is 72.
6. FRI formula uses 4 components only — `fri_scores.fan` value is ignored.
7. `go test ./... -race` is green.
8. Old `/api/players/{id}/vote` endpoint returns HTTP 410 Gone with explanatory body.
