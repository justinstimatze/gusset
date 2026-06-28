// Package converge is the reconcile layer that `gusset sync` runs: it builds
// this device's offer (the catalog of allowlisted extensions plus their
// encrypted chunks) and pulls a peer's strictly-newer extensions over an
// authenticated transport connection, applying them locally. It is the seam that
// ties store + chunk + crypto + syncx + transport together, kept free of policy
// and discovery (the command supplies an allow predicate and the connection) so
// it stays unit-testable over an in-memory pipe.
package converge

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/store"
	"github.com/justinstimatze/gusset/internal/syncx"
	"github.com/justinstimatze/gusset/internal/transport"

	"github.com/restic/chunker"
)

// Catalog is a device's advertised offer: the latest manifest per extension. It
// is the opaque blob carried by transport's opOffer (marshaled as JSON here;
// transport never inspects it).
type Catalog map[string]*chunk.Manifest

// BuildOffer snapshots and exports each extension in extIDs, returning a
// transport.StaticSource (the union of every export's encrypted chunks plus the
// JSON catalog to advertise) and the Catalog itself for local comparison.
// createdAt timestamps the exports (caller-supplied; this package reads no
// clock). An export failure for one extension fails the whole offer — a partial
// offer would silently drop an allowlisted extension, violating §6.
func BuildOffer(b *store.Firefox, k *crypto.Keys, pol chunker.Pol, extIDs []string, workDir string, createdAt int64) (transport.StaticSource, Catalog, error) {
	chunks := make(map[string][]byte)
	cat := make(Catalog, len(extIDs))
	for _, ext := range extIDs {
		m, cs, err := syncx.Export(b, ext, workDir, k, pol, createdAt)
		if errors.Is(err, store.ErrNoStore) {
			// Installed but no storage.local yet (e.g. freshly installed): nothing
			// to offer, but this machine can still receive it from a peer. Skip.
			continue
		}
		if err != nil {
			return transport.StaticSource{}, nil, fmt.Errorf("converge: export %s: %w", ext, err)
		}
		for a, c := range cs {
			chunks[a] = c
		}
		cat[ext] = m
	}
	blob, err := json.Marshal(cat)
	if err != nil {
		return transport.StaticSource{}, nil, fmt.Errorf("converge: marshal catalog: %w", err)
	}
	return transport.StaticSource{Chunks: chunks, OfferBlob: blob}, cat, nil
}

// Action is the outcome of reconciling one extension against a peer.
type Action string

const (
	Applied    Action = "applied"     // pulled the peer's newer version and applied it
	LocalNewer Action = "local-newer" // ours is newer or equal; nothing to pull
	Blocked    Action = "blocked"     // peer offers it but it is not allowlisted locally
	Locked     Action = "locked"      // pulled but could not apply: Firefox is running
	Failed     Action = "error"       // pull or apply failed; local state untouched
)

// Outcome reports what happened for one extension during a Pull.
type Outcome struct {
	Extension string
	Action    Action
	Detail    string
}

// Pull fetches the peer's catalog over client, and for each extension the peer
// offers that is allowlisted locally and strictly newer than ours (last-writer-
// wins by timestamp), reconstructs and applies it. local is this device's
// catalog (from BuildOffer) for the comparison. allow gates which of the peer's
// extensions we accept. It returns one Outcome per extension the peer offered;
// an error is returned only for a protocol-level failure (a bad/absent offer),
// not for per-extension apply failures, which are reported as Failed outcomes so
// one bad extension does not abort the rest.
//
// force takes the peer's copy unconditionally, skipping the last-writer-wins
// comparison. It is the seed/clone primitive: "this machine is new (or I want it
// re-mirrored), make it match the peer." Use it when local state is not worth
// preserving; it is the only thing that reliably overwrites a freshly-installed
// extension's default storage, whose snapshot timestamp would otherwise look
// newer than the peer's offer and block the apply.
func Pull(client *transport.Client, target *store.Firefox, k *crypto.Keys, local Catalog, allow func(extID string) bool, workDir string, force bool) ([]Outcome, error) {
	blob, err := client.Offer()
	if err != nil {
		return nil, fmt.Errorf("converge: fetch peer offer: %w", err)
	}
	peer := Catalog{}
	if len(blob) > 0 {
		if err := json.Unmarshal(blob, &peer); err != nil {
			return nil, fmt.Errorf("converge: unmarshal peer offer: %w", err)
		}
	}

	outcomes := make([]Outcome, 0, len(peer))
	for ext, pm := range peer {
		switch {
		case !allow(ext):
			outcomes = append(outcomes, Outcome{ext, Blocked, "not allowlisted locally"})
		case !force && syncx.Newer(local[ext], pm) != pm:
			// Tie or ours newer -> Newer returns the local manifest, not pm.
			// --force short-circuits this so a seed/clone takes the peer's copy
			// even when our (snapshot-stamped) local looks newer.
			outcomes = append(outcomes, Outcome{ext, LocalNewer, ""})
		default:
			if err := syncx.Import(target, pm, k, client.Get, workDir); err != nil {
				// A locked profile is a precondition the user can fix (close
				// Firefox), not a failure of the sync — report it distinctly so
				// the command can give clear restart guidance. The data was
				// fetched and verified; only the on-disk apply was deferred.
				if errors.Is(err, store.ErrProfileLocked) {
					outcomes = append(outcomes, Outcome{ext, Locked, "Firefox is running on this machine"})
					continue
				}
				outcomes = append(outcomes, Outcome{ext, Failed, err.Error()})
				continue
			}
			outcomes = append(outcomes, Outcome{ext, Applied, ""})
		}
	}
	return outcomes, nil
}
