-- Phase 5: per-event fan voting.
--
-- Every detected rating event now travels through one of two paths:
--
--   auto_apply=true   → proposed_delta becomes final_delta at insert time;
--                       the score updates immediately. Used for "obvious"
--                       triggers like doping, racism, Ballon d'Or — fans
--                       can't override these.
--
--   auto_apply=false  → voting_status='pending_vote'; voting_closes_at
--                       set to NOW()+24h; fans submit suggested_delta via
--                       POST /api/events/{id}/vote. Hourly cron picks up
--                       expired events, takes the median of votes (or
--                       proposed_delta if 0 votes), writes final_delta,
--                       moves status to 'finalized', updates the player
--                       score.
--
-- Score updates throughout the system now read `final_delta` (or fall back
-- to `delta` for legacy rows during the transition window — see
-- sumCharacterEventsForPlayer in repository.go).

ALTER TABLE character_events
    ADD COLUMN IF NOT EXISTS proposed_delta DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS final_delta DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS voting_status TEXT NOT NULL DEFAULT 'auto_applied',
    ADD COLUMN IF NOT EXISTS voting_closes_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS auto_apply BOOLEAN NOT NULL DEFAULT TRUE;

-- Backfill existing rows: under the pre-Phase-5 model every event was
-- effectively auto-applied with the raw delta. Lock them into that state
-- so the new code reading final_delta still sees the historical value.
UPDATE character_events
SET proposed_delta = delta,
    final_delta    = delta,
    voting_status  = 'auto_applied',
    auto_apply     = TRUE
WHERE proposed_delta IS NULL;

-- Index the pending queue so the hourly finalize cron is O(matching-rows).
CREATE INDEX IF NOT EXISTS idx_character_events_voting_pending
    ON character_events(voting_status, voting_closes_at)
    WHERE voting_status = 'pending_vote';

-- One vote per (event, IP). suggested_delta is the slider value the fan
-- submitted — clamped to [-5, +5] at the API layer to keep griefers from
-- distorting medians.
CREATE TABLE IF NOT EXISTS event_votes (
    id BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES character_events(id) ON DELETE CASCADE,
    ip_hash TEXT NOT NULL,
    suggested_delta DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (event_id, ip_hash)
);

CREATE INDEX IF NOT EXISTS idx_event_votes_event ON event_votes(event_id);
