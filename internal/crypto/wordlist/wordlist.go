// Package wordlist embeds the EFF "large" diceware wordlist for generating
// strong, memorable passphrases (~12.9 bits of entropy per word, so 8 words is
// ~103 bits).
//
// Source: Electronic Frontier Foundation, https://www.eff.org/dice
// (eff_large_wordlist.txt), licensed CC-BY-3.0:
// https://creativecommons.org/licenses/by/3.0/. See LICENSE-THIRD-PARTY.md.
package wordlist

import (
	_ "embed"
	"strings"
)

//go:embed eff_large.txt
var raw string

// Words returns the embedded wordlist, one word per line.
func Words() []string {
	return strings.Fields(raw)
}
