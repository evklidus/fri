-- Traffic counters for the admin dashboard.
--
-- Erik asked for a way to see user numbers and traffic from the admin
-- account (2026-09-09). Nothing was recorded: Caddy logs every request to
-- stdout, but docker rotates that at 30MB and nobody can answer "how many
-- people came last week" from it.
--
-- Two tables, both deliberately small.
--
-- traffic_daily is a counter per (day, section). The app aggregates in
-- memory and flushes every half minute, so a busy day costs a couple of
-- thousand tiny updates rather than one per request, and no single row
-- becomes a write hotspot.
--
-- traffic_visitors is one row per visitor per day, which is what makes
-- "unique visitors" countable without storing a visit log. The hash is
-- SHA-256 of the address with the day mixed in, so the same person is one
-- row today and a different row tomorrow: enough to count, not enough to
-- follow anyone across days. Raw addresses are never written, the same
-- rule votes already follow.
CREATE TABLE IF NOT EXISTS traffic_daily (
    day     DATE   NOT NULL,
    section TEXT   NOT NULL,
    views   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, section)
);

CREATE TABLE IF NOT EXISTS traffic_visitors (
    day          DATE NOT NULL,
    visitor_hash TEXT NOT NULL,
    PRIMARY KEY (day, visitor_hash)
);

-- The dashboard always reads a trailing window, never the whole history.
CREATE INDEX IF NOT EXISTS idx_traffic_daily_day ON traffic_daily (day DESC);
