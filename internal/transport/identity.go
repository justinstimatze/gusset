package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/justinstimatze/gusset/internal/crypto"
)

// peerAuthLabel derives the Ed25519 peer-authentication key from the user's
// passphrase. Every device that knows the passphrase derives the *same* keypair
// — which is exactly the trust boundary we want: "a device that proves it holds
// the passphrase" is "one of my own devices". Versioned, since changing it would
// re-key every pairing.
const peerAuthLabel = "gusset/v1/peer-auth-ed25519"

// certValidFrom/certValidUntil are a fixed, wide validity window. We pin the
// peer by public key in VerifyPeerCertificate (not by CA chain or expiry), so
// these only populate the certificate fields and never gate the handshake —
// keeping peer auth independent of clock skew between devices.
var (
	certValidFrom  = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	certValidUntil = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Identity is a device's peer-authentication identity, derived entirely from the
// passphrase. Because the keypair is passphrase-derived, all of a user's devices
// share it; the mutual-TLS handshake then proves possession of the passphrase to
// both sides without any certificate authority or pre-shared enrollment.
type Identity struct {
	pub  ed25519.PublicKey
	cert tls.Certificate
}

// DeriveIdentity builds the peer identity from derived keys. It is deterministic
// given the same passphrase (and salt), so two devices independently arrive at
// the same identity and authenticate each other.
func DeriveIdentity(k *crypto.Keys) (*Identity, error) {
	seed, err := k.Subkey(peerAuthLabel, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("transport: unexpected public key type")
	}
	cert, err := selfSignedCert(priv, pub)
	if err != nil {
		return nil, err
	}
	return &Identity{pub: pub, cert: cert}, nil
}

// PublicKey returns the device's peer-auth public key, e.g. to publish in the
// signaling blob so a peer can sanity-check who it expects to reach.
func (id *Identity) PublicKey() ed25519.PublicKey {
	out := make(ed25519.PublicKey, len(id.pub))
	copy(out, id.pub)
	return out
}

// ServerConfig is the TLS config for the listening side: it presents the
// passphrase-derived certificate and requires the dialer to present one whose
// public key matches the same derived identity.
func (id *Identity) ServerConfig() *tls.Config {
	return &tls.Config{
		Certificates:           []tls.Certificate{id.cert},
		MinVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAnyClientCert,
		InsecureSkipVerify:     true, //nolint:gosec // G402: peer pinned by public key, not CA/expiry
		VerifyPeerCertificate:  id.verifyPinnedPeer,
		VerifyConnection:       id.verifyPinnedConn, // also pins resumed sessions, which skip VerifyPeerCertificate
		SessionTicketsDisabled: true,                // short-lived sync connections; no resumption to reason about
	}
}

// ClientConfig is the TLS config for the dialing side: it presents the derived
// certificate and pins the listener's certificate to the same derived public
// key. ServerName is irrelevant because verification is by pinned key, not host.
func (id *Identity) ClientConfig() *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{id.cert},
		MinVersion:            tls.VersionTLS13,
		InsecureSkipVerify:    true, //nolint:gosec // G402: peer pinned by public key, not CA/host
		VerifyPeerCertificate: id.verifyPinnedPeer,
		VerifyConnection:      id.verifyPinnedConn, // also pins resumed sessions, which skip VerifyPeerCertificate
	}
}

// verifyPinnedPeer is the heart of peer authentication: the peer is trusted iff
// its leaf certificate carries our passphrase-derived public key. TLS 1.3 also
// requires the peer to prove possession of the matching private key via its
// CertificateVerify signature, so presenting the public key alone is not enough
// — only a device that derived the same key from the passphrase can complete the
// handshake. An attacker without the passphrase cannot.
func (id *Identity) verifyPinnedPeer(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return errors.New("transport: peer presented no certificate")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("transport: parse peer certificate: %w", err)
	}
	peerPub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok {
		return errors.New("transport: peer certificate is not Ed25519")
	}
	return id.pinPub(peerPub)
}

// verifyPinnedConn re-applies the pin from the assembled connection state. Go
// invokes VerifyConnection on every handshake — including resumed TLS 1.3
// sessions, which do not call VerifyPeerCertificate — so this closes the
// resumption-bypass gap. cs.PeerCertificates carries the peer chain in both the
// full and resumed cases.
func (id *Identity) verifyPinnedConn(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("transport: peer presented no certificate")
	}
	peerPub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return errors.New("transport: peer certificate is not Ed25519")
	}
	return id.pinPub(peerPub)
}

// pinPub is the constant-time pin check shared by both verification callbacks:
// the peer is trusted iff its key equals our passphrase-derived public key.
func (id *Identity) pinPub(peerPub ed25519.PublicKey) error {
	if subtle.ConstantTimeCompare(peerPub, id.pub) != 1 {
		return errors.New("transport: peer authentication failed (wrong passphrase)")
	}
	return nil
}

// selfSignedCert builds a deterministic self-signed certificate over the
// Ed25519 keypair. Ed25519 signing is deterministic and the template is fixed,
// so the certificate is stable across runs and devices — handy, though
// correctness only relies on the embedded public key, which we pin.
func selfSignedCert(priv ed25519.PrivateKey, pub ed25519.PublicKey) (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gusset-peer"},
		NotBefore:             certValidFrom,
		NotAfter:              certValidUntil,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("transport: create certificate: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        tmpl,
	}, nil
}
