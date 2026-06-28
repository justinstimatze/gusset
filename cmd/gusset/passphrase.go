package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/justinstimatze/gusset/internal/config"
	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/crypto/wordlist"
)

// genPassphraseCmd prints a fresh strong passphrase from the embedded EFF
// diceware wordlist, without storing it. It is the scriptable primitive;
// `gusset passphrase new` generates AND stores in one step. The user copies the
// same passphrase to every device they want to pair — it is the only shared secret.
func genPassphraseCmd(args []string) error {
	fs := flag.NewFlagSet("gen-passphrase", flag.ContinueOnError)
	n := fs.Int("words", 8, "number of words (8 ≈ 103 bits of entropy)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := crypto.GeneratePassphrase(wordlist.Words(), *n)
	if err != nil {
		return err
	}
	fmt.Println(p)
	return nil
}

// passphraseCmd dispatches the `gusset passphrase` subcommands, which store the
// root secret into the default file (<config-dir>/passphrase) with 0600 perms so
// nobody has to hand-create the file or remember to chmod it.
func passphraseCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gusset passphrase <new|set> (new: generate and store; set: store one you already have)")
	}
	switch args[0] {
	case "new":
		return passphraseNewCmd(args[1:])
	case "set":
		return passphraseSetCmd(args[1:])
	default:
		return fmt.Errorf("unknown passphrase subcommand %q (want `new` or `set`)", args[0])
	}
}

// passphraseNewCmd generates a strong passphrase, stores it 0600, and prints it
// once so it can be carried to the other devices. This is the first-machine path.
func passphraseNewCmd(args []string) error {
	fs := flag.NewFlagSet("passphrase new", flag.ContinueOnError)
	n := fs.Int("words", 8, "number of words (8 ≈ 103 bits of entropy)")
	force := fs.Bool("force", false, "replace an existing passphrase file (re-keys this device)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := crypto.GeneratePassphrase(wordlist.Words(), *n)
	if err != nil {
		return err
	}
	path, err := writePassphraseFile(p, *force)
	if err != nil {
		return err
	}
	fmt.Println(p)
	fmt.Printf("\nstored at %s (0600). Copy these words to your other devices and run\n`gusset passphrase set` there to pair them.\n", path)
	return nil
}

// passphraseSetCmd stores a passphrase you already have (typed at a hidden
// prompt, or piped on stdin) into the default file 0600. This is the path on a
// second device: paste the words the first device printed. It validates the
// structural floor so a fat-fingered one-word entry fails loudly.
func passphraseSetCmd(args []string) error {
	fs := flag.NewFlagSet("passphrase set", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace an existing passphrase file (re-keys this device)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := readSecretInput()
	if err != nil {
		return err
	}
	if err := crypto.ValidatePassphrase(p); err != nil {
		return err
	}
	path, err := writePassphraseFile(p, *force)
	if err != nil {
		return err
	}
	fmt.Printf("passphrase stored at %s (0600). Use the SAME words on every device.\n", path)
	return nil
}

// readSecretInput reads a passphrase without echoing it. At an interactive
// terminal it prompts twice and checks the two entries match; when stdin is not a
// terminal (a pipe, a test) it reads stdin once, so `echo … | gusset passphrase
// set` and automation still work. Prompts go to stderr so stdout stays clean.
func readSecretInput() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	fmt.Fprint(os.Stderr, "Enter passphrase: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Confirm passphrase: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(first))
	if p != strings.TrimSpace(string(second)) {
		return "", errors.New("passphrases did not match")
	}
	return p, nil
}

// writePassphraseFile writes the passphrase to <config-dir>/passphrase with 0600
// perms via a temp file + atomic rename, creating the config dir 0700 if needed.
// It refuses to clobber an existing file unless force is set — overwriting re-keys
// this device, which orphans it from every device still on the old passphrase.
func writePassphraseFile(p string, force bool) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "passphrase")
	if !force {
		switch _, statErr := os.Stat(path); {
		case statErr == nil:
			return "", fmt.Errorf("passphrase already set at %s — pass --force to replace it (this re-keys this device; every paired device must use the same words)", path)
		case !errors.Is(statErr, os.ErrNotExist):
			return "", statErr
		}
	}
	tmp, err := os.CreateTemp(dir, ".passphrase-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(strings.TrimSpace(p) + "\n"); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}
