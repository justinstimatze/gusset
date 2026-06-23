// Command gusset syncs Firefox extension settings (storage.local) across
// machines — the seam Firefox Sync leaves open. See HANDOFF.md for the design.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"sort"

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
	case "sync":
		err = syncCmd(args[1:])
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
  gusset doctor     resolve the active Firefox profile and list installed extensions
  gusset status     report sync status (peers and per-extension state, with reasons)
  gusset sync       sync allowlisted extensions with a LAN peer (on-demand; see --help)

  gusset sync flags:
    --extensions a,b   extension IDs to sync (the opt-in allowlist)
    --override a,b     force-enable sensitive (denylisted) extension IDs
    --for 10m          stay reachable for a bounded window, then exit (default 30s)
    --watch            stay reachable indefinitely (until Ctrl-C)
    --peer host:port   dial a peer directly, skipping mDNS discovery
  passphrase comes from GUSSET_PASSPHRASE_FILE (a path) or GUSSET_PASSPHRASE.
`)
}

// doctor resolves the local Firefox profile and reports what gusset sees. It is
// read-only and the first dogfoodable exercise of internal/profile.
func doctor() error {
	root, err := profile.FirefoxRoot()
	if err != nil {
		return err
	}
	fmt.Printf("firefox root:    %s\n", root)

	dir, err := profile.DefaultProfileDir(root)
	if err != nil {
		return err
	}
	fmt.Printf("active profile:  %s\n", dir)

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
