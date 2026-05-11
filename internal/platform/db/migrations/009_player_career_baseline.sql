-- Phase 4.2: persistent career baseline per player.
--
-- The current Performance score sees only this season — so a star with an
-- injury season ranks below a mid-tier player on a hot streak. We blend in
-- this baseline (40%) with the current season (60%) to anchor stars.
--
-- Refreshed monthly via the career_baseline sync (career stats don't change
-- fast). One row per player; ON CONFLICT … DO UPDATE keeps it idempotent.

CREATE TABLE IF NOT EXISTS player_career_baseline (
    player_id BIGINT PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    -- Number of seasons we actually got data for (may be < seasons_lookback
    -- if the player is younger or API didn't return that year).
    seasons_played INT NOT NULL DEFAULT 0,
    -- How far back we tried to look (5 by default). Stored so we can detect
    -- old rows when we change the policy.
    seasons_lookback INT NOT NULL DEFAULT 5,
    career_appearances INT NOT NULL DEFAULT 0,
    career_minutes INT NOT NULL DEFAULT 0,
    career_goals INT NOT NULL DEFAULT 0,
    career_assists INT NOT NULL DEFAULT 0,
    -- Minute-weighted mean rating across all seasons (rating × minutes /
    -- total_minutes). Better than naive mean because partial seasons get
    -- less weight than full ones.
    career_avg_rating DOUBLE PRECISION NOT NULL DEFAULT 0,
    -- Optional. We try /trophies?player={id} but accept "no data" silently.
    career_trophies_count INT NOT NULL DEFAULT 0,
    -- The 0–100 score blended into Performance. Pre-computed so the
    -- performance sync doesn't have to recompute on every call.
    baseline_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_career_baseline_computed
    ON player_career_baseline(computed_at);
