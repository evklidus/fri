-- Phase 4 follow-up: normalize Character to the events-only model.
--
-- Until this migration, fri_scores.character carried whatever value came from
-- the legacy HTML seed import plus accumulated per-sync deltas. This left
-- players like Vinicius Jr stuck at 52 because their seeded value never had
-- a positive event to balance it back to neutral.
--
-- New model (matches partner's "baseline + grows/falls" vision):
--
--     character = clamp(80 + Σ(events.delta where target_component='character'), 0, 100)
--
-- 80 is the neutral baseline. This migration recomputes everyone's character
-- score using that formula, fully replacing whatever was there. From this
-- point on, character_sync writes use the same formula in
-- ApplyCharacterSync — so reruns are idempotent.
--
-- We do this in SQL (not Go) so it runs deterministically once on next boot,
-- without needing a sync to fire first.

UPDATE fri_scores
SET character = GREATEST(0, LEAST(100, ROUND((80.0 + COALESCE(events.total_delta, 0))::numeric, 1))),
    character_updated_at = NOW()
FROM (
    SELECT
        player_id,
        SUM(delta) AS total_delta
    FROM character_events
    WHERE COALESCE(target_component, 'character') = 'character'
    GROUP BY player_id
) AS events
WHERE fri_scores.player_id = events.player_id;

-- Players with no events at all need their character set to the neutral
-- baseline too — otherwise the seed value lingers for "untouched" players.
UPDATE fri_scores
SET character = 80.0,
    character_updated_at = NOW()
WHERE player_id NOT IN (
    SELECT DISTINCT player_id
    FROM character_events
    WHERE COALESCE(target_component, 'character') = 'character'
);

-- Recompute FRI for every player so the new Character flows into the
-- weighted total in one shot. Mirrors applyFriFormula in Go:
--   FRI = P*0.35 + S*0.20 + F*0.20 + M*0.15 + C*0.10
UPDATE fri_scores
SET fri = ROUND(
    (performance * 0.35 +
     social      * 0.20 +
     fan         * 0.20 +
     media       * 0.15 +
     character   * 0.10
    )::numeric, 1),
    calculated_at = NOW();
