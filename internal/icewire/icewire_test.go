package icewire_test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/turn/v4"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/icewire"
	"github.com/justinstimatze/gusset/internal/transport"
)

// TestConnect_PunchesNATsAndCarriesChunkProtocol is the end-to-end proof: two
// devices behind simulated NATs (pion vnet, in-process, no hardware) gather ICE
// candidates, exchange them the way a sealed beacon would,
// hole-punch, bring up QUIC over the punched path with the passphrase-pinned
// identity, and then run gusset's real chunk protocol over it — a Get returns
// the served bytes. No part of internal/transport or internal/chunk is mocked.
func TestConnect_PunchesNATsAndCarriesChunkProtocol(t *testing.T) {
	v := buildVNet(t)
	defer v.close()

	id := deriveID(t)
	stunURL := fmt.Sprintf("stun:%s:%d", wanSTUNIP, wanSTUNPort)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// Both peers gather concurrently (each on its own NAT'd virtual net).
	sessA := gather(t, ctx, stunURL, v.net0)
	sessB := gather(t, ctx, stunURL, v.net1)
	epA, epB := sessA.Local(), sessB.Local()

	// Deterministic role tie-break stand-in: A controls (QUIC client / puller),
	// B is controlled (QUIC server). gusset will derive this from device ids.
	var connA, connB *icewire.Conn
	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); connA, errA = sessA.Connect(ctx, id, epB, true) }()
	go func() { defer wg.Done(); connB, errB = sessB.Connect(ctx, id, epA, false) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("connect failed: A=%v B=%v", errA, errB)
	}
	defer func() { _ = connA.Close() }()
	defer func() { _ = connB.Close() }()

	const addr = "chunk-address-0001"
	want := []byte("re-homed uBlock filter chunk")

	// B serves chunks; A pulls one. Both halves of gusset's transport, unchanged,
	// over the punched QUIC path.
	serveErr := make(chan error, 1)
	go func() {
		stream, err := connB.AcceptStream(ctx)
		if err != nil {
			serveErr <- err
			return
		}
		serveErr <- transport.Serve(stream, transport.MapSource{addr: want})
	}()

	stream, err := connA.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	got, err := transport.NewClient(stream).Get(addr)
	if err != nil {
		t.Fatalf("chunk Get over punched path: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("chunk mismatch: got %q want %q", got, want)
	}

	// transport.Serve returns when the client closes the stream; closing the
	// client stream above (via Conn.Close deferred) lets it finish.
	_ = stream.Close()
	if err := <-serveErr; err != nil && err.Error() != "EOF" {
		// Serve returns nil or an EOF-class error on a clean client close.
		t.Logf("serve ended with: %v", err)
	}
}

func gather(t *testing.T, ctx context.Context, stunURL string, n *vnet.Net) *icewire.Session {
	t.Helper()
	s, err := icewire.Gather(ctx, icewire.Config{STUNURLs: []string{stunURL}, Net: n})
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	return s
}

func deriveID(t *testing.T) *transport.Identity {
	t.Helper()
	k, err := crypto.DeriveKeys("correct horse battery staple lorem ipsum", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := transport.DeriveIdentity(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// --- pion vnet harness (in-process two-NAT topology) ---

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

type vnetTopo struct {
	wan        *vnet.Router
	net0, net1 *vnet.Net
	stunServer *turn.Server
}

func (v *vnetTopo) close() {
	_ = v.stunServer.Close()
	_ = v.wan.Stop()
}

// portRestrictedCone: one external mapping reused for all peers (so the srflx
// candidate is dialable) but only the contacted addr:port may reply (so a hole
// must be punched) — the common home-router NAT.
func portRestrictedCone() *vnet.NATType {
	return &vnet.NATType{
		MappingBehavior:   vnet.EndpointIndependent,
		FilteringBehavior: vnet.EndpointAddrPortDependent,
	}
}

func buildVNet(t *testing.T) *vnetTopo {
	t.Helper()
	lf := logging.NewDefaultLoggerFactory()

	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: lf})
	must(t, err)
	wanNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{wanSTUNIP}})
	must(t, err)
	must(t, wan.AddNet(wanNet))

	lan0, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: []string{globalIPA}, CIDR: localIPA + "/" + maskA, NATType: portRestrictedCone(), LoggerFactory: lf,
	})
	must(t, err)
	net0, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{localIPA}})
	must(t, err)
	must(t, lan0.AddNet(net0))
	must(t, wan.AddRouter(lan0))

	lan1, err := vnet.NewRouter(&vnet.RouterConfig{
		StaticIPs: []string{globalIPB}, CIDR: localIPB + "/" + maskB, NATType: portRestrictedCone(), LoggerFactory: lf,
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
