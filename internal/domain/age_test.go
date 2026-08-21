package domain

import (
	"testing"
	"time"
)

func TestAgeFromBirthDate(t *testing.T) {
	born := func(s string) *time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return &d
	}
	on := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return d
	}

	cases := []struct {
		name  string
		birth *time.Time
		when  time.Time
		want  int
	}{
		// Vinícius Júnior, born 2000-07-12. The stored age said 26 while
		// api-football said 25 — this is the arithmetic that settles it.
		{"birthday already passed", born("2000-07-12"), on("2026-08-19"), 26},
		{"day before birthday", born("2000-07-12"), on("2026-07-11"), 25},
		{"on the birthday", born("2000-07-12"), on("2026-07-12"), 26},
		{"day after birthday", born("2000-07-12"), on("2026-07-13"), 26},

		// Unknown birth date must yield 0 so callers keep the stored age
		// rather than showing a newborn.
		{"no birth date", nil, on("2026-08-19"), 0},
		{"zero time", &time.Time{}, on("2026-08-19"), 0},

		// Garbage in the column shouldn't reach the UI as a real age.
		{"birth in the future", born("2030-01-01"), on("2026-08-19"), 0},
		{"implausibly old", born("1850-01-01"), on("2026-08-19"), 0},
	}

	for _, tc := range cases {
		if got := AgeFromBirthDate(tc.birth, tc.when); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
