// Package icepunch is a throwaway spike — its own Go module, deliberately
// outside gusset's build (gusset's `go build ./...` and CI never descend into a
// nested module), so it proves a thing without committing gusset to the pion
// dependency tree.
//
// The thing it proves: pion/ice performs real NAT hole-punching entirely
// in-process, with no physical NAT hardware and no root, by running both peers
// through pion's virtual network (vnet). vnet models the RFC 4787 NAT taxonomy
// (mapping + filtering behavior), so we can stand up two peers behind their own
// NATs, let each discover its server-reflexive candidate from a STUN server that
// also lives in the virtual WAN, exchange candidates the way a sealed gusset
// beacon would carry them (marshal → send → unmarshal → add), and watch ICE
// punch a hole between them. This is the evidence behind adopting pion/ice for
// gusset's NAT-traversal data path.
//
// Honest boundary (documented, not asserted here to avoid a flaky negative): a
// symmetric↔symmetric pair does NOT connect this way — pion/ice does no port
// prediction, so that case needs a TURN relay (gusset Tier-2). And vnet itself
// leaves PortPreservation/Hairpinning unimplemented, so the real-world
// symmetric-NAT success *rate* is a field-measurement, not a simulation result.
// What the spike settles is correctness for the cone-NAT cases that are gusset's
// common cross-network target.
package icepunch

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/stun/v3"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/turn/v4"
)

// Virtual-network addressing. Two LANs, each behind its own NAT router, joined
// by a WAN that also hosts the STUN server. Mirrors pion's own vnet tests.
const (
	wanSTUNIP   = "1.2.3.4"
	wanSTUNPort = 3478
	globalIPA   = "27.1.1.1"
	localIPA    = "192.168.0.1"
	maskA       = "24"
	globalIPB   = "28.1.1.1"
	localIPB    = "10.2.0.1"
	maskB       = "24"
)

// portRestrictedCone is the common home-router NAT: one external mapping reused
// for every destination (EndpointIndependent mapping → the srflx candidate is
// dialable by a peer), but only the exact addr:port you contacted may reply
// (EndpointAddrPortDependent filtering → a hole must be punched first). This is
// the case gusset most needs to traverse.
func portRestrictedCone() *vnet.NATType {
	return &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointAddrPortDependent,
	}
}

// fullCone is the easy case: independent mapping and independent filtering.
func fullCone() *vnet.NATType {
	return &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointIndependent,
	}
}

func TestPionICE_PunchesPortRestrictedConeNATs(t *testing.T) {
	// The headline: both peers behind port-restricted cone NATs, connecting only
	// by hole-punching their STUN-discovered reflexive candidates.
	v := buildVNet(t, portRestrictedCone(), portRestrictedCone())
	defer v.close()

	a := newAgent(t, v.net0)
	defer func() { _ = a.Close() }()
	b := newAgent(t, v.net1)
	defer func() { _ = b.Close() }()

	ca, cb := connect(t, a, b)
	defer func() { _ = ca.Close() }()
	defer func() { _ = cb.Close() }()

	// Prove it traversed via a hole-punched reflexive pair, not some shortcut.
	// Strong proof: with addr:port filtering on both sides, the only way through
	// is the STUN-discovered reflexive candidate, so it must be the local pick.
	assertLocalType(t, a, ice.CandidateTypeServerReflexive)
	assertRoundTrip(t, ca, cb)
}

func TestPionICE_PunchesFullConeNATs(t *testing.T) {
	// The easy end of the spectrum, for contrast: still a real two-NAT traversal,
	// no host-reachability between the LANs.
	v := buildVNet(t, fullCone(), fullCone())
	defer v.close()

	a := newAgent(t, v.net0)
	defer func() { _ = a.Close() }()
	b := newAgent(t, v.net1)
	defer func() { _ = b.Close() }()

	ca, cb := connect(t, a, b)
	defer func() { _ = ca.Close() }()
	defer func() { _ = cb.Close() }()

	// Permissive filtering lets the host candidate work directly, so ICE may not
	// need the local srflx — but the selected path is still NAT-external: the
	// remote candidate is the peer's reflexive (global) address, proving traffic
	// crossed both NATs rather than staying on a LAN.
	assertRemoteReflexive(t, a)
	assertRoundTrip(t, ca, cb)
}

// --- harness: virtual network, agents, candidate exchange ---

type vnetTopo struct {
	wan        *vnet.Router
	net0, net1 *vnet.Net
	stunServer *turn.Server
}

func (v *vnetTopo) close() {
	_ = v.stunServer.Close()
	_ = v.wan.Stop()
}

// buildVNet constructs WAN + two NAT'd LANs + a STUN server in the WAN. Adapted
// from pion/ice's connectivity_vnet_test.go buildVNet.
func buildVNet(t *testing.T, nat0, nat1 *vnet.NATType) *vnetTopo {
	t.Helper()
	lf := logging.NewDefaultLoggerFactory()

	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: lf})
	must(t, err)
	wanNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{wanSTUNIP}})
	must(t, err)
	must(t, wan.AddNet(wanNet))

	lan0, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: []string{globalIPA}, CIDR: localIPA + "/" + maskA, NATType: nat0, LoggerFactory: lf,
	})
	must(t, err)
	net0, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{localIPA}})
	must(t, err)
	must(t, lan0.AddNet(net0))
	must(t, wan.AddRouter(lan0))

	lan1, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: []string{globalIPB}, CIDR: localIPB + "/" + maskB, NATType: nat1, LoggerFactory: lf,
	})
	must(t, err)
	net1, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{localIPB}})
	must(t, err)
	must(t, lan1.AddNet(net1))
	must(t, wan.AddRouter(lan1))

	must(t, wan.Start())

	stunServer, err := addVNetSTUN(wanNet, lf)
	must(t, err)

	return &vnetTopo{wan: wan, net0: net0, net1: net1, stunServer: stunServer}
}

// addVNetSTUN runs a STUN(/TURN) responder on the WAN. Copied from pion/ice's
// vnet test; we only use its STUN (Binding) behavior to hand out srflx.
func addVNetSTUN(wanNet *vnet.Net, lf logging.LoggerFactory) (*turn.Server, error) {
	pc, err := wanNet.ListenPacket("udp", fmt.Sprintf("%s:%d", wanSTUNIP, wanSTUNPort))
	if err != nil {
		return nil, err
	}
	return turn.NewServer(turn.ServerConfig{
		AuthHandler: func(username, realm string, _ net.Addr) ([]byte, bool) {
			if username == "user" {
				return turn.GenerateAuthKey("user", realm, "pass"), true
			}
			return nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP(wanSTUNIP),
				Address:      "0.0.0.0",
				Net:          wanNet,
			},
		}},
		Realm:         "pion.ly",
		LoggerFactory: lf,
	})
}

// newAgent builds an ICE agent bound to one virtual LAN, told to gather a
// reflexive candidate from the WAN STUN server. mDNS off (irrelevant here);
// UDP4 only.
func newAgent(t *testing.T, n *vnet.Net) *ice.Agent {
	t.Helper()
	stunURL, err := stun.ParseURI(fmt.Sprintf("stun:%s:%d", wanSTUNIP, wanSTUNPort))
	must(t, err)
	a, err := ice.NewAgent(&ice.AgentConfig{
		Urls:             []*stun.URI{stunURL},
		NetworkTypes:     []ice.NetworkType{ice.NetworkTypeUDP4},
		MulticastDNSMode: ice.MulticastDNSModeDisabled,
		Net:              n,
	})
	must(t, err)
	return a
}

// connect gathers and cross-feeds candidates, then Dials/Accepts. Returns the
// two connected ICE conns. This is the in-process analogue of gusset's flow:
// each side gathers, the sealed beacon carries the candidates to the peer, and
// the connectivity checks punch the hole.
func connect(t *testing.T, a, b *ice.Agent) (*ice.Conn, *ice.Conn) {
	t.Helper()
	aUfrag, aPwd, err := a.GetLocalUserCredentials()
	must(t, err)
	bUfrag, bPwd, err := b.GetLocalUserCredentials()
	must(t, err)

	gatherAndExchange(t, a, b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	accepted := make(chan struct{})
	var aConn *ice.Conn
	go func() {
		var acceptErr error
		aConn, acceptErr = a.Accept(ctx, bUfrag, bPwd)
		must(t, acceptErr)
		close(accepted)
	}()
	bConn, err := b.Dial(ctx, aUfrag, aPwd)
	must(t, err)
	<-accepted
	return aConn, bConn
}

// gatherAndExchange runs gathering on both agents and feeds each agent's
// candidates to the other over the public marshal/unmarshal seam — exactly the
// bytes a gusset beacon would transport. A nil candidate signals gathering done.
func gatherAndExchange(t *testing.T, a, b *ice.Agent) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)
	must(t, a.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			wg.Done()
			return
		}
		addRemote(t, b, c)
	}))
	must(t, b.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			wg.Done()
			return
		}
		addRemote(t, a, c)
	}))
	must(t, a.GatherCandidates())
	must(t, b.GatherCandidates())
	wg.Wait()
}

func addRemote(t *testing.T, dst *ice.Agent, c ice.Candidate) {
	t.Helper()
	rc, err := ice.UnmarshalCandidate(c.Marshal())
	must(t, err)
	must(t, dst.AddRemoteCandidate(rc))
}

func assertLocalType(t *testing.T, a *ice.Agent, want ice.CandidateType) {
	t.Helper()
	pair := selectedPair(t, a)
	if got := pair.Local.Type(); got != want {
		t.Fatalf("selected local candidate type = %s, want %s (pair: %s ⇄ %s)",
			got, want, pair.Local, pair.Remote)
	}
}

func assertRemoteReflexive(t *testing.T, a *ice.Agent) {
	t.Helper()
	pair := selectedPair(t, a)
	switch pair.Remote.Type() {
	case ice.CandidateTypeServerReflexive, ice.CandidateTypePeerReflexive:
		// NAT-external — traversal crossed both NATs.
	default:
		t.Fatalf("selected remote candidate type = %s, want a reflexive (NAT-external) type (pair: %s ⇄ %s)",
			pair.Remote.Type(), pair.Local, pair.Remote)
	}
}

func selectedPair(t *testing.T, a *ice.Agent) *ice.CandidatePair {
	t.Helper()
	pair, err := a.GetSelectedCandidatePair()
	must(t, err)
	if pair == nil {
		t.Fatal("no candidate pair selected")
	}
	return pair
}

func assertRoundTrip(t *testing.T, ca, cb *ice.Conn) {
	t.Helper()
	payload := []byte("gusset-hole-punch-ok")
	must(t, ca.SetWriteDeadline(time.Now().Add(5*time.Second)))
	if _, err := ca.Write(payload); err != nil {
		t.Fatalf("write over punched conn: %v", err)
	}
	must(t, cb.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, len(payload))
	n, err := cb.Read(buf)
	if err != nil {
		t.Fatalf("read over punched conn: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("round-trip mismatch: got %q want %q", buf[:n], payload)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
