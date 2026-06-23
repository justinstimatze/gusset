// Package config persists gusset's per-user settings — the sync allowlist, any
// sensitive-extension overrides, an optional per-user salt, and where to find
// the passphrase — so routine `gusset sync` runs need no flags or environment.
// It lives at the XDG config path (overridable with GUSSET_CONFIG_DIR for
// tests). The passphrase itself is never stored here; config only points at a
// 0600 file that holds it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/justinstimatze/gusset/internal/crypto"
)

// fileName is the config file within the config directory.
const fileName = "config.json"

// Config is the on-disk settings. All fields are optional; the zero value is the
// safe default (nothing allowlisted, passphrase-alone key derivation).
type Config struct {
	// Salt is an optional per-user random salt (crypto.NewSalt). When set it
	// must be identical on every paired device. When absent, derivation uses
	// crypto.AppSalt so the passphrase alone reproduces keys across machines —
	// the zero-config default that keeps "just the 8 words" working.
	Salt []byte `json:"salt,omitempty"`
	// Allowlist is the set of extension IDs opted into syncing.
	Allowlist []string `json:"allowlist,omitempty"`
	// Overrides force-enable otherwise-denied sensitive extensions.
	Overrides []string `json:"overrides,omitempty"`
	// PassphraseFile is the path to a 0600 file holding the passphrase. Empty
	// means fall back to the default location or the environment.
	PassphraseFile string `json:"passphrase_file,omitempty"`
}

// Dir returns the config directory: GUSSET_CONFIG_DIR if set, else
// <user-config-dir>/gusset.
func Dir() (string, error) {
	if d := os.Getenv("GUSSET_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "gusset"), nil
}

// Path returns the full config file path.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, fileName), nil
}

// Load reads the config. A missing file is not an error — it returns an empty
// Config (the safe default), so `gusset sync` works before `gusset init`.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", p, err)
	}
	return &c, nil
}

// Exists reports whether a config file is already present (so `init` does not
// clobber an existing salt — losing it would orphan a paired device).
func Exists() (bool, error) {
	p, err := Path()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Save writes the config atomically with 0600 perms (it may name a passphrase
// file path), creating the directory 0700 if needed.
func (c *Config) Save() error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(d, fileName)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("config: write: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("config: replace: %w", err)
	}
	return nil
}

// SaltOrApp returns the per-user salt if one is configured, else crypto.AppSalt
// (the passphrase-alone fallback). A configured salt shorter than the minimum is
// ignored in favor of AppSalt rather than producing a weak derivation.
func (c *Config) SaltOrApp() []byte {
	if len(c.Salt) >= crypto.SaltLen {
		return c.Salt
	}
	return crypto.AppSalt
}

// Allow adds extension IDs to the allowlist (deduplicated, sorted).
func (c *Config) Allow(ids ...string) { c.Allowlist = mergeSorted(c.Allowlist, ids) }

// Override adds extension IDs to the sensitive-override set.
func (c *Config) Override(ids ...string) { c.Overrides = mergeSorted(c.Overrides, ids) }

// Disallow removes extension IDs from the allowlist.
func (c *Config) Disallow(ids ...string) { c.Allowlist = remove(c.Allowlist, ids) }

func mergeSorted(base, add []string) []string {
	set := map[string]bool{}
	for _, s := range base {
		set[s] = true
	}
	for _, s := range add {
		if s != "" {
			set[s] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func remove(base, drop []string) []string {
	gone := map[string]bool{}
	for _, s := range drop {
		gone[s] = true
	}
	var out []string
	for _, s := range base {
		if !gone[s] {
			out = append(out, s)
		}
	}
	return out
}
