package service

import (
	"testing"

	"fri.local/football-reputation-index/internal/domain"
)

func TestDetectPerformanceEventsDroughtFlagsAttackers(t *testing.T) {
	player := domain.PlayerSyncTarget{ID: 1, Name: "X. Y.", Position: "FWD"}
	form := formSnapshot{Games: 5, Goals: 0, Assists: 0, Rating: 6.4}

	events := detectPerformanceEvents(player, form, 2025)

	if len(events) != 1 {
		t.Fatalf("expected 1 drought event, got %d", len(events))
	}
	if events[0].TargetComponent != "performance" {
		t.Errorf("target component = %q, want performance", events[0].TargetComponent)
	}
	if events[0].TriggerWord != "goal_drought_5_stats" {
		t.Errorf("trigger = %q, want goal_drought_5_stats", events[0].TriggerWord)
	}
	if events[0].Delta >= 0 {
		t.Errorf("drought delta should be negative, got %v", events[0].Delta)
	}
	if events[0].SourceRef == "" {
		t.Error("source_ref must be set so reruns can dedup")
	}
}

func TestDetectPerformanceEventsIgnoresDefendersAndGKs(t *testing.T) {
	form := formSnapshot{Games: 5, Goals: 0, Assists: 0}
	for _, pos := range []string{"DEF", "CB", "LB", "RB", "GK"} {
		player := domain.PlayerSyncTarget{ID: 1, Position: pos}
		if events := detectPerformanceEvents(player, form, 2025); len(events) != 0 {
			t.Errorf("position %q: drought event fired %d times, expected 0", pos, len(events))
		}
	}
}

func TestDetectPerformanceEventsAssistsBreakDrought(t *testing.T) {
	// 5 games, 0 goals, but 2 assists — the player is creating, no drought.
	form := formSnapshot{Games: 5, Goals: 0, Assists: 2}
	events := detectPerformanceEvents(domain.PlayerSyncTarget{ID: 1, Position: "FWD"}, form, 2025)
	if len(events) != 0 {
		t.Errorf("expected no drought when assists>0, got %d events", len(events))
	}
}

func TestDetectPerformanceEventsRequiresFullWindow(t *testing.T) {
	// 4 games is not enough — wait for the 5th data point before flagging.
	form := formSnapshot{Games: 4, Goals: 0}
	events := detectPerformanceEvents(domain.PlayerSyncTarget{ID: 1, Position: "FWD"}, form, 2025)
	if len(events) != 0 {
		t.Errorf("expected no drought with only %d games of form data, got %d events", form.Games, len(events))
	}
}

func TestDetectPerformanceEventsIdempotentWithinSameWeek(t *testing.T) {
	// Two calls within the same week should return the same source_ref so the
	// repository's UNIQUE (player_id, trigger_word, source_ref) dedup catches
	// the duplicate.
	player := domain.PlayerSyncTarget{ID: 1, Position: "FWD"}
	form := formSnapshot{Games: 5, Goals: 0, Assists: 0}

	a := detectPerformanceEvents(player, form, 2025)
	b := detectPerformanceEvents(player, form, 2025)

	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("both calls should produce one event each; got %d and %d", len(a), len(b))
	}
	if a[0].SourceRef != b[0].SourceRef {
		t.Errorf("source_ref must be stable within a week: %q != %q", a[0].SourceRef, b[0].SourceRef)
	}
}
