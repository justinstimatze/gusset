package transport

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

// pipeClient wires a Client to a Serve loop over an in-memory pipe — exercising
// the wire protocol with no sockets and no TLS.
func pipeClient(t *testing.T, src ChunkSource) *Client {
	t.Helper()
	c, s := net.Pipe()
	go func() { _ = Serve(s, src) }()
	client := NewClient(c)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestProto_HasAndGet(t *testing.T) {
	src := MapSource{
		"aabb": []byte("sealed-one"),
		"ccdd": bytes.Repeat([]byte{0x7f}, 4096),
	}
	client := pipeClient(t, src)

	for _, addr := range []string{"aabb", "ccdd"} {
		has, err := client.Has(addr)
		if err != nil {
			t.Fatalf("Has(%s): %v", addr, err)
		}
		if !has {
			t.Errorf("Has(%s) = false, want true", addr)
		}
		got, err := client.Get(addr)
		if err != nil {
			t.Fatalf("Get(%s): %v", addr, err)
		}
		if !bytes.Equal(got, src[addr]) {
			t.Errorf("Get(%s) mismatch", addr)
		}
	}
}

func TestProto_HasMissing(t *testing.T) {
	client := pipeClient(t, MapSource{"present": []byte("x")})
	has, err := client.Has("absent")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("Has(absent) = true, want false")
	}
}

func TestProto_GetMissingErrors(t *testing.T) {
	client := pipeClient(t, MapSource{})
	if _, err := client.Get("nope"); err == nil {
		t.Fatal("Get of a missing chunk should error")
	}
}

func TestProto_SequentialRequests(t *testing.T) {
	src := MapSource{"a": []byte("1"), "b": []byte("22"), "c": []byte("333")}
	client := pipeClient(t, src)
	// Many ordered requests over one connection must each get the right reply —
	// this is the access pattern chunk.Reconstruct uses.
	for i := 0; i < 50; i++ {
		for _, addr := range []string{"a", "b", "c"} {
			got, err := client.Get(addr)
			if err != nil {
				t.Fatalf("iter %d Get(%s): %v", i, addr, err)
			}
			if !bytes.Equal(got, src[addr]) {
				t.Fatalf("iter %d Get(%s) mismatch", i, addr)
			}
		}
	}
}

func TestProto_RejectsOversizedAddress(t *testing.T) {
	client := pipeClient(t, MapSource{})
	if _, err := client.Has(strings.Repeat("x", maxAddrLen+1)); err == nil {
		t.Fatal("oversized address should be rejected client-side")
	}
}
