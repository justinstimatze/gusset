package converge

import (
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/profile"
	"github.com/justinstimatze/gusset/internal/store"
	"github.com/justinstimatze/gusset/internal/transport"

	"github.com/restic/chunker"

	_ "modernc.org/sqlite"
)

const (
	uBOID      = "uBlock0@raymondhill.net"
	targetUUID = "aaaa1111-bbbb-2222-cccc-333344445555"
	passphrase = "correct horse battery staple lorem ipsum dolor sit"
)

func liveSource(t *testing.T) *store.Firefox {
	t.Helper()
	root, err := profile.FirefoxRoot()
	if err != nil {
		t.Skipf("no Firefox profile: %v", err)
	}
	dir, err := profile.DefaultProfileDir(root)
	if err != nil {
		t.Skipf("no active profile: %v", err)
	}
	uuids, err := profile.ExtensionUUIDs(dir)
	if err != nil || uuids[uBOID] == "" {
		t.Skipf("uBO not installed")
	}
	return store.NewFirefox(dir)
}

func keysAndPoly(t *testing.T) (*crypto.Keys, chunker.Pol) {
	t.Helper()
	k, err := crypto.DeriveKeys(passphrase, crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := chunk.DerivePolynomial(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, pol
}

func newTargetProfile(t *testing.T, extID string) string {
	t.Helper()
	dir := t.TempDir()
	prefs := `user_pref("extensions.webextensions.uuids", "{\"` + extID + `\":\"` + targetUUID + `\"}");` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "prefs.js"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// servePipe serves src over an in-memory pipe and returns a transport.Client for
// the other end — exercising the whole reconcile path with no TLS or sockets.
func servePipe(t *testing.T, src transport.ChunkSource) *transport.Client {
	t.Helper()
	c, s := net.Pipe()
	go func() { _ = transport.Serve(s, src) }()
	client := transport.NewClient(c)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestBuildOfferAndPull_AppliesPeerNewer is the converge crown test: a live uBO
// store is exported into an offer, served over the transport, and pulled onto a
// fresh target profile that has nothing — so the peer is newer and its data is
// applied, re-homed onto the target UUID.
func TestBuildOfferAndPull_AppliesPeerNewer(t *testing.T) {
	src := liveSource(t)
	k, pol := keysAndPoly(t)

	offer, _, err := BuildOffer(src, k, pol, []string{uBOID}, t.TempDir(), 2_000)
	if err != nil {
		t.Fatal(err)
	}
	client := servePipe(t, offer)

	targetDir := newTargetProfile(t, uBOID)
	target := store.NewFirefox(targetDir)
	allowAll := func(string) bool { return true }

	// Target has no local catalog, so the peer is newer for uBO.
	outcomes, err := Pull(client, target, k, Catalog{}, allowAll, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != Applied {
		t.Fatalf("expected uBO applied, got %+v", outcomes)
	}

	originName := "moz-extension+++" + targetUUID + "^userContextId=4294967295"
	matches, _ := filepath.Glob(filepath.Join(targetDir, "storage", "default", originName, "idb", "*.sqlite"))
	if len(matches) != 1 {
		t.Fatalf("expected one applied sqlite, found %d", len(matches))
	}
	db, err := sql.Open("sqlite", "file:"+matches[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var origin string
	if err := db.QueryRow("SELECT origin FROM database").Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(origin, targetUUID) {
		t.Errorf("origin not re-homed: %q", origin)
	}
}

// TestBuildOffer_SkipsExtensionWithNoStore confirms that an extension which is
// installed (has a UUID in prefs) but has no storage.local yet is skipped in the
// offer rather than failing the whole build — so a machine with a freshly
// installed, empty extension can still serve (nothing) and receive from a peer.
func TestBuildOffer_SkipsExtensionWithNoStore(t *testing.T) {
	k, pol := keysAndPoly(t)
	empty := store.NewFirefox(newTargetProfile(t, uBOID)) // uBO mapped, but no store
	offer, cat, err := BuildOffer(empty, k, pol, []string{uBOID}, t.TempDir(), 2_000)
	if err != nil {
		t.Fatalf("BuildOffer should skip a storeless extension, got: %v", err)
	}
	if len(cat) != 0 || len(offer.Chunks) != 0 {
		t.Fatalf("expected an empty offer, got %d catalog entries, %d chunks", len(cat), len(offer.Chunks))
	}
}

// TestTwoPeer_RealTLSConverge is the closest thing to a two-box test on one
// host: peer A serves its live uBO store over a real mutual-TLS listener, and
// peer B — a separate profile, an identity independently derived from the same
// passphrase — dials over the socket, fetches the catalog (opOffer over TLS),
// pulls the chunks, and applies them re-homed onto its own UUID. It exercises
// the full networked path the in-memory pipe tests cannot: TLS handshake, the
// wire protocol over a real connection, and apply onto a fresh profile.
func TestTwoPeer_RealTLSConverge(t *testing.T) {
	src := liveSource(t) // peer A's real data
	k, pol := keysAndPoly(t)

	offer, _, err := BuildOffer(src, k, pol, []string{uBOID}, t.TempDir(), 2_000)
	if err != nil {
		t.Fatal(err)
	}

	idA, err := transport.DeriveIdentity(k)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := transport.Listen("tcp", "127.0.0.1:0", idA, offer)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	defer func() { _ = srv.Close() }()

	// Peer B: same passphrase -> same identity, so mutual TLS authenticates.
	idB, err := transport.DeriveIdentity(k)
	if err != nil {
		t.Fatal(err)
	}
	client, err := transport.Dial("tcp", srv.Addr().String(), idB)
	if err != nil {
		t.Fatalf("peer B dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	targetDir := newTargetProfile(t, uBOID)
	target := store.NewFirefox(targetDir)
	outcomes, err := Pull(client, target, k, Catalog{}, func(string) bool { return true }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != Applied {
		t.Fatalf("expected uBO applied over TLS, got %+v", outcomes)
	}

	originName := "moz-extension+++" + targetUUID + "^userContextId=4294967295"
	matches, _ := filepath.Glob(filepath.Join(targetDir, "storage", "default", originName, "idb", "*.sqlite"))
	if len(matches) != 1 {
		t.Fatalf("expected one applied sqlite on peer B, found %d", len(matches))
	}
	db, err := sql.Open("sqlite", "file:"+matches[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var keys int
	if err := db.QueryRow("SELECT count(*) FROM object_data").Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys == 0 {
		t.Fatal("peer B received no keys")
	}
	t.Logf("two-peer over TLS: peer B applied %d keys, re-homed to its UUID", keys)
}

// TestPull_LocalNewerSkips confirms LWW: when our manifest is newer than the
// peer's, Pull does not apply (and never touches the target).
func TestPull_LocalNewerSkips(t *testing.T) {
	src := liveSource(t)
	k, pol := keysAndPoly(t)
	offer, _, err := BuildOffer(src, k, pol, []string{uBOID}, t.TempDir(), 1_000) // peer old
	if err != nil {
		t.Fatal(err)
	}
	client := servePipe(t, offer)

	target := store.NewFirefox(newTargetProfile(t, uBOID))
	local := Catalog{uBOID: &chunk.Manifest{CreatedAt: 9_999}} // ours newer
	outcomes, err := Pull(client, target, k, local, func(string) bool { return true }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != LocalNewer {
		t.Fatalf("expected local-newer, got %+v", outcomes)
	}
}

// TestPull_LockedWhenFirefoxRunning confirms that a peer-newer extension whose
// apply is blocked by a running Firefox (lock present) is reported as Locked,
// distinct from a generic error — so the command can tell the user to close
// Firefox and re-run rather than surfacing a cryptic failure.
func TestPull_LockedWhenFirefoxRunning(t *testing.T) {
	src := liveSource(t)
	k, pol := keysAndPoly(t)
	offer, _, err := BuildOffer(src, k, pol, []string{uBOID}, t.TempDir(), 2_000)
	if err != nil {
		t.Fatal(err)
	}
	client := servePipe(t, offer)

	targetDir := newTargetProfile(t, uBOID)
	if err := os.WriteFile(filepath.Join(targetDir, "lock"), []byte("running"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := store.NewFirefox(targetDir)
	outcomes, err := Pull(client, target, k, Catalog{}, func(string) bool { return true }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != Locked {
		t.Fatalf("expected locked, got %+v", outcomes)
	}
}

// TestPull_BlockedWhenNotAllowed confirms an un-allowlisted extension the peer
// offers is reported Blocked and not applied.
func TestPull_BlockedWhenNotAllowed(t *testing.T) {
	src := liveSource(t)
	k, pol := keysAndPoly(t)
	offer, _, err := BuildOffer(src, k, pol, []string{uBOID}, t.TempDir(), 2_000)
	if err != nil {
		t.Fatal(err)
	}
	client := servePipe(t, offer)

	target := store.NewFirefox(newTargetProfile(t, uBOID))
	denyAll := func(string) bool { return false }
	outcomes, err := Pull(client, target, k, Catalog{}, denyAll, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Action != Blocked {
		t.Fatalf("expected blocked, got %+v", outcomes)
	}
}
