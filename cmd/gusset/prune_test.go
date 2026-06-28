package main

import (
	"sort"
	"testing"
)

func TestStalePeerIDs(t *testing.T) {
	now := int64(1_000_000)
	window := int64(90) // seconds

	lastSeen := map[string]int64{
		"fresh":     now,        // just advertised
		"recent":    now - 30,   // within the window
		"edge":      now - 90,   // exactly the window — not yet stale (strict >)
		"stale":     now - 91,   // just past the window
		"verystale": now - 3600, // long gone
	}

	got := stalePeerIDs(lastSeen, now, window)
	sort.Strings(got)

	want := []string{"stale", "verystale"}
	if len(got) != len(want) {
		t.Fatalf("stalePeerIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stalePeerIDs = %v, want %v", got, want)
		}
	}
}

func TestStalePeerIDs_EmptyIsEmpty(t *testing.T) {
	if got := stalePeerIDs(map[string]int64{}, 100, 90); len(got) != 0 {
		t.Fatalf("expected no stale ids from empty map, got %v", got)
	}
}
