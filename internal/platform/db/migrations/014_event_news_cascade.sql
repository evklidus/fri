-- Fix the "zombie event" duplication bug.
--
-- Diagnosed on 2026-05-14: Vinicius Jr accumulated 8 ballon_dor events
-- because MediaSync rotates news_items each cycle (delete-all-then-insert),
-- and the FK `news_item_id REFERENCES news_items(id) ON DELETE SET NULL`
-- nulled out the link on each rotation. The unique index
-- `(player_id, news_item_id, trigger_word) WHERE news_item_id IS NOT NULL`
-- then no longer covered those orphaned rows, so the next character sync
-- re-fired the same article-trigger and inserted a fresh row. Repeat 8x.
--
-- Two fixes here:
--
-- 1. Switch the FK to ON DELETE CASCADE. When a news article rotates out
--    of the feed, any character events it triggered die with it. Event
--    history mirrors the article: if the article's gone, so is its score
--    impact. This is the right default — auditability of events requires
--    a stable article reference.
--
-- 2. Sweep up any existing orphaned rows that already have
--    news_item_id IS NULL but came from a news source (not a stats
--    detector). Anything WITHOUT a source_ref AND without a news_item_id
--    is unambiguously a zombie. Stats events keep a non-null source_ref,
--    so they survive.

-- Step 1: drop the old FK constraint and recreate with CASCADE.
ALTER TABLE character_events
    DROP CONSTRAINT IF EXISTS character_events_news_item_id_fkey;

ALTER TABLE character_events
    ADD CONSTRAINT character_events_news_item_id_fkey
    FOREIGN KEY (news_item_id) REFERENCES news_items(id) ON DELETE CASCADE;

-- Step 2: nuke zombies. Conservative match:
--   - news_item_id IS NULL  (lost its article link)
--   - source_ref IS NULL    (so not a stats-derived event, which always
--                             has source_ref like "fixture:..." or
--                             "drought:season:wk...")
DELETE FROM character_events
WHERE news_item_id IS NULL
  AND source_ref IS NULL;
