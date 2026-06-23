// Package transport moves encrypted chunks directly between a user's own
// devices. The data plane never sees plaintext: it serves and fetches opaque
// ciphertext keyed by content-address (the output of internal/chunk). v1 is
// Tier 0 — same-LAN direct over mutual-TLS, with peer authentication derived
// from the shared passphrase, so there is no third-party transport account and
// no server holding data (docs/transport-and-security.md §4).
//
// The package is layered so each piece is testable in isolation:
//   - proto.go  — the chunk request/response wire protocol over any
//     io.ReadWriteCloser (test it over net.Pipe, no sockets, no TLS).
//   - identity.go — passphrase → Ed25519 peer identity → pinned mutual-TLS.
//   - lan.go     — Tier-0 server/client wiring the two over loopback TCP.
package transport

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Wire opcodes. The protocol is a simple sequential request/response over a
// single authenticated connection: the fetcher asks, the source answers, in
// order. That matches how chunk.Reconstruct pulls chunks one at a time.
const (
	opHas   byte = 1 // "do you have this address?" -> 1 byte 0/1
	opGet   byte = 2 // "give me this address"      -> 1 byte found, then framed bytes
	opOffer byte = 3 // "what do you offer?"        -> one framed (opaque) catalog blob
)

// Protocol bounds. Addresses are hex of an HMAC-SHA256 (64 chars); 128 is ample
// headroom. A chunk is at most chunkMax (1 MiB) plus AEAD framing; 2 MiB is a
// generous ceiling. The offer catalog is just manifests (addresses + sizes), so
// 16 MiB covers a large allowlist comfortably. All bound memory against a
// hostile or buggy peer.
const (
	maxAddrLen   = 128
	maxChunkLen  = 2 * 1024 * 1024
	maxOfferLen  = 16 * 1024 * 1024
	readBufBytes = 64 * 1024
)

// These reject oversized framed reads before allocating, so a peer cannot make
// us allocate unbounded memory.
var (
	errAddrTooLong  = errors.New("transport: address frame too long")
	errChunkTooLong = errors.New("transport: chunk frame too long")
	errOfferTooLong = errors.New("transport: offer frame too long")
)

// ChunkSource is the server side: the local set of encrypted chunks this device
// can serve, keyed by content-address. A map[string][]byte (the output of
// chunk.Split) satisfies it via MapSource. Implementations must be safe for
// concurrent use if the same source is served on multiple connections.
type ChunkSource interface {
	// Has reports whether the address is available locally.
	Has(address string) bool
	// Get returns the sealed ciphertext for address, or ok=false if absent.
	Get(address string) (data []byte, ok bool)
}

// OfferSource is an optional capability a ChunkSource may also implement: it
// returns this device's catalog of what it has to sync, as an opaque blob (the
// reconcile layer marshals/unmarshals it — transport stays data-agnostic). A
// server whose source does not implement OfferSource answers opOffer with an
// empty catalog.
type OfferSource interface {
	Offer() ([]byte, error)
}

// MapSource adapts an in-memory address->ciphertext map (what chunk.Split
// returns) to a ChunkSource. It is read-only after construction, so concurrent
// Serve goroutines may share one safely.
type MapSource map[string][]byte

// Has implements ChunkSource.
func (m MapSource) Has(address string) bool {
	_, ok := m[address]
	return ok
}

// Get implements ChunkSource.
func (m MapSource) Get(address string) ([]byte, bool) {
	d, ok := m[address]
	return d, ok
}

// StaticSource bundles a chunk map with an opaque offer catalog, satisfying both
// ChunkSource and OfferSource — what `gusset sync` serves to a peer for one run.
// Read-only after construction, so concurrent Serve goroutines share it safely.
type StaticSource struct {
	Chunks    map[string][]byte
	OfferBlob []byte
}

// Has implements ChunkSource.
func (s StaticSource) Has(address string) bool {
	_, ok := s.Chunks[address]
	return ok
}

// Get implements ChunkSource.
func (s StaticSource) Get(address string) ([]byte, bool) {
	d, ok := s.Chunks[address]
	return d, ok
}

// Offer implements OfferSource.
func (s StaticSource) Offer() ([]byte, error) { return s.OfferBlob, nil }

// Serve answers chunk requests on conn from src until the peer closes the
// connection (io.EOF) or a protocol/IO error occurs. It is the server-side loop;
// one call handles one connection. A clean peer hangup returns nil.
func Serve(conn io.ReadWriter, src ChunkSource) error {
	r := bufio.NewReaderSize(conn, readBufBytes)
	w := bufio.NewWriterSize(conn, readBufBytes)
	for {
		op, err := r.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("transport: read op: %w", err)
		}
		switch op {
		case opOffer: // no address; answer with the (possibly empty) catalog
			blob, err := offerOf(src)
			if err != nil {
				return err
			}
			if err := writeFrame(w, blob); err != nil {
				return err
			}
		case opHas, opGet:
			addr, err := readFrame(r, maxAddrLen, errAddrTooLong)
			if err != nil {
				return err
			}
			if op == opHas {
				if err := writeBool(w, src.Has(string(addr))); err != nil {
					return err
				}
				break
			}
			data, ok := src.Get(string(addr))
			if err := writeBool(w, ok); err != nil {
				return err
			}
			if ok {
				if err := writeFrame(w, data); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("transport: unknown opcode %d", op)
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("transport: flush: %w", err)
		}
	}
}

// offerOf returns src's catalog blob if it implements OfferSource, else empty
// (a source with nothing to advertise is valid — the peer reads it as "offers
// nothing").
func offerOf(src ChunkSource) ([]byte, error) {
	os, ok := src.(OfferSource)
	if !ok {
		return nil, nil
	}
	return os.Offer()
}

// Client is the fetcher side: a sequential request/response wrapper over one
// authenticated connection. It satisfies the get/has shape that chunk.Missing
// and syncx.Import consume. A single connection is used serially, so calls are
// serialized with a mutex; this matches chunk.Reconstruct's one-at-a-time pull.
type Client struct {
	rw io.ReadWriteCloser
	r  *bufio.Reader
	w  *bufio.Writer
}

// NewClient wraps an already-established (and, in production, authenticated)
// connection as a chunk-fetching client.
func NewClient(rw io.ReadWriteCloser) *Client {
	return &Client{
		rw: rw,
		r:  bufio.NewReaderSize(rw, readBufBytes),
		w:  bufio.NewWriterSize(rw, readBufBytes),
	}
}

// Has asks the peer whether it holds address.
func (c *Client) Has(address string) (bool, error) {
	if err := c.request(opHas, address); err != nil {
		return false, err
	}
	return readBool(c.r)
}

// Get fetches the sealed ciphertext for address. It returns an error if the peer
// does not have it, so it plugs directly into chunk.Reconstruct's get callback,
// where a missing chunk must fail the reconstruction.
func (c *Client) Get(address string) ([]byte, error) {
	if err := c.request(opGet, address); err != nil {
		return nil, err
	}
	ok, err := readBool(c.r)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("transport: peer lacks chunk %s", short(address))
	}
	return readFrame(c.r, maxChunkLen, errChunkTooLong)
}

// Offer fetches the peer's catalog blob — the opaque advertisement of what it
// has to sync. The reconcile layer unmarshals it; an empty result means the peer
// offers nothing.
func (c *Client) Offer() ([]byte, error) {
	if err := c.w.WriteByte(opOffer); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	return readFrame(c.r, maxOfferLen, errOfferTooLong)
}

// Close closes the underlying connection.
func (c *Client) Close() error { return c.rw.Close() }

// request writes one opcode + address frame and flushes it.
func (c *Client) request(op byte, address string) error {
	if len(address) > maxAddrLen {
		return errAddrTooLong
	}
	if err := c.w.WriteByte(op); err != nil {
		return err
	}
	if err := writeFrame(c.w, []byte(address)); err != nil {
		return err
	}
	return c.w.Flush()
}

// --- framing helpers: a uvarint length prefix followed by that many bytes ---

func writeFrame(w *bufio.Writer, b []byte) error {
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(b)))
	if _, err := w.Write(hdr[:n]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r *bufio.Reader, limit int, tooLong error) ([]byte, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, fmt.Errorf("transport: read frame length: %w", err)
	}
	if n > uint64(limit) {
		return nil, tooLong
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("transport: read frame body: %w", err)
	}
	return buf, nil
}

func writeBool(w *bufio.Writer, v bool) error {
	b := byte(0)
	if v {
		b = 1
	}
	return w.WriteByte(b)
}

func readBool(r *bufio.Reader) (bool, error) {
	b, err := r.ReadByte()
	if err != nil {
		return false, fmt.Errorf("transport: read bool: %w", err)
	}
	return b == 1, nil
}

func short(addr string) string {
	if len(addr) > 12 {
		return addr[:12]
	}
	return addr
}
