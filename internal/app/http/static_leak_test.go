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

// TestInlineScriptStringsAreTerminated is the regression for six days of a
// dead site.
//
// On 2026-09-03 the About copy for Field Performance was rewritten to say
// "...so stars don't crater during an off year" and dropped into the i18n
// dictionary between single quotes. The apostrophe closed the string, the
// whole inline <script> stopped parsing, and with it went the dictionary,
// the router and the data loaders. Every page still returned 200 with the
// full HTML — the shell rendered, the nav worked, and the leaderboard,
// news feed and top players were simply empty. Nothing in the suite
// noticed: `node --check` ran against live.js, which was fine, and the
// leak test above only reads the file as text.
//
// A single- or double-quoted JavaScript string cannot span a line break, so
// an unterminated one at end of line is always a syntax error. That is the
// exact shape of this bug and of any apostrophe someone types into copy
// later. Checked here rather than with a JS toolchain so it runs wherever
// `go test` does.
func TestInlineScriptStringsAreTerminated(t *testing.T) {
	page, err := os.ReadFile("../../../web/static/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}

	scripts := regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`).FindAllStringSubmatch(string(page), -1)
	if len(scripts) == 0 {
		t.Fatal("no <script> blocks found — has the page been restructured?")
	}

	checked := 0
	for _, script := range scripts {
		body := script[1]
		if strings.TrimSpace(body) == "" {
			continue // <script src="..."></script>
		}
		checked++
		for lineNo, line := range strings.Split(body, "\n") {
			if quote, ok := unterminatedQuote(line); ok {
				t.Errorf("index.html inline script, line %d: unterminated %c-quoted string — an apostrophe in copy does this, and it stops the whole script from parsing:\n  %s",
					lineNo+1, quote, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no inline script bodies were checked")
	}
}

// unterminatedQuote reports whether a line of JavaScript ends inside a
// single- or double-quoted string. Template literals are ignored: they may
// span lines legitimately, so a line that opens one is not evidence of
// anything. Regex literals are tracked because a character class can hold
// quote characters — `/[&<>"']/g` is real code in this page — and reading
// those as string delimiters was a false alarm the first version of this
// test raised.
func unterminatedQuote(line string) (byte, bool) {
	const regexAllowedBefore = "(,=:[!&|?{};+-*%~^"

	var quote byte
	inRegex, inClass := false, false
	prev := byte(0) // last meaningful character, for telling regex from division

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		case inRegex:
			switch {
			case c == '\\':
				i++
			case c == '[':
				inClass = true
			case c == ']':
				inClass = false
			case c == '/' && !inClass:
				inRegex = false
			}
			continue
		case c == '\'' || c == '"':
			quote = c
		case c == '`':
			// A template literal may run past this line; nothing here can
			// tell, so stop reading rather than guess.
			return 0, false
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return 0, false // line comment
		case c == '/' && i+1 < len(line) && line[i+1] == '*':
			return 0, false // block comment, may span lines
		case c == '/':
			if prev == 0 || strings.IndexByte(regexAllowedBefore, prev) >= 0 || endsWithRegexKeyword(line[:i]) {
				inRegex, inClass = true, false
			}
		}
		if c != ' ' && c != '\t' {
			prev = c
		}
	}
	return quote, quote != 0
}

// endsWithRegexKeyword covers the keyword positions where a slash starts a
// regex rather than a division — `return /x/.test(s)` and friends.
func endsWithRegexKeyword(before string) bool {
	before = strings.TrimRight(before, " \t")
	for _, kw := range []string{"return", "typeof", "case", "in", "of", "new", "delete", "void", "throw", "do", "else"} {
		if strings.HasSuffix(before, kw) {
			rest := before[:len(before)-len(kw)]
			if rest == "" {
				return true
			}
			last := rest[len(rest)-1]
			isWord := last == '_' || last == '$' || (last >= 'a' && last <= 'z') || (last >= 'A' && last <= 'Z') || (last >= '0' && last <= '9')
			if !isWord {
				return true
			}
		}
	}
	return false
}
