-- Phase 5: Fan Poll is no longer a standalone FRI component.
--
-- Partner's vision (2026-05-09): "оставить четыре компонента (Field
-- Performance, Social Influence, Media Score, Character Index) без
-- фанатского голоса". Fans now influence the score by voting on each
-- individual event (Phase 5 voting), not by submitting a holistic
-- 5-question poll about a player.
--
-- Old formula: P×0.35 + S×0.20 + Fan×0.20 + M×0.15 + C×0.10  (sum 1.00)
-- New formula: P×0.40 + S×0.25 + M×0.20 + C×0.15            (sum 1.00)
--
-- The 0.20 previously held by Fan is redistributed proportionally
-- (+0.05 to each of the four remaining components). This keeps each
-- component's relative weight in roughly the same ballpark.
--
-- fri_scores.fan column is intentionally kept around: dropping it
-- would force changes to ListPlayers / GetPlayer / ApplyMediaSync /
-- everything that reads the score. It's now just dead data — the
-- column exists for back-compat with any external consumer but no
-- longer affects FRI.

UPDATE fri_scores
SET fri = ROUND(
    (performance * 0.40 +
     social      * 0.25 +
     media       * 0.20 +
     character   * 0.15
    )::numeric, 1),
    calculated_at = NOW();
