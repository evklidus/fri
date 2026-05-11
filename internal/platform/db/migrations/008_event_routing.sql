-- Phase 4.1 prep: events can now route their delta to the Performance score,
-- not just Character. Phase 4.3 will add fixture-derived events (no
-- news_item_id), so we also need an idempotency key that doesn't depend on a
-- news article: `source_ref` (e.g. "fixture:9482:hat_trick").
--
-- We keep the table name `character_events` for now — renaming would force
-- touching repository methods, history queries, and tests. A future phase
-- (5: fan-validated events) can do the rename if we add more event sources.

ALTER TABLE character_events
    ADD COLUMN IF NOT EXISTS target_component TEXT NOT NULL DEFAULT 'character',
    ADD COLUMN IF NOT EXISTS source_ref TEXT;

-- Filter index for the dashboards / debugging — most "what affected the
-- Performance score today?" queries will scope by component.
CREATE INDEX IF NOT EXISTS idx_character_events_target_component
    ON character_events(target_component);

-- Idempotency guard for fixture-derived events: re-running the stats sync
-- over the same fixtures must not double-count. Only applies to rows with a
-- non-null source_ref; news-derived rows continue to use the original guard
-- on (player_id, news_item_id, trigger_word).
CREATE UNIQUE INDEX IF NOT EXISTS idx_character_events_source_ref
    ON character_events(player_id, trigger_word, source_ref)
    WHERE source_ref IS NOT NULL;
