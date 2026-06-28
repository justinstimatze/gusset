package main

import (
	"flag"
	"fmt"

	"github.com/justinstimatze/gusset/internal/config"
)

// setupCmd prints a state-aware, step-by-step walkthrough for getting two
// machines syncing. It is read-only: it inspects what is already done, marks it,
// and shows the exact next command — it does not run anything or change state.
// The same numbered steps appear in the companion extension's onboarding panel,
// so the CLI and the UI walk the user through the identical journey and reinforce
// each other (the extension names the `gusset` commands; the CLI points at the
// extension).
func setupCmd(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfgExists, _ := config.Exists()
	passSet := false
	if _, perr := readPassphrase(cfg); perr == nil {
		passSet = true
	}
	allow := len(cfg.Allowlist) > 0

	box := func(done bool) string {
		if done {
			return "✓"
		}
		return " "
	}
	// arrow flags the first not-yet-done step so the eye lands on what's next.
	next := func(done bool) string {
		if done {
			return "  "
		}
		return "→ "
	}

	fmt.Println("gusset setup — sync your Firefox extension settings across your own machines")
	fmt.Println("Run the same steps on every machine; do step 3's `passphrase new` on the first")
	fmt.Println("machine only, and `passphrase set` (same words) on the rest.")
	fmt.Println()

	fmt.Printf("%s[%s] 1. daemon installed        gusset %s\n", next(true), box(true), buildVersion())

	fmt.Printf("%s[%s] 2. config created\n", next(cfgExists), box(cfgExists))
	if cfgExists {
		fmt.Printf("        this device: %q\n", cfg.DeviceName)
	} else {
		fmt.Println("        run on every machine:  gusset init")
		fmt.Println("        (the only thing to carry between machines is the passphrase itself;")
		fmt.Println("         a bring-your-own weak passphrase? use `gusset init --with-salt` instead)")
	}

	fmt.Printf("%s[%s] 3. shared passphrase set\n", next(passSet), box(passSet))
	if !passSet {
		fmt.Println("        first machine:  gusset passphrase new   (generates + stores it; copy the words)")
		fmt.Println("        other machines: gusset passphrase set   (paste the same words)")
	}

	fmt.Printf("%s[%s] 4. chose what to sync\n", next(allow), box(allow))
	if allow {
		fmt.Printf("        allowlist: %v\n", cfg.Allowlist)
	} else {
		fmt.Println("        run:  gusset doctor               (lists your installed extensions)")
		fmt.Println("        then: gusset allow <extension-id> (e.g. uBlock0@raymondhill.net)")
	}

	fmt.Printf("%s[ ] 5. companion extension (optional, gives you a live status UI)\n", next(false))
	fmt.Println("        install the signed .xpi from the latest release in Firefox, then start")
	fmt.Println("        the daemon with a status socket and pair it:")
	fmt.Println("          gusset sync --watch --ws 127.0.0.1:8765")
	fmt.Println("          gusset ws-token                 (paste the token into the extension)")

	fmt.Printf("%s[ ] 6. first sync (both machines on the same WiFi)\n", next(false))
	fmt.Println("        established machine: gusset sync --for 2m")
	fmt.Println("        new machine:         gusset sync --force --once   (seeds it from the other)")
	fmt.Println("        off the same network? add --rendezvous-dir <a folder both already sync>")
	fmt.Println()

	switch {
	case !cfgExists:
		fmt.Println("you are at step 2 — run `gusset init` to begin.")
	case !passSet:
		fmt.Println("you are at step 3 — set the passphrase next.")
	case !allow:
		fmt.Println("you are at step 4 — choose an extension to sync.")
	default:
		fmt.Println("steps 1–4 done. Install/pair the extension (5) and run your first sync (6).")
	}
	return nil
}
