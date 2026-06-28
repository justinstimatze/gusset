package chunk

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"

	"github.com/justinstimatze/gusset/internal/crypto"
)

func testKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	k, err := crypto.DeriveKeys("correct horse battery staple lorem ipsum dolor sit", crypto.AppSalt)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// deterministic pseudo-random blob (seeded; not crypto/rand) for reproducible tests.
func blob(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	r.Read(b)
	return b
}

func splitAll(t *testing.T, k *crypto.Keys, data []byte) (*Manifest, map[string][]byte) {
	t.Helper()
	pol, err := DerivePolynomial(k)
	if err != nil {
		t.Fatal(err)
	}
	m, store, err := Split(bytes.NewReader(data), k, pol, Meta{Extension: "uBlock0@raymondhill.net", Browser: "firefox", CreatedAt: 1750000000})
	if err != nil {
		t.Fatal(err)
	}
	return m, store
}

func getter(store map[string][]byte) func(string) ([]byte, error) {
	return func(addr string) ([]byte, error) {
		c, ok := store[addr]
		if !ok {
			return nil, errors.New("not found")
		}
		return c, nil
	}
}

func TestSplitReconstruct_RoundTrip(t *testing.T) {
	k := testKeys(t)
	for _, n := range []int{0, 1, 1000, 200_000, 5_000_000} {
		data := blob(int64(n)+1, n)
		m, store := splitAll(t, k, data)
		got, err := Reconstruct(m, k, getter(store))
		if err != nil {
			t.Fatalf("n=%d reconstruct: %v", n, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("n=%d round trip mismatch (got %d bytes)", n, len(got))
		}
		if m.TotalSize != int64(n) {
			t.Errorf("n=%d manifest total %d", n, m.TotalSize)
		}
	}
}

func TestDerivePolynomial_DeterministicPerKey(t *testing.T) {
	k := testKeys(t)
	a, err := DerivePolynomial(k)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DerivePolynomial(k)
	if a != b {
		t.Fatal("polynomial not deterministic for the same key")
	}
	other, _ := crypto.DeriveKeys("a wholly different eight word passphrase here now", crypto.AppSalt)
	c, _ := DerivePolynomial(other)
	if a == c {
		t.Fatal("different keys produced the same polynomial")
	}
}

func TestSplit_DeterministicChunking(t *testing.T) {
	// Same key + same data => same chunk boundaries and addresses on any machine.
	k := testKeys(t)
	data := blob(42, 2_000_000)
	m1, _ := splitAll(t, k, data)
	m2, _ := splitAll(t, k, data)
	if len(m1.Chunks) != len(m2.Chunks) {
		t.Fatalf("chunk counts differ: %d vs %d", len(m1.Chunks), len(m2.Chunks))
	}
	for i := range m1.Chunks {
		if m1.Chunks[i] != m2.Chunks[i] {
			t.Fatalf("chunk %d differs across runs", i)
		}
	}
	if len(m1.Chunks) < 2 {
		t.Fatalf("expected multiple chunks for 2MB, got %d", len(m1.Chunks))
	}
}

func TestSplit_Dedup(t *testing.T) {
	// A blob made of a repeated block should dedup: far fewer store entries than
	// manifest references.
	k := testKeys(t)
	block := blob(7, 300_000)
	data := bytes.Repeat(block, 8)
	m, store := splitAll(t, k, data)
	if len(store) >= len(m.Chunks) {
		t.Fatalf("no dedup: %d store entries for %d refs", len(store), len(m.Chunks))
	}
	got, err := Reconstruct(m, k, getter(store))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("deduped blob did not round-trip: err=%v", err)
	}
}

func TestMissing_Resumability(t *testing.T) {
	k := testKeys(t)
	m, store := splitAll(t, k, blob(9, 1_500_000))
	addrs := m.Addresses()
	if len(addrs) == 0 {
		t.Fatal("no addresses")
	}
	// Pretend we already have all but the first unique chunk.
	have := map[string]bool{}
	for _, a := range addrs[1:] {
		have[a] = true
	}
	missing := m.Missing(func(a string) bool { return have[a] })
	if len(missing) != 1 || missing[0] != addrs[0] {
		t.Fatalf("missing = %v, want [%s]", missing, short(addrs[0]))
	}
	_ = store
}

func TestReconstruct_RejectsSubstitutedChunk(t *testing.T) {
	// M2: serving a different (validly-encrypted) chunk's ciphertext under an
	// address must be rejected.
	k := testKeys(t)
	m, store := splitAll(t, k, blob(11, 1_000_000))
	if len(m.Addresses()) < 2 {
		t.Skip("need at least two distinct chunks")
	}
	a0, a1 := m.Addresses()[0], m.Addresses()[1]
	tampered := map[string][]byte{}
	for kk, vv := range store {
		tampered[kk] = vv
	}
	tampered[a0] = store[a1] // wrong ciphertext under a0's address

	_, err := Reconstruct(m, k, getter(tampered))
	if err == nil {
		t.Fatal("substituted chunk accepted")
	}
}

func TestReconstruct_RejectsTamperedManifest(t *testing.T) {
	k := testKeys(t)
	m, store := splitAll(t, k, blob(13, 800_000))

	// Drop a chunk from the manifest without re-signing.
	bad := *m
	bad.Chunks = bad.Chunks[:len(bad.Chunks)-1]
	if _, err := Reconstruct(&bad, k, getter(store)); err == nil {
		t.Fatal("tampered (unsigned) manifest accepted")
	}

	// Wrong key cannot verify a valid manifest.
	other, _ := crypto.DeriveKeys("yet another distinct eight word passphrase today", crypto.AppSalt)
	if err := m.Verify(other); err == nil {
		t.Fatal("manifest verified under wrong key")
	}
}

func TestReconstruct_MissingChunkErrors(t *testing.T) {
	k := testKeys(t)
	m, _ := splitAll(t, k, blob(17, 700_000))
	_, err := Reconstruct(m, k, func(string) ([]byte, error) { return nil, errors.New("absent") })
	if err == nil {
		t.Fatal("reconstruct succeeded with no chunks available")
	}
}
