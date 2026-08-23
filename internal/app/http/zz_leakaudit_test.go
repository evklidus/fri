package http

import (
	"encoding/json"
	"testing"
	"time"

	"fri.local/football-reputation-index/internal/domain"
)

func TestZZLeakAuditMaskedJSON(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	bd := time.Date(1999, 5, 4, 0, 0, 0, 0, time.UTC)
	in := []domain.PlayerWithScore{{
		Player: domain.Player{
			ID: 7, Slug: "kylian-mbappe", Name: "Kylian Mbappe", Club: "Real Madrid",
			League: "La Liga", Position: "FW", Age: 27, BirthDate: &bd, Emoji: "🇫🇷",
			PhotoData: "data:image/png;base64,AAA", PhotoURL: "https://cdn/x.png",
			ThemeBackground: "linear-gradient(135deg,#febe10,#00529f)",
			SummaryEN:       "en", SummaryRU: "ru", CreatedAt: now, UpdatedAt: now,
		},
		Score: domain.Score{PlayerID: 7, FRI: 94.2, Performance: 9, Social: 9,
			Fan: 9, FanBase: 9, Media: 9, Character: 9, TrendValue: 1.2,
			TrendDirection: "up", CalculatedAt: now, PerformanceUpdatedAt: now},
	}}
	out := maskLockedPlayers(in, false)
	b, _ := json.MarshalIndent(out[0], "", "  ")
	t.Log("\nMASKED ROW JSON:\n" + string(b))
}
