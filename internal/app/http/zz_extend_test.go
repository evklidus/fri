package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fri.local/football-reputation-index/internal/app/config"
	"fri.local/football-reputation-index/internal/domain"
)

func roster() []domain.PlayerWithScore {
	rows := []struct {
		id          int64
		name, club  string
		pos         string
		fri         float64
	}{
		{1, "K. Mbappé", "Real Madrid", "FW", 91},
		{2, "E. Haaland", "Man City", "FW", 90},
		{3, "J. Bellingham", "Real Madrid", "MF", 89},
		{4, "M. Salah", "Liverpool", "FW", 88},
		{5, "Raphinha", "FC Barcelona", "FW", 86},
		{6, "N'Golo Kanté", "Al Ittihad", "MF", 85},
		{7, "V. van Dijk", "Liverpool", "DF", 85},
		{8, "L. Yamal", "FC Barcelona", "FW", 83},
		{9, "T. Courtois", "Real Madrid", "GK", 80},
	}
	out := make([]domain.PlayerWithScore, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.PlayerWithScore{
			Player: domain.Player{ID: r.id, Slug: "s", Name: r.name, Club: r.club, Position: r.pos},
			Score:  domain.Score{PlayerID: r.id, FRI: r.fri},
		})
	}
	return out
}

func filtered(search, position, club string) []domain.PlayerWithScore {
	var out []domain.PlayerWithScore
	for _, p := range roster() {
		if search != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(search)) &&
			!strings.Contains(strings.ToLower(p.Club), strings.ToLower(search)) {
			continue
		}
		if position != "" && position != "all" && p.Position != position {
			continue
		}
		if club != "" && p.Club != club {
			continue
		}
		out = append(out, p)
	}
	return out
}

func get(t *testing.T, svc *fakeService, path string) map[string]any {
	t.Helper()
	r := NewRouter(config.Config{}, svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(stdhttp.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s -> %d %s", path, w.Code, w.Body.String())
	}
	return body
}

// EXTENSION 1: the search/club/position filters are an oracle on the withheld five.
func TestFilterOracle(t *testing.T) {
	svc := &fakeService{listPlayersFn: func(_ context.Context, s, p, c string) ([]domain.PlayerWithScore, error) {
		return filtered(s, p, c), nil
	}}

	names := func(path string) []string {
		body := get(t, svc, path)
		var out []string
		data, _ := body["data"].([]any)
		for _, raw := range data {
			row := raw.(map[string]any)
			if row["locked"] == true {
				out = append(out, "<LOCKED>")
				continue
			}
			out = append(out, row["name"].(string))
		}
		return out
	}

	full := names("/api/players")
	fmt.Println("unfiltered anonymous:", full)

	// (a) attribute leak, no wordlist: count per club vs visible names per club.
	for _, club := range []string{"Real Madrid", "Liverpool", "FC Barcelona", "Man City"} {
		f := names("/api/players?club=" + strings.ReplaceAll(club, " ", "+"))
		visible := 0
		for _, n := range full {
			for _, p := range roster() {
				if p.Name == n && p.Club == club {
					visible++
				}
			}
		}
		fmt.Printf("club=%-14s rows=%d visible-by-name=%d -> withheld-from-this-club=%d\n",
			club, len(f), visible, len(f)-visible)
	}

	// (b) membership oracle: name exists in roster but never appears unfiltered.
	for _, guess := range []string{"Mbappé", "Yamal", "Courtois", "Messi"} {
		hit := len(names("/api/players?search="+guess)) > 0
		shown := false
		for _, n := range full {
			if strings.Contains(n, guess) {
				shown = true
			}
		}
		fmt.Printf("guess=%-10s exists=%-5v shown-by-name=%-5v -> IN TOP FIVE: %v\n",
			guess, hit, shown, hit && !shown)
	}

	// (c) over-masking: a club filter blanks rows that are not globally top-5.
	fmt.Println("club=Liverpool anonymous:", names("/api/players?club=Liverpool"))
}

// EXTENSION 2: a news row with NULL player_id is never masked, whatever it says.
func TestNewsRowWithoutPlayerIDIsNeverMasked(t *testing.T) {
	svc := &fakeService{
		listPlayersFn: func(_ context.Context, s, p, c string) ([]domain.PlayerWithScore, error) {
			return filtered(s, p, c), nil
		},
		listNewsFn: func(context.Context, *int64) ([]domain.NewsItem, error) {
			id := int64(1)
			return []domain.NewsItem{
				{ID: 10, PlayerID: &id, PlayerName: "K. Mbappé", TitleEN: "linked row"},
				{ID: 11, PlayerID: nil, PlayerName: "K. Mbappé", TitleEN: "Mbappé scores hat-trick", SourceURL: "https://espn.com/k-mbappe-hat-trick"},
			}, nil
		},
	}
	body := get(t, svc, "/api/news/feed")
	for _, raw := range body["data"].([]any) {
		b, _ := json.Marshal(raw)
		fmt.Println("FEED ROW:", string(b))
	}
}
