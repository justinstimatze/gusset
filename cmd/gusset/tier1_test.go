package main

import (
	"context"
	"net"
	"testing"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/rendezvous"
	"github.com/justinstimatze/gusset/internal/status"
)

func tier1Keys(t *testing.T) *crypto.Keys {
	t.Helper()
	k, err := crypto.DeriveKeys("correct horse battery staple lorem ipsum", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestLocalLANEndpoints_FormatAndPort(t *testing.T) {
	// Whatever interfaces this host has, every returned endpoint must be a
	// dialable host:port at the requested port, never loopback or link-local.
	for _, ep := range localLANEndpoints(51234) {
		host, port, err := net.SplitHostPort(ep)
		if err != nil {
			t.Fatalf("endpoint %q is not host:port: %v", ep, err)
		}
		if port != "51234" {
			t.Errorf("endpoint %q: want port 51234, got %s", ep, port)
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			t.Errorf("endpoint %q: host is not an IPv4 address", ep)
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Errorf("endpoint %q: loopback/link-local should be excluded", ep)
		}
	}
}

func TestRendezvousSource_PeersOrdersFiltersAndExcludesSelf(t *testing.T) {
	k := tier1Keys(t)
	dir := t.TempDir()
	ctx := context.Background()

	// "self" publishes; it must never appear in its own fetch.
	self := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: k, selfID: "self"}
	if err := self.publish(ctx, rendezvous.Beacon{
		DeviceID: "self", Instance: "me",
		LANEndpoints: []string{"192.168.1.5:1000"}, IssuedAt: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}

	// A fresh peer with both a LAN endpoint and a reflexive candidate.
	peer := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: k, selfID: "peer"}
	if err := peer.publish(ctx, rendezvous.Beacon{
		DeviceID: "peer", Instance: "kestrel",
		LANEndpoints: []string{"192.168.1.9:2000"}, SrvReflexive: "203.0.113.7:2000",
		IssuedAt: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}

	// A stale peer that must be filtered out at fetch time.
	stale := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: k, selfID: "stale"}
	if err := stale.publish(ctx, rendezvous.Beacon{
		DeviceID: "stale", Instance: "ghost",
		LANEndpoints: []string{"192.168.1.99:3000"}, IssuedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// now is just past the fresh peer's IssuedAt; the stale one is far older
	// than rendezvousMaxAge.
	got, err := self.peers(ctx, 1_000_060)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one peer (self excluded, stale filtered), got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.deviceID != "peer" || p.instance != "kestrel" {
		t.Fatalf("wrong peer resolved: %+v", p)
	}
	if len(p.targets) != 2 {
		t.Fatalf("want 2 dial targets (LAN then reflexive), got %d", len(p.targets))
	}
	if p.targets[0].addr != "192.168.1.9:2000" || p.targets[0].link != status.LinkLAN {
		t.Errorf("first target should be the LAN endpoint, got %+v", p.targets[0])
	}
	if p.targets[1].addr != "203.0.113.7:2000" || p.targets[1].link != status.LinkDirectNAT {
		t.Errorf("second target should be the reflexive direct-NAT candidate, got %+v", p.targets[1])
	}
}

func TestRendezvousSource_CarriesICEEndpoint(t *testing.T) {
	k := tier1Keys(t)
	dir := t.TempDir()
	ctx := context.Background()

	// A peer behind a NAT may publish only an ICE endpoint (no dialable targets);
	// the source must still surface it as a peer so the hole-punch fallback fires.
	peer := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: k, selfID: "peer"}
	want := &rendezvous.ICEEndpoint{Ufrag: "uf", Pwd: "a-longer-ice-password", Candidates: []string{"candidate:1 1 udp 2113937151 203.0.113.7 51000 typ srflx"}}
	if err := peer.publish(ctx, rendezvous.Beacon{
		DeviceID: "peer", Instance: "kestrel", ICE: want, IssuedAt: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}

	self := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: k, selfID: "self"}
	got, err := self.peers(ctx, 1_000_060)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want one peer (ICE-only is still reachable via punch), got %d", len(got))
	}
	if got[0].ice == nil {
		t.Fatal("rzPeer.ice is nil; the ICE endpoint did not plumb through")
	}
	if len(got[0].targets) != 0 {
		t.Fatalf("an ICE-only beacon should have no direct dial targets, got %d", len(got[0].targets))
	}
	ep := toICEEndpoint(*got[0].ice)
	if ep.Ufrag != want.Ufrag || ep.Pwd != want.Pwd || len(ep.Candidates) != 1 {
		t.Fatalf("ICE endpoint conversion mismatch: %+v", ep)
	}
}

func TestRendezvousSource_PeersSkipsForeignPassphrase(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// A beacon sealed under a different passphrase lands in the same folder; it
	// must not Open for us, so it is silently skipped (not an error).
	other, err := crypto.DeriveKeys("an entirely unrelated set of eight words", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	foreign := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: other, selfID: "foreign"}
	if err := foreign.publish(ctx, rendezvous.Beacon{
		DeviceID: "foreign", Instance: "stranger",
		LANEndpoints: []string{"10.0.0.1:9000"}, IssuedAt: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}

	us := rendezvousSource{sig: rendezvous.DirSignaling{Dir: dir}, k: tier1Keys(t), selfID: "us"}
	got, err := us.peers(ctx, 1_000_060)
	if err != nil {
		t.Fatalf("a foreign beacon must be skipped, not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a beacon from another passphrase must not resolve as a peer, got %d", len(got))
	}
}
