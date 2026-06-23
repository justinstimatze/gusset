package status

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestModel_SnapshotSorted(t *testing.T) {
	m := New()
	m.SetPeer(Peer{DeviceID: "zeta", State: Connected, Link: LinkLAN})
	m.SetPeer(Peer{DeviceID: "alpha", State: Unreachable, Reason: PeerOffline})
	m.SetExtSync(ExtSync{Extension: "b@x", DeviceID: "zeta", State: InSync})
	m.SetExtSync(ExtSync{Extension: "a@x", DeviceID: "zeta", State: Pushing, Remaining: 3})

	snap := m.Snapshot()
	if len(snap.Peers) != 2 || snap.Peers[0].DeviceID != "alpha" {
		t.Fatalf("peers not sorted by device id: %+v", snap.Peers)
	}
	if len(snap.Extensions) != 2 || snap.Extensions[0].Extension != "a@x" {
		t.Fatalf("extensions not sorted: %+v", snap.Extensions)
	}
}

func TestModel_RemovePeerDropsItsExtensions(t *testing.T) {
	m := New()
	m.SetPeer(Peer{DeviceID: "laptop", State: Connected, Link: LinkLAN})
	m.SetExtSync(ExtSync{Extension: "uBlock0@x", DeviceID: "laptop", State: InSync})
	m.SetExtSync(ExtSync{Extension: "uBlock0@x", DeviceID: "phone", State: Stale})

	m.RemovePeer("laptop")
	snap := m.Snapshot()
	if len(snap.Peers) != 0 {
		t.Errorf("peer not removed: %+v", snap.Peers)
	}
	if len(snap.Extensions) != 1 || snap.Extensions[0].DeviceID != "phone" {
		t.Errorf("removing a peer should drop only its extension entries: %+v", snap.Extensions)
	}
}

func TestModel_SnapshotIsACopy(t *testing.T) {
	m := New()
	m.SetPeer(Peer{DeviceID: "a", State: Connected})
	snap := m.Snapshot()
	snap.Peers[0].State = Unreachable // mutate the copy
	if got := m.Snapshot().Peers[0].State; got != Connected {
		t.Fatalf("snapshot aliased internal state: %v", got)
	}
}

func TestModel_ConcurrentAccess(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%5))
			m.SetPeer(Peer{DeviceID: id, State: Connected})
			m.SetExtSync(ExtSync{Extension: "e", DeviceID: id, State: InSync})
			_ = m.Snapshot()
		}(i)
	}
	wg.Wait()
}

// TestNeverSilent is the core invariant: every non-converged state renders a
// non-empty reason, and a non-converged state that arrived without one renders
// the loud fallback rather than a blank.
func TestNeverSilent(t *testing.T) {
	nonConvergedPeers := []Peer{
		{State: Unreachable, Reason: PeerOffline},
		{State: Unreachable}, // missing reason -> loud fallback
	}
	for _, p := range nonConvergedPeers {
		if PeerWhy(p) == "" {
			t.Errorf("unreachable peer rendered no reason: %+v", p)
		}
	}
	if !strings.Contains(PeerWhy(Peer{State: Unreachable}), "report") {
		t.Error("missing-reason peer should render the loud fallback")
	}
	if PeerWhy(Peer{State: Connected}) != "" {
		t.Error("connected peer should need no reason")
	}

	nonConvergedExts := []ExtSync{
		{State: Pushing, Remaining: 5},
		{State: Pulling, Remaining: 2},
		{State: Stale},
		{State: Blocked},
		{State: Errored}, // missing detail -> loud fallback
	}
	for _, e := range nonConvergedExts {
		if ExtWhy(e) == "" {
			t.Errorf("non-converged ext rendered no reason: %+v", e)
		}
	}
	if !strings.Contains(ExtWhy(ExtSync{State: Errored}), "report") {
		t.Error("errored ext without detail should render the loud fallback")
	}
	if ExtWhy(ExtSync{State: InSync}) != "" {
		t.Error("in-sync ext should need no reason")
	}
}

func TestRender_EmptyIsExplicit(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, New().Snapshot(), 1_750_000_000)
	out := buf.String()
	if !strings.Contains(out, "none paired yet") || !strings.Contains(out, "none allowlisted yet") {
		t.Fatalf("empty status should explain itself, got:\n%s", out)
	}
}

func TestRender_ShowsReasons(t *testing.T) {
	m := New()
	m.SetPeer(Peer{DeviceID: "phone", Name: "Phone", State: Unreachable, Reason: AuthFailed, Since: 1_749_999_400})
	m.SetExtSync(ExtSync{Extension: "uBlock0@x", DeviceID: "phone", State: Blocked, Detail: "denylisted (override with --force)", Since: 1_749_999_000})

	var buf bytes.Buffer
	Render(&buf, m.Snapshot(), 1_750_000_000)
	out := buf.String()
	for _, want := range []string{"Phone", "auth-failed", "10m ago", "uBlock0@x", "denylisted"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestSnapshot_JSONShape(t *testing.T) {
	m := New()
	m.SetPeer(Peer{DeviceID: "a", State: Connected, Link: LinkLAN, Since: 1})
	b, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	// The WS and extension consume this shape; check the stable field names.
	for _, want := range []string{`"peers"`, `"device_id":"a"`, `"state":"connected"`, `"link":"lan"`, `"extensions"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json missing %q in %s", want, b)
		}
	}
}
