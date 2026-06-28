package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProfile creates a throwaway Firefox profile whose prefs.js maps the given
// stable extension ids to per-install UUIDs, mirroring what ExtensionUUIDs reads.
func writeProfile(t *testing.T, idToUUID map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	pref := `user_pref("extensions.webextensions.uuids", "{`
	first := true
	for id, uuid := range idToUUID {
		if !first {
			pref += ","
		}
		first = false
		pref += `\"` + id + `\":\"` + uuid + `\"`
	}
	pref += `}");` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "prefs.js"), []byte(pref), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveAllowIDs_MapsUUIDToStableID(t *testing.T) {
	const id, uuid = "uBlock0@raymondhill.net", "ba8ff762-346e-471a-8c1b-60abaa0cc23b"
	t.Setenv("GUSSET_PROFILE", writeProfile(t, map[string]string{id: uuid}))

	// The exact faceplant from the live test: the user pastes the per-install
	// UUID (right column) where the stable id belongs. It must be mapped back.
	got := resolveAllowIDs([]string{uuid})
	if len(got) != 1 || got[0] != id {
		t.Fatalf("expected the UUID to map to %q, got %v", id, got)
	}
}

func TestResolveAllowIDs_PassesStableIDThrough(t *testing.T) {
	const id = "uBlock0@raymondhill.net"
	t.Setenv("GUSSET_PROFILE", writeProfile(t, map[string]string{id: "ba8ff762-346e-471a-8c1b-60abaa0cc23b"}))

	// A stable id (the correct input) is returned unchanged.
	got := resolveAllowIDs([]string{id})
	if len(got) != 1 || got[0] != id {
		t.Fatalf("expected the stable id unchanged, got %v", got)
	}
}

func TestResolveAllowIDs_UnknownUUIDPassesThrough(t *testing.T) {
	t.Setenv("GUSSET_PROFILE", writeProfile(t, map[string]string{"a@b": "11111111-2222-3333-4444-555555555555"}))

	// A UUID-shaped value that maps to no installed extension is left as-is (the
	// command warns separately); it must not be dropped or rewritten.
	const orphan = "99999999-8888-7777-6666-555555555555"
	got := resolveAllowIDs([]string{orphan})
	if len(got) != 1 || got[0] != orphan {
		t.Fatalf("expected the unmapped UUID passed through, got %v", got)
	}
}

func TestResolveAllowIDs_NoProfilePassesThrough(t *testing.T) {
	// Point at a dir with no prefs.js so profile resolution fails; ids must still
	// pass through (allowing an extension not installed locally yet is valid).
	t.Setenv("GUSSET_PROFILE", t.TempDir())
	got := resolveAllowIDs([]string{"uBlock0@raymondhill.net"})
	if len(got) != 1 || got[0] != "uBlock0@raymondhill.net" {
		t.Fatalf("expected pass-through without a readable profile, got %v", got)
	}
}
