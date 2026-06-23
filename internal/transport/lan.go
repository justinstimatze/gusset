package transport

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// dialTimeout bounds how long a Tier-0 dial (TCP connect + TLS handshake) waits
// before reporting the peer unreachable. LAN round-trips are sub-millisecond; a
// few seconds is generous and keeps "peer offline" status snappy.
const dialTimeout = 5 * time.Second

// ConnPhase identifies where a per-connection failure happened, so an observer
// can classify it for status (docs/transport-and-security.md §6): a handshake
// failure is the status layer's auth-failed, a serve failure is error(detail).
type ConnPhase int

const (
	// PhaseHandshake is a mutual-TLS handshake failure — most often a peer that
	// cannot prove the shared passphrase (wrong passphrase / unauthorized dialer).
	PhaseHandshake ConnPhase = iota
	// PhaseServe is a protocol or IO error after a peer was authenticated.
	PhaseServe
)

func (p ConnPhase) String() string {
	if p == PhaseHandshake {
		return "handshake"
	}
	return "serve"
}

// ConnError reports a single connection's failure to an optional observer. The
// listener always swallows these so one bad peer cannot take it down; the
// observer exists so they are not *invisible* — the status layer and logs can
// see auth failures and protocol errors as they happen.
type ConnError struct {
	Remote net.Addr
	Phase  ConnPhase
	Err    error
}

// Option configures a Server at construction. Options are applied before the
// accept loop starts and never mutated after, so the Server's fields are safe to
// read from the per-connection goroutines without locking.
type Option func(*Server)

// WithConnErrorHandler installs an observer for per-connection failures
// (handshake and serve). It is invoked from the per-connection goroutine, so the
// handler must be safe for concurrent calls. It does not change the listener's
// behavior — failures are still swallowed — it only makes them observable.
func WithConnErrorHandler(fn func(ConnError)) Option {
	return func(s *Server) { s.onErr = fn }
}

// Server is the Tier-0 listening side: it accepts mutual-TLS connections from
// peers proving the shared passphrase and serves encrypted chunks from src.
type Server struct {
	ln    net.Listener
	src   ChunkSource
	onErr func(ConnError) // optional; set once at construction via Option

	mu     sync.Mutex
	closed bool
}

// Listen starts a Tier-0 LAN server on network/addr (e.g. "tcp",
// "0.0.0.0:0"), presenting id's passphrase-derived certificate. Use addr with
// port 0 to get an OS-assigned port, then read Addr to publish it via signaling.
// Pass WithConnErrorHandler to observe per-connection failures.
func Listen(network, addr string, id *Identity, src ChunkSource, opts ...Option) (*Server, error) {
	if src == nil {
		return nil, errors.New("transport: nil chunk source")
	}
	ln, err := tls.Listen(network, addr, id.ServerConfig())
	if err != nil {
		return nil, fmt.Errorf("transport: listen: %w", err)
	}
	s := &Server{ln: ln, src: src}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Addr is the actual listening address, including the OS-assigned port when
// addr used port 0. Publish this (with the peer-auth public key) via signaling.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Serve accepts connections until the server is closed, handling each on its own
// goroutine. A peer that fails the mutual-TLS handshake (wrong passphrase) is
// dropped without affecting others. Serve returns nil on a clean Close.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			return fmt.Errorf("transport: accept: %w", err)
		}
		go s.handle(conn)
	}
}

// handle completes the TLS handshake (so an unauthenticated peer is rejected
// before it can issue any request) and then serves chunks until the peer hangs
// up. Per-connection errors are swallowed — one bad peer must not take down the
// listener — but reported to the optional observer so they are not invisible.
func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			s.report(conn, PhaseHandshake, err) // failed peer auth or transport error
			return
		}
	}
	if err := Serve(conn, s.src); err != nil {
		s.report(conn, PhaseServe, err)
	}
}

// report hands a per-connection failure to the observer if one is installed.
func (s *Server) report(conn net.Conn, phase ConnPhase, err error) {
	if s.onErr != nil {
		s.onErr(ConnError{Remote: conn.RemoteAddr(), Phase: phase, Err: err})
	}
}

// Close stops accepting and unblocks Serve.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.ln.Close()
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Dial connects to a peer's published LAN endpoint and authenticates it against
// the shared passphrase via mutual TLS. The returned Client fetches encrypted
// chunks; its Get plugs straight into chunk.Reconstruct / syncx.Import. A wrong
// passphrase on either end fails here, at the handshake.
func Dial(network, addr string, id *Identity) (*Client, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	conn, err := tls.DialWithDialer(d, network, addr, id.ClientConfig())
	if err != nil {
		return nil, fmt.Errorf("transport: dial %s: %w", addr, err)
	}
	return NewClient(conn), nil
}
