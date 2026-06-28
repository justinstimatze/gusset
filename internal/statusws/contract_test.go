package statusws

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/justinstimatze/gusset/internal/status"
)

// update regenerates the golden contract fixtures instead of asserting against
// them: `go test ./internal/statusws/ -run Contract -update`.
var update = flag.Bool("update", false, "regenerate the daemon↔extension contract fixtures")

// fixtureDir is where the cross-language wire fixtures live. They are checked in
// under the extension tree so the vitest contract test
// (extension/lib/protocol.contract.test.ts) reads the exact bytes this Go
// producer emits — pinning the two sides of the localhost-WS protocol to one
// source of truth instead of a hand-maintained mirror.
var fixtureDir = filepath.Join("..", "..", "extension", "lib", "testdata", "protocol")

// allEnums enumerates every wire-level string the protocol can carry, per
// category. The Go constants in internal/status are the source of truth; the TS
// side (protocol.ts runtime arrays) must match this set exactly, which the
// vitest parity test asserts. Adding an enum value in Go and forgetting it in TS
// (or vice versa) then fails a test on one side or the other.
func allEnums() map[string][]string {
	return map[string][]string{
		"peer_states": {
			string(status.Discovering), string(status.Signaling), string(status.HolePunching),
			string(status.Connected), string(status.Unreachable),
		},
		"links":   {string(status.LinkLAN), string(status.LinkDirectNAT)},
		"reasons": {string(status.PeerOffline), string(status.NATFailed), string(status.AuthFailed)},
		"sync_states": {
			string(status.InSync), string(status.Pushing), string(status.Pulling), string(status.Stale),
			string(status.Blocked), string(status.Errored), string(status.Pending),
		},
		"log_levels":       {string(status.LogInfo), string(status.LogOK), string(status.LogWarn), string(status.LogError)},
		"server_msg_types": {"status", "beacon", "ping"},
		// Client -> server frame discriminants the daemon's readLoop accepts
		// (statusws.go). protocol.ts CLIENT_MSG_TYPES must equal this set.
		"client_msg_types": {"peers", "set-name"},
	}
}

// maximalSnapshot drives a real status.Model through the same setters the daemon
// uses, exercising every enum value at least once, then takes the Snapshot the
// server would serialize. Timestamps are fixed so the fixture is deterministic.
func maximalSnapshot() status.Snapshot {
	const t0 = 1_750_000_000
	m := status.New()
	m.SetSelf("rukh-2300de", "rukh")

	// One peer per PeerState, carrying a representative link/reason/detail so the
	// optional fields appear in the fixture too.
	m.SetPeer(status.Peer{DeviceID: "peer-discovering", State: status.Discovering, Since: t0})
	m.SetPeer(status.Peer{DeviceID: "peer-signaling", Name: "laptop", State: status.Signaling, Since: t0 + 1})
	m.SetPeer(status.Peer{DeviceID: "peer-punching", State: status.HolePunching, Since: t0 + 2})
	m.SetPeer(status.Peer{DeviceID: "peer-connected", Name: "desktop", State: status.Connected, Link: status.LinkDirectNAT, Since: t0 + 3})
	m.SetPeer(status.Peer{DeviceID: "peer-unreachable", State: status.Unreachable, Reason: status.NATFailed, Detail: "punch timed out", Since: t0 + 4})

	// One extension entry per SyncState; the transfer states carry the
	// progress-bar fields so remaining/total appear on the wire.
	m.SetExtSync(status.ExtSync{Extension: "a@x", DeviceID: "peer-connected", State: status.InSync, Since: t0})
	m.SetExtSync(status.ExtSync{Extension: "b@x", DeviceID: "peer-connected", State: status.Pushing, Remaining: 3, Total: 10, Since: t0})
	m.SetExtSync(status.ExtSync{Extension: "c@x", DeviceID: "peer-connected", State: status.Pulling, Remaining: 7, Total: 10, Since: t0})
	m.SetExtSync(status.ExtSync{Extension: "d@x", DeviceID: "peer-connected", State: status.Stale, Since: t0})
	m.SetExtSync(status.ExtSync{Extension: "e@x", DeviceID: "peer-connected", State: status.Blocked, Detail: "not allowlisted here", Since: t0})
	m.SetExtSync(status.ExtSync{Extension: "f@x", DeviceID: "peer-connected", State: status.Errored, Detail: "verify failed", Since: t0})
	m.SetExtSync(status.ExtSync{Extension: "g@x", DeviceID: "peer-connected", State: status.Pending, Detail: "restart Firefox to load", Since: t0})

	// One log line per LogLevel.
	m.Log(t0, status.LogInfo, "dialing peer-connected")
	m.Log(t0+1, status.LogOK, "applied a@x from desktop")
	m.Log(t0+2, status.LogWarn, "fetched g@x — close Firefox to apply")
	m.Log(t0+3, status.LogError, "couldn't sync f@x from desktop")

	return m.Snapshot()
}

// TestContract_GoldenFixtures is the producer half of the cross-language
// contract: it serializes the maximal snapshot and both server→client envelopes
// exactly as the daemon would, and golden-compares them against the checked-in
// fixtures the vitest consumer test reads. -update regenerates them.
func TestContract_GoldenFixtures(t *testing.T) {
	snap := maximalSnapshot()

	fixtures := map[string]any{
		// The bare Snapshot — the shape protocol.ts's Snapshot interface mirrors.
		"snapshot.json": snap,
		// The framed server→client messages daemon.ts dispatches on msg.type.
		"status_frame.json": statusMsg{Type: "status", Snapshot: snap},
		"beacon_frame.json": beaconMsg{Type: "beacon", Beacon: []byte("a-sealed-beacon")},
		// The authoritative enum sets the TS runtime arrays must equal.
		"enums.json": allEnums(),
	}

	for name, v := range fixtures {
		got, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		got = append(got, '\n')
		path := filepath.Join(fixtureDir, name)

		if *update {
			if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", path)
			continue
		}

		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run `go test ./internal/statusws/ -run Contract -update` to generate)", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale — the Go wire shape changed.\n"+
				"Regenerate with `go test ./internal/statusws/ -run Contract -update`, then update protocol.ts to match.\n"+
				"got:\n%s", name, got)
		}
	}
}
