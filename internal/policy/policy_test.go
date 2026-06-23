package policy

import "testing"

const (
	ordinaryExt  = "uBlock0@raymondhill.net"
	sensitiveExt = "keepassxc-browser@keepassxc.org" // on the built-in denylist
)

func TestEvaluate_SafeByDefault(t *testing.T) {
	p := New()
	d := p.Evaluate(ordinaryExt)
	if d.Allowed {
		t.Fatal("empty policy must allow nothing")
	}
	if d.Reason == "" {
		t.Fatal("decision must always carry a reason")
	}
}

func TestEvaluate_AllowlistedOrdinary(t *testing.T) {
	p := New()
	p.Allow(ordinaryExt)
	d := p.Evaluate(ordinaryExt)
	if !d.Allowed {
		t.Fatalf("allowlisted ordinary extension should be allowed: %s", d.Reason)
	}
	if d.Sensitive {
		t.Error("ordinary extension flagged sensitive")
	}
}

func TestEvaluate_SensitiveNeedsOverride(t *testing.T) {
	p := New()
	p.Allow(sensitiveExt)

	d := p.Evaluate(sensitiveExt)
	if d.Allowed {
		t.Fatal("sensitive extension must stay denied with allowlist alone")
	}
	if !d.Sensitive {
		t.Error("sensitive extension not flagged sensitive")
	}

	p.Override(sensitiveExt)
	d = p.Evaluate(sensitiveExt)
	if !d.Allowed {
		t.Fatalf("sensitive extension should be allowed after explicit override: %s", d.Reason)
	}
	if !d.Sensitive {
		t.Error("override should not clear the sensitive flag")
	}
}

func TestEvaluate_OverrideWithoutAllowlistStillDenied(t *testing.T) {
	p := New()
	p.Override(sensitiveExt) // override but never allowlisted
	d := p.Evaluate(sensitiveExt)
	if d.Allowed {
		t.Fatal("override alone must not enable syncing without allowlist")
	}
}

func TestDisallow(t *testing.T) {
	p := New()
	p.Allow(ordinaryExt)
	p.Disallow(ordinaryExt)
	if p.Evaluate(ordinaryExt).Allowed {
		t.Fatal("disallow should revoke")
	}
}

func TestZeroValuePolicyIsSafe(t *testing.T) {
	// A Policy{} with nil maps must not panic and must allow nothing.
	var p Policy
	if p.Evaluate(ordinaryExt).Allowed {
		t.Fatal("zero-value policy allowed something")
	}
	p.Allow(ordinaryExt) // must lazily init nil maps
	if !p.Evaluate(ordinaryExt).Allowed {
		t.Fatal("Allow on zero-value policy did not take effect")
	}
}

func TestEvaluateNamed_HeuristicCatchesUnknownCredentialExt(t *testing.T) {
	const unknownPM = "{some-unlisted-uuid}" // not on the built-in denylist
	p := New()
	p.Allow(unknownPM)

	// By ID alone it would be allowed; the name heuristic flags it.
	if d := p.Evaluate(unknownPM); !d.Allowed {
		t.Fatalf("ID-only eval unexpectedly denied: %s", d.Reason)
	}
	d := p.EvaluateNamed(unknownPM, "Acme Password Vault")
	if d.Allowed {
		t.Fatal("name heuristic should deny an unlisted credential-looking extension")
	}
	if !d.Sensitive {
		t.Error("name heuristic should set Sensitive")
	}

	// Explicit override lets it through.
	p.Override(unknownPM)
	if d := p.EvaluateNamed(unknownPM, "Acme Password Vault"); !d.Allowed {
		t.Fatalf("override should allow heuristic-flagged extension: %s", d.Reason)
	}
}

func TestEvaluateNamed_OrdinaryNamePasses(t *testing.T) {
	p := New()
	p.Allow(ordinaryExt)
	d := p.EvaluateNamed(ordinaryExt, "uBlock Origin")
	if !d.Allowed || d.Sensitive {
		t.Fatalf("ordinary named extension mis-flagged: allowed=%v sensitive=%v", d.Allowed, d.Sensitive)
	}
}

func TestLooksSensitiveName(t *testing.T) {
	sensitive := []string{"1Password", "Bitwarden", "KeePassXC", "Authenticator", "My Crypto Wallet", "Two-Factor Auth", "Seed Phrase Backup", "Proton Pass"}
	for _, n := range sensitive {
		if !LooksSensitiveName(n) {
			t.Errorf("%q should look sensitive", n)
		}
	}
	ordinary := []string{"uBlock Origin", "Dark Reader", "Tree Style Tab", "Reader View", ""}
	for _, n := range ordinary {
		if LooksSensitiveName(n) {
			t.Errorf("%q should not look sensitive", n)
		}
	}
}

func TestIsSensitiveAndList(t *testing.T) {
	if !IsSensitive(sensitiveExt) {
		t.Errorf("%s should be sensitive", sensitiveExt)
	}
	if IsSensitive(ordinaryExt) {
		t.Errorf("%s should not be sensitive", ordinaryExt)
	}
	list := SensitiveList()
	if len(list) == 0 {
		t.Fatal("sensitive list is empty")
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name > list[i].Name {
			t.Fatal("sensitive list not sorted by name")
		}
	}
}
