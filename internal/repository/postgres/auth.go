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

// DeleteNewsItem removes one article. The FK from character_events is ON
// DELETE CASCADE (migration 014), so any rating event this article triggered
// dies with it — which is the intent: if the article was wrong enough to
// delete, the score change it caused was wrong too.
//
// Returns false when no such row existed, so the handler can answer 404
// rather than pretending it deleted something.
func (r *Repository) DeleteNewsItem(ctx context.Context, id int64) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM news_items WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
