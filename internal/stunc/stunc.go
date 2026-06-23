// Package stunc is a minimal STUN client: it answers the one question Tier-1
// cross-network rendezvous needs — "what is my public IP:port?" — by sending a
// Binding request to a public STUN server and reading back the server-reflexive
// transport address (docs/transport-and-security.md §4, Tier 1).
//
// It speaks only the subset of RFC 8489 that requires: a Binding request and the
// XOR-MAPPED-ADDRESS (or legacy MAPPED-ADDRESS) of the response. That is ~100
// lines with no dependency, so gusset does not pull the full pion/ice tree just
// to learn its reflexive candidate. NAT hole-punching — the genuinely hard part
// that needs an ICE agent — is a later step (Tier 1 proper); when it is built,
// that is where pion/ice earns its place. Public STUN servers see only a public
// IP:port they were asked about, never any gusset data (§1 threat model).
package stunc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"
)

// magicCookie is the fixed STUN magic cookie (RFC 8489 §5). It is also the high
// 16 bits used to XOR the port, and the first 4 bytes used to XOR an IPv4 addr.
const magicCookie uint32 = 0x2112A442

const (
	bindingRequest         uint16 = 0x0001
	bindingSuccessResponse uint16 = 0x0101
	attrMappedAddress      uint16 = 0x0001
	attrXORMappedAddress   uint16 = 0x0020
	headerLen                     = 20
	txIDLen                       = 12
)

// Reflexive sends a STUN Binding request from conn to server and returns the
// server-reflexive transport address conn presents to the outside world, as a
// host:port string a peer on another network would dial.
//
// The caller owns conn deliberately: the reflexive mapping is specific to that
// local socket, so for hole-punching the same conn must later carry data. server
// is a host:port (e.g. "stun.l.google.com:19302"). The deadline comes from ctx;
// stray datagrams with a non-matching transaction ID are ignored until the right
// response arrives or ctx expires.
func Reflexive(ctx context.Context, conn net.PacketConn, server string) (string, error) {
	srvAddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return "", fmt.Errorf("stunc: resolve %q: %w", server, err)
	}

	var txID [txIDLen]byte
	if _, err := rand.Read(txID[:]); err != nil {
		return "", fmt.Errorf("stunc: transaction id: %w", err)
	}
	req := buildBindingRequest(txID)

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return "", fmt.Errorf("stunc: set deadline: %w", err)
		}
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}

	if _, err := conn.WriteTo(req, srvAddr); err != nil {
		return "", fmt.Errorf("stunc: send binding request: %w", err)
	}

	buf := make([]byte, 1280) // RFC 8489 recommends limiting STUN to the IPv6 min MTU
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("stunc: %w", ctx.Err())
		default:
		}
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return "", fmt.Errorf("stunc: read response: %w", err)
		}
		addr, ok := parseBindingResponse(buf[:n], txID)
		if !ok {
			continue // not our response (replay, unrelated datagram); keep waiting
		}
		return addr, nil
	}
}

// buildBindingRequest assembles a 20-byte STUN Binding request with no
// attributes: type, zero length, magic cookie, transaction ID.
func buildBindingRequest(txID [txIDLen]byte) []byte {
	msg := make([]byte, headerLen)
	binary.BigEndian.PutUint16(msg[0:2], bindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // message length: no attributes
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txID[:])
	return msg
}

// parseBindingResponse validates a STUN success response for txID and extracts
// the reflexive address from XOR-MAPPED-ADDRESS (preferred) or MAPPED-ADDRESS.
// ok is false for any message that is not our matching success response or that
// carries no usable address attribute.
func parseBindingResponse(msg []byte, txID [txIDLen]byte) (addr string, ok bool) {
	if len(msg) < headerLen {
		return "", false
	}
	if binary.BigEndian.Uint16(msg[0:2]) != bindingSuccessResponse {
		return "", false
	}
	if binary.BigEndian.Uint32(msg[4:8]) != magicCookie {
		return "", false
	}
	if !bytesEqual(msg[8:20], txID[:]) {
		return "", false
	}
	length := int(binary.BigEndian.Uint16(msg[2:4]))
	body := msg[headerLen:]
	if length > len(body) {
		return "", false
	}
	body = body[:length]

	var mapped string
	for len(body) >= 4 {
		atype := binary.BigEndian.Uint16(body[0:2])
		alen := int(binary.BigEndian.Uint16(body[2:4]))
		if 4+alen > len(body) {
			break
		}
		val := body[4 : 4+alen]
		switch atype {
		case attrXORMappedAddress:
			if a, ok := decodeXORMappedAddress(val, txID); ok {
				return a, true // XOR form is authoritative; return immediately
			}
		case attrMappedAddress:
			if a, ok := decodeMappedAddress(val); ok {
				mapped = a // keep as fallback; prefer XOR if we also see it
			}
		}
		// Attributes are padded to a 4-byte boundary.
		advance := 4 + alen
		if pad := alen % 4; pad != 0 {
			advance += 4 - pad
		}
		body = body[advance:]
	}
	if mapped != "" {
		return mapped, true
	}
	return "", false
}

// decodeXORMappedAddress decodes an XOR-MAPPED-ADDRESS value: a reserved byte, a
// family byte, the port XORed with the high 16 bits of the cookie, and the
// address XORed with the cookie (IPv4) or cookie||txID (IPv6).
func decodeXORMappedAddress(val []byte, txID [txIDLen]byte) (string, bool) {
	if len(val) < 4 {
		return "", false
	}
	family := val[1]
	port := binary.BigEndian.Uint16(val[2:4]) ^ uint16(magicCookie>>16)

	var cookie [4]byte
	binary.BigEndian.PutUint32(cookie[:], magicCookie)
	switch family {
	case 0x01: // IPv4
		if len(val) < 8 {
			return "", false
		}
		ip := make(net.IP, 4)
		for i := 0; i < 4; i++ {
			ip[i] = val[4+i] ^ cookie[i]
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(int(port))), true
	case 0x02: // IPv6
		if len(val) < 20 {
			return "", false
		}
		var mask [16]byte
		copy(mask[0:4], cookie[:])
		copy(mask[4:16], txID[:])
		ip := make(net.IP, 16)
		for i := 0; i < 16; i++ {
			ip[i] = val[4+i] ^ mask[i]
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(int(port))), true
	default:
		return "", false
	}
}

// decodeMappedAddress decodes a legacy (non-XOR) MAPPED-ADDRESS value.
func decodeMappedAddress(val []byte) (string, bool) {
	if len(val) < 4 {
		return "", false
	}
	family := val[1]
	port := binary.BigEndian.Uint16(val[2:4])
	switch family {
	case 0x01:
		if len(val) < 8 {
			return "", false
		}
		return net.JoinHostPort(net.IP(val[4:8]).String(), strconv.Itoa(int(port))), true
	case 0x02:
		if len(val) < 20 {
			return "", false
		}
		return net.JoinHostPort(net.IP(val[4:20]).String(), strconv.Itoa(int(port))), true
	default:
		return "", false
	}
}

// bytesEqual is a length-checked byte compare (transaction IDs are not secret, so
// a constant-time compare is unnecessary here).
func bytesEqual(a, b []byte) bool {
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
