package main

import (
	"flag"
	"fmt"

	"github.com/justinstimatze/gusset/internal/crypto"
	"github.com/justinstimatze/gusset/internal/crypto/wordlist"
)

// genPassphraseCmd prints a fresh strong passphrase from the embedded EFF
// diceware wordlist. The user copies the same one to every device they want to
// pair — it is the only shared secret.
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
