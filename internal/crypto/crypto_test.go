package crypto

import (
	"bytes"
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
	for _, pt := range [][]byte{nil, []byte(""), []byte("hello"), bytes.Repeat([]byte{0xAB}, 100_000)} {
		sealed, err := k.Seal(pt)
		if err != nil {
			t.Fatal(err)
		}
		got, err := k.Open(sealed)
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
	a, _ := k.Seal(pt)
	b, _ := k.Seal(pt)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of identical plaintext are byte-identical; nonce not random")
	}
}

func TestOpen_FailsClosed(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	sealed, _ := k.Seal([]byte("secret"))

	// Tampered byte.
	bad := bytes.Clone(sealed)
	bad[len(bad)-1] ^= 0xFF
	if _, err := k.Open(bad); err == nil {
		t.Error("tampered ciphertext opened")
	}

	// Wrong key.
	other := mustDerive(t, testPass+"x", AppSalt)
	if _, err := other.Open(sealed); err == nil {
		t.Error("ciphertext opened under wrong key")
	}

	// Truncated below nonce size.
	if _, err := k.Open([]byte{0x00}); err == nil {
		t.Error("truncated ciphertext opened")
	}
}

func TestAddress_KeyedAndStable(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	data := []byte("chunk bytes")
	if k.Address(data) != k.Address(data) {
		t.Fatal("address not stable")
	}
	// A different key yields a different address for the same data (keyed, not a
	// bare hash → no global confirmation oracle).
	other := mustDerive(t, testPass+"y", AppSalt)
	if k.Address(data) == other.Address(data) {
		t.Fatal("address not keyed to the user's secret")
	}
}

func TestSubkey_IndependentByLabel(t *testing.T) {
	k := mustDerive(t, testPass, AppSalt)
	a := k.Subkey("label-a", 32)
	b := k.Subkey("label-b", 32)
	if bytes.Equal(a, b) {
		t.Fatal("distinct labels produced identical subkeys")
	}
	if !bytes.Equal(a, k.Subkey("label-a", 32)) {
		t.Fatal("same label not deterministic")
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
