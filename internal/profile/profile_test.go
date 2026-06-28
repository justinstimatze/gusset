package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProfilesINI_SingleDefaultProfile(t *testing.T) {
	// Shape observed on the live snap profile: one [Profile0] flagged Default=1.
	const ini = `[Profile0]
Name=default
IsRelative=1
Path=n5mpphsf.default
Default=1

[General]
StartWithLastProfile=1
Version=2
`
	profiles, installDefault := parseProfilesINI(ini)
	if installDefault != "" {
		t.Fatalf("installDefault = %q, want empty", installDefault)
	}
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
	p := profiles[0]
	if p.path != "n5mpphsf.default" || !p.isRelative || !p.isDefault {
		t.Fatalf("unexpected profile: %+v", p)
	}
}

func TestParseProfilesINI_InstallSectionWins(t *testing.T) {
	const ini = `[Install4F96D1932A9F858E]
Default=abcd1234.default-release
Locked=1

[Profile1]
Name=dev-edition
IsRelative=1
Path=zzzz0000.dev
Default=1

[Profile0]
Name=default-release
IsRelative=1
Path=abcd1234.default-release
`
	profiles, installDefault := parseProfilesINI(ini)
	if installDefault != "abcd1234.default-release" {
		t.Fatalf("installDefault = %q", installDefault)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
}

func TestDefaultProfileDir(t *testing.T) {
	root := t.TempDir()

	t.Run("install default beats Default=1", func(t *testing.T) {
		const ini = `[Install4F96D1932A9F858E]
Default=abcd1234.default-release

[Profile1]
Name=dev
IsRelative=1
Path=zzzz0000.dev
Default=1

[Profile0]
Name=release
IsRelative=1
Path=abcd1234.default-release
`
		writeINI(t, root, ini)
		got, err := DefaultProfileDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "abcd1234.default-release"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("falls back to Default=1", func(t *testing.T) {
		const ini = `[Profile0]
IsRelative=1
Path=p0
[Profile1]
IsRelative=1
Path=p1
Default=1
`
		writeINI(t, root, ini)
		got, err := DefaultProfileDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "p1"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("sole profile needs no default flag", func(t *testing.T) {
		const ini = `[Profile0]
IsRelative=1
Path=only
`
		writeINI(t, root, ini)
		got, err := DefaultProfileDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "only"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("absolute non-relative path is honored", func(t *testing.T) {
		abs := filepath.Join(root, "elsewhere")
		ini := "[Profile0]\nIsRelative=0\nPath=" + abs + "\n"
		writeINI(t, root, ini)
		got, err := DefaultProfileDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if got != abs {
			t.Fatalf("got %q, want %q", got, abs)
		}
	})
}

func writeINI(t *testing.T, root, ini string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseExtensionUUIDs(t *testing.T) {
	// A trimmed copy of a real prefs.js line: the value is a JSON string
	// literal whose content is itself a JSON object (double-decode).
	const prefs = `user_pref("browser.startup.page", 3);
user_pref("extensions.webextensions.uuids", "{\"uBlock0@raymondhill.net\":\"ba8ff762-346e-471a-8c1b-60abaa0cc23b\",\"{d634138d-c276-4fc8-924b-40a0ea21d284}\":\"ca234034-7bff-4055-9abf-20d825c01d8a\"}");
user_pref("extensions.lastAppVersion", "152.0.1");
`
	m, err := parseExtensionUUIDs(prefs)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(m), m)
	}
	if got := m["uBlock0@raymondhill.net"]; got != "ba8ff762-346e-471a-8c1b-60abaa0cc23b" {
		t.Fatalf("uBO uuid = %q", got)
	}
	if got := m["{d634138d-c276-4fc8-924b-40a0ea21d284}"]; got != "ca234034-7bff-4055-9abf-20d825c01d8a" {
		t.Fatalf("braced-id uuid = %q", got)
	}
}

func TestParseExtensionUUIDs_MissingPrefIsEmpty(t *testing.T) {
	m, err := parseExtensionUUIDs(`user_pref("browser.startup.page", 3);`)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("got %d entries, want 0", len(m))
	}
}
