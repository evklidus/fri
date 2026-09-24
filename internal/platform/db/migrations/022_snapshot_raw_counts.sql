-- Keep the raw season counts a Performance score was built from.
--
-- Snapshots stored only derived rates (G+A per 90, minutes share, rating),
-- so "how many matches, goals and assists is this 84 based on?" had no
-- answer — and that is exactly what the partner asked to show behind a
-- "full statistics" button for signed-in visitors. The counts are summed
-- across every competition the score counts, and competitions keeps the
-- per-tournament split the log line already printed.
ALTER TABLE performance_snapshots
    ADD COLUMN IF NOT EXISTS appearances  INT,
    ADD COLUMN IF NOT EXISTS minutes      INT,
    ADD COLUMN IF NOT EXISTS goals        INT,
    ADD COLUMN IF NOT EXISTS assists      INT,
    ADD COLUMN IF NOT EXISTS competitions JSONB;
