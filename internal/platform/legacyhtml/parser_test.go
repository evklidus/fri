package legacyhtml

import "testing"

func TestParseFile(t *testing.T) {
	data, err := ParseFile("../../../web/source/fri-index.html")
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}

	if len(data.Players) == 0 {
		t.Fatal("expected players to be parsed")
	}

	if len(data.News) == 0 {
		t.Fatal("expected news to be parsed")
	}

	if data.Players[0].Name == "" {
		t.Fatal("expected first player name to be present")
	}

	if data.News[0].TitleEN == "" {
		t.Fatal("expected first news title to be present")
	}
}
