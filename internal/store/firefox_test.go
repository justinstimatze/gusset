package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/justinstimatze/gusset/internal/profile"
)

// uBOExtensionID is a stable, widely-installed extension to snapshot against a
// real profile. The test skips cleanly when it isn't installed.
const uBOExtensionID = "uBlock0@raymondhill.net"

// liveFirefox resolves the local profile or skips the test. These tests
// exercise the snapshot path against a real store; CI has no Firefox profile
// and skips, while a developer machine with Firefox runs them for real.
func liveFirefox(t *testing.T) (*Firefox, string) {
	t.Helper()
	root, err := profile.FirefoxRoot()
	if err != nil {
		t.Skipf("no Firefox profile on this machine: %v", err)
	}
	dir, err := profile.DefaultProfileDir(root)
	if err != nil {
		t.Skipf("could not resolve active profile: %v", err)
	}
	uuids, err := profile.ExtensionUUIDs(dir)
	if err != nil {
		t.Skipf("could not read uuids: %v", err)
	}
	if _, ok := uuids[uBOExtensionID]; !ok {
		t.Skipf("%s not installed", uBOExtensionID)
	}
	return NewFirefox(dir), dir
}

func TestFirefoxSnapshot_Live(t *testing.T) {
	fx, _ := liveFirefox(t)

	snap, err := fx.Snapshot(uBOExtensionID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// meta is well-formed and identifies the right store.
	if snap.Meta.DBName != storageLocalDBName {
		t.Errorf("db name = %q, want %q", snap.Meta.DBName, storageLocalDBName)
	}
	if snap.Meta.ExtensionID != uBOExtensionID {
		t.Errorf("ext id = %q", snap.Meta.ExtensionID)
	}
	if snap.Meta.SourceUUID == "" {
		t.Error("source UUID not recorded")
	}

	// The snapshot sqlite is valid and holds the live store's keys.
	snapDB := filepath.Join(snap.Dir, "storage.sqlite")
	db, err := sql.Open("sqlite", "file:"+snapDB+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var keys int
	if err := db.QueryRow("SELECT count(*) FROM object_data").Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys == 0 {
		t.Error("snapshot has zero storage.local keys; expected uBO settings")
	}
	t.Logf("snapshotted %d keys, %d external files", keys, len(snap.Meta.ExternalFiles))

	// Every external value referenced by the db is present on disk.
	for _, id := range snap.Meta.ExternalFiles {
		p := filepath.Join(snap.Dir, "files", strconv.Itoa(id))
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Errorf("external value %d missing or empty: %v", id, err)
		}
	}

	// meta.json round-trips.
	raw, err := os.ReadFile(filepath.Join(snap.Dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Meta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("meta.json invalid: %v", err)
	}
	if got.SourceUUID != snap.Meta.SourceUUID {
		t.Errorf("meta.json UUID = %q, want %q", got.SourceUUID, snap.Meta.SourceUUID)
	}
}

func TestFirefoxSnapshot_UnknownExtension(t *testing.T) {
	fx, _ := liveFirefox(t)
	if _, err := fx.Snapshot("does-not-exist@nowhere", t.TempDir()); err == nil {
		t.Fatal("expected error for uninstalled extension, got nil")
	}
}
