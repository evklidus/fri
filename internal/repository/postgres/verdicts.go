package postgres

import (
	"context"

	"fri.local/football-reputation-index/internal/domain"
)

// ArticleVerdicts returns the cached verdicts for a player's articles.
func (r *Repository) ArticleVerdicts(ctx context.Context, playerID int64, keys []string) (map[string]domain.ArticleVerdict, error) {
	out := make(map[string]domain.ArticleVerdict, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT article_key, about, impact, event, reason
		FROM article_verdicts WHERE player_id = $1 AND article_key = ANY($2)
	`, playerID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var v domain.ArticleVerdict
		if err := rows.Scan(&key, &v.About, &v.Impact, &v.Event, &v.Reason); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, rows.Err()
}

// SaveArticleVerdicts stores freshly judged verdicts.
func (r *Repository) SaveArticleVerdicts(ctx context.Context, playerID int64, model string, verdicts map[string]domain.ArticleVerdict) error {
	for key, v := range verdicts {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO article_verdicts (player_id, article_key, about, impact, event, reason, model)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (player_id, article_key) DO UPDATE
			SET about = EXCLUDED.about, impact = EXCLUDED.impact, event = EXCLUDED.event,
			    reason = EXCLUDED.reason, model = EXCLUDED.model, judged_at = now()
		`, playerID, key, v.About, v.Impact, v.Event, v.Reason, model); err != nil {
			return err
		}
	}
	return nil
}
