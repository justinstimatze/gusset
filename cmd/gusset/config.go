package main

import (
	"encoding/base64"
	"flag"
	"fmt"

	"github.com/justinstimatze/gusset/internal/config"
	"github.com/justinstimatze/gusset/internal/crypto"
)

// initCmd creates the config directory and an empty config. By default key
// derivation uses the passphrase alone (crypto.AppSalt), so the same 8 words
// reproduce keys on every machine with no extra sharing. --new-salt opts into
// the per-user-salt hardening: it generates a salt that you must copy to every
// other device (with --salt <b64>), in exchange for per-target Argon2id cost and
// no cross-user key linkage. init refuses to overwrite an existing config, since
// clobbering a shared salt would orphan a paired device.
func initCmd(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	newSalt := fs.Bool("new-salt", false, "generate a per-user salt (must be shared to other devices)")
	importSalt := fs.String("salt", "", "import a base64 salt printed by `init --new-salt` on another device")
	if err := fs.Parse(args); err != nil {
		return err
	}

	exists, err := config.Exists()
	if err != nil {
		return err
	}
	if exists {
		p, _ := config.Path()
		return fmt.Errorf("config already exists at %s (edit it or delete it to re-init)", p)
	}

	cfg := &config.Config{}
	switch {
	case *importSalt != "":
		salt, err := base64.StdEncoding.DecodeString(*importSalt)
		if err != nil {
			return fmt.Errorf("decode --salt: %w", err)
		}
		if len(salt) < crypto.SaltLen {
			return fmt.Errorf("--salt too short: need %d bytes", crypto.SaltLen)
		}
		cfg.Salt = salt
	case *newSalt:
		salt, err := crypto.NewSalt()
		if err != nil {
			return err
		}
		cfg.Salt = salt
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Printf("wrote %s\n", p)
	if len(cfg.Salt) > 0 {
		b64 := base64.StdEncoding.EncodeToString(cfg.Salt)
		fmt.Println("per-user salt — run this on every other device to pair them:")
		fmt.Printf("  gusset init --salt %s\n", b64)
	}
	d, _ := config.Dir()
	fmt.Printf("next: put your 8-word passphrase in %s/passphrase (chmod 600),\n", d)
	fmt.Println("  then `gusset allow <extension-id>` to opt extensions in (see `gusset doctor`).")
	return nil
}

// allowCmd adds extension IDs to the persisted allowlist.
func allowCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gusset allow <extension-id> [<extension-id>...]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Allow(args...)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("allowlist now: %v\n", cfg.Allowlist)
	return nil
}

// disallowCmd removes extension IDs from the persisted allowlist.
func disallowCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gusset disallow <extension-id> [<extension-id>...]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Disallow(args...)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("allowlist now: %v\n", cfg.Allowlist)
	return nil
}
