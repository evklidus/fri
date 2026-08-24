package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"fri.local/football-reputation-index/internal/domain"
)

// PlayerExists reports whether a player is already on the roster, by name or
// by slug. Both are UNIQUE, and either collision means the same footballer —
// so the caller can refuse before spending a provider request.
func (r *Repository) PlayerExists(ctx context.Context, name, slug string) (int64, bool, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		SELECT id FROM players WHERE name = $1 OR slug = $2 LIMIT 1
	`, strings.TrimSpace(name), strings.TrimSpace(slug)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// InsertPlayer adds one player, their starting scores and their provider
// mapping in a single transaction.
//
// All three or none. ListPlayers, GetPlayer and ListSyncTargets all INNER JOIN
// fri_scores, so a player row without a score row is not merely unranked — it
// is invisible to the site and to every sync, an orphan that only resurfaces
// as a unique-constraint violation the next time someone tries to add them.
//
// The provider mapping is pinned here on purpose: a player added this way
// never has to be found by fuzzy name search again, which is the path that
// once bound our goalkeeper to a defender of the same surname.
func (r *Repository) InsertPlayer(ctx context.Context, player domain.PlayerWithScore, providerID, providerTeamID string) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var playerID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO players (
			slug, name, club, league, position, age, birth_date, emoji,
			photo_data, photo_url, theme_background, summary_en, summary_ru
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'',$9,'','','')
		RETURNING id
	`,
		player.Slug, player.Name, player.Club, player.League, player.Position,
		player.Age, player.BirthDate, player.Emoji, player.PhotoURL,
	).Scan(&playerID)
	if err != nil {
		return 0, fmt.Errorf("insert player %s: %w", player.Name, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO fri_scores (
			player_id, fri, performance, social, fan, fan_base, media, character,
			trend_value, trend_direction, calculated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,'stable',now())
	`,
		playerID, player.FRI, player.Performance, player.Social,
		player.Fan, player.FanBase, player.Media, player.Character,
	); err != nil {
		return 0, fmt.Errorf("insert scores for %s: %w", player.Name, err)
	}

	if providerID != "" && providerID != "0" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO player_external_ids (player_id, provider, external_id, external_team_id)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (player_id, provider) DO UPDATE
			SET external_id = EXCLUDED.external_id,
			    external_team_id = EXCLUDED.external_team_id,
			    updated_at = now()
		`, playerID, "api-football", providerID, providerTeamID); err != nil {
			return 0, fmt.Errorf("pin provider mapping for %s: %w", player.Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return playerID, nil
}
