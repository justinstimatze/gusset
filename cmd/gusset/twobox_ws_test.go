package main

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/statusws"
)

// TestTwoBox_OverWebSocketCarrier proves the experimental path a friend hits with
// the companion extension: two `gusset sync --ws` daemons that find each other
// through the extension's storage.sync carrier (here, a mock "Firefox Sync" that
// relays their sealed beacons between the two WebSocket connections), then dial
// and sync over the LAN endpoints the beacons advertised. No --peer, no
// --rendezvous-dir — rendezvous goes entirely through the WS carrier.
//
// This exercises the carrier contract end-to-end against the production binary:
// the daemon pushing its beacon for the extension to publish, the extension
// reporting peers back, and the daemon dialing what it learned.
func TestTwoBox_OverWebSocketCarrier(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping two-process integration test in -short mode")
	}
	if !hasLANIPv4() {
		t.Skip("no non-loopback IPv4: beacons would carry no dialable LAN endpoint")
	}
	liveProfile, srcUUID := liveUBOProfile(t)

	bin := filepath.Join(t.TempDir(), "gusset")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build gusset: %v\n%s", err, out)
	}

	srcHome := makeSourceHome(t, liveProfile, srcUUID)
	tgtHome, tgtProfile := makeTargetHome(t)
	srcWS := freeLoopbackAddr(t)
	tgtWS := freeLoopbackAddr(t)

	// Both daemons serve their status/carrier WS and discover each other only
	// through it. Generous window so the bridge can relay beacons and they can
	// dial.
	src := startWSDaemon(t, bin, srcHome, srcWS)
	tgt := startWSDaemon(t, bin, tgtHome, tgtWS)

	// The mock Firefox Sync: derive the same token both daemons expect (passphrase
	// + AppSalt, the no-salt default), connect to both, and relay each one's
	// beacon to the other.
	k, err := crypto.DeriveKeys(twoBoxPass, crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	token, err := statusws.Token(k)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	bridgeFirefoxSync(t, ctx, token, srcWS, tgtWS)

	// Poll the target profile for the applied store rather than waiting the full
	// window.
	originName := "moz-extension+++" + twoBoxTarget + "^userContextId=4294967295"
	glob := filepath.Join(tgtProfile, "storage", "default", originName, "idb", "*.sqlite")
	deadline := time.Now().Add(22 * time.Second)
	var applied string
	for time.Now().Before(deadline) {
		if m, _ := filepath.Glob(glob); len(m) == 1 {
			applied = m[0]
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	_ = src.Process.Kill()
	_ = tgt.Process.Kill()

	if applied == "" {
		t.Fatalf("target never received the store over the WS carrier\nsource:\n%s\ntarget:\n%s",
			src.out(), tgt.out())
	}
	if keys := countKeys(t, applied); keys == 0 {
		t.Fatal("target received the store but it has no keys")
	} else {
		t.Logf("two-box over the WS carrier: target applied %d keys", keys)
	}
}

// wsDaemon is a running `gusset sync --ws` child with its captured output.
type wsDaemon struct {
	*exec.Cmd
	buf *strings.Builder
}

func (d *wsDaemon) out() string { return d.buf.String() }

func startWSDaemon(t *testing.T, bin, home, wsAddr string) *wsDaemon {
	t.Helper()
	cmd := exec.Command(bin, "sync", "--extensions", twoBoxExt, "--ws", wsAddr, "--for", "22s")
	cmd.Env = childEnv(home)
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return &wsDaemon{Cmd: cmd, buf: &buf}
}

// bridgeFirefoxSync connects to every daemon's WS as the companion extension and
// relays each daemon's beacon to the others — standing in for Firefox Sync
// carrying the sealed beacons between devices via storage.sync.
func bridgeFirefoxSync(t *testing.T, ctx context.Context, token string, wsAddrs ...string) {
	t.Helper()
	var mu sync.Mutex
	beacons := map[string][]byte{}
	conns := map[string]*websocket.Conn{}

	// Wait for each daemon's WS to come up, then authenticate.
	for _, addr := range wsAddrs {
		var c *websocket.Conn
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			dc, _, err := websocket.Dial(ctx, "ws://"+addr, nil)
			if err == nil {
				c = dc
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if c == nil {
			t.Fatalf("bridge could not connect to %s", addr)
		}
		if err := wsjson.Write(ctx, c, map[string]string{"token": token}); err != nil {
			t.Fatalf("bridge auth %s: %v", addr, err)
		}
		conns[addr] = c
	}

	forward := func() {
		mu.Lock()
		defer mu.Unlock()
		for addr, c := range conns {
			var others [][]byte
			for a2, b := range beacons {
				if a2 != addr && b != nil {
					others = append(others, b)
				}
			}
			if len(others) > 0 {
				_ = wsjson.Write(ctx, c, map[string]any{"type": "peers", "beacons": others})
			}
		}
	}

	for addr, c := range conns {
		go func(addr string, c *websocket.Conn) {
			for {
				var msg struct {
					Type   string `json:"type"`
					Beacon []byte `json:"beacon"`
				}
				if err := wsjson.Read(ctx, c, &msg); err != nil {
					return
				}
				if msg.Type == "beacon" {
					mu.Lock()
					beacons[addr] = msg.Beacon
					mu.Unlock()
					forward()
				}
			}
		}(addr, c)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.CloseNow()
		}
	})
}

// hasLANIPv4 reports whether a non-loopback IPv4 exists, which a beacon needs to
// carry a dialable LAN endpoint.
func hasLANIPv4() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsLinkLocalUnicast() {
				return true
			}
		}
	}
	return false
}
