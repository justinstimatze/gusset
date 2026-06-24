package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/justinstimatze/gusset/internal/ffctl"
	"github.com/justinstimatze/gusset/internal/profile"
)

// ErrProfileLocked is returned when Apply is asked to write a profile that
// Firefox appears to have open. Writing the IDB store out from under a running
// Firefox risks corruption, so Apply refuses.
var ErrProfileLocked = errors.New("store: Firefox profile is locked (close Firefox before applying)")

// ErrNotInstalled is returned when the target profile has no per-install UUID
// for the snapshot's extension — Firefox must have the extension installed (and
// have initialized its storage) before its store can be re-homed.
var ErrNotInstalled = errors.New("store: extension not installed on target profile")

// Apply installs a snapshot directory (as produced by Snapshot) into this
// Firefox profile, re-homing it onto the target machine's per-install UUID. It
// rewrites the UUID in all three places it is embedded (DELTA 2 in
// docs/firefox-internals-verified.md): the origin directory name, the
// .metadata-v2 origin record, and the sqlite database.origin column. The new
// IDB directory is staged on the same filesystem and swapped in with a rename,
// keeping a backup until the swap succeeds.
//
// Apply refuses to run against a locked (running) Firefox profile.
func (f *Firefox) Apply(snapshotDir string) error {
	meta, err := readMeta(snapshotDir)
	if err != nil {
		return err
	}
	if meta.Browser != "firefox" {
		return fmt.Errorf("store: snapshot is for %q, not firefox", meta.Browser)
	}
	if meta.IDBFileBase == "" {
		return errors.New("store: snapshot is missing idb_file_base")
	}
	// meta comes off the wire (a peer's meta.json), and three of its fields are
	// concatenated into filesystem paths below. A crafted snapshot must not be
	// able to escape the profile's storage directory, so validate them before
	// any path is built — fail closed on anything that isn't the expected shape.
	if err := validateMetaPaths(meta); err != nil {
		return err
	}
	if profileLocked(f.ProfileDir) {
		return ErrProfileLocked
	}

	uuids, err := profile.ExtensionUUIDs(f.ProfileDir)
	if err != nil {
		return err
	}
	target, ok := uuids[meta.ExtensionID]
	if !ok {
		return fmt.Errorf("%w: %s (install it and open it once first)", ErrNotInstalled, meta.ExtensionID)
	}

	originName := "moz-extension+++" + target + meta.OriginSuffix
	originDir := filepath.Join(f.ProfileDir, "storage", "default", originName)
	idbDir := filepath.Join(originDir, "idb")
	targetOrigin := "moz-extension://" + target + meta.OriginSuffix

	// Stage the replacement idb/ on the same filesystem as the destination so the
	// final swap is an atomic rename.
	defaultDir := filepath.Join(f.ProfileDir, "storage", "default")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		return err
	}
	stageRoot, err := os.MkdirTemp(defaultDir, ".gusset-apply-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()
	stageIdb := filepath.Join(stageRoot, "idb")
	if err := os.MkdirAll(stageIdb, 0o755); err != nil {
		return err
	}

	// 1. The sqlite, named by the (machine-independent) IDB base name.
	stagedSQLite := filepath.Join(stageIdb, meta.IDBFileBase+".sqlite")
	if err := copyFile(filepath.Join(snapshotDir, "storage.sqlite"), stagedSQLite, 0o644); err != nil {
		return fmt.Errorf("staging sqlite: %w", err)
	}
	// 2. Rewrite the embedded origin (DELTA 2, place 3).
	if err := rewriteDatabaseOrigin(stagedSQLite, targetOrigin); err != nil {
		return err
	}
	// 3. External value files.
	if len(meta.ExternalFiles) > 0 {
		stagedFiles := filepath.Join(stageIdb, meta.IDBFileBase+".files")
		if err := os.MkdirAll(stagedFiles, 0o755); err != nil {
			return err
		}
		for _, id := range meta.ExternalFiles {
			name := strconv.Itoa(id)
			if err := copyFile(filepath.Join(snapshotDir, "files", name), filepath.Join(stagedFiles, name), 0o644); err != nil {
				return fmt.Errorf("staging external value %d: %w", id, err)
			}
		}
	}

	// 4. Ensure the origin directory and its .metadata-v2 (DELTA 2, place 2).
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		return err
	}
	metaV2 := filepath.Join(originDir, ".metadata-v2")
	if _, statErr := os.Stat(metaV2); errors.Is(statErr, os.ErrNotExist) {
		if err := synthMetadataV2(snapshotDir, metaV2, meta.SourceUUID, target); err != nil {
			return err
		}
	}
	// If .metadata-v2 already exists, it already names the target UUID — leave it.

	// 5. Swap the new idb/ into place, keeping a backup until success.
	backup := idbDir + ".gusset-bak"
	_ = os.RemoveAll(backup)
	if _, statErr := os.Stat(idbDir); statErr == nil {
		if err := os.Rename(idbDir, backup); err != nil {
			return fmt.Errorf("backing up existing idb: %w", err)
		}
	}
	if err := os.Rename(stageIdb, idbDir); err != nil {
		// Restore the backup on failure.
		if _, e := os.Stat(backup); e == nil {
			_ = os.Rename(backup, idbDir)
		}
		return fmt.Errorf("swapping in new idb: %w", err)
	}
	_ = os.RemoveAll(backup)
	return nil
}

// idbFileBaseRE matches a bare filesystem basename: Firefox derives IDBFileBase
// from the database name alone, so it is always a plain identifier with no path
// separators. uuidRE matches a canonical 36-char UUID. originSuffixRE matches an
// empty suffix or a QuotaManager attribute suffix like "^userContextId=4294967295".
var (
	idbFileBaseRE  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	uuidRE         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	originSuffixRE = regexp.MustCompile(`^(\^[A-Za-z0-9_]+=[0-9A-Za-z]+)*$`)
)

// validateMetaPaths rejects a snapshot whose path-bearing meta fields are not
// the exact shape Apply expects. These fields arrive from a remote peer and are
// joined into filesystem paths, so an unexpected value (a separator, "..", a
// non-UUID) is treated as hostile and refused rather than sanitized.
func validateMetaPaths(meta Meta) error {
	if meta.IDBFileBase == "." || meta.IDBFileBase == ".." || !idbFileBaseRE.MatchString(meta.IDBFileBase) {
		return fmt.Errorf("store: refusing snapshot with unsafe idb_file_base %q", meta.IDBFileBase)
	}
	if !originSuffixRE.MatchString(meta.OriginSuffix) {
		return fmt.Errorf("store: refusing snapshot with unsafe origin_suffix %q", meta.OriginSuffix)
	}
	// SourceUUID is only consumed by synthMetadataV2, but it is embedded by
	// string substitution, so it must be a real UUID even though it never names a
	// path directly.
	if meta.SourceUUID != "" && !uuidRE.MatchString(meta.SourceUUID) {
		return fmt.Errorf("store: refusing snapshot with non-canonical source_uuid %q", meta.SourceUUID)
	}
	return nil
}

// rewriteDatabaseOrigin updates the single row of the IDB `database` table to
// the target origin, opening the staged copy read-write.
func rewriteDatabaseOrigin(sqlitePath, origin string) error {
	db, err := sql.Open("sqlite", "file:"+sqlitePath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("UPDATE database SET origin = ?", origin); err != nil {
		return fmt.Errorf("rewriting database.origin: %w", err)
	}
	return nil
}

// synthMetadataV2 writes a .metadata-v2 for a target origin that does not yet
// exist, by taking the snapshot's captured metadata and substituting the source
// UUID with the target UUID. Both are canonical 36-char UUIDs, so the
// substitution preserves the file's length-prefixed structure.
func synthMetadataV2(snapshotDir, dst, srcUUID, dstUUID string) error {
	data, err := os.ReadFile(filepath.Join(snapshotDir, "metadata-v2"))
	if err != nil {
		return fmt.Errorf("store: cannot synthesize origin metadata (%w); open the extension once on the target to initialize its storage, then retry", err)
	}
	if len(srcUUID) != len(dstUUID) {
		return fmt.Errorf("store: UUID length mismatch (%d vs %d)", len(srcUUID), len(dstUUID))
	}
	data = bytes.ReplaceAll(data, []byte(srcUUID), []byte(dstUUID))
	return os.WriteFile(dst, data, 0o600) //nolint:gosec // G703: dst is built from meta fields validated by validateMetaPaths
}

// profileLocked reports whether Firefox appears to have the profile open, erring
// toward refusal since writing the store out from under a running Firefox risks
// corruption. It combines the two lock mechanisms nsProfileLock can use:
//
//   - the "lock" symlink, classified by ffctl.InspectLock — refuse when a live
//     Firefox holds it (LockedLive) or its target is unparseable (LockUnknown);
//     a verified-stale lock (the holder is gone) is safe to write over.
//   - an fcntl lock on .parentlock, the primitive used where no parseable symlink
//     is written (the open question for macOS). The F_GETLK probe only flags a
//     lock a live process actually holds, so a lingering .parentlock never causes
//     a false refusal.
//
// Whichever mechanism the running Firefox uses, one of the two catches it.
func profileLocked(profileDir string) bool {
	if st, _, _ := ffctl.InspectLock(profileDir); st == ffctl.LockedLive || st == ffctl.LockUnknown {
		return true
	}
	return parentLockHeld(profileDir)
}

func readMeta(dir string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("store: bad meta.json: %w", err)
	}
	return m, nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm) //nolint:gosec // G703: staging paths derive from meta fields validated by validateMetaPaths
}
