package legacyhtml

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"fri.local/football-reputation-index/internal/domain"
	"github.com/dop251/goja"
)

type ParsedLegacyData struct {
	Players []domain.LegacyPlayer
	News    []domain.LegacyNews
}

func ParseFile(path string) (*ParsedLegacyData, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read legacy html: %w", err)
	}

	source := string(content)
	playersLiteral, err := extractArrayLiteral(source, "const players = ")
	if err != nil {
		return nil, err
	}

	newsLiteral, err := extractArrayLiteral(source, "const news = ")
	if err != nil {
		return nil, err
	}

	vm := goja.New()
	if _, err := vm.RunString("var players = " + playersLiteral + "; var news = " + newsLiteral + ";"); err != nil {
		return nil, fmt.Errorf("evaluate legacy arrays: %w", err)
	}

	var rawPlayers []map[string]any
	if err := vm.ExportTo(vm.Get("players"), &rawPlayers); err != nil {
		return nil, fmt.Errorf("export players: %w", err)
	}

	var rawNews []map[string]any
	if err := vm.ExportTo(vm.Get("news"), &rawNews); err != nil {
		return nil, fmt.Errorf("export news: %w", err)
	}

	players := make([]domain.LegacyPlayer, 0, len(rawPlayers))
	for _, item := range rawPlayers {
		players = append(players, domain.LegacyPlayer{
			Rank:   toInt(item["rank"]),
			Emoji:  toString(item["emoji"]),
			Name:   toString(item["name"]),
			Club:   toString(item["club"]),
			Pos:    toString(item["pos"]),
			Age:    toInt(item["age"]),
			FRI:    toFloat(item["fri"]),
			Perf:   toFloat(item["perf"]),
			Social: toFloat(item["social"]),
			Fan:    toFloat(item["fan"]),
			Media:  toFloat(item["media"]),
			Char:   toFloat(item["char"]),
			Trend:  toString(item["trend"]),
			Dir:    toString(item["dir"]),
			BG:     toString(item["bg"]),
			Photo:  toString(item["photo"]),
			SumEN:  toString(item["sum_en"]),
			SumRU:  toString(item["sum_ru"]),
		})
	}

	newsItems := make([]domain.LegacyNews, 0, len(rawNews))
	for _, item := range rawNews {
		newsItems = append(newsItems, domain.LegacyNews{
			Player:    toString(item["player"]),
			Impact:    toString(item["impact"]),
			Delta:     toString(item["delta"]),
			Time:      toString(item["time"]),
			TitleEN:   toString(item["t_en"]),
			TitleRU:   toString(item["t_ru"]),
			SummaryEN: toString(item["s_en"]),
			SummaryRU: toString(item["s_ru"]),
		})
	}

	return &ParsedLegacyData{
		Players: players,
		News:    newsItems,
	}, nil
}

func extractArrayLiteral(source, marker string) (string, error) {
	start := strings.Index(source, marker)
	if start == -1 {
		return "", fmt.Errorf("marker %q not found", marker)
	}

	bracketStart := strings.Index(source[start:], "[")
	if bracketStart == -1 {
		return "", fmt.Errorf("array start for %q not found", marker)
	}

	i := start + bracketStart
	depth := 0
	inString := false
	var quote byte

	for ; i < len(source); i++ {
		ch := source[i]

		if inString {
			if ch == '\\' {
				i++
				continue
			}
			if ch == quote {
				inString = false
			}
			continue
		}

		if ch == '\'' || ch == '"' {
			inString = true
			quote = ch
			continue
		}

		if ch == '[' {
			depth++
			continue
		}

		if ch == ']' {
			depth--
			if depth == 0 {
				return source[start+bracketStart : i+1], nil
			}
		}
	}

	return "", fmt.Errorf("unterminated array for %q", marker)
}

func toString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func toInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func toFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	default:
		return 0
	}
}
