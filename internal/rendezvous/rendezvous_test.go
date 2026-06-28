package rendezvous

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/justinstimatze/gusset/internal/crypto"
)

func testKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	k, err := crypto.DeriveKeys("correct horse battery staple lorem ipsum", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sampleBeacon() Beacon {
	return Beacon{
		DeviceID:     "laptop-7f3a",
		Instance:     "rukh",
		LANEndpoints: []string{"192.168.1.20:49200"},
		IssuedAt:     1_000_000,
	}
}

func TestSealOpen_RoundTrip(t *testing.T) {
	k := testKeys(t)
	sealed, err := Seal(sampleBeacon(), k)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(sealed, k)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleBeacon()
	want.SchemaVersion = SchemaVersion
	if got.DeviceID != want.DeviceID ||
		len(got.LANEndpoints) != 1 || got.LANEndpoints[0] != want.LANEndpoints[0] {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestSealOpen_CarriesICEEndpoint(t *testing.T) {
	k := testKeys(t)
	b := sampleBeacon()
	b.ICE = &ICEEndpoint{
		Ufrag:      "abcd",
		Pwd:        "a-much-longer-ice-password-value",
		Candidates: []string{"candidate:1 1 udp 2113937151 192.168.1.20 49200 typ host", "candidate:2 1 udp 1677729535 203.0.113.7 51000 typ srflx"},
	}
	sealed, err := Seal(b, k)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(sealed, k)
	if err != nil {
		t.Fatal(err)
	}
	if got.ICE == nil {
		t.Fatal("ICE endpoint did not survive the round-trip")
	}
	if got.ICE.Ufrag != b.ICE.Ufrag || got.ICE.Pwd != b.ICE.Pwd || len(got.ICE.Candidates) != 2 {
		t.Fatalf("ICE endpoint mismatch: got %+v", got.ICE)
	}
}

func TestSeal_RequiresDeviceID(t *testing.T) {
	k := testKeys(t)
	b := sampleBeacon()
	b.DeviceID = ""
	if _, err := Seal(b, k); err == nil {
		t.Fatal("Seal must reject a beacon with no DeviceID")
	}
}

func TestOpen_WrongKeyFails(t *testing.T) {
	sealed, err := Seal(sampleBeacon(), testKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	other, err := crypto.DeriveKeys("a completely different passphrase here", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(sealed, other); err == nil {
		t.Fatal("a beacon sealed by one passphrase must not Open under another")
	}
}

func TestOpen_RejectsChunkAAD(t *testing.T) {
	// A ciphertext sealed under a different AAD (as the chunk layer does) must not
	// Open as a beacon — domain separation holds.
	k := testKeys(t)
	notABeacon, err := k.Seal([]byte("chunk bytes"), []byte("some-content-address"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(notABeacon, k); err == nil {
		t.Fatal("a non-beacon ciphertext must not Open as a beacon")
	}
}

func TestFresh(t *testing.T) {
	b := sampleBeacon() // IssuedAt = 1_000_000
	maxAge := 10 * time.Minute
	if !Fresh(b, 1_000_000+60, maxAge) {
		t.Error("a 60s-old beacon should be fresh under a 10m window")
	}
	if Fresh(b, 1_000_000+3600, maxAge) {
		t.Error("a 1h-old beacon should be stale under a 10m window")
	}
	if Fresh(b, 999_999, maxAge) {
		t.Error("a beacon issued in the future should not be considered fresh")
	}
}

func TestDirSignaling_PublishFetchExcludesSelf(t *testing.T) {
	k := testKeys(t)
	dir := t.TempDir()
	sig := DirSignaling{Dir: dir}
	ctx := context.Background()

	// Two devices publish into the same shared dir.
	a := sampleBeacon()
	a.DeviceID = "device-a"
	b := sampleBeacon()
	b.DeviceID = "device-b"
	b.Instance = "kestrel"

	sealedA, err := Seal(a, k)
	if err != nil {
		t.Fatal(err)
	}
	sealedB, err := Seal(b, k)
	if err != nil {
		t.Fatal(err)
	}
	if err := sig.Publish(ctx, a.DeviceID, sealedA); err != nil {
		t.Fatal(err)
	}
	if err := sig.Publish(ctx, b.DeviceID, sealedB); err != nil {
		t.Fatal(err)
	}

	// Device A fetches: it should see only B's beacon, not its own.
	got, err := sig.Fetch(ctx, a.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("device A should see exactly one peer beacon, got %d", len(got))
	}
	peer, err := Open(got[0], k)
	if err != nil {
		t.Fatal(err)
	}
	if peer.DeviceID != "device-b" || peer.Instance != "kestrel" {
		t.Fatalf("fetched the wrong beacon: %+v", peer)
	}
}

func TestDirSignaling_PublishReplaces(t *testing.T) {
	k := testKeys(t)
	sig := DirSignaling{Dir: t.TempDir()}
	ctx := context.Background()

	b := sampleBeacon()
	b.DeviceID = "device-x"
	b.LANEndpoints = []string{"192.168.1.20:51000"}
	first, _ := Seal(b, k)
	if err := sig.Publish(ctx, b.DeviceID, first); err != nil {
		t.Fatal(err)
	}

	b.LANEndpoints = []string{"192.168.1.20:52000"} // re-publish with a new endpoint
	second, _ := Seal(b, k)
	if err := sig.Publish(ctx, b.DeviceID, second); err != nil {
		t.Fatal(err)
	}

	// A different device fetches and must see only the latest beacon.
	got, err := sig.Fetch(ctx, "device-other")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("publish should replace, leaving one beacon; got %d", len(got))
	}
	latest, err := Open(got[0], k)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.LANEndpoints) != 1 || latest.LANEndpoints[0] != "192.168.1.20:52000" {
		t.Fatalf("expected the re-published endpoint, got %v", latest.LANEndpoints)
	}
}

func TestDirSignaling_FetchSkipsOversizeBeacon(t *testing.T) {
	k := testKeys(t)
	dir := t.TempDir()
	sig := DirSignaling{Dir: dir}
	ctx := context.Background()

	// A legitimate small beacon alongside a hostile oversize ".beacon" file
	// (memory-exhaustion attempt by a writer with folder access).
	good := sampleBeacon()
	good.DeviceID = "real-device"
	sealed, err := Seal(good, k)
	if err != nil {
		t.Fatal(err)
	}
	if err := sig.Publish(ctx, good.DeviceID, sealed); err != nil {
		t.Fatal(err)
	}
	huge := make([]byte, maxBeaconBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "attacker.beacon"), huge, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := sig.Fetch(ctx, "reader")
	if err != nil {
		t.Fatalf("an oversize file must be skipped, not error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want only the real beacon, got %d entries", len(got))
	}
	if _, err := Open(got[0], k); err != nil {
		t.Fatalf("the kept entry should be the real (openable) beacon: %v", err)
	}
}

func TestDirSignaling_FetchCapsCount(t *testing.T) {
	dir := t.TempDir()
	// Flood the folder with more small ".beacon" files than the cap. Content is
	// junk (it never has to Open) — the point is Fetch must not return unbounded.
	for i := 0; i < maxBeacons+50; i++ {
		name := filepath.Join(dir, "flood-"+strconv.Itoa(i)+beaconExt)
		if err := os.WriteFile(name, []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := DirSignaling{Dir: dir}.Fetch(context.Background(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxBeacons {
		t.Fatalf("Fetch must cap at %d entries, got %d", maxBeacons, len(got))
	}
}

func TestDirSignaling_FetchMissingDirIsEmpty(t *testing.T) {
	sig := DirSignaling{Dir: t.TempDir() + "/does-not-exist"}
	got, err := sig.Fetch(context.Background(), "anyone")
	if err != nil {
		t.Fatalf("missing dir should be empty, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no beacons, got %d", len(got))
	}
}
