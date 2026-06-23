package transport

import (
	"bytes"
	"testing"
	"time"

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

// TestLAN_ObservesFailedHandshake proves the per-connection error gap is closed:
// a wrong-passphrase dialer is still rejected, and the failure is now visible to
// an observer (the status layer's auth-failed signal) instead of black-holed.
func TestLAN_ObservesFailedHandshake(t *testing.T) {
	server := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	attacker := deriveIdentity(t, "a totally different eight word secret phrase here")

	errs := make(chan ConnError, 1)
	srv, err := Listen("tcp", "127.0.0.1:0", server, MapSource{"x": []byte("secret")},
		WithConnErrorHandler(func(ce ConnError) { errs <- ce }))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	if _, err := Dial("tcp", srv.Addr().String(), attacker); err == nil {
		t.Fatal("dial with the wrong passphrase should fail")
	}
	select {
	case ce := <-errs:
		if ce.Phase != PhaseHandshake {
			t.Errorf("phase = %v, want handshake", ce.Phase)
		}
		if ce.Err == nil {
			t.Error("reported ConnError carried no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed handshake was not reported to the observer")
	}
}

// TestLAN_CleanDisconnectIsNotAnError confirms a normal hangup is not reported:
// the observer is for failures, not routine connection close.
func TestLAN_CleanDisconnectIsNotAnError(t *testing.T) {
	id := deriveIdentity(t, "correct horse battery staple lorem ipsum dolor sit")
	errs := make(chan ConnError, 1)
	srv, err := Listen("tcp", "127.0.0.1:0", id, MapSource{"a": []byte("1")},
		WithConnErrorHandler(func(ce ConnError) { errs <- ce }))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	client, err := Dial("tcp", srv.Addr().String(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get("a"); err != nil {
		t.Fatal(err)
	}
	_ = client.Close() // clean hangup

	select {
	case ce := <-errs:
		t.Fatalf("clean disconnect was reported as an error: %v", ce.Err)
	case <-time.After(300 * time.Millisecond):
		// no report — correct
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
