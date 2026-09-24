-- Remove a week of invented Performance scores.
--
-- The API-Football plan lapsed from Pro to Free on 2026-09-17. Free refuses
-- every current-season request, and the provider's fallback answered with
-- the demo generator instead: a rating, goal rate and league rank that were
-- all the same function of a hash, and a form score of zero for everyone.
-- Those were written as real snapshots and fed straight into FRI — Lamine
-- Yamal 47.6, Pedri 38.9, Messi 55.2 on Performance, all fiction.
--
-- The fallback now refuses instead (see fallbackSnapshot). This undoes what
-- it already wrote: every player whose newest snapshot is a fallback goes
-- back to their newest REAL snapshot, plus the performance events that
-- legitimately apply, and FRI follows. Players with no real snapshot at all
-- are left as they are — there is nothing truer to go back to.
WITH newest AS (
    SELECT DISTINCT ON (ps.player_id) ps.player_id, ps.provider
    FROM performance_snapshots ps
    ORDER BY ps.player_id, ps.snapshot_at DESC
), real_latest AS (
    SELECT DISTINCT ON (ps.player_id) ps.player_id, ps.normalized_score
    FROM performance_snapshots ps
    JOIN newest n ON n.player_id = ps.player_id AND n.provider = 'api-football-fallback'
    WHERE ps.provider = 'api-football'
    ORDER BY ps.player_id, ps.snapshot_at DESC
), events AS (
    SELECT r.player_id,
           COALESCE((
               SELECT SUM(COALESCE(ce.final_delta, ce.delta))
               FROM character_events ce
               WHERE ce.player_id = r.player_id
                 AND ce.target_component = 'performance'
                 AND ce.voting_status IN ('auto_applied', 'finalized')
           ), 0) AS delta_sum
    FROM real_latest r
)
UPDATE fri_scores s
SET performance = GREATEST(0, LEAST(100, ROUND((r.normalized_score + e.delta_sum)::numeric, 1))),
    fri = ROUND((GREATEST(0, LEAST(100, ROUND((r.normalized_score + e.delta_sum)::numeric, 1))) * 0.40
                 + s.social * 0.25 + s.media * 0.20 + s.character * 0.15)::numeric, 1),
    performance_updated_at = NOW(),
    calculated_at = NOW()
FROM real_latest r
JOIN events e ON e.player_id = r.player_id
WHERE s.player_id = r.player_id;

-- The invented snapshots themselves go, so no later calculation (the
-- finalize job reads the latest snapshot as its baseline) can pick one up.
DELETE FROM performance_snapshots WHERE provider = 'api-football-fallback';
