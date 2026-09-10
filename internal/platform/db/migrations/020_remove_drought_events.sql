-- Undo the goal-drought penalties.
--
-- Three separate charges were being made for one dry spell. buildFormScore
-- already normalises goals+assists per 90 across the last five matches
-- against the position's own ceiling, so a player with nothing in five
-- scores zero there — the full penalty the data supports, bounded and
-- self-clearing. The season rate carries the same fact again. On top of
-- that, a stats detector charged -1 per ISO week without limit, and two
-- keyword triggers charged -1.5 and -3.0 once per ARTICLE that used the
-- phrase.
--
-- N'Golo Kanté, a holding midfielder for whom a blank five-match run is
-- close to a coin flip, accumulated four of the weekly ones. They were
-- applied on 2026-09-10 when the finalize job was repaired, taking his
-- Performance from 56.7 to 52.7.
--
-- No published rating system deducts for a drought. Removing the rows here
-- so the sum that feeds Performance stops including them; the detectors
-- themselves are gone from the code in the same change.
-- The drought rows stay in place until the end, so every step can read the
-- list of affected players straight from them. An earlier draft parked that
-- list in a temp table and deleted first; the app runs a migration in one
-- transaction so it would have worked, but it broke the moment the same file
-- was replayed statement-by-statement, and a migration should not depend on
-- how it is fed to the database.
--
-- Nobody outside that list is touched. An earlier draft rebuilt Performance
-- for every player holding a snapshot, which would have quietly re-derived
-- scores this change has no business moving — including players whose sync
-- is currently skipping them, such as Salah mid-transfer.

-- Rebuild Performance for the affected players: their latest snapshot plus
-- the performance events that remain once droughts are excluded. Mirrors
-- the calculation in FinalizePendingEvents. A player with no snapshot is
-- left alone, there being nothing better to rebuild from.
WITH drought_players AS (
    SELECT DISTINCT player_id
    FROM character_events
    WHERE trigger_word IN ('goal_drought_5_stats', 'goal_drought_5', 'goal_drought_10')
),
latest AS (
    SELECT DISTINCT ON (ps.player_id) ps.player_id, ps.normalized_score
    FROM performance_snapshots ps
    JOIN drought_players d ON d.player_id = ps.player_id
    ORDER BY ps.player_id, ps.snapshot_at DESC
),
events AS (
    SELECT d.player_id,
           COALESCE((
               SELECT SUM(COALESCE(ce.final_delta, ce.delta))
               FROM character_events ce
               WHERE ce.player_id = d.player_id
                 AND ce.target_component = 'performance'
                 AND ce.voting_status IN ('auto_applied', 'finalized')
                 AND ce.trigger_word NOT IN ('goal_drought_5_stats', 'goal_drought_5', 'goal_drought_10')
           ), 0) AS delta_sum
    FROM drought_players d
)
UPDATE fri_scores s
SET performance = GREATEST(0, LEAST(100, ROUND((l.normalized_score + e.delta_sum)::numeric, 1))),
    performance_updated_at = NOW()
FROM latest l
JOIN events e ON e.player_id = l.player_id
WHERE s.player_id = l.player_id;

-- FRI follows Performance, for those same players only. Same weights as
-- applyFriFormula in Go.
UPDATE fri_scores
SET fri = ROUND(
        (performance * 0.40 +
         social      * 0.25 +
         media       * 0.20 +
         character   * 0.15
        )::numeric, 1),
    calculated_at = NOW()
WHERE player_id IN (
    SELECT DISTINCT player_id
    FROM character_events
    WHERE trigger_word IN ('goal_drought_5_stats', 'goal_drought_5', 'goal_drought_10')
);

-- Now the rows can go.
DELETE FROM character_events
WHERE trigger_word IN ('goal_drought_5_stats', 'goal_drought_5', 'goal_drought_10');
