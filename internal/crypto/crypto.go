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
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	keyLen  = 32 // XChaCha20-Poly1305 key / HMAC-SHA256 key
	saltLen = 16
	// maxHKDFOut is the RFC 5869 ceiling for HKDF-SHA256 output: 255*HashLen.
	maxHKDFOut = 255 * sha256.Size
)

// Argon2id parameters. Interactive-grade: ~64 MB, 3 passes. Tuned for a daemon
// deriving keys occasionally, not a login hot path.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB → 64 MiB
	argonThreads = 4
)

// Passphrase strength floor for user-supplied passphrases (enforced at setup via
// ValidatePassphrase, not on every derive). This is a coarse structural bar, not
// an entropy guarantee — the real assurance comes from GeneratePassphrase. A
// generated 8-word EFF phrase clears it with wide margin (≈103 bits).
const (
	MinPassphraseLen   = 16
	MinPassphraseWords = 4
)

// ErrWeakPassphrase is returned by ValidatePassphrase for passphrases below the
// structural floor.
var ErrWeakPassphrase = errors.New("crypto: passphrase too weak")

// AppSalt is a fallback that lets a passphrase alone reproduce keys on any
// machine. Prefer a per-user random salt (NewSalt), generated once and shared at
// first pairing: it restores Argon2id's per-target cost and removes any
// cross-user key/address linkage for identical passphrases. AppSalt is only for
// the no-shared-state "passphrase-alone" path, and is acceptable there *only*
// because the passphrase is high-entropy and there is no durable ciphertext
// store to attack offline (see docs/transport-and-security.md §2, M1).
var AppSalt = []byte("gusset/v1/app-salt\x00")

// Keys holds the derived key material. Construct it with DeriveKeys; never
// serialize it.
type Keys struct {
	master []byte
	enc    []byte
	addr   []byte
}

// DeriveKeys stretches a passphrase into key material using Argon2id and HKDF.
// Pass a shared random salt (NewSalt) for the recommended per-user variant, or
// AppSalt for the passphrase-alone fallback. DeriveKeys does not itself enforce
// passphrase strength — call ValidatePassphrase when the user first chooses or
// enters one; derivation thereafter must succeed for the already-chosen secret.
func DeriveKeys(passphrase string, salt []byte) (*Keys, error) {
	if passphrase == "" {
		return nil, errors.New("crypto: empty passphrase")
	}
	if len(salt) < saltLen {
		return nil, errors.New("crypto: salt too short")
	}
	master := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keyLen)
	k := &Keys{master: master}
	var err error
	if k.enc, err = k.Subkey("gusset/v1/enc", keyLen); err != nil {
		return nil, err
	}
	if k.addr, err = k.Subkey("gusset/v1/addr", keyLen); err != nil {
		return nil, err
	}
	return k, nil
}

// Subkey derives n bytes of independent key material under a label via HKDF.
// Distinct labels yield cryptographically independent keys, so callers (e.g. the
// transport's peer-auth identity) can derive their own material from the same
// passphrase without coordinating with this package. n must be in
// (0, maxHKDFOut].
func (k *Keys) Subkey(label string, n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("crypto: subkey length must be positive")
	}
	if n > maxHKDFOut {
		return nil, fmt.Errorf("crypto: subkey length %d exceeds HKDF-SHA256 max %d", n, maxHKDFOut)
	}
	r := hkdf.New(sha256.New, k.master, nil, []byte(label))
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("crypto: hkdf: %w", err)
	}
	return out, nil
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
//
// aad is additional authenticated data bound into the tag but not encrypted.
// The chunk layer MUST pass the chunk's content-address here, so a ciphertext
// served from the wrong address fails Open — see docs/transport-and-security.md
// §2 (M2). Pass nil only when there is genuinely no binding context.
func (k *Keys) Seal(plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.enc)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Seal appends the ciphertext to nonce, so the result is nonce||ct||tag.
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open reverses Seal. aad must match the value passed to Seal exactly. It fails
// closed on any tampering (wrong key, modified bytes, truncation, or mismatched
// aad — e.g. a ciphertext served from a different content-address).
func (k *Keys) Open(sealed, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.enc)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	return aead.Open(nil, nonce, ct, aad)
}

// Equal reports whether two key sets are identical, in constant time.
func (k *Keys) Equal(other *Keys) bool {
	return subtle.ConstantTimeCompare(k.master, other.master) == 1
}

// NewSalt returns a fresh random salt for the recommended per-user-salt variant.
func NewSalt() ([]byte, error) {
	b := make([]byte, saltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// ValidatePassphrase enforces a coarse structural floor on a user-supplied
// passphrase. It is intentionally conservative and does NOT estimate entropy of
// arbitrary strings (that is unreliable and gives false confidence). Use
// GeneratePassphrase for a passphrase with known, sufficient entropy; this check
// only rejects obviously-weak input on the bring-your-own path.
func ValidatePassphrase(p string) error {
	trimmed := strings.TrimSpace(p)
	if len(trimmed) < MinPassphraseLen {
		return fmt.Errorf("%w: need at least %d characters", ErrWeakPassphrase, MinPassphraseLen)
	}
	words := strings.Fields(trimmed)
	if len(words) < MinPassphraseWords {
		return fmt.Errorf("%w: need at least %d words", ErrWeakPassphrase, MinPassphraseWords)
	}
	uniq := make(map[string]struct{}, len(words))
	for _, w := range words {
		uniq[strings.ToLower(w)] = struct{}{}
	}
	if len(uniq) < MinPassphraseWords {
		return fmt.Errorf("%w: words must be distinct", ErrWeakPassphrase)
	}
	return nil
}

// EntropyBits returns the entropy of an n-word passphrase drawn uniformly from a
// wordlist of the given size: n*log2(size). Use it to show the user the strength
// of a generated phrase (8 words over the 7776-word EFF list ≈ 103 bits).
func EntropyBits(wordlistSize, words int) float64 {
	if wordlistSize < 2 || words <= 0 {
		return 0
	}
	return float64(words) * math.Log2(float64(wordlistSize))
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
	maxIdx := big.NewInt(int64(len(wordlist)))
	words := make([]byte, 0, n*8)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, maxIdx)
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
