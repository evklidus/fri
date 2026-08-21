-- Fresh ages and photos, kept fresh by construction.
--
-- Two problems this fixes.
--
-- 1. `age` is a static INT seeded from the source HTML in May. Nobody ages in
--    our database — Vinícius is stored as 26 while api-football reports 25,
--    and every stored age drifts by a year on each birthday regardless. A
--    birth date can't go stale, so we store that and compute the age when we
--    serve it. `age` stays for rows we haven't refreshed yet and as a
--    fallback when a player has no birth date on record.
--
-- 2. `photo_data` holds a base64 data URI per player. /api/players is 867KB
--    for 22 players, almost all of it images, and it grows linearly with the
--    roster — a thousand players would mean a ~40MB response. api-football
--    serves the same portraits from its CDN, so we keep a URL and let the
--    browser fetch (and cache) images itself. photo_data is left in place so
--    nothing breaks before the sync has populated URLs.
ALTER TABLE players
    ADD COLUMN IF NOT EXISTS birth_date DATE,
    ADD COLUMN IF NOT EXISTS photo_url TEXT NOT NULL DEFAULT '';
