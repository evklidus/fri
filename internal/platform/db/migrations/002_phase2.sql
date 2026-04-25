ALTER TABLE fri_scores
    ADD COLUMN IF NOT EXISTS performance_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS social_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS fan_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS media_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS character_updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE news_items
    ADD COLUMN IF NOT EXISTS source_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_tier DOUBLE PRECISION NOT NULL DEFAULT 50,
    ADD COLUMN IF NOT EXISTS sentiment DOUBLE PRECISION NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS component_updates (
    id BIGSERIAL PRIMARY KEY,
    component TEXT NOT NULL,
    provider TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    records_seen INT NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_component_updates_component_started_at
    ON component_updates(component, started_at DESC);

CREATE TABLE IF NOT EXISTS social_snapshots (
    id BIGSERIAL PRIMARY KEY,
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    followers BIGINT NOT NULL DEFAULT 0,
    engagement_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    mentions_growth_7d DOUBLE PRECISION NOT NULL DEFAULT 0,
    youtube_views_7d BIGINT NOT NULL DEFAULT 0,
    normalized_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    snapshot_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_social_snapshots_player_snapshot_at
    ON social_snapshots(player_id, snapshot_at DESC);
