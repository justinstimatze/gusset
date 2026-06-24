// Package syncx is the integration seam: it serializes a store snapshot
// directory into a single deterministic stream (so the chunker can process it)
// and back, and wires store → chunk → transport → reconstruct → store.Apply into
// export/import operations. It also holds the last-writer-wins conflict choice.
//
// Named syncx rather than sync to avoid shadowing the standard library's sync.
package syncx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// packMagic identifies the snapshot archive format. The format is deliberately
// minimal and timestamp-free so identical snapshot content packs to identical
// bytes — which is what lets content-defined chunking dedup across versions.
var packMagic = []byte("gusset-snap-v1\n")

// Pack serializes a snapshot directory into a deterministic byte stream: the
// magic header, the file count, then each regular file as
// (uvarint-len name, uvarint-len data), entries sorted by relative path. Empty
// directories are not represented (the snapshot layout has none that matter).
func Pack(dir string) ([]byte, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("syncx: walking snapshot: %w", err)
	}
	sort.Strings(rels)

	var buf bytes.Buffer
	buf.Write(packMagic)
	putUvarint(&buf, uint64(len(rels)))
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		putBytes(&buf, []byte(rel))
		putBytes(&buf, data)
	}
	return buf.Bytes(), nil
}

// Unpack writes a packed stream back into destDir as a snapshot directory. It
// rejects entries whose names would escape destDir (defense in depth — the
// stream is already authenticated by the manifest signature and per-chunk
// addresses, but unpacking untrusted-shaped data warrants the check).
func Unpack(data []byte, destDir string) error {
	r := bytes.NewReader(data)
	hdr := make([]byte, len(packMagic))
	if _, err := r.Read(hdr); err != nil || !bytes.Equal(hdr, packMagic) {
		return errors.New("syncx: bad snapshot archive header")
	}
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return fmt.Errorf("syncx: reading entry count: %w", err)
	}
	for i := uint64(0); i < n; i++ {
		name, err := getBytes(r)
		if err != nil {
			return err
		}
		body, err := getBytes(r)
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(string(name))
		dst, ok := safeJoin(destDir, rel)
		if !ok {
			return fmt.Errorf("syncx: unsafe entry path %q", string(name))
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// safeJoin joins rel onto base and confirms the result stays within base.
func safeJoin(base, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	dst := filepath.Join(base, rel)
	cleanBase := filepath.Clean(base) + string(os.PathSeparator)
	if dst != filepath.Clean(base) && !hasPrefix(dst, cleanBase) {
		return "", false
	}
	return dst, true
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func putUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	buf.Write(tmp[:binary.PutUvarint(tmp[:], v)])
}

func putBytes(buf *bytes.Buffer, b []byte) {
	putUvarint(buf, uint64(len(b)))
	buf.Write(b)
}

func getBytes(r *bytes.Reader) ([]byte, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > uint64(r.Len()) { //nolint:gosec // G115: r.Len() is non-negative; this is the bounds check that rejects an oversize length
		return nil, fmt.Errorf("syncx: entry length %d exceeds remaining %d", n, r.Len())
	}
	out := make([]byte, n)
	if _, err := r.Read(out); err != nil && n > 0 {
		return nil, err
	}
	return out, nil
}
