package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) PlayerCount(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM players`).Scan(&count)
	return count, err
}

func (r *Repository) ReplaceAllSeedData(ctx context.Context, players []domain.PlayerWithScore, history map[string][]domain.HistoryPoint, news []domain.NewsItem) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `TRUNCATE fan_votes, news_items, fri_history, fri_scores, players RESTART IDENTITY CASCADE`); err != nil {
		return fmt.Errorf("truncate seed tables: %w", err)
	}

	nameToID := make(map[string]int64, len(players))

	for _, player := range players {
		var playerID int64
		err := tx.QueryRow(ctx, `
			INSERT INTO players (
				slug, name, club, position, age, emoji, photo_data, theme_background, summary_en, summary_ru
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			RETURNING id
		`,
			player.Slug,
			player.Name,
			player.Club,
			player.Position,
			player.Age,
			player.Emoji,
			player.PhotoData,
			player.ThemeBackground,
			player.SummaryEN,
			player.SummaryRU,
		).Scan(&playerID)
		if err != nil {
			return fmt.Errorf("insert player %s: %w", player.Name, err)
		}

		nameToID[player.Name] = playerID

		if _, err := tx.Exec(ctx, `
			INSERT INTO fri_scores (
				player_id, fri, performance, social, fan, fan_base, media, character, trend_value, trend_direction, calculated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`,
			playerID,
			player.FRI,
			player.Performance,
			player.Social,
			player.Fan,
			player.FanBase,
			player.Media,
			player.Character,
			player.TrendValue,
			player.TrendDirection,
			player.CalculatedAt,
		); err != nil {
			return fmt.Errorf("insert fri score %s: %w", player.Name, err)
		}

		for _, point := range history[player.Name] {
			if _, err := tx.Exec(ctx, `
				INSERT INTO fri_history (player_id, fri, delta, calculated_at)
				VALUES ($1,$2,$3,$4)
			`, playerID, point.FRI, point.Delta, point.CalculatedAt); err != nil {
				return fmt.Errorf("insert history %s: %w", player.Name, err)
			}
		}
	}

	for _, item := range news {
		var playerID *int64
		if resolvedID, ok := nameToID[item.PlayerName]; ok {
			playerID = &resolvedID
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO news_items (
				player_id, player_name, impact_type, impact_delta, relative_time,
				title_en, title_ru, summary_en, summary_ru, source, published_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`,
			playerID,
			item.PlayerName,
			item.ImpactType,
			item.ImpactDelta,
			item.RelativeTime,
			item.TitleEN,
			item.TitleRU,
			item.SummaryEN,
			item.SummaryRU,
			item.Source,
			item.PublishedAt,
		); err != nil {
			return fmt.Errorf("insert news %s: %w", item.TitleEN, err)
		}
	}

	return tx.Commit(ctx)
}

func (r *Repository) ListPlayers(ctx context.Context, search, position, club string) ([]domain.PlayerWithScore, error) {
	var args []any
	var filters []string

	baseQuery := `
		SELECT
			p.id, p.slug, p.name, p.club, p.position, p.age, p.emoji, p.photo_data, p.theme_background, p.summary_en, p.summary_ru, p.created_at, p.updated_at,
			s.player_id, s.fri, s.performance, s.social, s.fan, s.fan_base, s.media, s.character, s.trend_value, s.trend_direction, s.calculated_at
		FROM players p
		JOIN fri_scores s ON s.player_id = p.id
	`

	if search != "" {
		args = append(args, "%"+strings.ToLower(search)+"%")
		filters = append(filters, fmt.Sprintf("(LOWER(p.name) LIKE $%d OR LOWER(p.club) LIKE $%d)", len(args), len(args)))
	}
	if position != "" && position != "all" {
		args = append(args, position)
		filters = append(filters, fmt.Sprintf("p.position = $%d", len(args)))
	}
	if club != "" {
		args = append(args, club)
		filters = append(filters, fmt.Sprintf("p.club = $%d", len(args)))
	}

	if len(filters) > 0 {
		baseQuery += " WHERE " + strings.Join(filters, " AND ")
	}

	baseQuery += " ORDER BY s.fri DESC, p.name ASC"

	rows, err := r.pool.Query(ctx, baseQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.PlayerWithScore
	for rows.Next() {
		var item domain.PlayerWithScore
		if err := rows.Scan(
			&item.ID,
			&item.Slug,
			&item.Name,
			&item.Club,
			&item.Position,
			&item.Age,
			&item.Emoji,
			&item.PhotoData,
			&item.ThemeBackground,
			&item.SummaryEN,
			&item.SummaryRU,
			&item.CreatedAt,
			&item.UpdatedAt,
			&item.PlayerID,
			&item.FRI,
			&item.Performance,
			&item.Social,
			&item.Fan,
			&item.FanBase,
			&item.Media,
			&item.Character,
			&item.TrendValue,
			&item.TrendDirection,
			&item.CalculatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}

	return result, rows.Err()
}

func (r *Repository) GetPlayer(ctx context.Context, id int64) (*domain.PlayerWithScore, error) {
	var item domain.PlayerWithScore
	err := r.pool.QueryRow(ctx, `
		SELECT
			p.id, p.slug, p.name, p.club, p.position, p.age, p.emoji, p.photo_data, p.theme_background, p.summary_en, p.summary_ru, p.created_at, p.updated_at,
			s.player_id, s.fri, s.performance, s.social, s.fan, s.fan_base, s.media, s.character, s.trend_value, s.trend_direction, s.calculated_at
		FROM players p
		JOIN fri_scores s ON s.player_id = p.id
		WHERE p.id = $1
	`, id).Scan(
		&item.ID,
		&item.Slug,
		&item.Name,
		&item.Club,
		&item.Position,
		&item.Age,
		&item.Emoji,
		&item.PhotoData,
		&item.ThemeBackground,
		&item.SummaryEN,
		&item.SummaryRU,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.PlayerID,
		&item.FRI,
		&item.Performance,
		&item.Social,
		&item.Fan,
		&item.FanBase,
		&item.Media,
		&item.Character,
		&item.TrendValue,
		&item.TrendDirection,
		&item.CalculatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &item, nil
}

func (r *Repository) GetHistory(ctx context.Context, playerID int64) ([]domain.HistoryPoint, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, player_id, fri, delta, calculated_at
		FROM fri_history
		WHERE player_id = $1
		ORDER BY calculated_at ASC
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []domain.HistoryPoint
	for rows.Next() {
		var point domain.HistoryPoint
		if err := rows.Scan(&point.ID, &point.PlayerID, &point.FRI, &point.Delta, &point.CalculatedAt); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (r *Repository) ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error) {
	query := `
		SELECT id, player_id, player_name, impact_type, impact_delta, relative_time,
		       title_en, title_ru, summary_en, summary_ru, source, published_at, created_at
		FROM news_items
	`
	var args []any
	if playerID != nil {
		query += ` WHERE player_id = $1`
		args = append(args, *playerID)
	}
	query += ` ORDER BY published_at DESC, id DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.NewsItem
	for rows.Next() {
		var item domain.NewsItem
		if err := rows.Scan(
			&item.ID,
			&item.PlayerID,
			&item.PlayerName,
			&item.ImpactType,
			&item.ImpactDelta,
			&item.RelativeTime,
			&item.TitleEN,
			&item.TitleRU,
			&item.SummaryEN,
			&item.SummaryRU,
			&item.Source,
			&item.PublishedAt,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) CreateVoteAndRefreshScore(ctx context.Context, vote domain.Vote) (*domain.Score, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO fan_votes (
			player_id, session_id, rating_overall, rating_hype, rating_tier, behavior_score, internal_score, ip_hash
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`,
		vote.PlayerID,
		vote.SessionID,
		vote.RatingOverall,
		vote.RatingHype,
		vote.RatingTier,
		vote.BehaviorScore,
		vote.InternalScore,
		vote.IPHash,
	); err != nil {
		return nil, err
	}

	score, err := refreshFanScore(ctx, tx, vote.PlayerID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return score, nil
}

func refreshFanScore(ctx context.Context, tx pgx.Tx, playerID int64) (*domain.Score, error) {
	var current domain.Score
	if err := tx.QueryRow(ctx, `
		SELECT player_id, fri, performance, social, fan, fan_base, media, character, trend_value, trend_direction, calculated_at
		FROM fri_scores
		WHERE player_id = $1
		FOR UPDATE
	`, playerID).Scan(
		&current.PlayerID,
		&current.FRI,
		&current.Performance,
		&current.Social,
		&current.Fan,
		&current.FanBase,
		&current.Media,
		&current.Character,
		&current.TrendValue,
		&current.TrendDirection,
		&current.CalculatedAt,
	); err != nil {
		return nil, err
	}

	var avgInternal float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(AVG(internal_score), 0) FROM fan_votes WHERE player_id = $1`, playerID).Scan(&avgInternal); err != nil {
		return nil, err
	}

	newFan := round1((current.FanBase * 0.7) + (avgInternal * 0.3))
	newFRI := round1((current.Performance * 0.35) + (current.Social * 0.20) + (newFan * 0.20) + (current.Media * 0.15) + (current.Character * 0.10))
	delta := round1(newFRI - current.FRI)
	direction := trendDirection(delta)

	if _, err := tx.Exec(ctx, `
		UPDATE fri_scores
		SET fan = $2, fri = $3, trend_value = $4, trend_direction = $5, calculated_at = $6
		WHERE player_id = $1
	`, playerID, newFan, newFRI, delta, direction, time.Now().UTC()); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO fri_history (player_id, fri, delta, calculated_at)
		VALUES ($1,$2,$3,$4)
	`, playerID, newFRI, delta, time.Now().UTC()); err != nil {
		return nil, err
	}

	current.Fan = newFan
	current.FRI = newFRI
	current.TrendValue = delta
	current.TrendDirection = direction
	current.CalculatedAt = time.Now().UTC()
	return &current, nil
}

func round1(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}

func trendDirection(delta float64) string {
	if delta > 0.3 {
		return "up"
	}
	if delta < -0.3 {
		return "down"
	}
	return "stable"
}
