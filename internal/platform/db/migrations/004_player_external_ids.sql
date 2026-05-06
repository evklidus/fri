CREATE TABLE IF NOT EXISTS player_external_ids (
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    external_team_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, provider)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_player_external_ids_provider_external_id
    ON player_external_ids(provider, external_id);
