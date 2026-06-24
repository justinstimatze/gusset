// Package statusws is the daemon's localhost WebSocket: it streams the status
// Snapshot (internal/status) to a local client — the companion extension's UI —
// pushing a fresh snapshot whenever the model changes.
//
// localhost is not a trust boundary: any local process can connect to
// 127.0.0.1, so the socket is gated by a token derived from the user's
// passphrase (Token). A client proves it holds that token in the first frame;
// an unauthenticated socket is closed before it sees any data. Binding is
// restricted to loopback — the daemon never exposes status to the network.
package statusws

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/rendezvous"
	"github.com/justinstimatze/gusset/internal/status"
)

// tokenLabel derives the WS access token from the passphrase. Versioned with the
// other gusset key labels; changing it re-keys the daemon↔extension pairing.
const tokenLabel = "gusset/v1/ws-daemon-token" //nolint:gosec // G101: an HKDF derivation label, not a credential

const (
	authTimeout  = 5 * time.Second  // a client must present its token promptly
	writeTimeout = 5 * time.Second  // bound a single snapshot push
	pingInterval = 20 * time.Second // app-level heartbeat (the browser WS API has none)
	pingTimeout  = 10 * time.Second // a client that misses a pong is dropped
)

// Token returns the hex WS access token derived from the passphrase keys. The
// daemon serves with it; the extension presents it. Both derive it identically,
// so pairing is "show the user this token once" rather than a key exchange.
func Token(k *crypto.Keys) (string, error) {
	b, err := k.Subkey(tokenLabel, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// authMsg is the first frame a client sends.
type authMsg struct {
	Token string `json:"token"`
}

// The daemon↔extension protocol is a small typed-envelope WebSocket. After the
// auth frame it is bidirectional:
//
//	server -> client  {"type":"status","snapshot":{...}}      live status
//	server -> client  {"type":"beacon","beacon":"<b64>"}      publish this to storage.sync
//	client -> server  {"type":"peers","beacons":["<b64>",...]} peers seen in storage.sync
//
// []byte fields marshal as base64 via encoding/json, so sealed beacons ride as
// strings. The carrier half (beacon/peers) lets the extension be the storage.sync
// courier the daemon cannot be: only the extension can use the storage.sync API,
// so it writes the daemon's beacon and reports back the beacons Firefox Sync
// brought from the user's other devices.
type statusMsg struct {
	Type     string          `json:"type"` // "status"
	Snapshot status.Snapshot `json:"snapshot"`
}

// beaconMsg asks the extension to publish this device's sealed beacon.
type beaconMsg struct {
	Type   string `json:"type"` // "beacon"
	Beacon []byte `json:"beacon"`
}

// clientMsg is a frame from the extension after auth. The first frame is authMsg;
// every frame after it is a clientMsg (today only "peers").
type clientMsg struct {
	Type    string   `json:"type"`
	Beacons [][]byte `json:"beacons,omitempty"`
}

// Server streams a status.Model over a token-gated localhost WebSocket and
// carries beacons to and from the connected extension. It implements
// rendezvous.Signaling: Publish hands the extension a beacon to write to
// storage.sync, and Fetch returns the peer beacons the extension last reported.
type Server struct {
	model *status.Model
	token string

	bmu        sync.Mutex
	beacon     []byte   // this device's sealed beacon, pushed to the extension to publish
	peers      [][]byte // peer beacons the extension last reported from storage.sync
	beaconSubs map[chan struct{}]struct{}
}

var _ rendezvous.Signaling = (*Server)(nil)

// NewServer builds a status WebSocket server for model, gated by token (from
// Token). It is an http.Handler; use Serve to run it bound to loopback.
func NewServer(model *status.Model, token string) *Server {
	return &Server{model: model, token: token, beaconSubs: map[chan struct{}]struct{}{}}
}

// SetBeacon records this device's sealed beacon and wakes connected extensions
// to publish it. Replaces any previous beacon.
func (s *Server) SetBeacon(sealed []byte) {
	s.bmu.Lock()
	s.beacon = append([]byte(nil), sealed...)
	for ch := range s.beaconSubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.bmu.Unlock()
}

// PeerBeacons returns a copy of the peer beacons the extension last reported.
func (s *Server) PeerBeacons() [][]byte {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	out := make([][]byte, len(s.peers))
	copy(out, s.peers)
	return out
}

func (s *Server) currentBeacon() []byte {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return s.beacon
}

func (s *Server) setPeers(beacons [][]byte) {
	s.bmu.Lock()
	s.peers = beacons
	s.bmu.Unlock()
}

func (s *Server) subscribeBeacon() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.bmu.Lock()
	s.beaconSubs[ch] = struct{}{}
	s.bmu.Unlock()
	return ch, func() {
		s.bmu.Lock()
		delete(s.beaconSubs, ch)
		s.bmu.Unlock()
	}
}

// Publish implements rendezvous.Signaling: it records this device's sealed
// beacon and asks the connected extension to write it to storage.sync. selfID is
// unused — the extension owns its own storage.sync key.
func (s *Server) Publish(_ context.Context, _ string, sealed []byte) error {
	s.SetBeacon(sealed)
	return nil
}

// Fetch implements rendezvous.Signaling: it returns the peer beacons the
// extension last reported from storage.sync. Finding none is not an error.
func (s *Server) Fetch(_ context.Context, _ string) ([][]byte, error) {
	return s.PeerBeacons(), nil
}

// ServeHTTP upgrades a request to a WebSocket, authenticates the first frame,
// and then streams snapshots until the client or context goes away.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The token is the trust gate, not the request origin: the extension's
	// origin (moz-extension://<random-uuid>) is unpredictable, so origin checks
	// can't authorize it. A page that lacks the token cannot get past auth.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	if !s.authenticate(r.Context(), conn) {
		_ = conn.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	if err := s.stream(r.Context(), conn); err != nil {
		// A normal client hangup is not an error worth surfacing.
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// authenticate reads the first frame and constant-time-compares its token.
func (s *Server) authenticate(ctx context.Context, conn *websocket.Conn) bool {
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	var msg authMsg
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(msg.Token), []byte(s.token)) == 1
}

// stream runs the connection after auth: a read pump that takes the extension's
// reported peer beacons, and this write loop that pushes the initial status +
// beacon, then a fresh status on every model change, the beacon whenever it
// changes, and a periodic ping so a dead client is dropped. The single read
// goroutine also lets coder/websocket process the ping's pong.
func (s *Server) stream(ctx context.Context, conn *websocket.Conn) error {
	statusCh, unsubStatus := s.model.Subscribe()
	defer unsubStatus()
	beaconCh, unsubBeacon := s.subscribeBeacon()
	defer unsubBeacon()

	readErr := make(chan error, 1)
	go func() { readErr <- s.readLoop(ctx, conn) }()

	if err := s.pushStatus(ctx, conn); err != nil { // initial state — never a blank UI
		return err
	}
	if b := s.currentBeacon(); b != nil {
		if err := s.pushBeacon(ctx, conn, b); err != nil {
			return err
		}
	}

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-statusCh:
			if err := s.pushStatus(ctx, conn); err != nil {
				return err
			}
		case <-beaconCh:
			if err := s.pushBeacon(ctx, conn, s.currentBeacon()); err != nil {
				return err
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

// readLoop consumes frames from the extension after auth. Today the only inbound
// message is "peers" (the beacons the extension sees in storage.sync); unknown
// types are ignored for forward compatibility. It returns when the connection
// ends, which is what tears down stream.
func (s *Server) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		var msg clientMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return err
		}
		if msg.Type == "peers" {
			s.setPeers(msg.Beacons)
		}
	}
}

// pushStatus writes one status snapshot.
func (s *Server) pushStatus(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, statusMsg{Type: "status", Snapshot: s.model.Snapshot()})
}

// pushBeacon asks the extension to publish a sealed beacon.
func (s *Server) pushBeacon(ctx context.Context, conn *websocket.Conn, sealed []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, beaconMsg{Type: "beacon", Beacon: sealed})
}

// Serve binds addr (which must be a loopback address) and serves until ctx is
// canceled. It refuses a non-loopback bind so status is never exposed to the
// network. The bound address is reported via onReady (handy when addr uses port
// 0); onReady may be nil.
func (s *Server) Serve(ctx context.Context, addr string, onReady func(net.Addr)) error {
	if err := requireLoopback(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if onReady != nil {
		onReady(ln.Addr())
	}
	httpSrv := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second}
	// ctx is the shutdown trigger; once it's canceled the grace period must come
	// from a fresh context, not the dead one.
	go func() { //nolint:gosec // G118: shutdown deadline intentionally uses a fresh context, since ctx is the already-canceled trigger
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// requireLoopback rejects any bind address whose host is not loopback.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("statusws: bad listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("statusws: refusing to bind non-loopback address %q (status is loopback-only)", addr)
	}
	return nil
}
