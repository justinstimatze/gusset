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
	// Pending: the data is fetched and on disk but not yet live — it needs a
	// local action to activate (Firefox loads storage.local at startup, so an
	// applied change needs a restart, and a change that could not be applied
	// because Firefox is running needs it closed and the sync re-run).
	Pending SyncState = "pending"
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
	Total     int       `json:"total,omitempty"`     // total chunks this transfer, for a determinate progress bar
	Detail    string    `json:"detail,omitempty"`    // override hint / error detail
	Since     int64     `json:"since"`
}

// LogLevel classifies an activity-log entry for display.
type LogLevel string

const (
	LogInfo  LogLevel = "info"
	LogOK    LogLevel = "ok"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// LogEntry is one activity-log line. The log exists so a user can check what
// gusset did when a sync doesn't work. It is privacy-first by construction: log
// events, counts, and ids (extension ids and device labels — already shown in
// the UI) — never the passphrase, tokens, sealed bytes, or any extension data
// value. The log lives only in memory, is shown only to the local extension over
// the loopback WebSocket, and is never synced or written to disk.
type LogEntry struct {
	Time    int64    `json:"time"` // caller-supplied unix seconds
	Level   LogLevel `json:"level"`
	Message string   `json:"message"`
}

// maxLog bounds the in-memory activity ring. Old entries fall off — gusset keeps
// no durable history.
const maxLog = 50

// Snapshot is an immutable, JSON-marshalable view of the whole model — the one
// shape all three surfaces render. Slices are sorted for stable output.
type Snapshot struct {
	Peers      []Peer     `json:"peers"`
	Extensions []ExtSync  `json:"extensions"`
	Log        []LogEntry `json:"log"`
}

// Model is the concurrency-safe live status. The daemon mutates it from several
// goroutines (accept loop, sync workers); readers take Snapshots, and a live
// surface (the localhost WS) can Subscribe to be woken on every change.
type Model struct {
	mu    sync.Mutex
	peers map[string]Peer
	exts  map[string]ExtSync
	log   []LogEntry
	subs  map[chan struct{}]struct{}
}

// New returns an empty model.
func New() *Model {
	return &Model{
		peers: map[string]Peer{},
		exts:  map[string]ExtSync{},
		subs:  map[chan struct{}]struct{}{},
	}
}

// Log appends an activity entry (now is caller-supplied unix seconds, as
// elsewhere in gusset). Keep messages to non-sensitive facts — events, counts,
// extension ids, device labels — never secrets or data values; see LogEntry. The
// ring is bounded by maxLog.
func (m *Model) Log(now int64, level LogLevel, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, LogEntry{Time: now, Level: level, Message: message})
	if len(m.log) > maxLog {
		m.log = m.log[len(m.log)-maxLog:]
	}
	m.notify()
}

// Subscribe registers for change notifications. It returns a channel that
// receives a (coalesced) signal whenever the model changes — at most one pending
// signal per subscriber, so a slow reader never blocks a writer and never misses
// that *a* change happened — and an unsubscribe function the caller must invoke
// when done. The caller responds to a signal by taking a fresh Snapshot.
func (m *Model) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

// notify wakes every subscriber. It must be called with m.mu held. The send is
// non-blocking: a subscriber that already has a pending signal stays at one.
func (m *Model) notify() {
	for ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// extKey namespaces a per-extension-per-peer entry.
func extKey(extension, deviceID string) string { return extension + "\x00" + deviceID }

// SetPeer records or replaces a peer's status.
func (m *Model) SetPeer(p Peer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[p.DeviceID] = p
	m.notify()
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
	m.notify()
}

// SetExtSync records or replaces one extension's sync state with one peer.
func (m *Model) SetExtSync(e ExtSync) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exts[extKey(e.Extension, e.DeviceID)] = e
	m.notify()
}

// Snapshot returns a sorted, deep copy safe to render or marshal without the
// lock. Peers sort by DeviceID; extension entries by (Extension, DeviceID).
func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := Snapshot{
		Peers:      make([]Peer, 0, len(m.peers)),
		Extensions: make([]ExtSync, 0, len(m.exts)),
		Log:        make([]LogEntry, 0, len(m.log)),
	}
	// Newest first — the UI reads top-down.
	for i := len(m.log) - 1; i >= 0; i-- {
		snap.Log = append(snap.Log, m.log[i])
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
