ALTER TABLE players
    ADD COLUMN IF NOT EXISTS league TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_players_league ON players(league);

-- Backfill existing rows so the leaderboard's league filter works without
-- re-seeding the DB. Source-of-truth is the in-app `clubLeagueIndex` map;
-- this SQL CASE mirrors it for the current seed roster.
UPDATE players SET league = CASE
    WHEN club IN ('Manchester City', 'Man City', 'Liverpool', 'Arsenal', 'Chelsea',
                  'Tottenham', 'Newcastle', 'Aston Villa', 'West Ham',
                  'Manchester United', 'Man United') THEN 'Premier League'
    WHEN club IN ('Real Madrid', 'FC Barcelona', 'Barcelona', 'Atletico Madrid',
                  'Real Sociedad', 'Villarreal') THEN 'La Liga'
    WHEN club IN ('Bayern Munich', 'Bayer Leverkusen', 'Borussia Dortmund',
                  'RB Leipzig') THEN 'Bundesliga'
    WHEN club IN ('Inter', 'Inter Milan', 'AC Milan', 'Juventus', 'Napoli', 'Roma',
                  'Atalanta') THEN 'Serie A'
    WHEN club IN ('PSG', 'Paris Saint-Germain', 'Paris Saint Germain', 'Marseille',
                  'Lyon', 'Monaco') THEN 'Ligue 1'
    WHEN club IN ('Al Nassr', 'Al Ittihad', 'Al Hilal') THEN 'Saudi Pro League'
    WHEN club IN ('Fenerbahce', 'Fenerbahçe', 'Galatasaray', 'Besiktas') THEN 'Süper Lig'
    WHEN club IN ('Inter Miami', 'LA Galaxy', 'LAFC') THEN 'MLS'
    ELSE 'Other'
END
WHERE league = '';
