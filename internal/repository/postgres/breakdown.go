package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"fri.local/football-reputation-index/internal/domain"
	"github.com/jackc/pgx/v5"
)

func competitionsJSON(lines []domain.CompetitionLine) []byte {
	if len(lines) == 0 {
		return nil
	}
	raw, err := json.Marshal(lines)
	if err != nil {
		return nil
	}
	return raw
}

// PlayerBreakdown gathers the inputs behind one player's FRI: the newest
// real performance snapshot, the career baseline, the newest social
// snapshot, a summary of the articles feeding Media, and the events that
// moved Character or Performance.
func (r *Repository) PlayerBreakdown(ctx context.Context, playerID int64) (*domain.PlayerBreakdown, error) {
	out := &domain.PlayerBreakdown{PlayerID: playerID}

	if err := r.pool.QueryRow(ctx, `
		SELECT player_id, fri, performance, social, fan, fan_base, media, character, trend_value, trend_direction, calculated_at
		FROM fri_scores WHERE player_id = $1
	`, playerID).Scan(&out.Score.PlayerID, &out.Score.FRI, &out.Score.Performance, &out.Score.Social, &out.Score.Fan,
		&out.Score.FanBase, &out.Score.Media, &out.Score.Character, &out.Score.TrendValue, &out.Score.TrendDirection,
		&out.Score.CalculatedAt); err != nil {
		return nil, err
	}

	var perf domain.PerformanceSnapshot
	var apps, mins, goals, assists *int
	var comps []byte
	err := r.pool.QueryRow(ctx, `
		SELECT provider, average_rating, goals_assists_per90, xg_xa_per90, position_rank_score, minutes_share,
		       form_score, last5_goals, last5_assists, last5_rating, normalized_score, snapshot_at,
		       appearances, minutes, goals, assists, competitions
		FROM performance_snapshots
		WHERE player_id = $1 AND provider = 'api-football'
		ORDER BY snapshot_at DESC LIMIT 1
	`, playerID).Scan(&perf.Provider, &perf.AverageRating, &perf.GoalsAssistsPer90, &perf.XGXAPer90, &perf.PositionRankScore,
		&perf.MinutesShare, &perf.FormScore, &perf.Last5Goals, &perf.Last5Assists, &perf.Last5Rating, &perf.NormalizedScore,
		&perf.SnapshotAt, &apps, &mins, &goals, &assists, &comps)
	switch {
	case err == nil:
		perf.PlayerID = playerID
		if apps != nil {
			perf.Appearances = *apps
		}
		if mins != nil {
			perf.Minutes = *mins
		}
		if goals != nil {
			perf.Goals = *goals
		}
		if assists != nil {
			perf.Assists = *assists
		}
		if len(comps) > 0 {
			_ = json.Unmarshal(comps, &perf.Competitions)
		}
		out.Performance = &perf
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	if baseline, err := r.GetCareerBaseline(ctx, playerID); err == nil && baseline != nil && baseline.SeasonsPlayed > 0 {
		out.Career = baseline
	}

	var social domain.SocialSnapshot
	err = r.pool.QueryRow(ctx, `
		SELECT provider, followers, engagement_rate, mentions_growth_7d, youtube_views_7d, normalized_score, snapshot_at
		FROM social_snapshots WHERE player_id = $1 ORDER BY snapshot_at DESC LIMIT 1
	`, playerID).Scan(&social.Provider, &social.Followers, &social.EngagementRate, &social.MentionsGrowth7D,
		&social.YouTubeViews7D, &social.NormalizedScore, &social.SnapshotAt)
	switch {
	case err == nil:
		social.PlayerID = playerID
		out.Social = &social
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	if err := r.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE impact_type = 'pos'),
		       count(*) FILTER (WHERE impact_type = 'neg'),
		       COALESCE(avg(sentiment), 0),
		       COALESCE(avg(source_tier), 0)
		FROM news_items WHERE player_id = $1 AND source IN (`+mediaNewsSourcesSQL+`)
	`, playerID).Scan(&out.Media.Articles, &out.Media.Positive, &out.Media.Negative,
		&out.Media.AvgSentiment, &out.Media.AvgTier); err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT trigger_word, COALESCE(target_component, 'character'), COALESCE(final_delta, delta),
		       COALESCE(voting_status, status), detected_at
		FROM character_events
		WHERE player_id = $1 AND COALESCE(voting_status, '') IN ('auto_applied', 'finalized', '')
		ORDER BY detected_at DESC LIMIT 30
	`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out.Character.Events = []domain.BreakdownEvent{}
	for rows.Next() {
		var e domain.BreakdownEvent
		if err := rows.Scan(&e.Trigger, &e.Component, &e.Delta, &e.Status, &e.At); err != nil {
			return nil, err
		}
		out.Character.Events = append(out.Character.Events, e)
	}
	out.Character.Baseline = characterBaseline
	return out, rows.Err()
}
