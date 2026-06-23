package transport

import (
	"bytes"
	"testing"

	"github.com/justinstimatze/gusset/internal/chunk"
	"github.com/justinstimatze/gusset/internal/crypto"
)

// serveLAN starts a Tier-0 server on an OS-assigned loopback port and returns
// its address. The server stops when the test ends.
func serveLAN(t *testing.T, id *Identity, src ChunkSource) string {
	t.Helper()
	srv, err := Listen("tcp", "127.0.0.1:0", id, src)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv.Addr().String()
}

func TestLAN_RoundTripOverMutualTLS(t *testing.T) {
	id := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	src := MapSource{
		"deadbeef": []byte("first sealed chunk"),
		"feedface": bytes.Repeat([]byte{0x42}, 100_000),
	}
	addr := serveLAN(t, id, src)

	client, err := Dial("tcp", addr, id)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	for a, want := range src {
		has, err := client.Has(a)
		if err != nil || !has {
			t.Fatalf("Has(%s) = %v, %v", a, has, err)
		}
		got, err := client.Get(a)
		if err != nil {
			t.Fatalf("Get(%s): %v", a, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Get(%s) mismatch", a)
		}
	}
}

func TestLAN_WrongPassphraseFailsHandshake(t *testing.T) {
	server := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	attacker := deriveIdentity(t, "a totally different eight word secret phrase here")
	addr := serveLAN(t, server, MapSource{"x": []byte("secret")})

	// A dialer that does not hold the passphrase cannot complete mutual TLS.
	if _, err := Dial("tcp", addr, attacker); err == nil {
		t.Fatal("dial with the wrong passphrase should fail at the handshake")
	}
}

// TestLAN_FeedsChunkReconstruct closes the seam: a blob is chunked+encrypted,
// the resulting store is served over the LAN transport, and the client's Get is
// handed straight to chunk.Reconstruct — the exact wiring syncx.Import will use.
func TestLAN_FeedsChunkReconstruct(t *testing.T) {
	k, err := crypto.DeriveKeys("correct horse battery staple lorem ipsum dolor sit", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := chunk.DerivePolynomial(k)
	if err != nil {
		t.Fatal(err)
	}
	blob := bytes.Repeat([]byte("gusset moves settings between your own machines. "), 5000)
	manifest, store, err := chunk.Split(bytes.NewReader(blob), k, pol, chunk.Meta{Extension: "demo"})
	if err != nil {
		t.Fatal(err)
	}

	id, err := DeriveIdentity(k)
	if err != nil {
		t.Fatal(err)
	}
	addr := serveLAN(t, id, MapSource(store))
	client, err := Dial("tcp", addr, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	got, err := chunk.Reconstruct(manifest, k, client.Get)
	if err != nil {
		t.Fatalf("reconstruct over transport: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatal("blob did not survive chunk -> transport -> reconstruct")
	}
}
