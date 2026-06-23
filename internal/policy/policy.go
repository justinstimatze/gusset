// Package policy decides which extensions gusset is allowed to sync. It is the
// "safe by default" gate: nothing syncs unless the user explicitly allowlists
// it, and a built-in denylist of credential/secret extensions is refused unless
// the user adds an explicit per-extension override. See
// docs/transport-and-security.md §3.
package policy

import (
	"regexp"
	"sort"
)

// Decision is the result of evaluating an extension against the policy.
type Decision struct {
	// Allowed reports whether gusset may sync this extension.
	Allowed bool
	// Reason is a stable, human-readable explanation, always set. It powers the
	// never-sync-silently status model — even an allowed decision says why.
	Reason string
	// Sensitive reports whether the extension is on the built-in sensitive
	// denylist (a known credential/2FA store), regardless of the final decision.
	Sensitive bool
}

// SensitiveExtension describes a known credential/secret extension that is
// refused by default. Both Firefox add-on IDs a manager ships under are listed
// where they differ.
type SensitiveExtension struct {
	ID   string
	Name string
}

// sensitiveExtensions is the built-in denylist: extensions whose storage.local
// holds vault data, session/unlock tokens, or 2FA secrets. They are excluded on
// security grounds (blast radius) and correctness grounds (they run their own
// E2E sync; their tokens are device-bound). Deny-with-override: a user who
// truly means it can add the ID to Overrides.
//
// STARTER LIST — VERIFY AND EXPAND. The entries below use the slug-style add-on
// IDs that are publicly documented by each project; the many managers that ship
// under opaque "{uuid}" AMO IDs are deliberately NOT guessed here, because a
// wrong UUID is worse than an absent one (it denies the wrong extension and
// gives false confidence about the right one). Populate UUID-form IDs by reading
// them off a real install or the AMO listing, not from memory. This list is
// defense-in-depth; the opt-in allowlist (empty by default) is the primary gate,
// so an extension missing here is still not synced unless the user adds it.
var sensitiveExtensions = []SensitiveExtension{
	{"keepassxc-browser@keepassxc.org", "KeePassXC-Browser"},
	{"support@lastpass.com", "LastPass"},
	{"jid1-BoFifL9Vbdl2zQ@jetpack", "NordPass"},
}

// sensitiveByID is the denylist indexed for O(1) lookup.
var sensitiveByID = func() map[string]SensitiveExtension {
	m := make(map[string]SensitiveExtension, len(sensitiveExtensions))
	for _, e := range sensitiveExtensions {
		m[e.ID] = e
	}
	return m
}()

// Policy holds the user's sync policy: an opt-in allowlist and a set of explicit
// overrides for otherwise-denied sensitive extensions. The zero value is the
// safe default — it allows nothing.
type Policy struct {
	// Allowlist is the set of extension IDs the user has opted into syncing.
	Allowlist map[string]bool
	// Overrides is the set of sensitive extension IDs the user has explicitly
	// force-enabled despite the denylist. An override only matters if the same
	// ID is also in Allowlist.
	Overrides map[string]bool
}

// New returns an empty Policy: safe by default, nothing allowed.
func New() *Policy {
	return &Policy{Allowlist: map[string]bool{}, Overrides: map[string]bool{}}
}

// Allow opts an extension into syncing.
func (p *Policy) Allow(id string) { p.ensure(); p.Allowlist[id] = true }

// Disallow removes an extension from the allowlist.
func (p *Policy) Disallow(id string) { p.ensure(); delete(p.Allowlist, id) }

// Override force-enables a sensitive extension that would otherwise be denied.
// It does not by itself allowlist the extension — both are required to sync.
func (p *Policy) Override(id string) { p.ensure(); p.Overrides[id] = true }

func (p *Policy) ensure() {
	if p.Allowlist == nil {
		p.Allowlist = map[string]bool{}
	}
	if p.Overrides == nil {
		p.Overrides = map[string]bool{}
	}
}

// Evaluate decides whether extID may be synced and always explains why, using
// only the built-in ID denylist. Prefer EvaluateNamed when a display name is
// available — it adds the name heuristic that catches credential extensions not
// on the denylist.
func (p *Policy) Evaluate(extID string) Decision {
	return p.evaluate(extID, "")
}

// EvaluateNamed is Evaluate plus a name-based heuristic: if the extension's
// human-readable name looks like a credential/secret store, it is treated as
// sensitive (deny-with-override) even when its ID is not on the built-in
// denylist. The heuristic can false-positive (e.g. a harmless "Password
// Generator"); the cost is only an explicit override, which is the safe
// direction. Callers that have the display name (doctor, the extension UI)
// should use this.
func (p *Policy) EvaluateNamed(extID, name string) Decision {
	return p.evaluate(extID, name)
}

// evaluate is the shared core. Order of checks: not allowlisted -> denied;
// allowlisted but sensitive without override -> denied; allowlisted and (not
// sensitive or overridden) -> allowed.
func (p *Policy) evaluate(extID, name string) Decision {
	sens, byID := sensitiveByID[extID]
	sensitive := byID
	label := ""
	switch {
	case byID:
		label = sens.Name + " is a known credential/secret store"
	case LooksSensitiveName(name):
		sensitive = true
		label = name + " — its name looks like a credential/secret store"
	}

	if !p.has(p.Allowlist, extID) {
		return Decision{
			Allowed:   false,
			Sensitive: sensitive,
			Reason:    "not in the allowlist (sync is opt-in; add it to enable)",
		}
	}

	if sensitive && !p.has(p.Overrides, extID) {
		return Decision{
			Allowed:   false,
			Sensitive: true,
			Reason: "blocked — " + label + "; such stores run their own encrypted " +
				"sync and hold device-bound tokens. Override explicitly if you really mean to.",
		}
	}

	reason := "allowlisted"
	if sensitive {
		reason = "allowlisted with an explicit sensitive-override"
	}
	return Decision{Allowed: true, Sensitive: sensitive, Reason: reason}
}

// sensitiveNameRe matches human-readable extension names suggestive of a
// credential, password, 2FA, or wallet store. Deliberately broad: a false
// positive only forces an explicit override (the safe direction), while a miss
// would let a credential extension sync unwarned.
var sensitiveNameRe = regexp.MustCompile(`(?i)(password|passwd|\bpass\b|vault|authenticat|two[- ]?factor|\b2fa\b|\botp\b|\btotp\b|keepass|bitwarden|lastpass|1password|dashlane|nordpass|proton ?pass|enpass|\bwallet\b|seed ?phrase|private ?key|\bsecret)`)

// LooksSensitiveName reports whether a human-readable extension name suggests a
// credential/secret store, for the deny-with-override heuristic.
func LooksSensitiveName(name string) bool {
	return name != "" && sensitiveNameRe.MatchString(name)
}

func (p *Policy) has(m map[string]bool, id string) bool { return m != nil && m[id] }

// IsSensitive reports whether an extension ID is on the built-in denylist.
func IsSensitive(extID string) bool { _, ok := sensitiveByID[extID]; return ok }

// SensitiveList returns the built-in denylist, sorted by name, for display (e.g.
// `gusset doctor` flagging which installed extensions are sensitive).
func SensitiveList() []SensitiveExtension {
	out := make([]SensitiveExtension, len(sensitiveExtensions))
	copy(out, sensitiveExtensions)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
