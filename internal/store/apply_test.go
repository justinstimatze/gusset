package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const targetUUID = "11112222-3333-4444-5555-666677778888"

// newTargetProfile builds a minimal synthetic profile whose prefs.js maps extID
// to targetUUID. It deliberately does NOT create the origin dir, so Apply
// exercises the synthesize-.metadata-v2 path. Returns the profile dir.
func newTargetProfile(t *testing.T, extID string) string {
	t.Helper()
	dir := t.TempDir()
	prefs := `user_pref("browser.startup.page", 3);
user_pref("extensions.webextensions.uuids", "{\"` + extID + `\":\"` + targetUUID + `\"}");
`
	if err := os.WriteFile(filepath.Join(dir, "prefs.js"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestApply_ReHomesToTargetUUID(t *testing.T) {
	src, _ := liveFirefox(t) // skips if no live profile / uBO
	snap, err := src.Snapshot(uBOExtensionID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Meta.IDBFileBase == "" {
		t.Fatal("snapshot did not record IDBFileBase")
	}

	targetProfile := newTargetProfile(t, uBOExtensionID)
	target := NewFirefox(targetProfile)
	if err := target.Apply(snap.Dir); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The store landed under the TARGET uuid's origin, with the name-derived base.
	originName := "moz-extension+++" + targetUUID + snap.Meta.OriginSuffix
	dstSQLite := filepath.Join(targetProfile, "storage", "default", originName, "idb", snap.Meta.IDBFileBase+".sqlite")
	if _, err := os.Stat(dstSQLite); err != nil {
		t.Fatalf("applied sqlite not found at %s: %v", dstSQLite, err)
	}

	// database.origin was rewritten to the target UUID, and the data is intact.
	db, err := sql.Open("sqlite", "file:"+dstSQLite+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var origin string
	if err := db.QueryRow("SELECT origin FROM database").Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(origin, targetUUID) {
		t.Errorf("database.origin = %q, expected target UUID", origin)
	}
	if strings.Contains(origin, snap.Meta.SourceUUID) {
		t.Errorf("database.origin still contains source UUID: %q", origin)
	}
	var keys int
	if err := db.QueryRow("SELECT count(*) FROM object_data").Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if keys == 0 {
		t.Error("applied store has no keys")
	}

	// External files re-homed to the target's .files dir.
	for _, id := range snap.Meta.ExternalFiles {
		p := filepath.Join(targetProfile, "storage", "default", originName, "idb", snap.Meta.IDBFileBase+".files", strconv.Itoa(id))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("external value %d not re-homed: %v", id, err)
		}
	}

	// .metadata-v2 was synthesized and names the target UUID, not the source.
	md, err := os.ReadFile(filepath.Join(targetProfile, "storage", "default", originName, ".metadata-v2"))
	if err == nil { // only present if the snapshot captured one
		if strings.Contains(string(md), snap.Meta.SourceUUID) {
			t.Error(".metadata-v2 still references source UUID")
		}
	}

	// Idempotent: applying again (origin now exists) still succeeds.
	if err := target.Apply(snap.Dir); err != nil {
		t.Fatalf("second apply: %v", err)
	}
}

func TestValidateMetaPaths(t *testing.T) {
	const okUUID = "11112222-3333-4444-5555-666677778888"
	ok := Meta{IDBFileBase: "3647222921wEXceSnetbo_3580.sqlite", OriginSuffix: "^userContextId=4294967295", SourceUUID: okUUID}
	if err := validateMetaPaths(ok); err != nil {
		t.Fatalf("rejected a legitimate meta: %v", err)
	}
	// Empty OriginSuffix (bare origin) and empty SourceUUID are both legitimate.
	if err := validateMetaPaths(Meta{IDBFileBase: "base", OriginSuffix: "", SourceUUID: ""}); err != nil {
		t.Fatalf("rejected a bare-origin meta: %v", err)
	}

	hostile := []Meta{
		{IDBFileBase: "../../../../etc/cron.d/x", OriginSuffix: "", SourceUUID: okUUID},
		{IDBFileBase: "..", OriginSuffix: "", SourceUUID: okUUID},
		{IDBFileBase: "ok/../../escape", OriginSuffix: "", SourceUUID: okUUID},
		{IDBFileBase: "base", OriginSuffix: "/../../evil", SourceUUID: okUUID},
		{IDBFileBase: "base", OriginSuffix: "^userContextId=4/../..", SourceUUID: okUUID},
		{IDBFileBase: "base", OriginSuffix: "", SourceUUID: "../../not-a-uuid-but-36-chars-long!!!"},
	}
	for _, m := range hostile {
		if err := validateMetaPaths(m); err == nil {
			t.Errorf("accepted a hostile meta that should be refused: %+v", m)
		}
	}
}

func TestApply_RefusesTraversalSnapshot(t *testing.T) {
	// A snapshot whose meta.json tries to escape the profile must be refused
	// before Apply touches the filesystem — nothing should be written outside.
	snapDir := t.TempDir()
	meta := Meta{
		ExtensionID: uBOExtensionID,
		Browser:     "firefox",
		IDBFileBase: "../../../../" + filepath.Join(t.TempDir(), "pwned"),
		SourceUUID:  "11112222-3333-4444-5555-666677778888",
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(snapDir, "meta.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	target := NewFirefox(newTargetProfile(t, uBOExtensionID))
	err := target.Apply(snapDir)
	if err == nil || !strings.Contains(err.Error(), "unsafe idb_file_base") {
		t.Fatalf("expected refusal of unsafe idb_file_base, got %v", err)
	}
}

func TestApply_RefusesLockedProfile(t *testing.T) {
	src, _ := liveFirefox(t)
	snap, err := src.Snapshot(uBOExtensionID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	targetProfile := newTargetProfile(t, uBOExtensionID)
	// Simulate a running Firefox by creating the lock symlink.
	if err := os.Symlink("127.0.1.1:+1234", filepath.Join(targetProfile, "lock")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := NewFirefox(targetProfile).Apply(snap.Dir); !errors.Is(err, ErrProfileLocked) {
		t.Fatalf("expected ErrProfileLocked, got %v", err)
	}
}

func TestApply_RefusesUninstalledExtension(t *testing.T) {
	src, _ := liveFirefox(t)
	snap, err := src.Snapshot(uBOExtensionID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Target profile knows a different extension, not uBO.
	targetProfile := newTargetProfile(t, "someoneelse@example.com")
	err = NewFirefox(targetProfile).Apply(snap.Dir)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected not-installed error, got %v", err)
	}
}
