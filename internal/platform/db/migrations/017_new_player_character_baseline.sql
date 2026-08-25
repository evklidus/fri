-- Repair players created below the Character baseline.
--
-- Migration 010 established the model: character = 80 + Σ(events), where 80
-- means "nothing recorded against them". Every player the seed produced sits
-- there or above.
--
-- The add-player endpoint (2026-08-24) did not know that. It started every
-- unmeasured component at the generic neutral 50, Character included, so
-- Rafael Leão and Julián Álvarez were entered 30 points below everyone else
-- for no reason other than being new — worth 4.5 FRI at Character's 15%
-- weight, which is what the partner noticed as "their rating is too low".
--
-- The code now starts new arrivals from domain.CharacterBaseline. This fixes
-- the rows already written. Scoped to players with no character events, so a
-- player genuinely dragged below 80 by a doping ban keeps their score.
UPDATE fri_scores
SET character = 80.0,
    character_updated_at = NOW()
WHERE character < 80.0
  AND player_id NOT IN (
    SELECT DISTINCT player_id
    FROM character_events
    WHERE COALESCE(target_component, 'character') = 'character'
  );

-- Recompute FRI for the rows just touched. Mirrors applyFriFormula in Go as
-- of Phase 5 — Fan no longer contributes:
--   FRI = P*0.40 + S*0.25 + M*0.20 + C*0.15
UPDATE fri_scores
SET fri = ROUND(
        (performance * 0.40 +
         social      * 0.25 +
         media       * 0.20 +
         character   * 0.15
        )::numeric, 1),
    calculated_at = NOW()
WHERE character = 80.0
  AND player_id NOT IN (
    SELECT DISTINCT player_id
    FROM character_events
    WHERE COALESCE(target_component, 'character') = 'character'
  );
