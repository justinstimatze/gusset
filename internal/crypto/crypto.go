// Package crypto turns the user's single passphrase into the keys gusset needs:
// chunk encryption, keyed content-addressing, and (via Subkey) peer
// authentication material. The transport only ever sees ciphertext and keyed
// hashes; the passphrase and derived keys never leave the machine. See
// docs/transport-and-security.md §2.
package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"math/big"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	keyLen  = 32 // XChaCha20-Poly1305 key / HMAC-SHA256 key
	saltLen = 16
)

// Argon2id parameters. Interactive-grade: ~64 MB, 3 passes. Tuned for a daemon
// deriving keys occasionally, not a login hot path.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB → 64 MiB
	argonThreads = 4
)

// AppSalt lets a passphrase alone reproduce the same keys on every machine —
// which is the product's "type the 8 words on the new device, done" UX. The
// passphrase is high-entropy (≈88 bits) and there is no online attack surface
// (no server), so a fixed salt is acceptable here; its anti-rainbow-table job is
// already done by the passphrase entropy. A per-user random salt shared at
// pairing is the stronger option and slots in via DeriveKeys' salt parameter
// when we want it.
var AppSalt = []byte("gusset/v1/app-salt\x00")

// Keys holds the derived key material. Construct it with DeriveKeys; never
// serialize it.
type Keys struct {
	master []byte
	enc    []byte
	addr   []byte
}

// DeriveKeys stretches a passphrase into key material using Argon2id and HKDF.
// Pass AppSalt for the passphrase-alone UX, or a shared random salt for the
// stronger per-user variant.
func DeriveKeys(passphrase string, salt []byte) (*Keys, error) {
	if passphrase == "" {
		return nil, errors.New("crypto: empty passphrase")
	}
	if len(salt) < saltLen {
		return nil, errors.New("crypto: salt too short")
	}
	master := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keyLen)
	k := &Keys{master: master}
	k.enc = k.Subkey("gusset/v1/enc", keyLen)
	k.addr = k.Subkey("gusset/v1/addr", keyLen)
	return k, nil
}

// Subkey derives n bytes of independent key material under a label via HKDF.
// Distinct labels yield cryptographically independent keys, so callers (e.g. the
// transport's peer-auth identity) can derive their own material from the same
// passphrase without coordinating with this package.
func (k *Keys) Subkey(label string, n int) []byte {
	r := hkdf.New(sha256.New, k.master, nil, []byte(label))
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		panic("crypto: hkdf read failed: " + err.Error()) // hkdf over a fixed reader cannot fail short
	}
	return out
}

// Address returns the keyed content address of data: HMAC-SHA256(K_addr, data),
// hex-encoded. Keyed (not a bare hash) so identical plaintext is not a global
// confirmation oracle and is not linkable across users; dedup within the user's
// own data still works.
func (k *Keys) Address(data []byte) string {
	mac := hmac.New(sha256.New, k.addr)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// Seal encrypts plaintext with XChaCha20-Poly1305 and returns nonce||ciphertext.
// The 192-bit XChaCha nonce makes random nonces collision-safe at our volumes.
func (k *Keys) Seal(plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.enc)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Seal appends the ciphertext to nonce, so the result is nonce||ct||tag.
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. It fails closed on any tampering (wrong key, modified
// bytes, truncation).
func (k *Keys) Open(sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.enc)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	return aead.Open(nil, nonce, ct, nil)
}

// Equal reports whether two key sets are identical, in constant time. Used by
// tests and by peer-auth checks; avoids leaking via timing.
func (k *Keys) Equal(other *Keys) bool {
	return subtle.ConstantTimeCompare(k.master, other.master) == 1
}

// NewSalt returns a fresh random salt for the stronger per-user-salt variant.
func NewSalt() ([]byte, error) {
	b := make([]byte, saltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// GeneratePassphrase builds an n-word passphrase by sampling uniformly, with
// crypto/rand, from the supplied wordlist. The wordlist is injected rather than
// embedded so we don't ship a fabricated list: vendor the EFF large wordlist
// (7776 words → ≈12.9 bits/word) and pass it here. n=8 over that list is ≈103
// bits; the design's "8 words" target assumes a list of that quality.
func GeneratePassphrase(wordlist []string, n int) (string, error) {
	if n <= 0 {
		return "", errors.New("crypto: word count must be positive")
	}
	if len(wordlist) < 2 {
		return "", errors.New("crypto: wordlist too small")
	}
	max := big.NewInt(int64(len(wordlist)))
	words := make([]byte, 0, n*8)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		if i > 0 {
			words = append(words, ' ')
		}
		words = append(words, wordlist[idx.Int64()]...)
	}
	return string(words), nil
}
