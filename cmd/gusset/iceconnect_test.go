package main

import (
	"testing"

	"github.com/justinstimatze/gusset/internal/rendezvous"
)

// TestICEControlling_OppositeRoles is the safety net for the hole-punch
// coordination: for any two distinct device ids, the two peers — each calling
// iceControlling with the arguments swapped — must end up on opposite sides
// (exactly one controlling). If both read the same role the punch deadlocks, so
// a regression to `>=`, `<`, or a non-strict comparison must fail here.
func TestICEControlling_OppositeRoles(t *testing.T) {
	pairs := [][2]string{
		{"aaaa", "bbbb"},
		{"device-1", "device-2"},
		{"zzzz", "aaaa"},
		{"01HXMET", "01HXMEZ"}, // ULID-ish: lexical order is the tie-break
	}
	for _, p := range pairs {
		a, b := p[0], p[1]
		ra := iceControlling(a, b)
		rb := iceControlling(b, a) // the peer's view: args swapped
		if ra == rb {
			t.Errorf("ids %q/%q: both sides got controlling=%v; exactly one must control", a, b, ra)
		}
	}
}

// TestICEControlling_GreaterIDWins pins the direction so the documented
// invariant ("the greater device id controls") can't silently invert.
func TestICEControlling_GreaterIDWins(t *testing.T) {
	if !iceControlling("bbbb", "aaaa") {
		t.Error("greater id should control")
	}
	if iceControlling("aaaa", "bbbb") {
		t.Error("lesser id should not control")
	}
}

// TestICEControlling_EqualIDsDocumentsHazard documents (and pins) the one case
// the design relies on persisted-unique ids to prevent: equal ids make both
// sides non-controlling, which would deadlock. The persisted device ids are
// unique by construction; this test exists so the hazard is visible if that
// assumption is ever weakened.
func TestICEControlling_EqualIDsDocumentsHazard(t *testing.T) {
	if iceControlling("same", "same") {
		t.Error("equal ids must not yield controlling=true (strict comparison)")
	}
}

// TestToICEEndpoint_RoundTrip confirms the beacon's wire-format ICE endpoint
// maps field-for-field onto the icewire type — the seam that keeps rendezvous
// free of the pion dependency. A dropped field here silently breaks the punch.
func TestToICEEndpoint_RoundTrip(t *testing.T) {
	in := rendezvous.ICEEndpoint{
		Ufrag:      "uf",
		Pwd:        "a-longer-ice-password",
		Candidates: []string{"candidate:1 1 udp 2113937151 203.0.113.7 51000 typ srflx"},
	}
	got := toICEEndpoint(in)
	if got.Ufrag != in.Ufrag || got.Pwd != in.Pwd {
		t.Errorf("ufrag/pwd mismatch: %+v", got)
	}
	if len(got.Candidates) != len(in.Candidates) || got.Candidates[0] != in.Candidates[0] {
		t.Errorf("candidates not carried: %+v", got.Candidates)
	}
}
