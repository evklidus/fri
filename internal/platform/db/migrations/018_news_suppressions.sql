-- Remember what the moderators deleted.
--
-- The media sync rebuilds the news feed from scratch twice a day: every
-- media-provider row is deleted and the provider's current answer inserted
-- in its place. That rebuild had no memory of what an admin had removed, so
-- a deleted article was back within twelve hours — and since the player's
-- Media score was never recomputed on deletion either, its plus/minus never
-- left. The partner reported both on 2026-08-26.
--
-- Keyed per (player, article). One article is routinely filed under several
-- players — a live transfer blog that mentions each of them once — and
-- removing it from one player's feed says nothing about the others.
-- player_id is 0 for the rare row whose player was deleted.
CREATE TABLE IF NOT EXISTS news_suppressions (
    player_id     BIGINT NOT NULL,
    article_key   TEXT NOT NULL,
    title         TEXT NOT NULL DEFAULT '',
    suppressed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, article_key)
);
