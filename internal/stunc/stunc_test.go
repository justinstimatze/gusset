package stunc

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// fakeSTUNServer answers Binding requests with an XOR-MAPPED-ADDRESS of the
// sender's own address, like a real STUN server. It runs until the returned
// stop is called. The whole reflexive round-trip is exercised over loopback —
// no public server, no network flakiness.
func fakeSTUNServer(t *testing.T, xorMapped bool) (addr string, stop func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1280)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return // closed
			}
			if n < headerLen {
				continue
			}
			var txID [txIDLen]byte
			copy(txID[:], buf[8:20])
			ua, ok := from.(*net.UDPAddr)
			if !ok {
				continue
			}
			resp := buildResponse(txID, ua, xorMapped)
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String(), func() {
		_ = pc.Close()
		<-done
	}
}

// buildResponse assembles a Binding success response carrying the sender's
// address as either an XOR-MAPPED-ADDRESS or a legacy MAPPED-ADDRESS attribute.
func buildResponse(txID [txIDLen]byte, ua *net.UDPAddr, xor bool) []byte {
	ip4 := ua.IP.To4()
	val := make([]byte, 8) // reserved, family, port(2), ipv4(4)
	val[0] = 0
	val[1] = 0x01 // IPv4
	port := uint16(ua.Port)
	if xor {
		port ^= uint16(magicCookie >> 16)
	}
	binary.BigEndian.PutUint16(val[2:4], port)
	var cookie [4]byte
	binary.BigEndian.PutUint32(cookie[:], magicCookie)
	for i := 0; i < 4; i++ {
		b := ip4[i]
		if xor {
			b ^= cookie[i]
		}
		val[4+i] = b
	}

	atype := attrMappedAddress
	if xor {
		atype = attrXORMappedAddress
	}
	attr := make([]byte, 4+len(val))
	binary.BigEndian.PutUint16(attr[0:2], atype)
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(val)))
	copy(attr[4:], val)

	msg := make([]byte, headerLen+len(attr))
	binary.BigEndian.PutUint16(msg[0:2], bindingSuccessResponse)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txID[:])
	copy(msg[headerLen:], attr)
	return msg
}

func TestReflexive_XORMapped(t *testing.T) {
	server, stop := fakeSTUNServer(t, true)
	defer stop()
	assertReflexiveMatchesLocal(t, server)
}

func TestReflexive_LegacyMapped(t *testing.T) {
	// A server that only sends the legacy (non-XOR) attribute must still work.
	server, stop := fakeSTUNServer(t, false)
	defer stop()
	assertReflexiveMatchesLocal(t, server)
}

// assertReflexiveMatchesLocal runs Reflexive against the fake server and checks
// the reflexive address it reports equals the client socket's own loopback
// address — over loopback the "public" mapping is just the client's own port.
func assertReflexiveMatchesLocal(t *testing.T, server string) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := Reflexive(ctx, conn, server)
	if err != nil {
		t.Fatalf("Reflexive: %v", err)
	}
	want := conn.LocalAddr().String()
	if got != want {
		t.Errorf("reflexive address = %q, want the client's own %q", got, want)
	}
}

func TestReflexive_IgnoresStrayDatagram(t *testing.T) {
	// A datagram with a non-matching transaction ID must be ignored, not parsed
	// as the answer. The server replies with garbage first, then the real
	// response, and Reflexive must skip the garbage.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1280)
		n, from, err := pc.ReadFrom(buf)
		if err != nil || n < headerLen {
			return
		}
		var txID [txIDLen]byte
		copy(txID[:], buf[8:20])
		ua := from.(*net.UDPAddr)
		_, _ = pc.WriteTo([]byte("not a stun message at all"), from) // stray
		_, _ = pc.WriteTo(buildResponse(txID, ua, true), from)       // real
	}()
	defer func() { _ = pc.Close(); <-done }()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := Reflexive(ctx, conn, pc.LocalAddr().String())
	if err != nil {
		t.Fatalf("Reflexive: %v", err)
	}
	if got != conn.LocalAddr().String() {
		t.Errorf("got %q, want %q", got, conn.LocalAddr().String())
	}
}

func TestReflexive_TimesOutWhenServerSilent(t *testing.T) {
	// Point at a closed port so no response ever arrives; Reflexive must honor
	// the context deadline rather than block forever.
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := dead.LocalAddr().String()
	_ = dead.Close() // nothing listens there now

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Reflexive(ctx, conn, server); err == nil {
		t.Fatal("expected a timeout error when the server never responds")
	}
}
