// Command gusset syncs Firefox extension settings (storage.local) across
// machines — the seam Firefox Sync leaves open. See docs/design.md for the design.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"sort"

	"github.com/justinstimatze/gusset/internal/ffctl"
	"github.com/justinstimatze/gusset/internal/profile"
)

// version is overridden at release via
// -ldflags "-X main.version=$(git describe --tags --always --dirty)".
// Do not hand-edit; the git tag is the single source of truth.
var version = "dev"

// buildVersion resolves the version string, preferring the ldflags-baked value,
// then the module version from `go install ...@vX.Y.Z`, then the VCS revision
// from a local `go build`, then "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				rev = s.Value[:12]
			} else {
				rev = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		return rev + dirty
	}
	return version
}

func main() {
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "version":
		fmt.Println(buildVersion())
	case "doctor":
		err = doctor()
	case "status":
		err = statusCmd()
	case "ws-token":
		err = wsTokenCmd(args[1:])
	case "sync":
		err = syncCmd(args[1:])
	case "gen-passphrase":
		err = genPassphraseCmd(args[1:])
	case "init":
		err = initCmd(args[1:])
	case "allow":
		err = allowCmd(args[1:])
	case "disallow":
		err = disallowCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "gusset: unknown command %q\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gusset: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `gusset — sync Firefox extension settings across machines

usage:
  gusset version    print the build version
  gusset gen-passphrase  print a fresh strong passphrase to share across your devices
  gusset doctor     resolve the active Firefox profile and list installed extensions
  gusset init       create the config and a per-user salt (--salt to pair a device; --no-salt to skip)
  gusset allow      add extension IDs to the persisted sync allowlist
  gusset disallow   remove extension IDs from the allowlist
  gusset status     report sync status (peers and per-extension state, with reasons)
  gusset ws-token   print the localhost-WebSocket token to pair the companion extension
  gusset sync       sync allowlisted extensions with a LAN peer (on-demand; see --help)

  gusset sync flags:
    --extensions a,b      extension IDs to sync (the opt-in allowlist)
    --override a,b        force-enable sensitive (denylisted) extension IDs
    --force               take the peer's copy unconditionally, ignoring last-writer-wins
                          (seed a new machine to match an established one; one-shot)
    --for 10m             stay reachable for a bounded window, then exit (default 30s)
    --once                exit as soon as the local pull finishes, skipping the
                          reachable-back window (ideal for a one-way --force seed)
    --watch               stay reachable indefinitely (until Ctrl-C)
    --peer host:port      dial a peer directly, skipping discovery
    --listen host:port    bind a specific listen address (default :0, an OS-picked port)
    --profile dir         Firefox profile to sync (default: active; or GUSSET_PROFILE)
    --restart-firefox     close Firefox to apply, then relaunch it (closes your browser)
    --rendezvous-dir dir  reach peers off the LAN by trading sealed beacons through
                          a shared folder (Tier 1; e.g. a synced/Dropbox dir)
    --device-id id        stable id for this device in beacons (default: hostname)
    --stun host:port      STUN server; adds the public-IP beacon candidate and
                          enables the ICE NAT-hole-punch fallback
    --ws host:port        serve live status to the companion extension over a
                          loopback WebSocket (e.g. 127.0.0.1:8765); pair with
                          'gusset ws-token'
  passphrase comes from GUSSET_PASSPHRASE_FILE (a path) or GUSSET_PASSPHRASE.
`)
}

// doctor resolves the local Firefox profile and reports what gusset sees. It is
// read-only and the first dogfoodable exercise of internal/profile.
func doctor() error {
	dir := os.Getenv("GUSSET_PROFILE")
	if dir == "" {
		root, err := profile.FirefoxRoot()
		if err != nil {
			return err
		}
		fmt.Printf("firefox root:    %s\n", root)
		dir, err = profile.DefaultProfileDir(root)
		if err != nil {
			return err
		}
	}
	fmt.Printf("active profile:  %s\n", dir)

	switch st, pid, _ := ffctl.InspectLock(dir); st {
	case ffctl.LockedLive:
		fmt.Printf("profile lock:    held by running Firefox (pid %d) — close it before applying a sync\n", pid)
	case ffctl.LockedStale:
		fmt.Printf("profile lock:    stale (pid %d is not Firefox) — `gusset sync` will clear it\n", pid)
	case ffctl.Unlocked:
		fmt.Printf("profile lock:    none (Firefox not running)\n")
	default:
		fmt.Printf("profile lock:    present but unrecognized — left untouched\n")
	}

	uuids, err := profile.ExtensionUUIDs(dir)
	if err != nil {
		return err
	}
	fmt.Printf("extensions:      %d with a per-install UUID\n", len(uuids))

	ids := make([]string, 0, len(uuids))
	for id := range uuids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %-44s %s\n", id, uuids[id])
	}
	return nil
}
