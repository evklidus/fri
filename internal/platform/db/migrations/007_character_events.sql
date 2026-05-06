-- Auto-detected reputation events derived from news_items keyword scans.
-- Status defaults to 'auto' so a future moderation UI (phase 4) can flip
-- entries to 'confirmed'/'rejected' without changing schema.
CREATE TABLE IF NOT EXISTS character_events (
    id BIGSERIAL PRIMARY KEY,
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    news_item_id BIGINT REFERENCES news_items(id) ON DELETE SET NULL,
    trigger_word TEXT NOT NULL,
    delta DOUBLE PRECISION NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'auto',
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency guard: scanning the same article twice for the same trigger
-- must not double-count its delta. NULL news_item_id (manually-added events)
-- is allowed to repeat.
CREATE UNIQUE INDEX IF NOT EXISTS idx_character_events_unique
    ON character_events(player_id, news_item_id, trigger_word)
    WHERE news_item_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_character_events_player_detected
    ON character_events(player_id, detected_at DESC);
