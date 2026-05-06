package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
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

	if _, err := tx.Exec(ctx, `TRUNCATE fan_votes, social_snapshots, performance_snapshots, component_updates, news_items, fri_history, fri_scores, players RESTART IDENTITY CASCADE`); err != nil {
		return fmt.Errorf("truncate seed tables: %w", err)
	}

	nameToID := make(map[string]int64, len(players))

	for _, player := range players {
		var playerID int64
		err := tx.QueryRow(ctx, `
			INSERT INTO players (
				slug, name, club, league, position, age, emoji, photo_data, theme_background, summary_en, summary_ru
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING id
		`,
			player.Slug,
			player.Name,
			player.Club,
			player.League,
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
				player_id, fri, performance, social, fan, fan_base, media, character, trend_value, trend_direction, calculated_at,
				performance_updated_at, social_updated_at, fan_updated_at, media_updated_at, character_updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$11,$11,$11,$11)
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
				title_en, title_ru, summary_en, summary_ru, source, source_url, source_tier, sentiment, published_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
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
			item.SourceURL,
			defaultFloat(item.SourceTier, 50),
			item.Sentiment,
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
			p.id, p.slug, p.name, p.club, p.league, p.position, p.age, p.emoji, p.photo_data, p.theme_background, p.summary_en, p.summary_ru, p.created_at, p.updated_at,
			s.player_id, s.fri, s.performance, s.social, s.fan, s.fan_base, s.media, s.character, s.trend_value, s.trend_direction, s.calculated_at,
			s.performance_updated_at, s.social_updated_at, s.fan_updated_at, s.media_updated_at, s.character_updated_at
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
		item, err := scanPlayerWithScore(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}

	return result, rows.Err()
}

func (r *Repository) GetPlayer(ctx context.Context, id int64) (*domain.PlayerWithScore, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT
			p.id, p.slug, p.name, p.club, p.league, p.position, p.age, p.emoji, p.photo_data, p.theme_background, p.summary_en, p.summary_ru, p.created_at, p.updated_at,
			s.player_id, s.fri, s.performance, s.social, s.fan, s.fan_base, s.media, s.character, s.trend_value, s.trend_direction, s.calculated_at,
			s.performance_updated_at, s.social_updated_at, s.fan_updated_at, s.media_updated_at, s.character_updated_at
		FROM players p
		JOIN fri_scores s ON s.player_id = p.id
		WHERE p.id = $1
	`, id)

	item, err := scanPlayerWithScore(row)
	if err != nil {
		return nil, err
	}

	return &item, nil
}

func (r *Repository) ListSyncTargets(ctx context.Context) ([]domain.PlayerSyncTarget, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			p.id, p.name, p.club, p.position, p.age,
			s.player_id, s.fri, s.performance, s.social, s.fan, s.fan_base, s.media, s.character, s.trend_value, s.trend_direction, s.calculated_at,
			s.performance_updated_at, s.social_updated_at, s.fan_updated_at, s.media_updated_at, s.character_updated_at
		FROM players p
		JOIN fri_scores s ON s.player_id = p.id
		ORDER BY p.name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []domain.PlayerSyncTarget
	for rows.Next() {
		var target domain.PlayerSyncTarget
		if err := rows.Scan(
			&target.ID,
			&target.Name,
			&target.Club,
			&target.Position,
			&target.Age,
			&target.Score.PlayerID,
			&target.Score.FRI,
			&target.Score.Performance,
			&target.Score.Social,
			&target.Score.Fan,
			&target.Score.FanBase,
			&target.Score.Media,
			&target.Score.Character,
			&target.Score.TrendValue,
			&target.Score.TrendDirection,
			&target.Score.CalculatedAt,
			&target.Score.PerformanceUpdatedAt,
			&target.Score.SocialUpdatedAt,
			&target.Score.FanUpdatedAt,
			&target.Score.MediaUpdatedAt,
			&target.Score.CharacterUpdatedAt,
		); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}

	return targets, rows.Err()
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
		       title_en, title_ru, summary_en, summary_ru, source, source_url, source_tier, sentiment, published_at, created_at
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
			&item.SourceURL,
			&item.SourceTier,
			&item.Sentiment,
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

	score, _, err := refreshFanScore(ctx, tx, vote.PlayerID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return score, nil
}

func (r *Repository) StartComponentUpdate(ctx context.Context, component, provider string) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO component_updates(component, provider, status, started_at)
		VALUES ($1, $2, 'running', now())
		RETURNING id
	`, component, provider).Scan(&id)
	return id, err
}

func (r *Repository) FinishComponentUpdate(ctx context.Context, updateID int64, status, message string, recordsSeen int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE component_updates
		SET status = $2, message = $3, records_seen = $4, finished_at = now()
		WHERE id = $1
	`, updateID, status, message, recordsSeen)
	return err
}

func (r *Repository) ListComponentUpdates(ctx context.Context, limit int) ([]domain.ComponentUpdate, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, component, provider, status, message, records_seen, started_at, finished_at
		FROM component_updates
		ORDER BY started_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.ComponentUpdate
	for rows.Next() {
		var item domain.ComponentUpdate
		if err := rows.Scan(
			&item.ID,
			&item.Component,
			&item.Provider,
			&item.Status,
			&item.Message,
			&item.RecordsSeen,
			&item.StartedAt,
			&item.FinishedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (r *Repository) ApplySocialSync(ctx context.Context, snapshots []domain.SocialSnapshot, provider string) ([]domain.PlayerSyncDelta, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	deltas := make([]domain.PlayerSyncDelta, 0, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotProvider := provider
		if strings.TrimSpace(snapshot.Provider) != "" {
			snapshotProvider = snapshot.Provider
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO social_snapshots (
				player_id, provider, followers, engagement_rate, mentions_growth_7d, youtube_views_7d, normalized_score, snapshot_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`,
			snapshot.PlayerID,
			snapshotProvider,
			snapshot.Followers,
			snapshot.EngagementRate,
			snapshot.MentionsGrowth7D,
			snapshot.YouTubeViews7D,
			snapshot.NormalizedScore,
			snapshot.SnapshotAt,
		); err != nil {
			return nil, err
		}

		score, delta, err := refreshComponentScore(ctx, tx, snapshot.PlayerID, "social", snapshot.NormalizedScore, snapshot.SnapshotAt)
		if err != nil {
			return nil, err
		}

		deltas = append(deltas, domain.PlayerSyncDelta{
			PlayerID:    snapshot.PlayerID,
			PlayerName:  snapshot.PlayerName,
			Component:   "social",
			OldValue:    delta.OldComponentValue,
			NewValue:    snapshot.NormalizedScore,
			OldFRI:      delta.OldFRI,
			NewFRI:      score.FRI,
			ImpactDelta: round1(score.FRI - delta.OldFRI),
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return deltas, nil
}

func (r *Repository) ApplyPerformanceSync(ctx context.Context, snapshots []domain.PerformanceSnapshot, provider string) ([]domain.PlayerSyncDelta, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	deltas := make([]domain.PlayerSyncDelta, 0, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotProvider := provider
		if strings.TrimSpace(snapshot.Provider) != "" {
			snapshotProvider = snapshot.Provider
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO performance_snapshots (
				player_id, provider, average_rating, goals_assists_per90, xg_xa_per90, position_rank_score, minutes_share,
				form_score, last5_goals, last5_assists, last5_rating,
				normalized_score, snapshot_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		`,
			snapshot.PlayerID,
			snapshotProvider,
			snapshot.AverageRating,
			snapshot.GoalsAssistsPer90,
			snapshot.XGXAPer90,
			snapshot.PositionRankScore,
			snapshot.MinutesShare,
			snapshot.FormScore,
			snapshot.Last5Goals,
			snapshot.Last5Assists,
			snapshot.Last5Rating,
			snapshot.NormalizedScore,
			snapshot.SnapshotAt,
		); err != nil {
			return nil, err
		}

		score, delta, err := refreshComponentScore(ctx, tx, snapshot.PlayerID, "performance", snapshot.NormalizedScore, snapshot.SnapshotAt)
		if err != nil {
			return nil, err
		}

		deltas = append(deltas, domain.PlayerSyncDelta{
			PlayerID:    snapshot.PlayerID,
			PlayerName:  snapshot.PlayerName,
			Component:   "performance",
			OldValue:    delta.OldComponentValue,
			NewValue:    snapshot.NormalizedScore,
			OldFRI:      delta.OldFRI,
			NewFRI:      score.FRI,
			ImpactDelta: round1(score.FRI - delta.OldFRI),
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return deltas, nil
}

// GetExternalIDs returns the mapping for (playerID, provider) or (nil, nil)
// when no record exists. Other errors propagate.
func (r *Repository) GetExternalIDs(ctx context.Context, playerID int64, provider string) (*domain.PlayerExternalIDs, error) {
	var ids domain.PlayerExternalIDs
	var teamID *string
	err := r.pool.QueryRow(ctx, `
		SELECT player_id, provider, external_id, external_team_id, updated_at
		FROM player_external_ids
		WHERE player_id = $1 AND provider = $2
	`, playerID, provider).Scan(
		&ids.PlayerID,
		&ids.Provider,
		&ids.ExternalID,
		&teamID,
		&ids.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if teamID != nil {
		ids.ExternalTeamID = *teamID
	}
	return &ids, nil
}

func (r *Repository) UpsertExternalIDs(ctx context.Context, ids domain.PlayerExternalIDs) error {
	var teamID *string
	if trimmed := strings.TrimSpace(ids.ExternalTeamID); trimmed != "" {
		teamID = &trimmed
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO player_external_ids (player_id, provider, external_id, external_team_id, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (player_id, provider) DO UPDATE
		SET external_id = EXCLUDED.external_id,
		    external_team_id = EXCLUDED.external_team_id,
		    updated_at = now()
	`, ids.PlayerID, ids.Provider, ids.ExternalID, teamID)
	return err
}

// ApplyCharacterSync inserts new character events (idempotently via the
// unique index on (player_id, news_item_id, trigger_word)) and applies a
// per-player aggregated delta to fri_scores.character. The delta is clamped
// per call so a single sync can't tank a score below 0 or above 100.
//
// Returns one PlayerSyncDelta per player whose Character actually moved —
// players where every event was a duplicate are silently skipped.
func (r *Repository) ApplyCharacterSync(ctx context.Context, candidates []domain.CharacterEventCandidate, perPlayerCap float64) ([]domain.PlayerSyncDelta, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Group fired (newly-inserted) candidates by player so we can apply one
	// capped delta per player.
	perPlayerDelta := make(map[int64]float64)

	for _, c := range candidates {
		// Idempotent insert; ON CONFLICT skips duplicates already on file.
		var inserted int
		if err := tx.QueryRow(ctx, `
			INSERT INTO character_events (player_id, news_item_id, trigger_word, delta, status)
			VALUES ($1, $2, $3, $4, 'auto')
			ON CONFLICT (player_id, news_item_id, trigger_word) WHERE news_item_id IS NOT NULL
			DO NOTHING
			RETURNING 1
		`, c.PlayerID, c.NewsItemID, c.TriggerWord, c.Delta).Scan(&inserted); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			continue // duplicate — already counted in a previous sync
		}
		perPlayerDelta[c.PlayerID] += c.Delta
	}

	if len(perPlayerDelta) == 0 {
		// All events were dedupes; nothing to do but still commit (no inserts).
		return nil, tx.Commit(ctx)
	}

	// Preload player names so the resulting deltas carry a human-readable
	// label without an extra round-trip per player.
	names := make(map[int64]string, len(perPlayerDelta))
	for playerID := range perPlayerDelta {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM players WHERE id = $1`, playerID).Scan(&name); err == nil {
			names[playerID] = name
		}
	}

	deltas := make([]domain.PlayerSyncDelta, 0, len(perPlayerDelta))
	now := time.Now().UTC()
	for playerID, delta := range perPlayerDelta {
		// Cap aggregated delta so one sync can't move Character by more than
		// `perPlayerCap` points either direction.
		applied := delta
		if applied > perPlayerCap {
			applied = perPlayerCap
		}
		if applied < -perPlayerCap {
			applied = -perPlayerCap
		}

		current, err := lockScore(ctx, tx, playerID)
		if err != nil {
			return nil, err
		}
		oldFRI := current.FRI
		oldChar := current.Character
		newChar := round1(oldChar + applied)
		if newChar < 0 {
			newChar = 0
		}
		if newChar > 100 {
			newChar = 100
		}

		current.Character = newChar
		current.CharacterUpdatedAt = now
		applyFriFormula(current)

		if _, err := tx.Exec(ctx, `
			UPDATE fri_scores
			SET character = $2,
			    fri = $3,
			    trend_value = $4,
			    trend_direction = $5,
			    calculated_at = $6,
			    character_updated_at = $7
			WHERE player_id = $1
		`, playerID, current.Character, current.FRI, current.TrendValue, current.TrendDirection, current.CalculatedAt, current.CharacterUpdatedAt); err != nil {
			return nil, err
		}

		if historyDelta := round1(current.FRI - oldFRI); historyDelta != 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO fri_history (player_id, fri, delta, calculated_at)
				VALUES ($1,$2,$3,$4)
			`, playerID, current.FRI, historyDelta, current.CalculatedAt); err != nil {
				return nil, err
			}
		}

		deltas = append(deltas, domain.PlayerSyncDelta{
			PlayerID:    playerID,
			PlayerName:  names[playerID],
			Component:   "character",
			OldValue:    oldChar,
			NewValue:    newChar,
			OldFRI:      oldFRI,
			NewFRI:      current.FRI,
			ImpactDelta: round1(current.FRI - oldFRI),
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return deltas, nil
}

// HasRecentVote returns true when a vote exists for (playerID, ipHash) with
// created_at >= since. Used by the anti-abuse cooldown check.
func (r *Repository) HasRecentVote(ctx context.Context, playerID int64, ipHash string, since time.Time) (bool, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM fan_votes
		WHERE player_id = $1 AND ip_hash = $2 AND created_at >= $3
	`, playerID, ipHash, since).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) DeleteExternalIDs(ctx context.Context, playerID int64, provider string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM player_external_ids
		WHERE player_id = $1 AND provider = $2
	`, playerID, provider)
	return err
}

func (r *Repository) ApplyMediaSync(ctx context.Context, results []domain.MediaSyncPlayerResult, provider string) ([]domain.PlayerSyncDelta, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM news_items WHERE source = $1 OR source = 'legacy-html'`, provider); err != nil {
		return nil, err
	}

	var deltas []domain.PlayerSyncDelta
	for _, result := range results {
		score, delta, err := refreshComponentScore(ctx, tx, result.PlayerID, "media", result.MediaScore, time.Now().UTC())
		if err != nil {
			return nil, err
		}

		deltas = append(deltas, domain.PlayerSyncDelta{
			PlayerID:    result.PlayerID,
			PlayerName:  result.PlayerName,
			Component:   "media",
			OldValue:    delta.OldComponentValue,
			NewValue:    result.MediaScore,
			OldFRI:      delta.OldFRI,
			NewFRI:      score.FRI,
			ImpactDelta: round1(score.FRI - delta.OldFRI),
		})

		for _, article := range result.Articles {
			if _, err := tx.Exec(ctx, `
				INSERT INTO news_items (
					player_id, player_name, impact_type, impact_delta, relative_time, title_en, title_ru, summary_en, summary_ru,
					source, source_url, source_tier, sentiment, published_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			`,
				article.PlayerID,
				article.PlayerName,
				article.ImpactType,
				article.ImpactDelta,
				article.RelativeTime,
				article.TitleEN,
				article.TitleRU,
				article.SummaryEN,
				article.SummaryRU,
				article.Source,
				article.SourceURL,
				article.SourceTier,
				article.Sentiment,
				article.PublishedAt,
			); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return deltas, nil
}

func refreshFanScore(ctx context.Context, tx pgx.Tx, playerID int64) (*domain.Score, *scoreDelta, error) {
	var current domain.Score
	if err := tx.QueryRow(ctx, `
		SELECT player_id, fri, performance, social, fan, fan_base, media, character, trend_value, trend_direction, calculated_at,
		       performance_updated_at, social_updated_at, fan_updated_at, media_updated_at, character_updated_at
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
		&current.PerformanceUpdatedAt,
		&current.SocialUpdatedAt,
		&current.FanUpdatedAt,
		&current.MediaUpdatedAt,
		&current.CharacterUpdatedAt,
	); err != nil {
		return nil, nil, err
	}

	var avgInternal float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(AVG(internal_score), 0) FROM fan_votes WHERE player_id = $1`, playerID).Scan(&avgInternal); err != nil {
		return nil, nil, err
	}

	newFan := round1((current.FanBase * 0.7) + (avgInternal * 0.3))
	oldFRI := current.FRI
	oldFan := current.Fan

	current.Fan = newFan
	current.FanUpdatedAt = time.Now().UTC()
	applyFriFormula(&current)

	if _, err := tx.Exec(ctx, `
		UPDATE fri_scores
		SET fan = $2, fri = $3, trend_value = $4, trend_direction = $5, calculated_at = $6, fan_updated_at = $7
		WHERE player_id = $1
	`, playerID, current.Fan, current.FRI, current.TrendValue, current.TrendDirection, current.CalculatedAt, current.FanUpdatedAt); err != nil {
		return nil, nil, err
	}

	if historyDelta := round1(current.FRI - oldFRI); historyDelta != 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fri_history (player_id, fri, delta, calculated_at)
			VALUES ($1,$2,$3,$4)
		`, playerID, current.FRI, historyDelta, current.CalculatedAt); err != nil {
			return nil, nil, err
		}
	}

	return &current, &scoreDelta{
		OldFRI:            oldFRI,
		OldComponentValue: oldFan,
	}, nil
}

func refreshComponentScore(ctx context.Context, tx pgx.Tx, playerID int64, component string, newValue float64, updatedAt time.Time) (*domain.Score, *scoreDelta, error) {
	current, err := lockScore(ctx, tx, playerID)
	if err != nil {
		return nil, nil, err
	}

	oldFRI := current.FRI
	delta := &scoreDelta{OldFRI: oldFRI}

	switch component {
	case "media":
		delta.OldComponentValue = current.Media
		current.Media = round1(newValue)
		current.MediaUpdatedAt = updatedAt
	case "social":
		delta.OldComponentValue = current.Social
		current.Social = round1(newValue)
		current.SocialUpdatedAt = updatedAt
	case "performance":
		delta.OldComponentValue = current.Performance
		current.Performance = round1(newValue)
		current.PerformanceUpdatedAt = updatedAt
	case "character":
		delta.OldComponentValue = current.Character
		current.Character = round1(newValue)
		current.CharacterUpdatedAt = updatedAt
	default:
		return nil, nil, fmt.Errorf("unsupported component %q", component)
	}

	applyFriFormula(current)

	if _, err := tx.Exec(ctx, `
		UPDATE fri_scores
		SET performance = $2,
		    social = $3,
		    fan = $4,
		    media = $5,
		    character = $6,
		    fri = $7,
		    trend_value = $8,
		    trend_direction = $9,
		    calculated_at = $10,
		    performance_updated_at = $11,
		    social_updated_at = $12,
		    fan_updated_at = $13,
		    media_updated_at = $14,
		    character_updated_at = $15
		WHERE player_id = $1
	`,
		playerID,
		current.Performance,
		current.Social,
		current.Fan,
		current.Media,
		current.Character,
		current.FRI,
		current.TrendValue,
		current.TrendDirection,
		current.CalculatedAt,
		current.PerformanceUpdatedAt,
		current.SocialUpdatedAt,
		current.FanUpdatedAt,
		current.MediaUpdatedAt,
		current.CharacterUpdatedAt,
	); err != nil {
		return nil, nil, err
	}

	if historyDelta := round1(current.FRI - oldFRI); historyDelta != 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fri_history (player_id, fri, delta, calculated_at)
			VALUES ($1,$2,$3,$4)
		`, playerID, current.FRI, historyDelta, current.CalculatedAt); err != nil {
			return nil, nil, err
		}
	}

	return current, delta, nil
}

func lockScore(ctx context.Context, tx pgx.Tx, playerID int64) (*domain.Score, error) {
	var current domain.Score
	err := tx.QueryRow(ctx, `
		SELECT player_id, fri, performance, social, fan, fan_base, media, character, trend_value, trend_direction, calculated_at,
		       performance_updated_at, social_updated_at, fan_updated_at, media_updated_at, character_updated_at
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
		&current.PerformanceUpdatedAt,
		&current.SocialUpdatedAt,
		&current.FanUpdatedAt,
		&current.MediaUpdatedAt,
		&current.CharacterUpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &current, nil
}

type scoreDelta struct {
	OldFRI            float64
	OldComponentValue float64
}

func applyFriFormula(score *domain.Score) {
	previousFRI := score.FRI
	score.FRI = round1((score.Performance * 0.35) + (score.Social * 0.20) + (score.Fan * 0.20) + (score.Media * 0.15) + (score.Character * 0.10))
	delta := round1(score.FRI - previousFRI)
	score.TrendValue = round1(math.Abs(delta))
	score.TrendDirection = trendDirection(delta)
	score.CalculatedAt = time.Now().UTC()
}

func scanPlayerWithScore(row interface {
	Scan(dest ...any) error
}) (domain.PlayerWithScore, error) {
	var item domain.PlayerWithScore
	err := row.Scan(
		&item.ID,
		&item.Slug,
		&item.Name,
		&item.Club,
		&item.League,
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
		&item.PerformanceUpdatedAt,
		&item.SocialUpdatedAt,
		&item.FanUpdatedAt,
		&item.MediaUpdatedAt,
		&item.CharacterUpdatedAt,
	)
	return item, err
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
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

func defaultFloat(value, fallback float64) float64 {
	if value == 0 {
		return fallback
	}
	return value
}
