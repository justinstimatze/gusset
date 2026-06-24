// Package icewire is gusset's NAT-traversal data path: when a direct dial to a
// peer's LAN endpoints fails, two devices punch a hole with ICE and carry the
// chunk protocol over it.
//
// A punched ICE path is a UDP datagram conn, but the chunk protocol
// (internal/transport) needs a reliable, ordered, mutually-authenticated byte
// stream. icewire supplies the missing layer: it runs QUIC over the ICE conn,
// reusing the *same* passphrase-derived pinned-mutual-TLS identity the LAN
// transport uses (transport.Identity's TLS configs, with a gusset ALPN added).
// The result is a *quic.Stream — already the io.ReadWriteCloser that
// transport.NewClient / transport.Serve consume — so nothing in the chunk or
// reconcile layers changes.
//
// Signaling (exchanging ICE credentials + candidates) is the caller's job: an
// Endpoint is small and JSON-friendly so it rides inside a sealed
// rendezvous.Beacon. The agent injection point (Config.Net) lets tests drive the
// whole stack over pion's virtual network — NAT traversal is verified in-process,
// with no hardware (see icewire_test.go).
package icewire

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pion/ice/v3"
	"github.com/pion/stun/v2"
	pnet "github.com/pion/transport/v3"
	"github.com/quic-go/quic-go"

	"github.com/justinstimatze/gusset/internal/transport"
)

// alpn names gusset's application protocol in the QUIC/TLS handshake. Versioned
// so a future incompatible stream format cannot be mistaken for this one.
const alpn = "gusset-chunk/1"

// Config parameters a Session. STUNURLs are "stun:host:port" strings used to
// gather server-reflexive candidates; Net, when non-nil, is the (virtual)
// network the ICE agent runs on — production leaves it nil for the real network,
// tests inject a pion vnet.
type Config struct {
	STUNURLs []string
	Net      pnet.Net
}

// Endpoint is everything a peer needs to reach this device over ICE: the agent's
// short-term credentials and its gathered candidates (marshaled). It is carried,
// sealed, inside a rendezvous beacon.
type Endpoint struct {
	Ufrag      string   `json:"ufrag"`
	Pwd        string   `json:"pwd"`
	Candidates []string `json:"candidates"`
}

// Session is a gathered-but-not-yet-connected ICE agent. The caller publishes
// Local() to the peer, learns the peer's Endpoint, then calls Connect.
type Session struct {
	agent *ice.Agent
	local Endpoint
}

// Gather builds an ICE agent, gathers its candidates, and returns a Session
// holding the local Endpoint to publish. The agent is owned by the Session;
// Close (or a successful Connect's returned Conn.Close) releases it.
func Gather(ctx context.Context, cfg Config) (*Session, error) {
	urls := make([]*stun.URI, 0, len(cfg.STUNURLs))
	for _, u := range cfg.STUNURLs {
		parsed, err := stun.ParseURI(u)
		if err != nil {
			return nil, fmt.Errorf("icewire: parse stun url %q: %w", u, err)
		}
		urls = append(urls, parsed)
	}

	agent, err := ice.NewAgent(&ice.AgentConfig{
		Urls:             urls,
		NetworkTypes:     []ice.NetworkType{ice.NetworkTypeUDP4},
		MulticastDNSMode: ice.MulticastDNSModeDisabled,
		Net:              cfg.Net,
	})
	if err != nil {
		return nil, fmt.Errorf("icewire: new agent: %w", err)
	}

	// OnCandidate fires per candidate and once with nil when gathering finishes.
	done := make(chan struct{})
	if err := agent.OnCandidate(func(c ice.Candidate) {
		if c == nil {
			close(done)
		}
	}); err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("icewire: on candidate: %w", err)
	}
	if err := agent.GatherCandidates(); err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("icewire: gather: %w", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		_ = agent.Close()
		return nil, fmt.Errorf("icewire: gather: %w", ctx.Err())
	}

	ufrag, pwd, err := agent.GetLocalUserCredentials()
	if err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("icewire: local credentials: %w", err)
	}
	cands, err := agent.GetLocalCandidates()
	if err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("icewire: local candidates: %w", err)
	}
	marshaled := make([]string, 0, len(cands))
	for _, c := range cands {
		marshaled = append(marshaled, c.Marshal())
	}

	return &Session{
		agent: agent,
		local: Endpoint{Ufrag: ufrag, Pwd: pwd, Candidates: marshaled},
	}, nil
}

// Local is this device's endpoint to publish to the peer.
func (s *Session) Local() Endpoint { return s.local }

// Close releases the agent if Connect was never called (or failed).
func (s *Session) Close() error {
	if s.agent == nil {
		return nil
	}
	err := s.agent.Close()
	s.agent = nil
	return err
}

// Connect feeds the peer's candidates to the agent, completes ICE (hole-punch),
// then runs QUIC over the punched path with the passphrase-pinned identity, and
// returns a Conn carrying reliable mutually-authenticated streams. Exactly one
// side must be controlling — gusset breaks the tie deterministically (e.g. by
// device id) so both agree. The controlling side is the QUIC client.
func (s *Session) Connect(ctx context.Context, id *transport.Identity, peer Endpoint, controlling bool) (*Conn, error) {
	if s.agent == nil {
		return nil, errors.New("icewire: session already consumed or closed")
	}
	agent := s.agent
	s.agent = nil // ownership moves to the returned Conn (or is closed on error)

	for _, raw := range peer.Candidates {
		c, err := ice.UnmarshalCandidate(raw)
		if err != nil {
			_ = agent.Close()
			return nil, fmt.Errorf("icewire: unmarshal peer candidate: %w", err)
		}
		if err := agent.AddRemoteCandidate(c); err != nil {
			_ = agent.Close()
			return nil, fmt.Errorf("icewire: add peer candidate: %w", err)
		}
	}

	var iceConn *ice.Conn
	var err error
	if controlling {
		iceConn, err = agent.Dial(ctx, peer.Ufrag, peer.Pwd)
	} else {
		iceConn, err = agent.Accept(ctx, peer.Ufrag, peer.Pwd)
	}
	if err != nil {
		_ = agent.Close()
		return nil, fmt.Errorf("icewire: ice connect: %w", err)
	}

	qconn, err := quicOverICE(ctx, iceConn, id, controlling)
	if err != nil {
		_ = iceConn.Close()
		_ = agent.Close()
		return nil, err
	}
	return &Conn{qconn: qconn, ice: iceConn, agent: agent}, nil
}

// quicConfig is shared by both ends. Path-MTU discovery is disabled because the
// punched path's MTU is already conservative and probing adds nothing here; a
// keep-alive holds the NAT mappings open during idle stretches of a sync.
func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:          30 * time.Second,
		HandshakeIdleTimeout:    10 * time.Second,
		KeepAlivePeriod:         5 * time.Second,
		DisablePathMTUDiscovery: true,
	}
}

func quicOverICE(ctx context.Context, ic *ice.Conn, id *transport.Identity, controlling bool) (*quic.Conn, error) {
	pc := packetConnOverICE(ic)
	if controlling {
		cfg := id.ClientConfig()
		cfg.NextProtos = []string{alpn}
		qc, err := quic.Dial(ctx, pc, ic.RemoteAddr(), cfg, quicConfig())
		if err != nil {
			return nil, fmt.Errorf("icewire: quic dial: %w", err)
		}
		return qc, nil
	}
	cfg := id.ServerConfig()
	cfg.NextProtos = []string{alpn}
	lis, err := quic.Listen(pc, cfg, quicConfig())
	if err != nil {
		return nil, fmt.Errorf("icewire: quic listen: %w", err)
	}
	qc, err := lis.Accept(ctx)
	if err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("icewire: quic accept: %w", err)
	}
	// The listener has handed us the one connection from this single punched
	// path; closing it does not affect the accepted conn.
	_ = lis.Close()
	return qc, nil
}

// Conn is an established, authenticated QUIC connection over a punched ICE path.
// Either side may open a stream (to pull) or accept one (to serve), so a single
// punched path carries a full bidirectional reconcile. Each stream is the
// io.ReadWriteCloser the chunk protocol consumes.
type Conn struct {
	qconn *quic.Conn
	ice   *ice.Conn
	agent *ice.Agent
}

// OpenStream opens a stream to drive the chunk client (converge.Pull via
// transport.NewClient).
func (c *Conn) OpenStream(ctx context.Context) (*quic.Stream, error) {
	s, err := c.qconn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("icewire: open stream: %w", err)
	}
	return s, nil
}

// AcceptStream accepts a stream to drive the chunk server (transport.Serve).
func (c *Conn) AcceptStream(ctx context.Context) (*quic.Stream, error) {
	s, err := c.qconn.AcceptStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("icewire: accept stream: %w", err)
	}
	return s, nil
}

// Close tears down the QUIC connection, the ICE conn, and the agent.
func (c *Conn) Close() error {
	qerr := c.qconn.CloseWithError(0, "")
	_ = c.ice.Close()
	_ = c.agent.Close()
	return qerr
}

// iceePacketConn adapts an *ice.Conn (a net.Conn over the single selected ICE
// pair) to net.PacketConn, which quic.Dial/Listen require. ICE preserves
// datagram boundaries, so each ReadFrom/WriteTo maps to one Read/Write; the
// remote is fixed to the one punched peer.
type iceePacketConn struct {
	c      *ice.Conn
	remote net.Addr
}

func packetConnOverICE(c *ice.Conn) *iceePacketConn {
	return &iceePacketConn{c: c, remote: c.RemoteAddr()}
}

func (p *iceePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.c.Read(b)
	return n, p.remote, err
}
func (p *iceePacketConn) WriteTo(b []byte, _ net.Addr) (int, error) { return p.c.Write(b) }
func (p *iceePacketConn) Close() error                              { return p.c.Close() }
func (p *iceePacketConn) LocalAddr() net.Addr                       { return p.c.LocalAddr() }
func (p *iceePacketConn) SetDeadline(t time.Time) error             { return p.c.SetDeadline(t) }
func (p *iceePacketConn) SetReadDeadline(t time.Time) error         { return p.c.SetReadDeadline(t) }
func (p *iceePacketConn) SetWriteDeadline(t time.Time) error        { return p.c.SetWriteDeadline(t) }

var _ net.PacketConn = (*iceePacketConn)(nil)
