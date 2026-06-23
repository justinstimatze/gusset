package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testPass = "correct horse battery staple lorem ipsum dolor sit"

func mustDerive(t *testing.T, pass string, salt []byte) *Keys {
	t.Helper()
	k, err := DeriveKeys(pass, salt)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestDeriveKeys_DeterministicAcrossMachines(t *testing.T) {
	// Same passphrase + same salt must reproduce identical keys — this is what
	// lets a second machine derive the same keys from the passphrase alone.
	a := mustDerive(t, testPass, AppSalt)
	b := mustDerive(t, testPass, AppSalt)
	if !a.Equal(b) {
		t.Fatal("same passphrase+salt produced different keys")
	}
	if a.Address([]byte("x")) != b.Address([]byte("x")) {
		t.Fatal("addresses differ for identical keys")
	}
}

func TestDeriveKeys_DiffersByPassphraseAndSalt(t *testing.T) {
	base := mustDerive(t, testPass, AppSalt)

	other := mustDerive(t, testPass+"!", AppSalt)
	if base.Equal(other) {
		t.Error("different passphrase produced same keys")
	}

	salt, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	saltVariant := mustDerive(t, testPass, salt)
	if base.Equal(saltVariant) {
		t.Error("different salt produced same keys")
	}
}

func TestDeriveKeys_Rejects(t *testing.T) {
	if _, err := DeriveKeys("", AppSalt); err == nil {
		t.Error("empty passphrase accepted")
	}
	if _, err := DeriveKeys(testPass, []byte("short")); err == nil {
		t.Error("short salt accepted")
	}
}

func TestSealOpen_RoundTrip(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	aad := []byte("content-address")
	for _, pt := range [][]byte{nil, []byte(""), []byte("hello"), bytes.Repeat([]byte{0xAB}, 100_000)} {
		sealed, err := k.Seal(pt, aad)
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.Open(sealed, aad)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round trip mismatch (len %d)", len(pt))
		}
	}
}

func TestSeal_NonceIsRandom(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	pt := []byte("same plaintext")
	a, _ := k.Seal(pt, nil)
	b, _ := k.Seal(pt, nil)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of identical plaintext are byte-identical; nonce not random")
	}
}

func TestOpen_FailsClosed(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	aad := []byte("addr-A")
	sealed, _ := k.Seal([]byte("secret"), aad)

	// Tampered byte.
	bad := bytes.Clone(sealed)
	bad[len(bad)-1] ^= 0xFF
	if _, err := k.Open(bad, aad); err == nil {
		t.Error("tampered ciphertext opened")
	}

	// Wrong key.
	other := mustDerive(t, testPass+"x", AppSalt)
	if _, err := other.Open(sealed, aad); err == nil {
		t.Error("ciphertext opened under wrong key")
	}

	// Truncated below nonce size.
	if _, err := k.Open([]byte{0x00}, aad); err == nil {
		t.Error("truncated ciphertext opened")
	}
}

func TestOpen_AADBindsContext(t *testing.T) {
	// M2: a ciphertext sealed under one address must not open under another —
	// this is what stops a chunk being served from the wrong content-address.
	k := mustDerive(t, testPass, AppSalt)
	sealed, err := k.Seal([]byte("chunk bytes"), []byte("addr-A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Open(sealed, []byte("addr-B")); err == nil {
		t.Fatal("ciphertext opened under a different aad/address")
	}
	if _, err := k.Open(sealed, []byte("addr-A")); err != nil {
		t.Fatalf("ciphertext did not open under its own aad: %v", err)
	}
}

func TestAddress_KeyedAndStable(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	data := []byte("chunk bytes")
	if k.Address(data) != k.Address(data) {
		t.Fatal("address not stable")
	}
	other := mustDerive(t, testPass+"y", AppSalt)
	if k.Address(data) == other.Address(data) {
		t.Fatal("address not keyed to the user's secret")
	}
}

func TestSubkey_IndependentByLabel(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	a, err := k.Subkey("label-a", 32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.Subkey("label-b", 32)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("distinct labels produced identical subkeys")
	}
	again, _ := k.Subkey("label-a", 32)
	if !bytes.Equal(a, again) {
		t.Fatal("same label not deterministic")
	}
}

func TestSubkey_Bounds(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	if _, err := k.Subkey("x", 0); err == nil {
		t.Error("zero-length subkey accepted")
	}
	if _, err := k.Subkey("x", maxHKDFOut+1); err == nil {
		t.Error("oversized subkey accepted (should error, not panic)")
	}
	if _, err := k.Subkey("x", maxHKDFOut); err != nil {
		t.Errorf("max-length subkey rejected: %v", err)
	}
}

func TestValidatePassphrase(t *testing.T) {
	if err := ValidatePassphrase(testPass); err != nil {
		t.Errorf("good passphrase rejected: %v", err)
	}
	weak := []string{
		"",
		"short",
		"onlytwo words",       // too few words
		"aaaa aaaa aaaa aaaa", // not distinct
		"a b c d",             // too short overall
	}
	for _, w := range weak {
		if err := ValidatePassphrase(w); !errors.Is(err, ErrWeakPassphrase) {
			t.Errorf("weak passphrase %q accepted (err=%v)", w, err)
		}
	}
}

func TestEntropyBits(t *testing.T) {
	// 8 words over 7776 → ~103 bits.
	got := EntropyBits(7776, 8)
	if got < 102 || got > 104 {
		t.Errorf("EntropyBits(7776,8) = %.1f, want ≈103", got)
	}
	if EntropyBits(1, 8) != 0 || EntropyBits(7776, 0) != 0 {
		t.Error("degenerate inputs should yield 0")
	}
}

func TestGeneratePassphrase(t *testing.T) {
	words := strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet")
	p, err := GeneratePassphrase(words, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(p)); got != 8 {
		t.Fatalf("got %d words, want 8", got)
	}
	for _, w := range strings.Fields(p) {
		if !contains(words, w) {
			t.Fatalf("generated word %q not from list", w)
		}
	}
	if _, err := GeneratePassphrase(words, 0); err == nil {
		t.Error("zero word count accepted")
	}
	if _, err := GeneratePassphrase([]string{"only"}, 8); err == nil {
		t.Error("tiny wordlist accepted")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
