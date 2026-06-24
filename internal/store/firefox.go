package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/justinstimatze/gusset/internal/profile"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, registered as "sqlite"
)

// ErrNoStore is returned by Snapshot when the extension is installed (has a
// per-install UUID) but has not created its storage.local database yet — a
// freshly installed extension with no settings. Callers that sync many
// extensions (converge.BuildOffer) treat this as "nothing to offer for this
// one", not a failure: such a machine still receives the extension's settings
// from a peer.
var ErrNoStore = errors.New("store: no storage.local database for extension yet")

// storageLocalDBName is the IndexedDB database that backs browser.storage.local.
// We identify the right sqlite by this name rather than by guessing the origin
// directory, because an extension may open other IndexedDB databases (e.g. uBO's
// regenerable uBlock0CacheStorage) under a sibling origin.
const storageLocalDBName = "webExtensions-storage-local"

// defaultOriginSuffix is the QuotaManager origin suffix under which storage.local
// lives on current Firefox (4294967295 = 0xFFFFFFFF, the default user context).
const defaultOriginSuffix = "^userContextId=4294967295"

// Firefox is a store.Backend over a resolved Firefox profile directory.
type Firefox struct {
	ProfileDir string
}

// NewFirefox returns a Backend rooted at an already-resolved profile directory
// (see internal/profile).
func NewFirefox(profileDir string) *Firefox { return &Firefox{ProfileDir: profileDir} }

var _ Backend = (*Firefox)(nil)

// Snapshot captures extID's storage.local into a fresh directory under workDir.
// It resolves the per-install UUID, locates the storage.local IDB by database
// name, takes a consistent copy via VACUUM INTO while Firefox holds the store
// open, and copies the out-of-line external value files alongside.
func (f *Firefox) Snapshot(extID, workDir string) (*Snapshot, error) {
	uuids, err := profile.ExtensionUUIDs(f.ProfileDir)
	if err != nil {
		return nil, err
	}
	uuid, ok := uuids[extID]
	if !ok {
		return nil, fmt.Errorf("extension %q has no per-install UUID in prefs.js (installed?)", extID)
	}

	dbPath, suffix, err := f.findStorageLocalDB(uuid)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp(workDir, "gusset-snap-")
	if err != nil {
		return nil, fmt.Errorf("creating snapshot dir: %w", err)
	}
	// On any failure past this point, don't leave a half-written snapshot behind.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()

	snapDB := filepath.Join(dir, "storage.sqlite")
	if err := vacuumInto(dbPath, snapDB); err != nil {
		return nil, err
	}

	// Capture the origin's QuotaManager metadata so Apply can re-home the store
	// onto a machine where the origin dir does not yet exist. originDir is the
	// grandparent of the sqlite (<origin>/idb/<base>.sqlite).
	originDir := filepath.Dir(filepath.Dir(dbPath))
	if md, err := os.ReadFile(filepath.Join(originDir, ".metadata-v2")); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "metadata-v2"), md, 0o600); err != nil { //nolint:gosec // G703: dir is gusset's own freshly-created snapshot directory, not remote input
			return nil, fmt.Errorf("capturing .metadata-v2: %w", err)
		}
	}

	fileIDs, err := externalFileIDs(snapDB)
	if err != nil {
		return nil, err
	}
	if len(fileIDs) > 0 {
		// The out-of-line values live in a sibling "<db>.files" directory — the
		// ".sqlite" suffix is replaced, not appended.
		srcFiles := strings.TrimSuffix(dbPath, ".sqlite") + ".files"
		if err := copyExternalFiles(srcFiles, filepath.Join(dir, "files"), fileIDs); err != nil {
			return nil, err
		}
	}

	snap := &Snapshot{
		Dir: dir,
		Meta: Meta{
			ExtensionID:   extID,
			Browser:       "firefox",
			SourceUUID:    uuid,
			OriginSuffix:  suffix,
			DBName:        storageLocalDBName,
			IDBFileBase:   strings.TrimSuffix(filepath.Base(dbPath), ".sqlite"),
			ExternalFiles: fileIDs,
		},
	}
	if err := writeMeta(dir, snap.Meta); err != nil {
		return nil, err
	}

	cleanup = false
	return snap, nil
}

// findStorageLocalDB locates the storage.local IDB sqlite for a given install
// UUID. It probes both origin-directory variants (bare and default-user-context)
// and returns the first sqlite whose `database` table names it the storage.local
// store, along with that origin's suffix.
func (f *Firefox) findStorageLocalDB(uuid string) (dbPath, originSuffix string, err error) {
	base := filepath.Join(f.ProfileDir, "storage", "default")
	origins := []string{
		"moz-extension+++" + uuid + defaultOriginSuffix,
		"moz-extension+++" + uuid,
	}
	var probed []string
	for _, origin := range origins {
		idbDir := filepath.Join(base, origin, "idb")
		matches, _ := filepath.Glob(filepath.Join(idbDir, "*.sqlite"))
		probed = append(probed, idbDir)
		for _, candidate := range matches {
			name, err := databaseName(candidate)
			if err != nil {
				continue // unreadable/locked candidate; try the next
			}
			if name == storageLocalDBName {
				return candidate, originSuffixOf(origin), nil
			}
		}
	}
	return "", "", fmt.Errorf("%w: UUID %s under: %s",
		ErrNoStore, uuid, strings.Join(probed, ", "))
}

// originSuffixOf returns the "^userContextId=..." suffix of an origin dir name,
// or "" if there is none.
func originSuffixOf(origin string) string {
	if i := strings.IndexByte(origin, '^'); i >= 0 {
		return origin[i:]
	}
	return ""
}

// databaseName reads the single row of the IDB `database` table to identify the
// store. The db is opened read-only and immutably; it is only inspected, never
// snapshotted, so ignoring any concurrent WAL writes is fine here.
func databaseName(path string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	var name string
	if err := db.QueryRow("SELECT name FROM database LIMIT 1").Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

// vacuumInto produces a clean, checkpointed standalone copy of a live sqlite
// database. VACUUM INTO runs in a read transaction, so it yields a consistent
// point-in-time copy even while Firefox holds the store open in WAL mode.
func vacuumInto(srcPath, destPath string) error {
	db, err := sql.Open("sqlite", "file:"+srcPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening source db: %w", err)
	}
	defer func() { _ = db.Close() }()
	// VACUUM INTO does not accept a bound parameter for the path on all sqlite
	// builds; embed it as an escaped string literal.
	stmt := "VACUUM INTO '" + strings.ReplaceAll(destPath, "'", "''") + "'" //nolint:gosec // G202: VACUUM INTO cannot bind the path; the literal is single-quote-escaped
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("VACUUM INTO snapshot: %w", err)
	}
	return nil
}

// externalFileIDs returns the ids of out-of-line values recorded in the
// snapshot's `file` table. Each corresponds to a blob under the source store's
// "<db>.sqlite.files/<id>".
func externalFileIDs(snapDB string) ([]int, error) {
	db, err := sql.Open("sqlite", "file:"+snapDB+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT id FROM file ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("reading file table: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// copyExternalFiles copies each referenced external value file from the live
// store's .files directory into the snapshot. A file referenced by the snapshot
// is kept alive by its refcount, so it is still present here despite the small
// window between the VACUUM and this copy.
func copyExternalFiles(srcDir, destDir string, ids []int) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, id := range ids {
		name := fmt.Sprintf("%d", id)
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			return fmt.Errorf("reading external value %d: %w", id, err)
		}
		if err := os.WriteFile(filepath.Join(destDir, name), data, 0o644); err != nil { //nolint:gosec // G703: destDir is gusset's own freshly-created snapshot directory; name is a numeric id
			return fmt.Errorf("writing external value %d: %w", id, err)
		}
	}
	return nil
}

func writeMeta(dir string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), append(data, '\n'), 0o644)
}
