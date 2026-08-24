package http

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestStaticFilesNameNoPlayers guards a leak that no API change can fix.
//
// The i18n dictionary carried `poll_rate:'Rate K. Mbappé'` and a matching club
// and position, left over from a "rate the player of the day" block that was
// removed. Nothing rendered them, but they shipped in the page source to every
// anonymous visitor — and Mbappé was rank 1. View-source and Ctrl-F defeated
// the entire leaderboard gate without a single API call.
//
// Player-specific copy has to come from the API, which is gated. Static copy
// is not, so this test fails if a player's name reappears in the shipped
// files. Names are read from the seed HTML so the list can't drift.
func TestStaticFilesNameNoPlayers(t *testing.T) {
	seed, err := os.ReadFile("../../../web/source/fri-index.html")
	if err != nil {
		t.Skipf("seed roster unavailable: %v", err)
	}

	// Player entries in the seed are objects starting with a rank, so anchor on
	// that. Matching a bare `name:'...'` swept up i18n keys like
	// `p1_name:'Players'` and failed on ordinary UI copy.
	nameRe := regexp.MustCompile(`\{\s*rank\s*:\s*\d+\s*,[^}]*?\bname\s*:\s*'([^']{3,40})'`)
	matches := nameRe.FindAllStringSubmatch(string(seed), -1)
	if len(matches) == 0 {
		t.Skip("no player names found in the seed file; nothing to guard")
	}

	surnames := make(map[string]bool)
	for _, m := range matches {
		full := strings.TrimSpace(m[1])
		// The distinctive half is the surname: "K. Mbappé" → "Mbappé".
		parts := strings.Fields(full)
		last := parts[len(parts)-1]
		// Skip initials and short tokens that would match ordinary prose.
		if len([]rune(last)) < 5 {
			continue
		}
		surnames[last] = true
	}

	for _, file := range []string{"../../../web/static/index.html", "../../../web/static/assets/live.js"} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(content)
		for surname := range surnames {
			if strings.Contains(text, surname) {
				t.Errorf("%s contains the player name %q. Anything player-specific must come from the "+
					"API, which withholds the top five; static copy ships to everyone.", file, surname)
			}
		}
	}
}
