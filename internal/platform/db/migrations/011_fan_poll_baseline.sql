-- Phase 4 ext-C: normalize Fan Poll to events-only model.
--
-- Same diagnosis as migration 010 was for Character: the seed import baked
-- in subjective fan_base values (Bellingham 95+, Yamal 91+) that lingered
-- on fri_scores.fan even after his on-field form fell off. The reality
-- check on 2026-05-12 against Ballon d'Or power rankings confirmed the
-- fan-poll seed was the main contributor to Bellingham sitting at #4 when
-- consensus says he's mid-pack.
--
-- New model:
--   fan = clamp(50 + (avg_internal_vote - 50)*1.0, 0, 100)
--       = avg_internal_vote when votes exist, else 50 (neutral baseline)
--
-- The Go code in refreshFanScore enforces this on every vote. This
-- migration resets the stored state once so the seeded values stop
-- influencing the leaderboard right after deploy — no need to wait for
-- the first vote per player.

-- For players who have voting history, recompute from the real vote average.
UPDATE fri_scores
SET fan = GREATEST(0, LEAST(100, ROUND(votes.avg_score::numeric, 1))),
    fan_base = 50,
    fan_updated_at = NOW()
FROM (
    SELECT player_id, AVG(internal_score) AS avg_score
    FROM fan_votes
    GROUP BY player_id
) AS votes
WHERE fri_scores.player_id = votes.player_id;

-- For players with no votes yet, fall to the neutral baseline.
UPDATE fri_scores
SET fan = 50.0,
    fan_base = 50,
    fan_updated_at = NOW()
WHERE player_id NOT IN (
    SELECT DISTINCT player_id FROM fan_votes
);

-- Recompute FRI for everyone — fan weight is 20%, so this matters.
UPDATE fri_scores
SET fri = ROUND(
    (performance * 0.35 +
     social      * 0.20 +
     fan         * 0.20 +
     media       * 0.15 +
     character   * 0.10
    )::numeric, 1),
    calculated_at = NOW();
