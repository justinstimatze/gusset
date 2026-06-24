// Package profile resolves the on-disk location of the active Firefox profile
// and the data gusset needs from it. All facts encoded here were verified
// against a live profile — see docs/firefox-internals-verified.md.
//
// The OS- and install-method-specific differences (snap vs flatpak vs plain on
// Linux; macOS) are confined to FirefoxRoot. Everything downstream operates on
// a resolved profile directory and is OS-agnostic.
package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// firefoxRootCandidates returns the candidate Firefox profile roots for the
// current OS, in probe order. On Linux the order matters: an Ubuntu-default
// install is a snap and its profile lives under the snap's confined HOME, not
// ~/.mozilla — so snap is probed first, then flatpak, then a plain
// distro/tarball/.deb install.
func firefoxRootCandidates(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "Firefox"),
		}
	case "windows":
		// Firefox keeps the profile root under %APPDATA% (roaming), which is
		// <home>\AppData\Roaming on a standard install.
		return []string{
			filepath.Join(home, "AppData", "Roaming", "Mozilla", "Firefox"),
		}
	default: // linux and other unixes
		return []string{
			filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
			filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"),
			filepath.Join(home, ".mozilla", "firefox"),
		}
	}
}

// FirefoxRoot returns the first candidate Firefox profile root that exists and
// contains a profiles.ini. It does not assume a particular install method.
func FirefoxRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	candidates := firefoxRootCandidates(home)
	for _, root := range candidates {
		if _, err := os.Stat(filepath.Join(root, "profiles.ini")); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("no Firefox profiles.ini found under any of: %s", strings.Join(candidates, ", "))
}

// iniProfile is one [ProfileN] section of profiles.ini.
type iniProfile struct {
	name       string
	path       string
	isRelative bool
	isDefault  bool
}

// DefaultProfileDir parses root/profiles.ini and returns the absolute path of
// the active profile directory. Resolution order matches Firefox's own:
//  1. an [InstallXXXX] section's Default= key (the per-install default), else
//  2. a [ProfileN] section with Default=1, else
//  3. the sole profile if there is exactly one.
func DefaultProfileDir(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "profiles.ini"))
	if err != nil {
		return "", fmt.Errorf("reading profiles.ini: %w", err)
	}
	profiles, installDefault := parseProfilesINI(string(data))
	if len(profiles) == 0 {
		return "", fmt.Errorf("profiles.ini in %s declares no profiles", root)
	}

	// 1. Install-section default wins when present — it is keyed by Path.
	if installDefault != "" {
		for _, p := range profiles {
			if p.path == installDefault {
				return resolveProfilePath(root, p), nil
			}
		}
		// Install Default may be an absolute path not matching any Path key.
		if filepath.IsAbs(installDefault) {
			return installDefault, nil
		}
		return filepath.Join(root, installDefault), nil
	}

	// 2. A profile flagged Default=1.
	for _, p := range profiles {
		if p.isDefault {
			return resolveProfilePath(root, p), nil
		}
	}

	// 3. Exactly one profile.
	if len(profiles) == 1 {
		return resolveProfilePath(root, profiles[0]), nil
	}

	return "", fmt.Errorf("profiles.ini in %s lists %d profiles but none is marked default", root, len(profiles))
}

func resolveProfilePath(root string, p iniProfile) string {
	if !p.isRelative && filepath.IsAbs(p.path) {
		return p.path
	}
	return filepath.Join(root, p.path)
}

// parseProfilesINI returns the [ProfileN] sections and the Path value of any
// [Install*] section's Default= key (empty if none). profiles.ini is a small
// INI file; we parse it directly rather than pull in a dependency.
func parseProfilesINI(s string) (profiles []iniProfile, installDefault string) {
	var section string
	var cur *iniProfile
	flush := func() {
		if cur != nil {
			profiles = append(profiles, *cur)
			cur = nil
		}
	}
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			section = line[1 : len(line)-1]
			if strings.HasPrefix(section, "Profile") {
				cur = &iniProfile{name: section}
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch {
		case cur != nil:
			switch key {
			case "Path":
				cur.path = val
			case "IsRelative":
				cur.isRelative = val == "1"
			case "Default":
				cur.isDefault = val == "1"
			}
		case strings.HasPrefix(section, "Install") && key == "Default":
			installDefault = val
		}
	}
	flush()
	return profiles, installDefault
}

// uuidsPrefRe extracts the JSON-string value of the
// extensions.webextensions.uuids pref from prefs.js. The captured group is a
// double-quoted JSON-string literal (a JSON object with escaped inner quotes).
var uuidsPrefRe = regexp.MustCompile(`user_pref\("extensions\.webextensions\.uuids",\s*("(?:[^"\\]|\\.)*")\);`)

// ExtensionUUIDs reads prefs.js from a resolved profile directory and returns
// the map of stable extension ID -> per-install internal UUID. The on-disk
// storage path embeds the UUID, which differs per machine, so callers key on
// the stable extension ID and resolve the path locally via this map.
func ExtensionUUIDs(profileDir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(profileDir, "prefs.js"))
	if err != nil {
		return nil, fmt.Errorf("reading prefs.js: %w", err)
	}
	m, err := parseExtensionUUIDs(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing extensions.webextensions.uuids: %w", err)
	}
	return m, nil
}

func parseExtensionUUIDs(prefs string) (map[string]string, error) {
	match := uuidsPrefRe.FindStringSubmatch(prefs)
	if match == nil {
		// No extensions with assigned UUIDs is a valid empty state.
		return map[string]string{}, nil
	}
	// The pref value is a JSON string literal whose content is itself JSON.
	var inner string
	if err := json.Unmarshal([]byte(match[1]), &inner); err != nil {
		return nil, fmt.Errorf("unquoting pref value: %w", err)
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(inner), &m); err != nil {
		return nil, fmt.Errorf("decoding uuid map: %w", err)
	}
	return m, nil
}
