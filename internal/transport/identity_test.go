package transport

import (
	"bytes"
	"testing"

	"github.com/justinstimatze/gusset/internal/crypto"
)

func deriveIdentity(t *testing.T, passphrase string) *Identity {
	t.Helper()
	k, err := crypto.DeriveKeys(passphrase, crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := DeriveIdentity(k)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIdentity_DeterministicAcrossDevices(t *testing.T) {
	// Two devices deriving from the same passphrase must reach the same peer
	// identity — that is what lets them authenticate each other without enrollment.
	a := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	b := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	if !bytes.Equal(a.PublicKey(), b.PublicKey()) {
		t.Fatal("same passphrase produced different peer identities")
	}
}

func TestIdentity_DiffersByPassphrase(t *testing.T) {
	a := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	b := deriveIdentity(t, "a totally different eight word secret phrase here")
	if bytes.Equal(a.PublicKey(), b.PublicKey()) {
		t.Fatal("different passphrases produced the same peer identity")
	}
}

func TestIdentity_VerifyPinnedPeer(t *testing.T) {
	a := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	b := deriveIdentity(t, "a totally different eight word secret phrase here")

	// A peer presenting the matching derived certificate verifies; a mismatched
	// one is rejected. (The full handshake also proves private-key possession;
	// this exercises the pinning predicate directly.)
	if err := a.verifyPinnedPeer(a.cert.Certificate, nil); err != nil {
		t.Errorf("matching identity should verify: %v", err)
	}
	if err := a.verifyPinnedPeer(b.cert.Certificate, nil); err == nil {
		t.Error("mismatched identity should fail verification")
	}
	if err := a.verifyPinnedPeer(nil, nil); err == nil {
		t.Error("empty certificate chain should fail verification")
	}
}

// PublicKey must hand out a copy, so a caller cannot mutate the identity's key.
func TestIdentity_PublicKeyIsCopy(t *testing.T) {
	id := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	pub := id.PublicKey()
	for i := range pub {
		pub[i] ^= 0xff
	}
	if bytes.Equal(id.PublicKey(), pub) {
		t.Fatal("PublicKey returned a mutable view of internal state")
	}
}
