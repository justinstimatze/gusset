package icepunch

// The second half of the spike: a punched ICE path is a UDP *datagram* conn, but
// gusset's chunk protocol needs a reliable, ordered, mutually-authenticated byte
// stream (it length-frames Has/Get and runs over passphrase-pinned mutual TLS).
// This proves the missing layer: run QUIC over the ICE conn, reusing gusset's
// exact auth model (one shared cert, each side pins the peer to it), and get a
// reliable stream that round-trips — the io.ReadWriteCloser the chunk layer
// already consumes. If this is green, the sync.go integration is mechanical:
// gather+punch -> QUIC -> stream -> existing chunk protocol, unchanged.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/quic-go/quic-go"
)

const alpn = "gusset-chunk/1"

func TestQUICOverICE_ReliableMutualAuthStream(t *testing.T) {
	// Same in-process two-NAT topology as the ICE spike: both peers behind
	// port-restricted cone NATs, reachable only by hole-punching.
	v := buildVNet(t, portRestrictedCone(), portRestrictedCone())
	defer v.close()

	a := newAgent(t, v.net0)
	defer func() { _ = a.Close() }()
	b := newAgent(t, v.net1)
	defer func() { _ = b.Close() }()

	icA, icB := connect(t, a, b) // two ends of the punched UDP path
	defer func() { _ = icA.Close() }()
	defer func() { _ = icB.Close() }()

	// One shared cert stands in for gusset's passphrase-derived identity; both
	// sides present it and pin the peer to it (gusset's transport auth model).
	serverTLS, clientTLS := pinnedTLSPair(t)
	qconf := &quic.Config{
		MaxIdleTimeout:          10 * time.Second,
		HandshakeIdleTimeout:    8 * time.Second,
		DisablePathMTUDiscovery: true, // keep packets small over vnet
		KeepAlivePeriod:         2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const want = "gusset chunk stream over a hole-punched path"
	srvErr := make(chan error, 1)

	// Server side (peer A): QUIC over its ICE conn, accept one stream, echo back.
	go func() { srvErr <- runQUICServer(ctx, icA, serverTLS, qconf, want) }()

	// Client side (peer B): dial QUIC over its ICE conn, open a stream, exchange.
	got, err := runQUICClient(ctx, icB, clientTLS, qconf, want)
	if err != nil {
		t.Fatalf("quic client over ICE: %v", err)
	}
	if got != want {
		t.Fatalf("stream round-trip mismatch:\n got %q\nwant %q", got, want)
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("quic server over ICE: %v", err)
	}
}

func runQUICServer(ctx context.Context, ic *ice.Conn, tlsConf *tls.Config, qconf *quic.Config, want string) error {
	lis, err := quic.Listen(packetConnOverICE(ic), tlsConf, qconf)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = lis.Close() }()

	conn, err := lis.Accept(ctx)
	if err != nil {
		return fmt.Errorf("accept conn: %w", err)
	}
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept stream: %w", err)
	}
	// Read the client's message, echo it back — proves a real bidirectional,
	// reliable, authenticated stream, not just a handshake.
	buf := make([]byte, len(want))
	if _, err := readFull(stream, buf); err != nil {
		return fmt.Errorf("server read: %w", err)
	}
	if _, err := stream.Write(buf); err != nil {
		return fmt.Errorf("server write: %w", err)
	}
	return nil
}

func runQUICClient(ctx context.Context, ic *ice.Conn, tlsConf *tls.Config, qconf *quic.Config, msg string) (string, error) {
	conn, err := quic.Dial(ctx, packetConnOverICE(ic), ic.RemoteAddr(), tlsConf, qconf)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.CloseWithError(0, "done") }()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return "", fmt.Errorf("open stream: %w", err)
	}
	if _, err := stream.Write([]byte(msg)); err != nil {
		return "", fmt.Errorf("client write: %w", err)
	}
	buf := make([]byte, len(msg))
	if _, err := readFull(stream, buf); err != nil {
		return "", fmt.Errorf("client read: %w", err)
	}
	return string(buf), nil
}

// --- ice.Conn -> net.PacketConn ---

// iceePacketConn adapts an *ice.Conn (a net.Conn over the single selected ICE
// pair) to net.PacketConn, which is what quic.Dial/Listen require. ICE preserves
// datagram boundaries (one Write == one UDP datagram), so each ReadFrom/WriteTo
// maps to one Read/Write; the remote is fixed to the punched peer.
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

func (p *iceePacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	return p.c.Write(b)
}

func (p *iceePacketConn) Close() error                       { return p.c.Close() }
func (p *iceePacketConn) LocalAddr() net.Addr                { return p.c.LocalAddr() }
func (p *iceePacketConn) SetDeadline(t time.Time) error      { return p.c.SetDeadline(t) }
func (p *iceePacketConn) SetReadDeadline(t time.Time) error  { return p.c.SetReadDeadline(t) }
func (p *iceePacketConn) SetWriteDeadline(t time.Time) error { return p.c.SetWriteDeadline(t) }

// --- pinned mutual TLS (mirrors internal/transport's identity model) ---

// pinnedTLSPair builds server and client *tls.Config that share one self-signed
// cert and each require the peer to present that exact cert — the spike analogue
// of gusset deriving one keypair from the passphrase and pinning to it. No CA.
func pinnedTLSPair(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	cert, der := selfSignedCert(t)
	pin := func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 || !bytesEqualSpike(rawCerts[0], der) {
			return errors.New("peer cert is not the pinned passphrase identity")
		}
		return nil
	}
	server = &tls.Config{
		Certificates:          []tls.Certificate{cert},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: pin,
		NextProtos:            []string{alpn},
		MinVersion:            tls.VersionTLS13,
	}
	client = &tls.Config{
		Certificates:          []tls.Certificate{cert},
		InsecureSkipVerify:    true, // no CA; trust is the pin below, not a chain
		VerifyPeerCertificate: pin,
		NextProtos:            []string{alpn},
		MinVersion:            tls.VersionTLS13,
		ServerName:            "gusset",
	}
	return server, client
}

func selfSignedCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gusset"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31-1, 0),
		DNSNames:     []string{"gusset"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der
}

func bytesEqualSpike(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readFull(s *quic.Stream, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := s.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}
