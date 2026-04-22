CREATE TABLE IF NOT EXISTS players (
    id BIGSERIAL PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL UNIQUE,
    club TEXT NOT NULL,
    position TEXT NOT NULL,
    age INT NOT NULL,
    emoji TEXT NOT NULL DEFAULT '',
    photo_data TEXT NOT NULL DEFAULT '',
    theme_background TEXT NOT NULL DEFAULT '',
    summary_en TEXT NOT NULL DEFAULT '',
    summary_ru TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS fri_scores (
    player_id BIGINT PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    fri DOUBLE PRECISION NOT NULL,
    performance DOUBLE PRECISION NOT NULL,
    social DOUBLE PRECISION NOT NULL,
    fan DOUBLE PRECISION NOT NULL,
    fan_base DOUBLE PRECISION NOT NULL,
    media DOUBLE PRECISION NOT NULL,
    character DOUBLE PRECISION NOT NULL,
    trend_value DOUBLE PRECISION NOT NULL DEFAULT 0,
    trend_direction TEXT NOT NULL DEFAULT 'stable',
    calculated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS fri_history (
    id BIGSERIAL PRIMARY KEY,
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    fri DOUBLE PRECISION NOT NULL,
    delta DOUBLE PRECISION NOT NULL DEFAULT 0,
    calculated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_fri_history_player_time ON fri_history(player_id, calculated_at DESC);

CREATE TABLE IF NOT EXISTS news_items (
    id BIGSERIAL PRIMARY KEY,
    player_id BIGINT REFERENCES players(id) ON DELETE SET NULL,
    player_name TEXT NOT NULL,
    impact_type TEXT NOT NULL,
    impact_delta DOUBLE PRECISION NOT NULL DEFAULT 0,
    relative_time TEXT NOT NULL DEFAULT '',
    title_en TEXT NOT NULL,
    title_ru TEXT NOT NULL,
    summary_en TEXT NOT NULL,
    summary_ru TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'legacy-html',
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_news_items_published_at ON news_items(published_at DESC);

CREATE TABLE IF NOT EXISTS fan_votes (
    id BIGSERIAL PRIMARY KEY,
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    rating_overall INT NOT NULL,
    rating_hype INT NOT NULL,
    rating_tier INT NOT NULL,
    behavior_score INT NOT NULL,
    internal_score DOUBLE PRECISION NOT NULL,
    ip_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_fan_votes_player_id ON fan_votes(player_id, created_at DESC);

