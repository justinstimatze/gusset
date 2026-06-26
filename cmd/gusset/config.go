package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/justinstimatze/gusset/internal/config"
	"github.com/justinstimatze/gusset/internal/crypto"
)

// uuidShape matches a Firefox per-install extension UUID (8-4-4-4-12 hex). It is
// used only to recognize when the user pasted a UUID where a stable extension id
// belongs — see resolveAllowIDs.
var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// initCmd creates the config directory and an empty config. By default it
// generates a per-user salt: the first device prints a one-line command to run
// on every other device (which imports the salt with --salt <b64>), and in
// exchange key derivation is per-user — Argon2id is salted, so a weak
// bring-your-own passphrase is not precomputation-attackable and keys never link
// across users. --no-salt opts out, deriving from the passphrase alone
// (crypto.AppSalt) so the same 8 words reproduce keys with no extra sharing —
// appropriate only for a high-entropy generated passphrase. init refuses to
// overwrite an existing config, since clobbering a shared salt would orphan a
// paired device.
func initCmd(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	noSalt := fs.Bool("no-salt", false, "derive from the passphrase alone, no per-user salt (only safe with a high-entropy generated passphrase)")
	importSalt := fs.String("salt", "", "import a base64 salt printed by `gusset init` on another device")
	deviceName := fs.String("device-name", "", "friendly name for this device shown in the UI (default: hostname)")
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
	case *noSalt:
		// passphrase-only: leave cfg.Salt nil so crypto.AppSalt is used.
	default:
		salt, err := crypto.NewSalt()
		if err != nil {
			return err
		}
		cfg.Salt = salt
	}

	// Generate this device's stable unique id and friendly name now, so the
	// identity is set before the first sync. --device-name overrides the default.
	host, _ := os.Hostname()
	if *deviceName != "" {
		cfg.DeviceName = *deviceName
	}
	if _, err := cfg.EnsureIdentity(host); err != nil {
		return err
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Printf("wrote %s (this device: %s)\n", p, cfg.DeviceName)
	if len(cfg.Salt) > 0 {
		b64 := base64.StdEncoding.EncodeToString(cfg.Salt)
		fmt.Println("per-user salt — run this on every other device to pair them:")
		fmt.Printf("  gusset init --salt %s\n", b64)
	}
	d, _ := config.Dir()
	fmt.Printf("next: put your passphrase in %s/passphrase (chmod 600) —\n", d)
	fmt.Println("  generate one with `gusset gen-passphrase` and use the SAME words on every device.")
	fmt.Println("  then `gusset allow <extension-id>` to opt extensions in (see `gusset doctor`).")
	return nil
}

// allowCmd adds extension IDs to the persisted allowlist. It forgives the common
// mistake of pasting a per-install UUID (the right column of `gusset doctor`)
// where the stable extension id belongs (the left column) — gusset keys the
// allowlist on the stable id, so an un-mapped UUID would silently match nothing.
func allowCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gusset allow <extension-id> [<extension-id>...]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Allow(resolveAllowIDs(args)...)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("allowlist now: %v\n", cfg.Allowlist)
	return nil
}

// resolveAllowIDs maps any per-install UUID in args back to its stable extension
// id, using the active profile's prefs.js. It is best-effort: if the profile
// can't be read, or a value matches no installed UUID, the value is passed
// through unchanged. A UUID that was successfully mapped is reported; a value
// that merely looks like a UUID but mapped to nothing gets a warning, since it
// will not match any extension.
func resolveAllowIDs(args []string) []string {
	var uuidToID map[string]string
	if _, installed, perr := localProfile(profileOverride("")); perr == nil {
		uuidToID = make(map[string]string, len(installed))
		for id, uuid := range installed {
			uuidToID[uuid] = id
		}
	}

	resolved := make([]string, 0, len(args))
	for _, a := range args {
		switch id, mapped := uuidToID[a]; {
		case mapped:
			fmt.Printf("interpreted %s as %s (that was the per-install UUID; allowlisting the stable id)\n", a, id)
			resolved = append(resolved, id)
		case uuidShape.MatchString(a):
			fmt.Fprintf(os.Stderr, "note: %q looks like a per-install UUID, not a stable extension id — gusset allowlists the stable id (the left column of `gusset doctor`). Allowing it as given; it will match nothing if it is a UUID.\n", a)
			resolved = append(resolved, a)
		default:
			resolved = append(resolved, a)
		}
	}
	return resolved
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
