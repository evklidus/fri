package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fri.local/football-reputation-index/internal/domain"
)

// ErrEmailTaken is returned when registration hits the unique index on
// users.email. Callers turn it into a 409 rather than a 500 — it's a normal
// thing for a person to do, not a failure.
var ErrEmailTaken = errors.New("email already registered")

// normalizeEmail is applied on every read and write so an account can't be
// duplicated by capitalisation or a stray space.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (r *Repository) CreateUser(ctx context.Context, email, passwordHash string, isAdmin bool) (domain.User, error) {
	var user domain.User
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, is_admin)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, is_admin, created_at, last_login_at
	`, normalizeEmail(email), passwordHash, isAdmin).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.IsAdmin, &user.CreatedAt, &user.LastLoginAt,
	)
	if err != nil {
		// 23505 is unique_violation; the only unique constraint here is email.
		if strings.Contains(err.Error(), "23505") {
			return domain.User{}, ErrEmailTaken
		}
		return domain.User{}, err
	}
	return user, nil
}

// GetUserByEmail returns the user or pgx.ErrNoRows. Callers must treat a
// missing user and a wrong password identically, so that the response can't
// be used to discover which addresses are registered.
func (r *Repository) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	var user domain.User
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, is_admin, created_at, last_login_at
		FROM users WHERE email = $1
	`, normalizeEmail(email)).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.IsAdmin, &user.CreatedAt, &user.LastLoginAt,
	)
	return user, err
}

func (r *Repository) TouchUserLogin(ctx context.Context, userID int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return err
}

func (r *Repository) CreateSession(ctx context.Context, token string, userID int64, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_sessions (token, user_id, expires_at) VALUES ($1, $2, $3)
	`, token, userID, expiresAt)
	return err
}

// UserBySessionToken resolves a cookie back to its account, rejecting expired
// sessions in the same query so an expired token can never authenticate.
func (r *Repository) UserBySessionToken(ctx context.Context, token string) (domain.User, error) {
	var user domain.User
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.password_hash, u.is_admin, u.created_at, u.last_login_at
		FROM user_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token = $1 AND s.expires_at > now()
	`, token).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.IsAdmin, &user.CreatedAt, &user.LastLoginAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, err
	}
	return user, err
}

func (r *Repository) DeleteSession(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM user_sessions WHERE token = $1`, token)
	return err
}

// DeleteExpiredSessions clears rows nobody can use any more. Called from the
// same scheduler tick that finalizes events.
func (r *Repository) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountUsers backs the registered-user number the founders report to
// investors, so it counts accounts rather than sessions.
func (r *Repository) CountUsers(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count)
	return count, err
}

// mediaNewsSourcesSQL lists every provider whose rows make up the news feed,
// for the sync that rotates them and the delete that rescores from them. One
// list, so a provider added to one path cannot be forgotten by the other.
const mediaNewsSourcesSQL = `'mediastack', 'gdelt', 'google-news-rss', 'legacy-html'`

// DeleteNewsItem removes one article from a player's feed and rescores the
// player from the articles that remain, in one transaction. Two things were
// missing here, and the partner noticed both on 2026-08-26:
//
//   - The media sync rebuilds the feed from scratch twice a day, so without
//     a record of the deletion the article was back within twelve hours.
//     The suppression row is that record; the sync reads it before scoring.
//   - The score was never recomputed, so the article's plus/minus outlived
//     the article. Media is now rebuilt from the remaining rows by the same
//     formula the sync uses, and FRI follows.
//
// Any character event the article triggered dies with it (FK cascade,
// migration 014), and Character is rebuilt from the events that remain.
// Returns nil, nil when no such row exists.
func (r *Repository) DeleteNewsItem(ctx context.Context, id int64, rescore func([]domain.ArticleStats) float64) (*domain.NewsDeletion, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	deletion := domain.NewsDeletion{NewsID: id}
	var (
		playerID  *int64
		sourceURL string
	)
	err = tx.QueryRow(ctx, `
		SELECT player_id, player_name, title_en, COALESCE(source_url, '')
		FROM news_items
		WHERE id = $1
		FOR UPDATE
	`, id).Scan(&playerID, &deletion.PlayerName, &deletion.Title, &sourceURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cascadedEvents int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM character_events WHERE news_item_id = $1`, id).Scan(&cascadedEvents); err != nil {
		return nil, err
	}

	var keyPlayer int64
	if playerID != nil {
		keyPlayer = *playerID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO news_suppressions (player_id, article_key, title)
		VALUES ($1, $2, $3)
		ON CONFLICT (player_id, article_key) DO NOTHING
	`, keyPlayer, domain.NewsArticleKey(sourceURL, deletion.Title), deletion.Title); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM news_items WHERE id = $1`, id); err != nil {
		return nil, err
	}

	if playerID == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &deletion, nil
	}
	deletion.PlayerID = *playerID

	rows, err := tx.Query(ctx, `
		SELECT COALESCE(sentiment, 0), COALESCE(source_tier, 60)
		FROM news_items
		WHERE player_id = $1 AND source IN (`+mediaNewsSourcesSQL+`)
	`, *playerID)
	if err != nil {
		return nil, err
	}
	var remaining []domain.ArticleStats
	for rows.Next() {
		var stats domain.ArticleStats
		if err := rows.Scan(&stats.Sentiment, &stats.SourceTier); err != nil {
			rows.Close()
			return nil, err
		}
		remaining = append(remaining, stats)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	deletion.Remaining = len(remaining)

	now := time.Now().UTC()
	score, delta, err := refreshComponentScore(ctx, tx, *playerID, "media", rescore(remaining), now)
	if err != nil {
		return nil, err
	}
	deletion.OldMedia, deletion.NewMedia = delta.OldComponentValue, score.Media
	deletion.OldFRI, deletion.NewFRI = delta.OldFRI, score.FRI

	if cascadedEvents > 0 {
		total, err := sumCharacterEventsForPlayer(ctx, tx, *playerID)
		if err != nil {
			return nil, err
		}
		score, _, err = refreshComponentScore(ctx, tx, *playerID, "character", clamp0to100(characterBaseline+total), now)
		if err != nil {
			return nil, err
		}
		deletion.NewFRI = score.FRI
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &deletion, nil
}

// ListNewsSuppressions is everything the moderators have removed, for the
// media sync to skip before it scores.
func (r *Repository) ListNewsSuppressions(ctx context.Context) ([]domain.NewsSuppression, error) {
	rows, err := r.pool.Query(ctx, `SELECT player_id, article_key FROM news_suppressions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.NewsSuppression
	for rows.Next() {
		var entry domain.NewsSuppression
		if err := rows.Scan(&entry.PlayerID, &entry.ArticleKey); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
