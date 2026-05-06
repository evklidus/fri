CREATE TABLE IF NOT EXISTS performance_snapshots (
    id BIGSERIAL PRIMARY KEY,
    player_id BIGINT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    average_rating DOUBLE PRECISION NOT NULL DEFAULT 0,
    goals_assists_per90 DOUBLE PRECISION NOT NULL DEFAULT 0,
    xg_xa_per90 DOUBLE PRECISION NOT NULL DEFAULT 0,
    position_rank_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    minutes_share DOUBLE PRECISION NOT NULL DEFAULT 0,
    normalized_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    snapshot_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_performance_snapshots_player_snapshot_at
    ON performance_snapshots(player_id, snapshot_at DESC);
