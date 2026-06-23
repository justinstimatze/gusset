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

// Server is the Tier-0 listening side: it accepts mutual-TLS connections from
// peers proving the shared passphrase and serves encrypted chunks from src.
type Server struct {
	ln  net.Listener
	src ChunkSource

	mu     sync.Mutex
	closed bool
}

// Listen starts a Tier-0 LAN server on network/addr (e.g. "tcp",
// "0.0.0.0:0"), presenting id's passphrase-derived certificate. Use addr with
// port 0 to get an OS-assigned port, then read Addr to publish it via signaling.
func Listen(network, addr string, id *Identity, src ChunkSource) (*Server, error) {
	if src == nil {
		return nil, errors.New("transport: nil chunk source")
	}
	ln, err := tls.Listen(network, addr, id.ServerConfig())
	if err != nil {
		return nil, fmt.Errorf("transport: listen: %w", err)
	}
	return &Server{ln: ln, src: src}, nil
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
// up. Per-connection errors are intentionally swallowed: one bad peer must not
// take down the listener, and the status layer (later) reports reachability.
func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			return // failed peer auth or transport error; drop quietly
		}
	}
	_ = Serve(conn, s.src)
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
