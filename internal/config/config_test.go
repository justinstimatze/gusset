package config

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/justinstimatze/gusset/internal/crypto"
)

func TestEnsureIdentity(t *testing.T) {
	c := &Config{}
	changed, err := c.EnsureIdentity("My-Host")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first EnsureIdentity should report a change")
	}
	if c.DeviceName != "My-Host" {
		t.Fatalf("DeviceName = %q, want the hostname", c.DeviceName)
	}
	if !strings.HasPrefix(c.DeviceID, "My-Host-") || len(c.DeviceID) != len("My-Host-")+6 {
		t.Fatalf("DeviceID = %q, want hostname + '-' + 6 hex", c.DeviceID)
	}

	// Idempotent: a second call changes nothing and keeps the same identity.
	id, name := c.DeviceID, c.DeviceName
	if changed, _ := c.EnsureIdentity("ignored"); changed {
		t.Fatal("second EnsureIdentity should report no change")
	}
	if c.DeviceID != id || c.DeviceName != name {
		t.Fatal("EnsureIdentity must not alter an existing identity")
	}

	// The whole point: two devices that share a hostname get DISTINCT ids, so
	// they don't collide in mDNS self-detection, the status map, or ICE tie-break.
	other := &Config{}
	if _, err := other.EnsureIdentity("My-Host"); err != nil {
		t.Fatal(err)
	}
	if other.DeviceID == c.DeviceID {
		t.Fatal("same-hostname devices must receive distinct device ids")
	}
}

func TestLoad_MissingIsEmptyNotError(t *testing.T) {
	t.Setenv("GUSSET_CONFIG_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Allowlist) != 0 || len(c.Salt) != 0 {
		t.Fatalf("missing config should load empty, got %+v", c)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	t.Setenv("GUSSET_CONFIG_DIR", t.TempDir())
	salt, _ := crypto.NewSalt()
	c := &Config{
		Salt:           salt,
		Allowlist:      []string{"uBlock0@raymondhill.net"},
		Overrides:      []string{"keepassxc-browser@keepassxc.org"},
		PassphraseFile: filepath.Join(t.TempDir(), "passphrase"),
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Salt, salt) {
		t.Errorf("salt mismatch")
	}
	if !reflect.DeepEqual(got.Allowlist, c.Allowlist) || !reflect.DeepEqual(got.Overrides, c.Overrides) {
		t.Errorf("lists mismatch: %+v", got)
	}
	if got.PassphraseFile != c.PassphraseFile {
		t.Errorf("passphrase file mismatch: %q", got.PassphraseFile)
	}
}

func TestExists(t *testing.T) {
	t.Setenv("GUSSET_CONFIG_DIR", t.TempDir())
	if ok, err := Exists(); err != nil || ok {
		t.Fatalf("expected not-exists, got ok=%v err=%v", ok, err)
	}
	if err := (&Config{}).Save(); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(); err != nil || !ok {
		t.Fatalf("expected exists, got ok=%v err=%v", ok, err)
	}
}

func TestSaltOrApp(t *testing.T) {
	empty := &Config{}
	if !bytes.Equal(empty.SaltOrApp(), crypto.AppSalt) {
		t.Error("empty config should fall back to AppSalt")
	}
	salt, _ := crypto.NewSalt()
	if !bytes.Equal((&Config{Salt: salt}).SaltOrApp(), salt) {
		t.Error("configured salt should be used")
	}
	// A too-short salt must not be used for derivation.
	if !bytes.Equal((&Config{Salt: []byte("short")}).SaltOrApp(), crypto.AppSalt) {
		t.Error("short salt should fall back to AppSalt, not derive weakly")
	}
}

func TestAllowOverrideDisallow(t *testing.T) {
	c := &Config{}
	c.Allow("b@x", "a@x", "b@x", "") // dedup + drop empty
	if !reflect.DeepEqual(c.Allowlist, []string{"a@x", "b@x"}) {
		t.Fatalf("allow dedup/sort failed: %+v", c.Allowlist)
	}
	c.Disallow("a@x")
	if !reflect.DeepEqual(c.Allowlist, []string{"b@x"}) {
		t.Fatalf("disallow failed: %+v", c.Allowlist)
	}
	c.Override("k@x", "k@x")
	if !reflect.DeepEqual(c.Overrides, []string{"k@x"}) {
		t.Fatalf("override dedup failed: %+v", c.Overrides)
	}
}
