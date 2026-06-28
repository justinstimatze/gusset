# gusset

Firefox Sync brings your add-ons back on a new machine — but not the settings
*inside* them. uBlock Origin returns with default filters; every rule you wrote
is gone. **gusset syncs the settings your Firefox extensions keep in
`storage.local` directly between your own machines** — no account, no server, no
company in the middle.

## Quick start

On **each** machine you want to sync (Linux, macOS, or Windows):

**1. Install the app** — the small program that does the syncing:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/justinstimatze/gusset/main/install.sh | sh
# Windows (PowerShell)
irm https://raw.githubusercontent.com/justinstimatze/gusset/main/install.ps1 | iex
```

**2. Install the extension** — open this **in Firefox**:
[**install the gusset extension →**](https://github.com/justinstimatze/gusset/releases/latest/download/gusset.xpi)
Firefox keeps it updated automatically from future signed releases.

**3. Run the guided setup** and follow along:

```sh
gusset setup
```

`gusset setup` walks you through the rest — set a passphrase, pick which
extensions to sync, run the first sync — and the extension shows the same steps
in its popup, so the terminal and the toolbar guide you together. The full
written walkthrough is [just below](#set-up-two-machines). Everything after that
is optional reading.

## Set up two machines

The whole journey, laptop to desktop, sharing uBlock Origin's settings. (This is
the same sequence `gusset setup` prints — keep it open if you like, or just
follow the terminal.)

**Step 1 — install the app, on both machines** (Windows/by-hand options in
[How it works](#installing-by-hand)):

```sh
curl -fsSL https://raw.githubusercontent.com/justinstimatze/gusset/main/install.sh | sh
```

**Step 2 — create the config, on both machines:**

```sh
gusset init
```

(Bringing your own, possibly weak passphrase instead of a generated one? Use
`gusset init --with-salt` on the first machine and run the `--salt …` line it
prints on the others — it adds a per-user salt that protects a weak phrase.)

**Step 3 — set the shared passphrase.** This is the *only* thing you carry
between machines:

```sh
gusset passphrase new    # FIRST machine: generates + stores it; copy the 8 words it prints
gusset passphrase set    # OTHER machines: paste those same 8 words (hidden prompt)
```

**Step 4 — choose what to sync, on both machines:**

```sh
gusset doctor                          # lists your installed extensions
gusset allow uBlock0@raymondhill.net   # opt one in (the allowlist is empty by default)
```

**Step 5 — install the extension, on both machines.** Open this **in Firefox**:
[install the gusset extension →](https://github.com/justinstimatze/gusset/releases/latest/download/gusset.xpi).
It shows live sync status and pairs to the app with the token from
`gusset ws-token`.

**Step 6 — sync.** On the same WiFi the two find each other automatically:

```sh
gusset sync --for 2m        # the machine whose settings are already right: stay reachable
gusset sync --force --once  # the new machine: seed it from the other, one time
```

The settings apply and uBlock Origin on the second machine comes up carrying your
filters. After this first seed, drop `--force`: ongoing syncs reconcile by
last-writer-wins, so whichever machine you changed most recently wins. Applying
needs Firefox closed on the receiving end — `gusset sync --restart-firefox`
closes it, applies, and relaunches it for you.

**Off the same network?** Point both at one shared folder — anything that already
moves files between them (Dropbox, Syncthing, …):

```sh
gusset sync --rendezvous-dir ~/Dropbox/gusset --for 2m
```

Each side drops a sealed beacon there listing where it can be reached, then dials
the other. The folder only ever holds ciphertext; it learns nothing.

The passphrase is the only shared secret and never leaves your machines — it is
not written into the config and not sent anywhere. `gusset passphrase` stores it
at `<config-dir>/passphrase` with `0600` perms (override with
`GUSSET_PASSPHRASE_FILE`). The same passphrase on every device is what makes them
one identity; a mismatch is rejected as an unauthenticated peer.

## Command reference

```sh
gusset setup          # guided, state-aware first-time setup (start here)
gusset doctor         # resolve the active Firefox profile, list installed extensions (read-only)
gusset init           # create the config (passphrase-only; --with-salt adds a per-user salt)
gusset passphrase new # generate + store the shared passphrase, and print it once
gusset passphrase set # store a passphrase you already have (paste it at a hidden prompt)
gusset allow ID       # opt an extension into syncing (the allowlist is empty by default)
gusset status         # show peers and per-extension sync state, with reasons
gusset ws-token       # print the loopback-WebSocket token to pair the companion extension
gusset sync           # sync allowlisted extensions with a peer (see `gusset sync --help`)
```

## How it works

You do not need any of this to use gusset — the [Quick start](#quick-start) is
the whole story. This is for the curious and the cautious.

**No account, no server.** Your machines find each other and exchange the bytes
themselves, encrypted the whole way with a key that exists only on your devices.
There is no company in the middle holding your configuration. That is the correct
number of third parties: zero. (A gusset is the small piece sewn in where two
parts meet, so the seam does not split — the name is the design.)

**What moves, and how.** gusset snapshots the data an allowlisted extension keeps
in `storage.local` (with UUID re-homing so a re-installed add-on still matches),
chunks and encrypts it, and hands it to a device-to-device transport secured by
mutual TLS derived from your passphrase. On the same network the machines find
each other by mDNS; across networks they trade sealed beacons through any folder
that already syncs between them (`--rendezvous-dir`), and if direct routes fail
and `--stun` is set they punch through the NAT (ICE) and reconcile over the hole.

<a name="installing-by-hand"></a>
**Installing by hand.** The one-liner downloads the prebuilt binary for your OS
and architecture, verifies its SHA-256 (and, with the GitHub CLI present, its
SLSA build-provenance attestation), and installs `gusset`. Prefer to do it
yourself? Grab the archive from the
[latest release](https://github.com/justinstimatze/gusset/releases/latest),
unpack it, and put `gusset` on your `PATH` — Linux, macOS, and Windows, Intel and
ARM. The signed `.xpi` is attached to the same release.

**Status — early, and working.** The CLI is the proven core, verified end-to-end
on Linux and Windows: a real Linux→Windows sync applied an extension's settings
and they showed up in uBlock Origin's own interface. macOS builds and passes CI
but has not yet been run against a live browser — this README will say so until
it has. The companion extension's status UI and daemon link are dogfood-tested;
the `storage.sync` beacon carrier is the experimental edge. Still to come: a
relay for the symmetric-NAT pairs hole-punching cannot cross. See
[docs/design.md](docs/design.md) for the rationale,
[docs/firefox-internals-verified.md](docs/firefox-internals-verified.md) for the
internals verified against a real profile, and [TESTING.md](TESTING.md) for a
staged, most-proven-path-first quickstart.

## Privacy

gusset needs no account and no server, and keeps your settings — and who syncs
with whom — out of any third party's hands. Carriers (a shared folder, or Firefox
Sync) only ever see sealed ciphertext; the settings move device-to-device. There
is no telemetry, and the passphrase is never logged or sent.

Two things worth knowing honestly:

- On a local network, gusset announces itself over mDNS so your devices can find
  each other — a LAN observer can see that *a* gusset device is present, by an
  **opaque random id, not your hostname**. Through Firefox Sync, your own account
  can see that gusset is installed and how many devices you have; the beacon
  values stay encrypted (Mozilla cannot read them).
- `--stun` is opt-in. Enabling it lets the STUN server you name learn your public
  IP; to sync across networks with **no** third party, use a shared folder
  (`--rendezvous-dir`) instead — only sealed ciphertext goes there.

Full detail in [SECURITY.md](SECURITY.md) and
[docs/transport-and-security.md](docs/transport-and-security.md).

## Build from source

You do not need this — the installer pulls a prebuilt binary. This is for
contributors and platforms without a release archive.

```sh
make build      # ./gusset, version stamped from the git tag
make test       # go test -race ./...
make lint       # golangci-lint
```

## License

MIT — see [LICENSE](LICENSE). Take it, use it, change it. It is yours now.

Contact: justin@justinstimatze.com
