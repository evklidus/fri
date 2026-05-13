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

// ApplyCharacterSync inserts new rating events (idempotently — see uniqueness
// indexes on character_events) and updates per-player component scores.
// Routing by candidate.TargetComponent:
//
//   - "character"  → score is RECOMPUTED from scratch as
//                    clamp(characterBaseline + Σ(all character events), 0, 100).
//                    This makes character fully data-driven: no inherited seed
//                    value, no "stuck" historical state. Per the partner's
//                    "baseline + grows/falls" vision (2026-05-09 chat).
//
//   - "performance" → score gets the per-sync capped Δ added on top of its
//                     current value. Performance has its own snapshot source
//                     (api-football) which overwrites the column on every
//                     performance sync, so events here are intentionally
//                     ephemeral — they get re-fired by the stats detector
//                     next sync if the condition still holds.
//
// Returns one PlayerSyncDelta per (player, component) pair that actually
// moved.
func (r *Repository) ApplyCharacterSync(ctx context.Context, candidates []domain.CharacterEventCandidate, perPlayerCap float64) ([]domain.PlayerSyncDelta, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// (playerID, component) -> summed delta from newly-inserted candidates.
	type bucketKey struct {
		playerID  int64
		component string
	}
	buckets := make(map[bucketKey]float64)

	for _, c := range candidates {
		component := normalizeTargetComponent(c.TargetComponent)
		newsRef := nullableInt64(c.NewsItemID)
		sourceRef := nullableString(c.SourceRef)

		// Idempotent insert. We don't rely on the unique index alone (it only
		// covers news-derived rows); a SELECT pre-check catches dups for
		// source_ref-keyed rows on engines that don't support partial unique
		// well, while ON CONFLICT keeps the news-derived path fast.
		var existing int
		if c.NewsItemID > 0 {
			err := tx.QueryRow(ctx, `
				SELECT 1 FROM character_events
				WHERE player_id = $1 AND news_item_id = $2 AND trigger_word = $3
				LIMIT 1
			`, c.PlayerID, c.NewsItemID, c.TriggerWord).Scan(&existing)
			if err == nil {
				continue // already counted in a previous sync
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		} else if c.SourceRef != "" {
			err := tx.QueryRow(ctx, `
				SELECT 1 FROM character_events
				WHERE player_id = $1 AND trigger_word = $2 AND source_ref = $3
				LIMIT 1
			`, c.PlayerID, c.TriggerWord, c.SourceRef).Scan(&existing)
			if err == nil {
				continue
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}

		// Phase 5: bifurcate by autoApply.
		//
		//   autoApply=true   → final_delta set immediately; voting_status='auto_applied'.
		//                      Bucket the delta so the component score updates in
		//                      this same transaction.
		//
		//   autoApply=false  → final_delta=NULL; voting_status='pending_vote';
		//                      voting_closes_at = NOW() + 24h. Do NOT bucket — the
		//                      component score will move only after the finalize
		//                      cron picks the event up and writes final_delta.
		if c.AutoApply {
			if _, err := tx.Exec(ctx, `
				INSERT INTO character_events
					(player_id, news_item_id, trigger_word, delta, status, target_component,
					 source_ref, proposed_delta, final_delta, voting_status, auto_apply)
				VALUES ($1, $2, $3, $4, 'auto', $5, $6, $4, $4, 'auto_applied', TRUE)
			`, c.PlayerID, newsRef, c.TriggerWord, c.Delta, component, sourceRef); err != nil {
				return nil, err
			}
			buckets[bucketKey{c.PlayerID, component}] += c.Delta
		} else {
			if _, err := tx.Exec(ctx, `
				INSERT INTO character_events
					(player_id, news_item_id, trigger_word, delta, status, target_component,
					 source_ref, proposed_delta, final_delta, voting_status, voting_closes_at, auto_apply)
				VALUES ($1, $2, $3, $4, 'auto', $5, $6, $4, NULL, 'pending_vote', NOW() + INTERVAL '24 hours', FALSE)
			`, c.PlayerID, newsRef, c.TriggerWord, c.Delta, component, sourceRef); err != nil {
				return nil, err
			}
			// Intentionally NOT bucketed — score moves later, via finalize.
		}
	}

	if len(buckets) == 0 {
		// Everything was a duplicate; nothing changed.
		return nil, tx.Commit(ctx)
	}

	// Preload player names once per unique playerID for the response payload.
	uniquePlayers := make(map[int64]struct{})
	for key := range buckets {
		uniquePlayers[key.playerID] = struct{}{}
	}
	names := make(map[int64]string, len(uniquePlayers))
	for playerID := range uniquePlayers {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM players WHERE id = $1`, playerID).Scan(&name); err == nil {
			names[playerID] = name
		}
	}

	deltas := make([]domain.PlayerSyncDelta, 0, len(buckets))
	now := time.Now().UTC()
	for key, delta := range buckets {
		current, err := lockScore(ctx, tx, key.playerID)
		if err != nil {
			return nil, err
		}
		oldFRI := current.FRI

		var oldValue, newValue float64
		switch key.component {
		case "performance":
			// Performance events are additive on top of the live snapshot;
			// per-sync cap prevents one batch of fixtures from swinging
			// Performance by more than perPlayerCap.
			applied := delta
			if applied > perPlayerCap {
				applied = perPlayerCap
			}
			if applied < -perPlayerCap {
				applied = -perPlayerCap
			}
			oldValue = current.Performance
			newValue = clamp0to100(round1(oldValue + applied))
			current.Performance = newValue
			current.PerformanceUpdatedAt = now

		default: // "character" — full recompute from event history
			totalDelta, err := sumCharacterEventsForPlayer(ctx, tx, key.playerID)
			if err != nil {
				return nil, err
			}
			oldValue = current.Character
			newValue = clamp0to100(round1(characterBaseline + totalDelta))
			current.Character = newValue
			current.CharacterUpdatedAt = now
		}
		applyFriFormula(current)

		// Single UPDATE that always writes performance + character + their
		// timestamps. Cheap and avoids branching the SQL on component.
		if _, err := tx.Exec(ctx, `
			UPDATE fri_scores
			SET performance = $2,
			    character = $3,
			    fri = $4,
			    trend_value = $5,
			    trend_direction = $6,
			    calculated_at = $7,
			    performance_updated_at = $8,
			    character_updated_at = $9
			WHERE player_id = $1
		`,
			key.playerID,
			current.Performance, current.Character,
			current.FRI, current.TrendValue, current.TrendDirection, current.CalculatedAt,
			current.PerformanceUpdatedAt, current.CharacterUpdatedAt,
		); err != nil {
			return nil, err
		}

		if historyDelta := round1(current.FRI - oldFRI); historyDelta != 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO fri_history (player_id, fri, delta, calculated_at)
				VALUES ($1,$2,$3,$4)
			`, key.playerID, current.FRI, historyDelta, current.CalculatedAt); err != nil {
				return nil, err
			}
		}

		deltas = append(deltas, domain.PlayerSyncDelta{
			PlayerID:    key.playerID,
			PlayerName:  names[key.playerID],
			Component:   key.component,
			OldValue:    oldValue,
			NewValue:    newValue,
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

// normalizeTargetComponent maps empty/legacy values to the default and
// rejects unknown components.
func normalizeTargetComponent(target string) string {
	switch strings.TrimSpace(strings.ToLower(target)) {
	case "performance":
		return "performance"
	default:
		return "character"
	}
}

// nullableInt64 turns a zero-valued ID into NULL so the partial index on
// (player_id, news_item_id, trigger_word) WHERE news_item_id IS NOT NULL
// only sees rows that actually have a news linkage.
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func clamp0to100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// characterBaseline is the neutral starting point for every player's
// Character score before any events have fired. Picked at 80 so:
//   - A player with no incidents lands at 80 (good citizen, not perfect)
//   - Most positive triggers (+0.5 ... +1.5) push them up to 85–95
//   - Severe negative triggers (doping -8, racism -6) bring them below 70
//   - The 0–100 clamp at the boundaries handles extreme accumulations
//
// Aligns with the partner's "baseline + grows/falls" model (chat 2026-05-09).
// No more inherited seed values — Character is fully events-driven.
const characterBaseline = 80.0

// sumCharacterEventsForPlayer returns the algebraic sum of *final* delta
// across all character-targeted events for a player. Pending-vote events
// don't contribute until they're finalized — that's the whole point of
// Phase 5 voting: the community gets a 24h window to override the proposed
// delta before it actually moves the score.
//
// Performance-targeted events are excluded (they live in their own column
// and are intentionally ephemeral; see ApplyCharacterSync doc).
//
// COALESCE(final_delta, delta) handles legacy rows from before migration 012
// where final_delta is NULL — those rows had `delta` applied directly under
// the old model, so we keep using that value to preserve historical state.
func sumCharacterEventsForPlayer(ctx context.Context, tx pgx.Tx, playerID int64) (float64, error) {
	var sum float64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(final_delta, delta)), 0)
		FROM character_events
		WHERE player_id = $1
		  AND COALESCE(target_component, 'character') = 'character'
		  AND COALESCE(voting_status, 'auto_applied') IN ('auto_applied', 'finalized')
	`, playerID).Scan(&sum)
	return sum, err
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

// GetCareerBaseline returns the persisted baseline row for a player, or
// (nil, nil) when none has been computed yet. The performance sync uses this
// to blend long-term career data with the current season — see
// blendBaselineIntoPerformance in phase2_sync.go.
func (r *Repository) GetCareerBaseline(ctx context.Context, playerID int64) (*domain.PlayerCareerBaseline, error) {
	var b domain.PlayerCareerBaseline
	err := r.pool.QueryRow(ctx, `
		SELECT player_id, seasons_played, seasons_lookback,
		       career_appearances, career_minutes, career_goals, career_assists,
		       career_avg_rating, career_trophies_count,
		       baseline_score, computed_at
		FROM player_career_baseline
		WHERE player_id = $1
	`, playerID).Scan(
		&b.PlayerID,
		&b.SeasonsPlayed, &b.SeasonsLookback,
		&b.CareerAppearances, &b.CareerMinutes, &b.CareerGoals, &b.CareerAssists,
		&b.CareerAvgRating, &b.CareerTrophiesCount,
		&b.BaselineScore, &b.ComputedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// UpsertCareerBaseline writes (or refreshes) a player's career snapshot.
// computed_at is always set to "now" server-side so concurrent writers
// can't fight over the timestamp.
func (r *Repository) UpsertCareerBaseline(ctx context.Context, b domain.PlayerCareerBaseline) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO player_career_baseline (
			player_id, seasons_played, seasons_lookback,
			career_appearances, career_minutes, career_goals, career_assists,
			career_avg_rating, career_trophies_count, baseline_score, computed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now())
		ON CONFLICT (player_id) DO UPDATE SET
			seasons_played = EXCLUDED.seasons_played,
			seasons_lookback = EXCLUDED.seasons_lookback,
			career_appearances = EXCLUDED.career_appearances,
			career_minutes = EXCLUDED.career_minutes,
			career_goals = EXCLUDED.career_goals,
			career_assists = EXCLUDED.career_assists,
			career_avg_rating = EXCLUDED.career_avg_rating,
			career_trophies_count = EXCLUDED.career_trophies_count,
			baseline_score = EXCLUDED.baseline_score,
			computed_at = now()
	`,
		b.PlayerID, b.SeasonsPlayed, b.SeasonsLookback,
		b.CareerAppearances, b.CareerMinutes, b.CareerGoals, b.CareerAssists,
		b.CareerAvgRating, b.CareerTrophiesCount, b.BaselineScore,
	)
	return err
}

// ────────────────────────────────────────────────────────────────────────
// Phase 5 — per-event fan voting
// ────────────────────────────────────────────────────────────────────────

// ListPendingEventsForPlayer returns events still accepting votes for a
// given player. The caller renders each as a vote-slider card. `limit`
// caps the number returned; pass 0 for the default (50).
func (r *Repository) ListPendingEventsForPlayer(ctx context.Context, playerID int64, limit int) ([]domain.PendingEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	return r.listPendingEvents(ctx, &playerID, limit)
}

// ListPendingEvents returns ALL pending events across the system. Used by
// the site-wide "Vote on today's events" page.
func (r *Repository) ListPendingEvents(ctx context.Context, limit int) ([]domain.PendingEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.listPendingEvents(ctx, nil, limit)
}

// listPendingEvents is the shared implementation. playerID == nil → fetch
// all players; non-nil → scope to that player. The vote-count and median
// are computed by a left-joined subquery to avoid the N+1 trap.
func (r *Repository) listPendingEvents(ctx context.Context, playerID *int64, limit int) ([]domain.PendingEvent, error) {
	args := []any{limit}
	scope := ""
	if playerID != nil {
		args = append(args, *playerID)
		scope = "AND ce.player_id = $2"
	}

	// PostgreSQL has no median aggregate built-in, but PERCENTILE_CONT(0.5)
	// over the suggested_delta returns it. We compute it per event in a
	// LATERAL subquery rather than as a window — clearer plan + same cost.
	q := fmt.Sprintf(`
		SELECT
			ce.id,
			ce.player_id,
			p.name,
			ce.trigger_word,
			COALESCE(ce.target_component, 'character'),
			COALESCE(ce.proposed_delta, ce.delta),
			ce.news_item_id,
			COALESCE(n.title_en, ''),
			ce.detected_at,
			ce.voting_closes_at,
			COALESCE(v.cnt, 0)        AS votes_count,
			v.median                  AS votes_median
		FROM character_events ce
		JOIN players p ON p.id = ce.player_id
		LEFT JOIN news_items n ON n.id = ce.news_item_id
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) AS cnt,
				PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY suggested_delta) AS median
			FROM event_votes
			WHERE event_id = ce.id
		) v ON TRUE
		WHERE ce.voting_status = 'pending_vote'
		  AND (ce.voting_closes_at IS NULL OR ce.voting_closes_at > NOW())
		  %s
		ORDER BY ce.detected_at DESC
		LIMIT $1
	`, scope)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.PendingEvent, 0)
	for rows.Next() {
		var e domain.PendingEvent
		var newsID *int64
		var median *float64
		if err := rows.Scan(
			&e.ID, &e.PlayerID, &e.PlayerName, &e.TriggerWord, &e.TargetComponent,
			&e.ProposedDelta, &newsID, &e.NewsTitle,
			&e.DetectedAt, &e.VotingClosesAt, &e.VotesCount, &median,
		); err != nil {
			return nil, err
		}
		e.NewsItemID = newsID
		e.VotesMedian = median
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetPendingEvent returns one pending event with its vote stats, or
// (nil, nil) if no such event exists or it's no longer pending.
func (r *Repository) GetPendingEvent(ctx context.Context, eventID int64) (*domain.PendingEvent, error) {
	var e domain.PendingEvent
	var newsID *int64
	var median *float64
	err := r.pool.QueryRow(ctx, `
		SELECT
			ce.id, ce.player_id, p.name, ce.trigger_word,
			COALESCE(ce.target_component, 'character'),
			COALESCE(ce.proposed_delta, ce.delta),
			ce.news_item_id, COALESCE(n.title_en, ''),
			ce.detected_at, ce.voting_closes_at,
			COALESCE((SELECT COUNT(*) FROM event_votes WHERE event_id = ce.id), 0),
			(SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY suggested_delta)
			 FROM event_votes WHERE event_id = ce.id)
		FROM character_events ce
		JOIN players p ON p.id = ce.player_id
		LEFT JOIN news_items n ON n.id = ce.news_item_id
		WHERE ce.id = $1 AND ce.voting_status = 'pending_vote'
	`, eventID).Scan(
		&e.ID, &e.PlayerID, &e.PlayerName, &e.TriggerWord, &e.TargetComponent,
		&e.ProposedDelta, &newsID, &e.NewsTitle,
		&e.DetectedAt, &e.VotingClosesAt, &e.VotesCount, &median,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	e.NewsItemID = newsID
	e.VotesMedian = median
	return &e, nil
}

// SubmitEventVote records (or updates) a fan's slider vote on one pending
// event. Returns:
//   - inserted=true: brand-new vote from this IP
//   - inserted=false: existing vote updated (idempotent re-vote)
//   - error == errEventNotVotable: event is finalized / closed / doesn't exist
//
// The handler clamps suggested_delta to [-5, +5] before calling here.
func (r *Repository) SubmitEventVote(ctx context.Context, eventID int64, ipHash string, suggestedDelta float64) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var status string
	var closesAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT voting_status, voting_closes_at
		FROM character_events
		WHERE id = $1
	`, eventID).Scan(&status, &closesAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrEventNotVotable
		}
		return false, err
	}
	if status != "pending_vote" || closesAt == nil || time.Now().UTC().After(*closesAt) {
		return false, ErrEventNotVotable
	}

	// UPSERT — same (event_id, ip_hash) replaces the suggested_delta. This
	// lets a fan change their mind during the voting window without us
	// stuffing the median with multiple votes from one IP.
	tag, err := tx.Exec(ctx, `
		INSERT INTO event_votes (event_id, ip_hash, suggested_delta)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id, ip_hash)
		DO UPDATE SET suggested_delta = EXCLUDED.suggested_delta,
		              created_at = NOW()
	`, eventID, ipHash, suggestedDelta)
	if err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	// tag.RowsAffected() is 1 for both INSERT and UPDATE under ON CONFLICT.
	// To distinguish them we'd need RETURNING — for now the caller treats
	// both as "ok". Keeping this for future extensibility.
	_ = tag
	return true, nil
}

// FinalizePendingEvents picks all events past their voting_closes_at,
// computes the median vote (or falls back to proposed_delta if no votes),
// writes final_delta and voting_status='finalized', then refreshes the
// affected component scores. Designed to be idempotent — a partial run can
// safely re-fire.
//
// Returns the number of events finalized.
func (r *Repository) FinalizePendingEvents(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Pull the expired pending events with their median in one shot. We
	// also pick up the proposed_delta as a fallback for zero-vote events.
	rows, err := tx.Query(ctx, `
		SELECT
			ce.id,
			ce.player_id,
			COALESCE(ce.target_component, 'character'),
			COALESCE(ce.proposed_delta, ce.delta),
			(SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY suggested_delta)
			 FROM event_votes WHERE event_id = ce.id) AS median,
			(SELECT COUNT(*) FROM event_votes WHERE event_id = ce.id) AS votes
		FROM character_events ce
		WHERE ce.voting_status = 'pending_vote'
		  AND ce.voting_closes_at IS NOT NULL
		  AND ce.voting_closes_at <= NOW()
		FOR UPDATE OF ce
	`)
	if err != nil {
		return 0, err
	}

	type finalizeRow struct {
		eventID   int64
		playerID  int64
		component string
		fallback  float64
		median    *float64
		votes     int
	}
	var batch []finalizeRow
	for rows.Next() {
		var r finalizeRow
		if err := rows.Scan(&r.eventID, &r.playerID, &r.component, &r.fallback, &r.median, &r.votes); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, r)
	}
	rows.Close()

	if len(batch) == 0 {
		return 0, tx.Commit(ctx)
	}

	// Set final_delta + voting_status='finalized' for each event.
	affectedPlayers := make(map[int64]map[string]struct{})
	for _, r := range batch {
		final := r.fallback
		if r.median != nil {
			final = round1(*r.median)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE character_events
			SET final_delta = $2,
			    voting_status = 'finalized'
			WHERE id = $1
		`, r.eventID, final); err != nil {
			return 0, err
		}
		if affectedPlayers[r.playerID] == nil {
			affectedPlayers[r.playerID] = make(map[string]struct{})
		}
		affectedPlayers[r.playerID][r.component] = struct{}{}
	}

	// Refresh each affected (player, component) score from scratch — same
	// model as the post-Phase-4 character recompute. For performance-target
	// events we apply the cumulative final_delta on top of the live snapshot.
	now := time.Now().UTC()
	for playerID, comps := range affectedPlayers {
		current, err := lockScore(ctx, tx, playerID)
		if err != nil {
			return 0, err
		}
		oldFRI := current.FRI
		for component := range comps {
			switch component {
			case "performance":
				// Sum all finalized performance event deltas for this player
				// and ADD them to the live snapshot value. Performance is
				// snapshot-driven; events shift it.
				var sumPerf float64
				if err := tx.QueryRow(ctx, `
					SELECT COALESCE(SUM(COALESCE(final_delta, delta)), 0)
					FROM character_events
					WHERE player_id = $1
					  AND target_component = 'performance'
					  AND voting_status IN ('auto_applied', 'finalized')
				`, playerID).Scan(&sumPerf); err != nil {
					return 0, err
				}
				// Get the latest performance snapshot's normalized score —
				// that's our true "current season" baseline before events.
				var baseSnapshot float64
				if err := tx.QueryRow(ctx, `
					SELECT COALESCE(MAX(normalized_score), $2)
					FROM performance_snapshots
					WHERE player_id = $1
					ORDER BY snapshot_at DESC
					LIMIT 1
				`, playerID, current.Performance).Scan(&baseSnapshot); err != nil {
					// No snapshot history — keep current value
					baseSnapshot = current.Performance
				}
				current.Performance = clamp0to100(round1(baseSnapshot + sumPerf))
				current.PerformanceUpdatedAt = now

			default: // character
				totalDelta, err := sumCharacterEventsForPlayer(ctx, tx, playerID)
				if err != nil {
					return 0, err
				}
				current.Character = clamp0to100(round1(characterBaseline + totalDelta))
				current.CharacterUpdatedAt = now
			}
		}
		applyFriFormula(current)

		if _, err := tx.Exec(ctx, `
			UPDATE fri_scores
			SET performance = $2,
			    character = $3,
			    fri = $4,
			    trend_value = $5,
			    trend_direction = $6,
			    calculated_at = $7,
			    performance_updated_at = $8,
			    character_updated_at = $9
			WHERE player_id = $1
		`,
			playerID, current.Performance, current.Character,
			current.FRI, current.TrendValue, current.TrendDirection, current.CalculatedAt,
			current.PerformanceUpdatedAt, current.CharacterUpdatedAt,
		); err != nil {
			return 0, err
		}
		if historyDelta := round1(current.FRI - oldFRI); historyDelta != 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO fri_history (player_id, fri, delta, calculated_at)
				VALUES ($1,$2,$3,$4)
			`, playerID, current.FRI, historyDelta, current.CalculatedAt); err != nil {
				return 0, err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// ErrEventNotVotable is returned when the caller tries to vote on a finalized
// event, a non-existent event, or one whose voting window has closed. The
// HTTP layer maps it to 410 Gone.
var ErrEventNotVotable = errors.New("event is not accepting votes")

func (r *Repository) ApplyMediaSync(ctx context.Context, results []domain.MediaSyncPlayerResult, provider string) ([]domain.PlayerSyncDelta, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Each media sync replaces the full media news set. We delete every
	// known media-provider source (not just the current one), so switching
	// providers — e.g. google-news-rss → gdelt → mediastack — doesn't leave
	// stale articles from earlier providers in the feed.
	if _, err := tx.Exec(ctx, `
		DELETE FROM news_items
		WHERE source IN ('mediastack', 'gdelt', 'google-news-rss', 'legacy-html')
	`); err != nil {
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

// fanBaseline is the neutral starting Fan Poll score every player has before
// any verified votes accumulate. Same philosophy as characterBaseline (Phase
// 4 ext-A): "baseline + grows/falls" per partner's vision (2026-05-09).
// fan_base column is kept around for back-compat but is no longer the
// driver — votes are.
const fanBaseline = 50.0

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

	var voteCount int
	var avgInternal float64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(AVG(internal_score), 0)
		FROM fan_votes
		WHERE player_id = $1
	`, playerID).Scan(&voteCount, &avgInternal); err != nil {
		return nil, nil, err
	}

	// New event-driven semantics: with zero votes the score sits at the
	// neutral baseline (50). When votes arrive the score is the
	// vote-average, clamped to 0..100. No seeded fan_base influence — that
	// was the bias that put Bellingham at 95 even after his form fell off.
	var newFan float64
	if voteCount == 0 {
		newFan = fanBaseline
	} else {
		newFan = round1(clamp0to100(avgInternal))
	}
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

// applyFriFormula computes the overall FRI from the four component scores.
// Phase 5 (2026-05-13): Fan is no longer a component — fans now contribute
// via per-event voting. The 0.20 previously held by Fan is redistributed
// across the four remaining signals.
//
//	Performance × 0.40    (was 0.35)
//	Social      × 0.25    (was 0.20)
//	Media       × 0.20    (was 0.15)
//	Character   × 0.15    (was 0.10)
//	──────────────────────────────────
//	                1.00   (Fan = 0)
//
// `score.Fan` is intentionally NOT read — the column is kept for back-compat
// with API consumers that still read it but it doesn't influence FRI.
func applyFriFormula(score *domain.Score) {
	previousFRI := score.FRI
	score.FRI = round1((score.Performance * 0.40) + (score.Social * 0.25) + (score.Media * 0.20) + (score.Character * 0.15))
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
