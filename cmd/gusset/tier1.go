package main

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/rendezvous"
	"github.com/justinstimatze/gusset/internal/status"
)

// rendezvousMaxAge bounds how old a fetched beacon may be before it is ignored:
// a peer that published long ago and went offline should not be dialed. It is
// generous relative to a sync window so a beacon published at the start of a
// peer's run stays valid through ours, and the loop re-publishes each pass to
// keep our own beacon fresh while the window is open.
const rendezvousMaxAge = 15 * time.Minute

// rendezvousSource is the Tier-1 peer feed: a Signaling carrier (today a shared
// folder via rendezvous.DirSignaling, tomorrow the companion extension's
// storage.sync) plus the key that seals and opens beacons. It is the
// cross-network analogue of discovery.Browse — peers that cannot hear each
// other's mDNS multicast still exchange sealed beacons through the carrier and
// learn where to dial (docs/transport-and-security.md §4, Tier 1).
type rendezvousSource struct {
	sig    rendezvous.Signaling
	k      *crypto.Keys
	selfID string
}

// rzPeer is one opened beacon resolved to dial targets (most-likely-first) plus
// the peer's ICE endpoint, if any, for the hole-punch fallback when every direct
// target fails.
type rzPeer struct {
	instance string
	deviceID string
	name     string
	targets  []dialTarget
	ice      *rendezvous.ICEEndpoint
}

// publish seals this device's beacon and writes it to the carrier, replacing any
// previous beacon for this device.
func (s rendezvousSource) publish(ctx context.Context, b rendezvous.Beacon) error {
	sealed, err := rendezvous.Seal(b, s.k)
	if err != nil {
		return err
	}
	return s.sig.Publish(ctx, s.selfID, sealed)
}

// peers fetches every other device's beacon, drops the unsealable (a beacon from
// a different passphrase, or tampered) and the stale, and returns each as ordered
// dial targets. now is caller-supplied unix seconds — this layer reads no clock.
func (s rendezvousSource) peers(ctx context.Context, now int64) ([]rzPeer, error) {
	sealedList, err := s.sig.Fetch(ctx, s.selfID)
	if err != nil {
		return nil, err
	}
	var out []rzPeer
	for _, sealed := range sealedList {
		b, err := rendezvous.Open(sealed, s.k)
		if err != nil {
			continue // not ours, or tampered — it Opens only under the shared passphrase
		}
		if !rendezvous.Fresh(b, now, rendezvousMaxAge) {
			continue
		}
		targets := beaconTargets(b)
		if len(targets) == 0 && b.ICE == nil {
			continue // nothing dialable and no hole-punch endpoint either
		}
		out = append(out, rzPeer{instance: b.Instance, deviceID: b.DeviceID, name: b.Name, targets: targets, ice: b.ICE})
	}
	return out, nil
}

// beaconTargets orders a beacon's candidates the way they should be dialed: LAN
// endpoints first (most likely to work, no NAT in the way). A peer that no LAN
// endpoint reaches falls back to the ICE hole-punch path (the beacon's ICE
// endpoint), driven by the caller — there is no direct-public-IP guess, which
// was usually undialable and leaked the public address into the beacon.
func beaconTargets(b rendezvous.Beacon) []dialTarget {
	var targets []dialTarget
	for _, e := range b.LANEndpoints {
		targets = append(targets, dialTarget{addr: e, link: status.LinkLAN})
	}
	return targets
}

// gatherBeacon builds this device's reachability advertisement: every
// non-loopback IPv4 at the listener's port. These are what reach a peer on the
// same routed network or VPN where mDNS multicast does not cross. Cross-NAT
// reachability rides the ICE endpoint gathered separately (gatherICESession)
// and advertised alongside this beacon, not a server-reflexive guess.
func gatherBeacon(deviceID, instance, name string, listenPort int, now int64) rendezvous.Beacon {
	return rendezvous.Beacon{
		DeviceID:     deviceID,
		Instance:     instance,
		Name:         name,
		LANEndpoints: localLANEndpoints(listenPort),
		IssuedAt:     now,
	}
}

// localLANEndpoints returns this host's non-loopback IPv4 interface addresses
// joined with the listener's port — the host:port forms a peer on the same
// network can dial. The listener binds 0.0.0.0, so its reachable addresses are
// exactly the host's interface IPs at that port.
func localLANEndpoints(port int) []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipnet.IP.To4()
		if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, net.JoinHostPort(v4.String(), strconv.Itoa(port)))
	}
	return out
}
