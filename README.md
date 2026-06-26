# gusset

A small daemon that syncs **Firefox extension settings** (the data extensions
keep in `storage.local`) across your machines — the one thing Firefox Sync does
*not* carry. Firefox Sync already moves bookmarks, history, logins, open tabs,
the *list* of installed add-ons, and an allowlist of prefs. It does not move an
extension's own stored settings, so a fresh Firefox reinstalls uBlock Origin but
leaves it with default filters. gusset fills exactly that seam.

The name is the metaphor: a gusset is the small inserted piece that joins two
parts and keeps the seam from failing. gusset joins your machines at the one
joint Firefox leaves open.

Target: **Firefox first**, on **Linux, macOS, and Windows**. Linux and Windows
are both verified against a live browser — a real Linux→Windows sync applied an
extension's settings and they rendered in uBlock Origin's own UI. macOS builds
and passes CI but isn't yet dogfood-tested against a live browser. See
[docs/design.md](docs/design.md) for the design
rationale and [docs/firefox-internals-verified.md](docs/firefox-internals-verified.md)
for the load-bearing internals, verified against a live profile.

## Status

Early but working. The full local pipeline is in place — profile resolver, store
snapshot+apply (UUID re-homing), policy allowlist, passphrase crypto,
content-defined chunking, and the device-to-device transport over
passphrase-derived mutual TLS. `gusset sync` syncs allowlisted extensions between
two machines, finding each other by mDNS on the same network, or — across
networks — by trading sealed beacons through a shared folder (`--rendezvous-dir`).
When no direct route works and `--stun` is set, it punches through NATs (ICE) and
reconciles over the punched connection. The CLI sync is verified end-to-end on
both Linux and Windows — a real Linux→Windows run applied an extension's settings
and they rendered in uBlock Origin's own UI. macOS builds and passes CI and
should behave the same, but the live-browser apply there isn't dogfood-tested yet.

**Trying it?** [TESTING.md](TESTING.md) is a staged quickstart (most-proven path
first). The CLI sync is the well-tested core; the companion extension end-to-end
is still experimental.

The companion WebExtension under [`extension/`](extension/) carries beacons over
Firefox Sync's `storage.sync` and hosts the status UI — a toolbar popup plus a
dashboard with the per-extension × per-device sync grid (the daemon serves it
over `gusset sync --ws`). Still to come: a relay for the symmetric-NAT pairs
hole-punching can't cross. See [docs/design.md](docs/design.md) for the design.

## Install

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/justinstimatze/gusset/main/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/justinstimatze/gusset/main/install.ps1 | iex
```

Each downloads the latest release for your OS/arch, verifies its SHA-256 (and,
with the GitHub CLI installed, its SLSA build-provenance attestation), and
installs the `gusset` binary. Until the first release is tagged, build from
source instead — the installer says so and points you here.

## Build

```sh
make build      # ./gusset, version stamped from the git tag
make test       # go test -race ./...
make lint       # golangci-lint
```

## Usage

```sh
gusset version        # build version
gusset gen-passphrase # print a strong passphrase to share across your devices
gusset doctor         # resolve the active Firefox profile, list installed extensions
gusset init           # create the config and a per-user salt (prints a command to pair other devices)
gusset allow ID       # opt an extension into syncing (the allowlist is empty by default)
gusset status         # show peers and per-extension sync state, with reasons
gusset sync           # sync allowlisted extensions with a peer (see `gusset sync --help`)
```

`doctor` is read-only — it touches nothing, and is the quickest way to confirm
gusset can find your profile.

A passphrase is the only shared secret. Put your 8 words in a `0600` file and
point gusset at it with `GUSSET_PASSPHRASE_FILE` (or the default
`<config-dir>/passphrase`); it is never written into the config. On each
machine:

```sh
gusset allow uBlock0@raymondhill.net      # opt in the extensions you want synced
gusset sync --for 2m                       # same WiFi: peers find each other by mDNS
```

To sync two machines that are not on the same LAN, point both at one shared
folder (anything that syncs files between them works):

```sh
gusset sync --rendezvous-dir ~/Dropbox/gusset --for 2m
```

Each side publishes a sealed beacon (its reachable endpoints) to the folder and
dials the other; the folder only ever holds opaque ciphertext. Applying incoming
settings needs Firefox closed on the receiver — `gusset sync --restart-firefox`
does that and relaunches it for you.

## License

MIT — see [LICENSE](LICENSE).

Contact: justin@justinstimatze.com
