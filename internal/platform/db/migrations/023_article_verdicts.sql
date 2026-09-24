-- Claude's reading of each article, cached so each one is paid for once.
--
-- The media sync asks MediaStack for a player's latest 25 articles twice a
-- day, and most of them are the same articles as last time. Keyed by
-- (player, article) because the same live blog can be about one player and
-- merely mention another.
CREATE TABLE IF NOT EXISTS article_verdicts (
    player_id   BIGINT NOT NULL,
    article_key TEXT NOT NULL,
    about       TEXT NOT NULL,
    impact      DOUBLE PRECISION NOT NULL,
    event       TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL,
    judged_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, article_key)
);

-- Events waited only 24 hours for votes, so the Vote on Events page was
-- empty most of the time: anything detected overnight was finalized before
-- most people ever saw it. Three days gives an event a weekend.
UPDATE character_events
SET voting_closes_at = detected_at + INTERVAL '72 hours'
WHERE voting_status = 'pending_vote' AND voting_closes_at > now();
