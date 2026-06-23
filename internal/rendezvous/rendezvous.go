// Package rendezvous is the Tier-1 signaling layer: how two of your machines on
// different networks find each other's reachability when mDNS cannot reach
// across the internet (docs/transport-and-security.md §4, Tier 1).
//
// Each device publishes a Beacon — its LAN endpoints and STUN-discovered
// public (server-reflexive) candidate — to a Signaling channel, and reads its
// peers' beacons to learn where to dial. The Beacon is sealed with the
// passphrase-derived key before publication, so the carrier (Firefox Sync, via
// the companion extension's storage.sync) sees only opaque ciphertext: "no
// plaintext, no usable secrets" (§1). Sealing is also the authentication gate —
// a beacon that Opens was sealed by a holder of the same passphrase, so a forged
// or tampered entry is rejected without any separate signature. Even a leaked
// beacon is not enough to impersonate: connecting still requires the
// passphrase-derived mutual-TLS in internal/transport, which a reader cannot
// forge.
//
// This package is the signaling *contract* plus a filesystem-backed Signaling
// impl (DirSignaling) for local and cross-process tests — mirroring how the
// transport was exercised over loopback before mDNS existed. The production
// Signaling impl (the companion extension writing to storage.sync) is browser-
// side and is the next Tier-1 step; it becomes just another implementation of
// this interface. NAT hole-punching (an ICE agent over the reflexive candidates)
// is the other deferred Tier-1 step; see internal/stunc for the candidate the
// beacon carries.
package rendezvous

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/justinstimatze/gusset/internal/crypto"
)

// SchemaVersion is the wire version of a Beacon. A reader rejects any other
// version rather than misread a future format (the same "version the format"
// discipline as the mDNS service name).
const SchemaVersion = 1

// beaconAAD domain-separates beacon ciphertext from chunk ciphertext: both are
// sealed with the same crypto.Keys, but Open checks the AAD, so a chunk can
// never be misread as a beacon or vice versa.
var beaconAAD = []byte("gusset/v1/rendezvous-beacon")

// ErrSchema is returned by Open for a beacon whose schema version is not
// understood.
var ErrSchema = errors.New("rendezvous: unsupported beacon schema version")

// Beacon is a device's reachability advertisement. It is small by design — a few
// hundred bytes — because it rides Firefox Sync, where gusset is a good citizen
// (no bulk on storage.sync).
type Beacon struct {
	SchemaVersion int      `json:"v"`
	DeviceID      string   `json:"device_id"`     // stable per-device id; distinguishes your own machines (the shared passphrase identity cannot)
	Instance      string   `json:"instance"`      // human label (hostname), for status display
	LANEndpoints  []string `json:"lan,omitempty"` // host:port candidates on the local network
	SrvReflexive  string   `json:"srflx,omitempty"`
	IssuedAt      int64    `json:"issued_at"` // caller-supplied unix seconds; this package reads no clock
}

// Seal validates and serializes a beacon, then AEAD-seals it for publication.
// The returned bytes are opaque to anyone without the passphrase-derived key.
func Seal(b Beacon, k *crypto.Keys) ([]byte, error) {
	if b.DeviceID == "" {
		return nil, errors.New("rendezvous: beacon needs a DeviceID")
	}
	if b.SchemaVersion == 0 {
		b.SchemaVersion = SchemaVersion
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("rendezvous: marshal beacon: %w", err)
	}
	sealed, err := k.Seal(raw, beaconAAD)
	if err != nil {
		return nil, fmt.Errorf("rendezvous: seal beacon: %w", err)
	}
	return sealed, nil
}

// Open reverses Seal: it fails closed on any tampering, wrong key, or mismatched
// AAD (so a chunk ciphertext cannot be opened here), and rejects an unknown
// schema version.
func Open(sealed []byte, k *crypto.Keys) (Beacon, error) {
	raw, err := k.Open(sealed, beaconAAD)
	if err != nil {
		return Beacon{}, fmt.Errorf("rendezvous: open beacon: %w", err)
	}
	var b Beacon
	if err := json.Unmarshal(raw, &b); err != nil {
		return Beacon{}, fmt.Errorf("rendezvous: unmarshal beacon: %w", err)
	}
	if b.SchemaVersion != SchemaVersion {
		return Beacon{}, fmt.Errorf("%w: got %d, want %d", ErrSchema, b.SchemaVersion, SchemaVersion)
	}
	return b, nil
}

// Fresh reports whether a beacon issued at b.IssuedAt is still current at now
// (both unix seconds), i.e. no older than maxAge. A stale beacon (peer long
// offline) is dropped so a dead endpoint is not dialed; the caller supplies now
// so this package reads no clock.
func Fresh(b Beacon, now int64, maxAge time.Duration) bool {
	age := now - b.IssuedAt
	return age >= 0 && age <= int64(maxAge/time.Second)
}

// Signaling is the channel over which devices exchange sealed beacons. The
// production impl is the companion extension publishing each device's beacon to
// its own storage.sync key (carried end-to-end-encrypted by Firefox Sync) and
// reading peers' keys — deferred, browser-side. DirSignaling below is the
// filesystem-backed impl for tests and for a manual file-drop rendezvous.
type Signaling interface {
	// Publish stores this device's sealed beacon under selfID, replacing any
	// previous beacon for that device.
	Publish(ctx context.Context, selfID string, sealed []byte) error
	// Fetch returns every other device's latest sealed beacon, excluding selfID.
	// Finding none is not an error (no peer has published yet).
	Fetch(ctx context.Context, selfID string) ([][]byte, error)
}

// DirSignaling is a Signaling backed by a shared directory: each device writes
// its sealed beacon to "<hex(selfID)>.beacon". It stands in for Firefox Sync in
// tests and supports a manual rendezvous (e.g. a synced folder) without the
// companion extension. The directory holds only sealed (opaque) beacons.
type DirSignaling struct {
	Dir string
}

const beaconExt = ".beacon"

// maxBeaconBytes and maxBeacons bound what Fetch will read from the carrier. A
// sealed beacon is a few hundred bytes by design, and a person syncs a handful
// of their own devices — so these caps are generous for honest use yet stop a
// hostile or buggy writer with access to the shared folder (which the threat
// model treats as an untrusted courier) from exhausting memory with one giant
// file or a flood of small ones. An over-cap file is skipped, not read: a beacon
// that large is not a real beacon.
const (
	maxBeaconBytes = 64 << 10 // 64 KiB
	maxBeacons     = 256
)

// fileName hex-encodes selfID so any device id is a safe, collision-free
// filename.
func (d DirSignaling) fileName(selfID string) string {
	return hex.EncodeToString([]byte(selfID)) + beaconExt
}

// Publish writes the sealed beacon atomically (temp file + rename, 0600).
func (d DirSignaling) Publish(_ context.Context, selfID string, sealed []byte) error {
	if selfID == "" {
		return errors.New("rendezvous: empty selfID")
	}
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return fmt.Errorf("rendezvous: make signaling dir: %w", err)
	}
	final := filepath.Join(d.Dir, d.fileName(selfID))
	tmp, err := os.CreateTemp(d.Dir, "beacon-*.tmp")
	if err != nil {
		return fmt.Errorf("rendezvous: temp beacon: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("rendezvous: chmod beacon: %w", err)
	}
	if _, err := tmp.Write(sealed); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("rendezvous: write beacon: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("rendezvous: close beacon: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("rendezvous: publish beacon: %w", err)
	}
	return nil
}

// Fetch reads every "*.beacon" except this device's, returning the sealed bytes.
// An empty or absent directory yields no beacons and no error.
func (d DirSignaling) Fetch(_ context.Context, selfID string) ([][]byte, error) {
	self := d.fileName(selfID)
	entries, err := os.ReadDir(d.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rendezvous: read signaling dir: %w", err)
	}
	var out [][]byte
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != beaconExt || name == self {
			continue
		}
		// The carrier is untrusted: anyone with write access to the folder can
		// drop files. Stop once we have collected enough — a beacon that does not
		// Open is discarded by the caller anyway, so reading more is wasted work
		// and a memory-exhaustion lever.
		if len(out) >= maxBeacons {
			break
		}
		data, err := readCapped(filepath.Join(d.Dir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // a peer replaced its beacon between ReadDir and here
			}
			return nil, fmt.Errorf("rendezvous: read beacon %s: %w", name, err)
		}
		if data == nil {
			continue // over the size cap — not a real beacon, skip it
		}
		out = append(out, data)
	}
	return out, nil
}

// readCapped reads a file but never holds more than maxBeaconBytes in memory: it
// reads one byte past the cap and, if that byte exists, reports the file as
// over-cap by returning (nil, nil) rather than the contents. Using a bounded
// reader (not a stat) makes the cap robust against a writer that grows the file
// between a size check and the read.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBeaconBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBeaconBytes {
		return nil, nil // over the cap; refuse without keeping it
	}
	return data, nil
}

var _ Signaling = DirSignaling{}
