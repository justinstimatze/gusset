// Package store reads and writes an extension's settings store as an opaque
// blob (v1 is blob-level — it never decodes the StructuredClone values inside).
// The Backend interface keeps OS and browser differences from leaking past this
// package; firefox.go is the only implementation for v1.
package store

// Snapshot is a consistent, self-contained capture of one extension's storage,
// materialized on disk under Dir. The layout is:
//
//	<Dir>/storage.sqlite   a clean, checkpointed copy of the IDB database
//	<Dir>/files/<id>       each out-of-line value referenced by the database
//	<Dir>/meta.json        the Meta below, for Apply on another machine
//
// The split matters: Firefox stores large storage.local values as external
// files referenced by integer id from the sqlite `file` table, so the sqlite
// alone is an incomplete store. See docs/firefox-internals-verified.md.
type Snapshot struct {
	Dir  string `json:"-"`
	Meta Meta   `json:"meta"`
}

// Meta records what Apply needs to re-home a snapshot onto a different machine,
// where the per-install identifiers differ. The fields below "browser" are
// Firefox-specific and empty for other backends.
type Meta struct {
	ExtensionID string `json:"extension_id"`
	Browser     string `json:"browser"`

	// SourceUUID is the per-install internal UUID this snapshot was taken from.
	// It is embedded on disk in three places (dir name, .metadata-v2, and the
	// sqlite `database.origin` column) and must be rewritten to the target
	// machine's UUID on Apply.
	SourceUUID string `json:"source_uuid,omitempty"`
	// OriginSuffix is the QuotaManager origin suffix of the source store, e.g.
	// "^userContextId=4294967295", or empty for the bare origin.
	OriginSuffix string `json:"origin_suffix,omitempty"`
	// DBName is the IndexedDB database name, always "webExtensions-storage-local"
	// for the storage.local backend — recorded for verification.
	DBName string `json:"db_name,omitempty"`
	// ExternalFiles lists the ids present under <Dir>/files/.
	ExternalFiles []int `json:"external_files,omitempty"`
}

// Backend captures and restores an extension's storage. Snapshot is read-only
// with respect to the live profile; Apply mutates it.
type Backend interface {
	// Snapshot captures extID's storage into a fresh directory created under
	// workDir and returns it. It does not modify the live profile.
	Snapshot(extID, workDir string) (*Snapshot, error)
}
