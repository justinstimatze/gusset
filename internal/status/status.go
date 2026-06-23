// Package status is gusset's single source of truth for "what is happening, and
// why." A P2P settings-sync is legitimately "not synced" much of the time (peer
// asleep, denylisted extension, auth failure); that is only acceptable if the
// system always shows the reason. This package holds that model; the daemon
// updates it, and the three surfaces — `gusset status`, the localhost
// WebSocket, and the extension UI — all render the same Snapshot
// (docs/transport-and-security.md §6).
//
// It is deliberately decoupled from the transport: the daemon bridges a
// transport.ConnError into a peer state here, so this package imports nothing of
// the data plane. Timestamps are caller-supplied (unix seconds), as elsewhere in
// gusset, so state transitions are deterministic in tests.
package status

import (
	"sort"
	"sync"
)

// PeerState is a paired device's connectivity, following the progression in
// docs/transport-and-security.md §6.
type PeerState string

const (
	Discovering  PeerState = "discovering"
	Signaling    PeerState = "signaling"
	HolePunching PeerState = "hole-punching"
	Connected    PeerState = "connected"
	Unreachable  PeerState = "unreachable"
)

// Link is how a connected peer is reached. Tier 0 is always LinkLAN.
type Link string

const (
	LinkLAN       Link = "lan"
	LinkDirectNAT Link = "direct-nat"
)

// Reason explains an Unreachable peer. AuthFailed is what a transport
// PhaseHandshake failure maps to.
type Reason string

const (
	PeerOffline Reason = "peer-offline"
	NATFailed   Reason = "nat-traversal-failed"
	AuthFailed  Reason = "auth-failed"
)

// SyncState is one extension's convergence state with one peer.
type SyncState string

const (
	InSync  SyncState = "in-sync"
	Pushing SyncState = "pushing"
	Pulling SyncState = "pulling"
	Stale   SyncState = "stale"
	Blocked SyncState = "blocked"
	Errored SyncState = "error"
)

// Peer is a paired device's status.
type Peer struct {
	DeviceID string    `json:"device_id"`
	Name     string    `json:"name,omitempty"`
	State    PeerState `json:"state"`
	Link     Link      `json:"link,omitempty"`   // when Connected
	Reason   Reason    `json:"reason,omitempty"` // when Unreachable
	Detail   string    `json:"detail,omitempty"` // free-text elaboration
	Since    int64     `json:"since"`            // unix secs, entered this state
}

// ExtSync is one extension's sync state with one peer.
type ExtSync struct {
	Extension string    `json:"extension"`
	DeviceID  string    `json:"device_id"`
	State     SyncState `json:"state"`
	Remaining int       `json:"remaining,omitempty"` // chunks left, for Pushing/Pulling
	Detail    string    `json:"detail,omitempty"`    // override hint / error detail
	Since     int64     `json:"since"`
}

// Snapshot is an immutable, JSON-marshalable view of the whole model — the one
// shape all three surfaces render. Slices are sorted for stable output.
type Snapshot struct {
	Peers      []Peer    `json:"peers"`
	Extensions []ExtSync `json:"extensions"`
}

// Model is the concurrency-safe live status. The daemon mutates it from several
// goroutines (accept loop, sync workers); readers take Snapshots.
type Model struct {
	mu    sync.Mutex
	peers map[string]Peer
	exts  map[string]ExtSync
}

// New returns an empty model.
func New() *Model {
	return &Model{peers: map[string]Peer{}, exts: map[string]ExtSync{}}
}

// extKey namespaces a per-extension-per-peer entry.
func extKey(extension, deviceID string) string { return extension + "\x00" + deviceID }

// SetPeer records or replaces a peer's status.
func (m *Model) SetPeer(p Peer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[p.DeviceID] = p
}

// RemovePeer drops a peer and all its per-extension entries (e.g. on unpair).
func (m *Model) RemovePeer(deviceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.peers, deviceID)
	for k, e := range m.exts {
		if e.DeviceID == deviceID {
			delete(m.exts, k)
		}
	}
}

// SetExtSync records or replaces one extension's sync state with one peer.
func (m *Model) SetExtSync(e ExtSync) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exts[extKey(e.Extension, e.DeviceID)] = e
}

// Snapshot returns a sorted, deep copy safe to render or marshal without the
// lock. Peers sort by DeviceID; extension entries by (Extension, DeviceID).
func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := Snapshot{
		Peers:      make([]Peer, 0, len(m.peers)),
		Extensions: make([]ExtSync, 0, len(m.exts)),
	}
	for _, p := range m.peers {
		snap.Peers = append(snap.Peers, p)
	}
	for _, e := range m.exts {
		snap.Extensions = append(snap.Extensions, e)
	}
	sort.Slice(snap.Peers, func(i, j int) bool {
		return snap.Peers[i].DeviceID < snap.Peers[j].DeviceID
	})
	sort.Slice(snap.Extensions, func(i, j int) bool {
		if snap.Extensions[i].Extension != snap.Extensions[j].Extension {
			return snap.Extensions[i].Extension < snap.Extensions[j].Extension
		}
		return snap.Extensions[i].DeviceID < snap.Extensions[j].DeviceID
	})
	return snap
}
