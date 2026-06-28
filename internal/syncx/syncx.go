package syncx

import (
	"bytes"
	"os"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/store"

	"github.com/restic/chunker"
)

// Export snapshots an extension's store, packs it deterministically, and chunks
// it into a signed manifest plus the deduplicated, encrypted store entries to
// ship. The plaintext snapshot is removed before returning (local hygiene —
// docs/transport-and-security.md §7). createdAt is caller-supplied so this
// package never reads the clock.
func Export(b *store.Firefox, extID, workDir string, k *crypto.Keys, pol chunker.Pol, createdAt int64) (*chunk.Manifest, map[string][]byte, error) {
	snap, err := b.Snapshot(extID, workDir)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(snap.Dir) }()

	packed, err := Pack(snap.Dir)
	if err != nil {
		return nil, nil, err
	}
	return chunk.Split(bytes.NewReader(packed), k, pol, chunk.Meta{
		Extension: snap.Meta.ExtensionID,
		Browser:   snap.Meta.Browser,
		CreatedAt: createdAt,
	})
}

// Import reconstructs a packed snapshot from a manifest (fetching chunks via
// get), unpacks it, and applies it to the local profile, re-homing it onto this
// machine's UUID. The reconstructed plaintext snapshot is removed afterward.
func Import(b *store.Firefox, m *chunk.Manifest, k *crypto.Keys, get func(address string) ([]byte, error), workDir string) error {
	blob, err := chunk.Reconstruct(m, k, get)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(workDir, "gusset-import-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	if err := Unpack(blob, dir); err != nil {
		return err
	}
	return b.Apply(dir)
}

// Newer implements last-writer-wins per extension: it returns whichever manifest
// has the later CreatedAt, preferring a on an exact tie. nil inputs are handled.
func Newer(a, b *chunk.Manifest) *chunk.Manifest {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.CreatedAt > a.CreatedAt:
		return b
	default:
		return a
	}
}
