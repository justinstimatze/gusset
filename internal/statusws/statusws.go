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
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/justinstimatze/gusset/internal/crypto"
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

// statusMsg is what the server pushes: a typed envelope around a Snapshot, so
// the protocol can carry other message types later without ambiguity.
type statusMsg struct {
	Type     string          `json:"type"` // always "status" for now
	Snapshot status.Snapshot `json:"snapshot"`
}

// Server streams a status.Model over a token-gated localhost WebSocket.
type Server struct {
	model *status.Model
	token string
}

// NewServer builds a status WebSocket server for model, gated by token (from
// Token). It is an http.Handler; use Serve to run it bound to loopback.
func NewServer(model *status.Model, token string) *Server {
	return &Server{model: model, token: token}
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

// stream pushes the current snapshot, then a fresh one on every model change,
// with a periodic ping so a dead client is detected and dropped.
func (s *Server) stream(ctx context.Context, conn *websocket.Conn) error {
	// CloseRead drains incoming frames in the background (processing pongs and
	// the client's close), and gives a context that ends when the peer goes away.
	ctx = conn.CloseRead(ctx)

	changed, unsubscribe := s.model.Subscribe()
	defer unsubscribe()

	if err := s.push(ctx, conn); err != nil { // initial state — never a blank UI
		return err
	}

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
			if err := s.push(ctx, conn); err != nil {
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

// push writes one status snapshot.
func (s *Server) push(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, statusMsg{Type: "status", Snapshot: s.model.Snapshot()})
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
