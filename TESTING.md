# Testing gusset

Thanks for trying gusset. It syncs a Firefox extension's settings (the data an
extension keeps in `storage.local`) between your own machines — the seam Firefox
Sync leaves open. This is **early software**; the tiers below go from "well
tested" to "experimental," so start at the top.

> **Back up first.** gusset's "apply" step rewrites an extension's on-disk store.
> It stages changes, keeps a backup until the swap succeeds, and **refuses to
> write while Firefox is running** — but this is a test build pointed at your
> real data, so copy your Firefox profile folder somewhere safe before you start.
> Find it with `gusset doctor`. Apply detects a running Firefox two ways (the
> profile `lock` symlink and an fcntl lock on `.parentlock`), which covers both
> Linux, macOS, and Windows. The guard is verified against a live browser on
> Linux; on macOS and Windows the *refuse-while-running* path hasn't been
> exercised yet (the Windows dogfood applied with Firefox closed), so close
> Firefox yourself on those two to be safe.

## Install

**Linux / macOS:**

```sh
curl -fsSL https://raw.githubusercontent.com/justinstimatze/gusset/main/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/justinstimatze/gusset/main/install.ps1 | iex
```

Each downloads the latest release for your OS/arch, verifies its SHA-256 against
the release `checksums.txt` (and, if you have the GitHub CLI, its SLSA
build-provenance attestation), and installs the `gusset` binary. *No release is
tagged yet* — until the first one lands the installer prints a note and you build
from source (below); it starts working the moment a release is published.

## What you need

- Two machines (Linux, macOS, or Windows), **on the same WiFi** for the simplest path.
- Either the installer above, or Go 1.26+ to build from source (`make build`).
- The **same passphrase on both machines** — this is the only shared secret.
  Generate a strong one with `./gusset gen-passphrase` and copy the *same* output
  to both machines. gusset derives all keys from it; there is no account and no
  server.

## 1. Build and set up (both machines)

```sh
make build                      # produces ./gusset
./gusset gen-passphrase         # on ONE machine; copy the same output to both
./gusset init --no-salt         # creates the config (no salt to copy around)
# put that SAME passphrase in the file init points you at, then:
chmod 600 ~/.config/gusset/passphrase
./gusset doctor                 # confirm it finds your Firefox profile + extensions
./gusset allow uBlock0@raymondhill.net   # opt in the extension(s) you want synced
```

`init` also gives this machine a name (your hostname) and a unique id, so the two
machines never get confused even if they share a hostname.

## Tier 1 — same-WiFi sync (the most-tested path)

On **both** machines, on the same network:

```sh
./gusset sync --for 2m
```

They find each other over mDNS, authenticate with the shared passphrase, and
reconcile. To apply incoming settings the receiver's Firefox must be closed —
either close it and re-run, or let gusset do it:

```sh
./gusset sync --for 2m --restart-firefox   # closes Firefox, applies, reopens it
```

Change a uBO setting on machine A, sync, and check it on B. That's the core.

## Tier 2 — across networks via a shared folder

Not on the same WiFi? Point both machines at one folder that already syncs
between them (Dropbox, Syncthing, etc.):

```sh
./gusset sync --rendezvous-dir ~/Dropbox/gusset --for 2m
```

Only sealed, opaque blobs ever touch the folder.

## Testing on one machine, with two Firefox profiles

No second machine? Simulate two "devices" with two Firefox profiles and two
gusset configs on the same box.

1. Make a second profile and put a different uBO config in it:
   ```sh
   firefox -P                      # Profile Manager → create e.g. "gusset-test"
   ```
   Open it once, install uBlock Origin, tweak a setting, then **close Firefox
   completely** (both profiles — apply needs Firefox not running). Find each
   profile's directory with `GUSSET_PROFILE= gusset doctor` or in `about:profiles`.

2. Two configs, same passphrase, distinct device ids (generated automatically):
   ```sh
   PASS="$(./gusset gen-passphrase)"        # the same secret for both

   for d in A B; do
     GUSSET_CONFIG_DIR=/tmp/gusset-$d ./gusset init --no-salt --device-name "Device$d"
     printf '%s' "$PASS" > /tmp/gusset-$d/passphrase && chmod 600 /tmp/gusset-$d/passphrase
     GUSSET_CONFIG_DIR=/tmp/gusset-$d ./gusset allow uBlock0@raymondhill.net
   done
   ```

3. With **Firefox closed**, run both — A serves on a fixed port, B dials it
   directly (no mDNS needed on one box):
   ```sh
   # terminal 1 — Device A (its profile is the newer one you want to propagate)
   GUSSET_CONFIG_DIR=/tmp/gusset-A GUSSET_PROFILE=/path/to/profileA \
     ./gusset sync --listen 127.0.0.1:9001 --for 60s

   # terminal 2 — Device B pulls from A
   GUSSET_CONFIG_DIR=/tmp/gusset-B GUSSET_PROFILE=/path/to/profileB \
     ./gusset sync --peer 127.0.0.1:9001 --for 60s
   ```
   Reconcile is last-writer-wins by timestamp, so the profile you edited most
   recently wins. Open profile B → uBO should now show Device A's setting.

## Tier 3 — the companion extension (status UI; experimental)

The Firefox extension shows live sync status and (experimentally) can carry the
cross-network rendezvous over Firefox Sync's `storage.sync`.

1. Run the daemon with the local status socket on:
   ```sh
   ./gusset sync --watch --ws 127.0.0.1:8765
   ./gusset ws-token            # prints the pairing token
   ```
2. Load the extension in Firefox (temporary — it goes away on restart, which is
   fine for testing): open `about:debugging#/runtime/this-firefox` →
   **Load Temporary Add-on** → pick `extension/.output/firefox-mv3/manifest.json`
   (build it first with `cd extension && npm install && npm run build:firefox`).
3. Click the gusset toolbar icon → paste `ws://127.0.0.1:8765` and the token →
   **Save & connect**. You should see "Connected" and your devices.
4. **Experimental — extension as the rendezvous carrier:** if both machines are
   signed into the same Firefox account (so `storage.sync` actually syncs), the
   extension can replace the shared folder. Run `gusset sync --watch --ws …`
   *without* `--rendezvous-dir`. This path is the least tested — expect rough
   edges, and fall back to Tier 1/2 if it doesn't connect.

## Reporting problems

The dashboard's **Activity** log (and `gusset status`) shows what happened, kept
locally only — never synced. When something doesn't work, that log plus the
terminal output is the most useful thing to send. It never contains your
passphrase, tokens, or any extension data — just events and counts.

## Honest status

- **Tested:** same-WiFi and shared-folder CLI sync, the snapshot/apply round-trip
  on a real uBO store, NAT hole-punching (in simulation).
- **Experimental:** the companion extension end-to-end, and the `storage.sync`
  carrier. Symmetric-NAT pairs that can't hole-punch aren't covered yet.

Found a bug or a confusing step? That's exactly what this round is for — say so.
