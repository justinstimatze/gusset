package syncx

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/profile"
	"github.com/justinstimatze/gusset/internal/store"

	_ "modernc.org/sqlite"
)

const (
	uBOID      = "uBlock0@raymondhill.net"
	targetUUID = "11112222-3333-4444-5555-666677778888"
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

func newTargetProfile(t *testing.T, extID string) string {
	t.Helper()
	dir := t.TempDir()
	prefs := `user_pref("extensions.webextensions.uuids", "{\"` + extID + `\":\"` + targetUUID + `\"}");` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "prefs.js"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestExportImport_EndToEnd is the crown-jewel integration test: a live store is
// snapshotted, packed, chunked+encrypted, carried as an opaque address->cipher
// map (standing in for the transport), then reconstructed, unpacked, and applied
// onto a different machine's UUID — exercising every package at once.
func TestExportImport_EndToEnd(t *testing.T) {
	src := liveSource(t)
	k, err := crypto.DeriveKeys(passphrase, crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := chunk.DerivePolynomial(k)
	if err != nil {
		t.Fatal(err)
	}

	manifest, chunks, err := Export(src, uBOID, t.TempDir(), k, pol, 1_750_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) == 0 || len(chunks) == 0 {
		t.Fatal("export produced no chunks")
	}
	t.Logf("exported %d chunk refs, %d unique encrypted chunks", len(manifest.Chunks), len(chunks))

	// The "transport": opaque ciphertext keyed by content-address.
	get := func(addr string) ([]byte, error) {
		c, ok := chunks[addr]
		if !ok {
			return nil, errors.New("missing chunk " + addr)
		}
		return c, nil
	}

	targetProfile := newTargetProfile(t, uBOID)
	target := store.NewFirefox(targetProfile)
	if err := Import(target, manifest, k, get, t.TempDir()); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Verify the store landed under the target UUID with data intact.
	originName := "moz-extension+++" + targetUUID + "^userContextId=4294967295"
	matches, _ := filepath.Glob(filepath.Join(targetProfile, "storage", "default", originName, "idb", "*.sqlite"))
	if len(matches) != 1 {
		t.Fatalf("expected one applied sqlite, found %d under %s", len(matches), originName)
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
		t.Fatal("imported store has no keys")
	}
	var origin string
	if err := db.QueryRow("SELECT origin FROM database").Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(origin, targetUUID) {
		t.Errorf("origin not re-homed: %q", origin)
	}
	t.Logf("imported %d keys, origin=%s", keys, origin)
}

func TestExportImport_WrongKeyFailsImport(t *testing.T) {
	src := liveSource(t)
	k, _ := crypto.DeriveKeys(passphrase, crypto.AppSalt)
	pol, _ := chunk.DerivePolynomial(k)
	manifest, chunks, err := Export(src, uBOID, t.TempDir(), k, pol, 1_750_000_000)
	if err != nil {
		t.Fatal(err)
	}
	get := func(addr string) ([]byte, error) { return chunks[addr], nil }

	wrong, _ := crypto.DeriveKeys("a totally different eight word secret phrase here", crypto.AppSalt)
	target := store.NewFirefox(newTargetProfile(t, uBOID))
	if err := Import(target, manifest, wrong, get, t.TempDir()); err == nil {
		t.Fatal("import succeeded under the wrong key")
	}
}

func TestNewer_LWW(t *testing.T) {
	older := &chunk.Manifest{CreatedAt: 100}
	newer := &chunk.Manifest{CreatedAt: 200}
	if Newer(older, newer) != newer || Newer(newer, older) != newer {
		t.Fatal("Newer did not pick the later manifest")
	}
	if Newer(nil, newer) != newer || Newer(newer, nil) != newer {
		t.Fatal("Newer mishandled nil")
	}
	if Newer(newer, newer) != newer {
		t.Fatal("tie should return the first arg")
	}
}
