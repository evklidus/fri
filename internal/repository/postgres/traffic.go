package postgres

import (
	"context"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

// FlushTraffic writes a batch of buffered counters. Views are added to
// whatever is already recorded for that day and section, so a flush is safe
// to repeat and two app instances can both report into the same day.
//
// Visitors are inserted and ignored on conflict: the primary key is what
// makes the count distinct, so the caller does not have to remember who it
// has already seen today.
func (r *Repository) FlushTraffic(ctx context.Context, views map[domain.EntryPoint]int64, visitors []string, day time.Time) error {
	if len(views) == 0 && len(visitors) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for key, count := range views {
		if count <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO traffic_daily (day, section, views)
			VALUES ($1, $2, $3)
			ON CONFLICT (day, section) DO UPDATE SET views = traffic_daily.views + EXCLUDED.views
		`, day, key.Section, count); err != nil {
			return err
		}
	}
	for _, hash := range visitors {
		if _, err := tx.Exec(ctx, `
			INSERT INTO traffic_visitors (day, visitor_hash)
			VALUES ($1, $2)
			ON CONFLICT (day, visitor_hash) DO NOTHING
		`, day, hash); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// TrafficDays returns one row per day in the trailing window, including the
// days nothing happened — a gap in a chart reads as "no data", which is a
// different claim from "nobody came".
func (r *Repository) TrafficDays(ctx context.Context, days int) ([]domain.TrafficDay, error) {
	rows, err := r.pool.Query(ctx, `
		WITH window_days AS (
			SELECT generate_series(
				(CURRENT_DATE - ($1::int - 1)),
				CURRENT_DATE,
				INTERVAL '1 day'
			)::date AS day
		)
		SELECT
			to_char(w.day, 'YYYY-MM-DD'),
			COALESCE(SUM(t.views) FILTER (WHERE t.section <> 'api'), 0),
			COALESCE(SUM(t.views) FILTER (WHERE t.section = 'api'), 0),
			(SELECT count(*) FROM traffic_visitors v WHERE v.day = w.day)
		FROM window_days w
		LEFT JOIN traffic_daily t ON t.day = w.day
		GROUP BY w.day
		ORDER BY w.day
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.TrafficDay
	for rows.Next() {
		var d domain.TrafficDay
		if err := rows.Scan(&d.Day, &d.PageLoads, &d.APICalls, &d.Visitors); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EntryPoints ranks the sections visitors arrived on over the window. The
// API bucket is left out: it is machine traffic from our own pages, not a
// place anyone landed.
func (r *Repository) EntryPoints(ctx context.Context, days int) ([]domain.EntryPoint, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT section, SUM(views)
		FROM traffic_daily
		WHERE day > CURRENT_DATE - $1::int AND section <> 'api'
		GROUP BY section
		ORDER BY SUM(views) DESC
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.EntryPoint
	for rows.Next() {
		var e domain.EntryPoint
		if err := rows.Scan(&e.Section, &e.Views); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SignupDays is registrations per day over the window, zero-filled for the
// same reason TrafficDays is.
func (r *Repository) SignupDays(ctx context.Context, days int) ([]domain.SignupDay, error) {
	rows, err := r.pool.Query(ctx, `
		WITH window_days AS (
			SELECT generate_series(
				(CURRENT_DATE - ($1::int - 1)),
				CURRENT_DATE,
				INTERVAL '1 day'
			)::date AS day
		)
		SELECT to_char(w.day, 'YYYY-MM-DD'), count(u.id)
		FROM window_days w
		LEFT JOIN users u ON u.created_at::date = w.day
		GROUP BY w.day
		ORDER BY w.day
	`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.SignupDay
	for rows.Next() {
		var s domain.SignupDay
		if err := rows.Scan(&s.Day, &s.Users); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UserTotals is the account summary. "Active" means signed in at least once
// in the last week, which is the only activity signal accounts carry.
func (r *Repository) UserTotals(ctx context.Context) (domain.UserTotals, error) {
	var t domain.UserTotals
	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE created_at > now() - interval '7 days'),
			count(*) FILTER (WHERE created_at > now() - interval '30 days'),
			count(*) FILTER (WHERE last_login_at > now() - interval '7 days'),
			count(*) FILTER (WHERE is_admin)
		FROM users
	`).Scan(&t.Total, &t.NewLast7Days, &t.NewLast30Days, &t.ActiveLast7Days, &t.Admins)
	return t, err
}

// ContentTotals is what the system holds right now, so an empty dashboard
// can be told apart from a sync that has stopped producing.
func (r *Repository) ContentTotals(ctx context.Context) (domain.ContentTotals, error) {
	var t domain.ContentTotals
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM players),
			(SELECT count(*) FROM news_items),
			(SELECT count(*) FROM character_events WHERE voting_status = 'pending'),
			(SELECT count(*) FROM fan_votes)
	`).Scan(&t.Players, &t.NewsItems, &t.PendingEvents, &t.VotesAllTime)
	return t, err
}
