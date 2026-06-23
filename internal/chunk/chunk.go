// Package chunk turns a blob into content-defined, encrypted, content-addressed
// chunks and a manifest, and reverses the process. It is the seam between the
// store (which produces a snapshot blob) and the transport (which moves opaque
// ciphertext). Only changed chunks ship; identical chunks dedup.
//
// Security invariants (docs/transport-and-security.md §2):
//   - Chunking happens on plaintext, then each chunk is encrypted — encrypting
//     first would destroy dedup.
//   - Each chunk is addressed by HMAC(K_addr, plaintext) (keyed, not a bare
//     hash) and sealed with that address as AEAD additional data, so a chunk
//     cannot be served from the wrong address (M2). Reconstruct also re-verifies
//     the address after decryption — belt and suspenders.
package chunk

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/justinstimatze/gusset/internal/crypto"

	"github.com/restic/chunker"
)

// Chunk-boundary parameters. They MUST be identical on every machine, or chunk
// boundaries diverge and dedup breaks — so they are fixed compile-time
// constants, not configuration. Smaller than restic's ~1 MiB default so a small
// settings change re-ships ~64 KiB, not a megabyte.
const (
	chunkMin     = 16 * 1024   // 16 KiB
	chunkMax     = 1024 * 1024 // 1 MiB
	chunkAvgBits = 16          // ~64 KiB average chunk size
)

const manifestVersion = 1

// polyLabel derives the per-user chunker polynomial. Changing it would change
// everyone's chunk boundaries, so it is versioned.
const polyLabel = "gusset/v1/chunker-poly"

// ChunkRef is one chunk's place in a manifest: its keyed content address and its
// plaintext length.
type ChunkRef struct {
	Address string `json:"a"`
	Size    int    `json:"n"`
}

// Manifest is the ordered, signed list of chunks that reconstruct one blob. It
// is small (addresses + sizes) and rides the control plane. It carries a keyed
// signature so tampering with the chunk order/list is detectable.
type Manifest struct {
	Version   int        `json:"v"`
	Extension string     `json:"extension,omitempty"`
	Browser   string     `json:"browser,omitempty"`
	CreatedAt int64      `json:"created_at,omitempty"` // unix seconds, caller-supplied
	TotalSize int64      `json:"total_size"`
	Chunks    []ChunkRef `json:"chunks"`
	Sig       string     `json:"sig"`
}

// Meta supplies the non-content manifest fields. CreatedAt is caller-supplied
// (this package never reads the clock) so callers control timestamping.
type Meta struct {
	Extension string
	Browser   string
	CreatedAt int64
}

// DerivePolynomial returns the user's deterministic chunker polynomial, seeded
// from their key. Same key → same polynomial on every machine → identical chunk
// boundaries → working dedup; different users get different boundaries.
func DerivePolynomial(k *crypto.Keys) (chunker.Pol, error) {
	return chunker.DerivePolynomial(k.Stream(polyLabel))
}

// Split chunks r into content-defined chunks, encrypts each, and returns the
// signed manifest plus the deduplicated store entries to ship
// (address -> sealed ciphertext). Identical chunks appear once in the store map
// but as many times as they occur in the manifest order.
func Split(r io.Reader, k *crypto.Keys, pol chunker.Pol, meta Meta) (*Manifest, map[string][]byte, error) {
	c := chunker.NewWithBoundaries(r, pol, chunkMin, chunkMax)
	c.SetAverageBits(chunkAvgBits)

	m := &Manifest{
		Version:   manifestVersion,
		Extension: meta.Extension,
		Browser:   meta.Browser,
		CreatedAt: meta.CreatedAt,
	}
	store := make(map[string][]byte)
	buf := make([]byte, chunkMax)

	for {
		ch, err := c.Next(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("chunk: %w", err)
		}
		pt := ch.Data // valid only until the next Next call; we never retain it
		addr := k.Address(pt)
		if _, seen := store[addr]; !seen {
			sealed, err := k.Seal(pt, []byte(addr))
			if err != nil {
				return nil, nil, fmt.Errorf("seal chunk: %w", err)
			}
			store[addr] = sealed
		}
		m.Chunks = append(m.Chunks, ChunkRef{Address: addr, Size: len(pt)})
		m.TotalSize += int64(len(pt))
	}

	if err := m.sign(k); err != nil {
		return nil, nil, err
	}
	return m, store, nil
}

// Reconstruct rebuilds the original blob from a manifest, fetching each chunk's
// ciphertext via get. It verifies the manifest signature, opens each chunk with
// its address as AAD, and re-verifies Address(plaintext) == the requested
// address — so a corrupt, substituted, or misfiled chunk is rejected.
func Reconstruct(m *Manifest, k *crypto.Keys, get func(address string) ([]byte, error)) ([]byte, error) {
	if err := m.Verify(k); err != nil {
		return nil, err
	}
	out := make([]byte, 0, m.TotalSize)
	for i, ref := range m.Chunks {
		sealed, err := get(ref.Address)
		if err != nil {
			return nil, fmt.Errorf("fetch chunk %d (%s): %w", i, short(ref.Address), err)
		}
		pt, err := k.Open(sealed, []byte(ref.Address))
		if err != nil {
			return nil, fmt.Errorf("decrypt chunk %d (%s): %w", i, short(ref.Address), err)
		}
		if k.Address(pt) != ref.Address {
			return nil, fmt.Errorf("chunk %d address mismatch (corrupt or substituted)", i)
		}
		if len(pt) != ref.Size {
			return nil, fmt.Errorf("chunk %d size mismatch: got %d, want %d", i, len(pt), ref.Size)
		}
		out = append(out, pt...)
	}
	if int64(len(out)) != m.TotalSize {
		return nil, fmt.Errorf("reconstructed size %d != manifest total %d", len(out), m.TotalSize)
	}
	return out, nil
}

// Addresses returns the unique chunk addresses referenced by the manifest, for a
// transport deciding what to fetch or push.
func (m *Manifest) Addresses() []string {
	seen := make(map[string]struct{}, len(m.Chunks))
	out := make([]string, 0, len(m.Chunks))
	for _, ref := range m.Chunks {
		if _, ok := seen[ref.Address]; ok {
			continue
		}
		seen[ref.Address] = struct{}{}
		out = append(out, ref.Address)
	}
	return out
}

// Missing returns the unique addresses the manifest needs that the has callback
// reports absent — the basis for resumable, only-fetch-what's-missing transfer.
func (m *Manifest) Missing(has func(address string) bool) []string {
	var out []string
	for _, addr := range m.Addresses() {
		if !has(addr) {
			out = append(out, addr)
		}
	}
	return out
}

// sign computes the manifest's keyed signature over its canonical form.
func (m *Manifest) sign(k *crypto.Keys) error {
	body, err := m.canonical()
	if err != nil {
		return err
	}
	m.Sig = k.Address(body)
	return nil
}

// Verify checks the manifest signature in constant time.
func (m *Manifest) Verify(k *crypto.Keys) error {
	if m.Sig == "" {
		return errors.New("manifest: missing signature")
	}
	body, err := m.canonical()
	if err != nil {
		return err
	}
	want := k.Address(body)
	if subtle.ConstantTimeCompare([]byte(want), []byte(m.Sig)) != 1 {
		return errors.New("manifest: signature mismatch (tampered or wrong key)")
	}
	return nil
}

// canonical serializes the manifest with an empty signature field for signing
// and verification. Struct-field marshaling is deterministic, so this is stable.
func (m *Manifest) canonical() ([]byte, error) {
	clone := *m
	clone.Sig = ""
	return json.Marshal(&clone)
}

func short(addr string) string {
	if len(addr) > 12 {
		return addr[:12]
	}
	return addr
}
