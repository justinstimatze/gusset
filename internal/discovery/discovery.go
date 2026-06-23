// Package discovery is gusset's LAN rendezvous: it advertises this device's
// Tier-0 transport listener over mDNS and finds peers doing the same, so two
// `gusset sync` runs on the same WiFi find each other with no Firefox Sync, no
// extension, and no account (docs/transport-and-security.md §8). mDNS only
// announces "a gusset peer is here at host:port"; who may actually connect is
// still gated by the passphrase-derived mutual-TLS in internal/transport.
package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/grandcat/zeroconf"
)

// service and domain name the mDNS service. Versioned in the name so a future
// incompatible discovery format does not cross-talk with v1.
const (
	service = "_gusset._tcp"
	domain  = "local."
)

// Peer is a gusset endpoint discovered on the LAN, with Addr already in the
// host:port form transport.Dial accepts.
type Peer struct {
	Instance string
	Addr     string
}

// Advertise announces this device's transport listener on the LAN. instance is a
// human-recognizable label (e.g. the hostname); port is the transport listener's
// actual port (use Server.Addr to get the OS-assigned one). The returned stop
// function withdraws the advertisement; call it when the sync window ends, so
// the announcement lives only as long as the listener (the §8 privacy posture).
func Advertise(instance string, port int) (stop func(), err error) {
	server, err := zeroconf.Register(instance, service, domain, port, []string{"v=1"}, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: advertise: %w", err)
	}
	return server.Shutdown, nil
}

// Browse looks for gusset peers on the LAN for up to timeout and returns those
// found, skipping self (matched by instance label) and deduplicating by address.
// A peer that advertises no IPv4 address is skipped. It is not an error to find
// none — on an isolated network or with mDNS blocked, the caller reports "no
// peer found" rather than failing.
func Browse(ctx context.Context, self string, timeout time.Duration) ([]Peer, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry, 16)
	var peers []Peer
	seen := make(map[string]bool)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range entries {
			if e.Instance == self {
				continue // don't discover ourselves
			}
			ip := firstIPv4(e)
			if ip == "" {
				continue
			}
			addr := net.JoinHostPort(ip, strconv.Itoa(e.Port))
			if seen[addr] {
				continue
			}
			seen[addr] = true
			peers = append(peers, Peer{Instance: e.Instance, Addr: addr})
		}
	}()

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := resolver.Browse(cctx, service, domain, entries); err != nil {
		return nil, fmt.Errorf("discovery: browse: %w", err)
	}
	<-cctx.Done() // zeroconf closes entries when the context is cancelled
	<-done
	return peers, nil
}

// firstIPv4 returns the first usable IPv4 address an entry advertises, or "".
func firstIPv4(e *zeroconf.ServiceEntry) string {
	for _, ip := range e.AddrIPv4 {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}
