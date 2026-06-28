package wordlist_test

import (
	"testing"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/crypto/wordlist"
)

// TestEmbeddedWordlistGeneratesValidPassphrase guards the friend-facing flow:
// `gusset gen-passphrase` must always produce a passphrase that passes the
// strength floor, which requires the embedded list to be large enough.
func TestEmbeddedWordlistGeneratesValidPassphrase(t *testing.T) {
	w := wordlist.Words()
	if len(w) < 4000 {
		t.Fatalf("embedded wordlist too small for adequate entropy: %d words", len(w))
	}
	for _, dup := range w[:1] {
		if dup == "" {
			t.Fatal("wordlist contains an empty entry")
		}
	}
	p, err := crypto.GeneratePassphrase(w, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := crypto.ValidatePassphrase(p); err != nil {
		t.Fatalf("generated 8-word passphrase was rejected by ValidatePassphrase: %v", err)
	}
}
