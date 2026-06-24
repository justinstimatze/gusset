# Firefox internals — verified

Verified against a **live profile**, 2026-06-23. These are the load-bearing
internals [design.md](design.md) relies on; where reality differs from the
WebExtensions/Firefox assumptions the design started from, the delta is called
out as **DELTA** — those change the design.

## Test environment

- Firefox **152.0.1** (snap, `mozilla` publisher, rev 8521), **running** during
  all reads.
- OS: Linux. Single profile `n5mpphsf.default`, `Default=1` in `profiles.ini`.
- Real extension under test: **uBlock Origin** (`uBlock0@raymondhill.net`).

### DELTA 0 — this is a *snap* Firefox; the profile root is not `~/.mozilla`

Profile root is `~/snap/firefox/common/.mozilla/firefox/`, **not**
`~/.mozilla/firefox/`. `~/.mozilla/firefox/` does not exist here at all.

The design's cross-platform table treats "Linux" as one
path. It is not. The profile-root resolver must probe, in order:

1. `~/snap/firefox/common/.mozilla/firefox/` (snap — Ubuntu default since 22.04)
2. `~/.var/app/org.mozilla.firefox/.mozilla/firefox/` (flatpak)
3. `~/.mozilla/firefox/` (distro/tarball)

Pick the one that exists and contains a `profiles.ini`. Do **not** hardcode
`~/.mozilla`. (macOS is unaffected: `~/Library/Application Support/Firefox/`.)

---

## Assumption 1 — storage.local backend + on-disk path → CONFIRMED, with deltas

- IDB-backed sqlite is the active backend. Path shape confirmed:
  `<profile>/storage/default/moz-extension+++<UUID>.../idb/<mangled>.sqlite`.
- The global gate pref `extensions.webextensions.ExtensionStorageIDB.enabled`
  is **absent** → default-on in current Firefox. Per-extension migration is
  recorded by `extensions.webextensions.ExtensionStorageIDB.migrated.<ext-id> = true`;
  uBO has it `true`, so uBO is on IDB.
- Legacy JSON backend (`<profile>/browser-extension-data/<ext-id>/storage.js`)
  **also coexists**, but here only built-in `…@search.mozilla.org` add-ons use
  it. A real extension can still be JSON-backed if it has no `migrated.<id>`
  flag and only a `browser-extension-data/<id>/` dir — that's the easy case.

### DELTA 1a — there are TWO IDB databases per extension; pick the right one

Under uBO's UUID there are two origin directories, each with its own IDB db:

| origin dir | IDB database | size | what it is |
|---|---|---|---|
| `moz-extension+++<UUID>^userContextId=4294967295/` | `webExtensions-storage-local` | 2.0 MB | **storage.local — the settings blob we want** |
| `moz-extension+++<UUID>/` (no suffix) | `uBlock0CacheStorage` | 647 KB | uBO's compiled-filter cache — **regenerable, do NOT sync** |

The real `storage.local` lives under the **`^userContextId=4294967295`** origin
(4294967295 = 0xFFFFFFFF = the default/no-container context). Syncing the
no-suffix `…CacheStorage` db would ship megabytes of regenerable cache. The
store backend must target the `webExtensions-storage-local` db specifically, not
"all idb/*.sqlite under the UUID".

(The on-disk db-dir names are Firefox-mangled: the database name is interleaved
forward/reversed. `…wleabcEoxlt-eengsairo` → `webExtensions-storage-local`,
`…ueBglaorcokt0SCeahc` → `uBlock0CacheStorage`. Don't parse the mangled name —
open each candidate sqlite and read `SELECT name FROM database` to identify it.)

### DELTA 1b — large values are stored OUT-OF-LINE as external files

`object_data` schema: `(object_store_id, key BLOB, index_data_values, file_ids
TEXT, data BLOB)`. uBO's store: 42 keys, of which **10 reference external files**
via the `file_ids` column → the blobs in the sibling `<mangled>.sqlite.files/`
directory (named `5729`, `11872`, …). There is also a `file(id, refcount)` table.

**A blob-level snapshot must capture `<db>.sqlite` + `<db>.sqlite.files/` as one
atomic unit.** The sqlite alone is an incomplete store. This also means CDC
chunks two things (db file + N external files), not one.

### DELTA 1c — values AND keys are encoded; v1 "don't parse" is vindicated

- `data` BLOBs are opaque binary (Snappy-framed StructuredClone), e.g.
  `103C0300…`, `D01F0403…`. Not JSON. Confirms the v1 decision to never parse.
- Even the **keys** are encoded: IDB string-key encoding shifts each byte +1
  (so 0x00 can terminate). `X'30626D6D70786665536672…'` → "0bmmpxfeSfrvft…" →
  decode −1 → `allowedRequestCount`. Recoverable, but more decoder surface that
  blob-level sync sidesteps entirely. (All of this is the v2 StructuredClone gate.)

---

## Assumption 2 — per-install random UUID → CONFIRMED exactly

`prefs.js` holds `extensions.webextensions.uuids` as a JSON string: a map of
**stable extension id → per-install UUID**. Sample (this machine):

```
"uBlock0@raymondhill.net": "ba8ff762-346e-471a-8c1b-60abaa0cc23b"
"{d634138d-c276-4fc8-924b-40a0ea21d284}": "ca234034-7bff-4055-9abf-20d825c01d8a"
```

The uBO UUID matches its `storage/default/moz-extension+++ba8ff762…` dir. So the
flow is confirmed: key on the **stable ext-id**, look up the local UUID in this
pref, resolve the on-disk path. Parse the value as JSON (it's a JSON string
inside the pref string — double-decode).

### DELTA 2 — re-homing must rewrite the UUID in THREE places, not just the dirname

The per-install UUID is embedded redundantly. To drop machine A's blob onto
machine B (different UUID), rewrite **all three**:

1. The origin **directory name** `moz-extension+++<UUID>^userContextId=…`.
2. The **`.metadata-v2`** file at the origin root — contains the origin string
   `…^userContextId=4294967295\x00=ba8ff762-…` (QuotaManager origin record).
3. The sqlite **`database.origin`** column:
   `moz-extension://ba8ff762-…^userContextId=4294967295`.

Miss any one and Firefox's QuotaManager will reject or orphan the store. This is
the concrete shape of the "UUID resolution on the other side" the design calls
for — it belongs in `internal/store/firefox.go`.

---

## Assumption 3 — storage.sync engine + quota → backing store CONFIRMED

- `<profile>/storage-sync-v2.sqlite` exists (229 KB). Tables: `storage_sync_data`
  `(ext_id TEXT PK, data TEXT /* JSON payload, NULL=tombstone */,
  sync_change_counter)`, `storage_sync_mirror`, `meta`.
- **The companion-extension manifests land here as JSON TEXT** keyed by ext_id —
  good: the courier writes plain JSON via the supported `storage.sync` API, and
  we never touch this sqlite directly (the design's rejected fragile path stays
  rejected).
- Quota constants (~100 KB/8 KB/512 items) were NOT independently re-derived from
  source here — they're not in prefs. Treat as still-to-confirm against
  `dom/storage` source, but they don't block: a manifest is a few KB regardless.

### DELTA 3 — Sync precondition CONFIRMED live (2026-06-23, post-signin)

After signing into a Firefox Account on this machine (device "rukh"), the
control-plane substrate is empirically confirmed via the sync log
(`weave/logs/`):

- `services.sync.username` set, `services.sync.lastSync` populated,
  `declinedEngines=""` (nothing opted out).
- The **`extension-storage` engine ran a full cycle**: constructed → "Got a
  bridged engine!" (Rust bridged engine) → 0 applied / 0 failed / 0 outgoing
  (nothing to carry until the courier exists — expected).
- It is enrolled **server-side** in `meta/global`:
  `"extension-storage":{"version":1,"syncID":"…"}`. So `storage.sync` writes
  from our courier will be carried by this engine across devices.
- Non-issue noted: `Sync.Doctor … Skipping check of extension-storage - disabled
  via preferences` is the health-*check* opting out, NOT the engine — the engine
  clearly ran.

**Remaining gate (user-state, not architecture):** the FxA account was still
`UNVERIFIED_ACCOUNT` in the log (every ERROR line is this) until the account
email is verified. Sync engines run regardless, but verify the email to clear
the error state and ensure reliable cross-device sync.

---

## Assumption 4 — native messaging host registration → CONFIRMED + snap delta

No host manifest dirs exist yet (nothing installed — expected). The standard
Linux user path is `~/.mozilla/native-messaging-hosts/`.

### DELTA 4 — snap confinement relocates the host dir AND constrains the binary

Because this Firefox is a snap, `$HOME` inside the sandbox is
`~/snap/firefox/common`, so Firefox looks for native-messaging-host manifests at
**`~/snap/firefox/common/.mozilla/native-messaging-hosts/`**, not
`~/.mozilla/native-messaging-hosts/`. The install helper must detect snap/flatpak
and write to the matching confined location:

- snap:    `~/snap/firefox/common/.mozilla/native-messaging-hosts/`
- flatpak: `~/.var/app/org.mozilla.firefox/.mozilla/native-messaging-hosts/`
- plain:   `~/.mozilla/native-messaging-hosts/`

**The risk if we use native messaging:** when Firefox launches a native-messaging
host, the host binary runs *inside* Firefox's snap AppArmor confinement (`snap
connections firefox` shows `home`, `dot-mozilla-firefox`, `browser-support`
plugs). So the daemon-as-host would inherit a restricted filesystem view and may
not reach a git repo / config / socket outside the snap world. Known
friction point; weekend-eating to debug.

### DECISION — avoid native messaging; use a localhost socket instead

> **Superseded for v1** by docs/transport-and-security.md §8: v1 has no
> persistent daemon and no companion extension on the data path. It is the
> on-demand `gusset sync` binary, discovered on the LAN by mDNS. The
> user-service daemon + localhost-WS-to-extension below return only at Tier 1
> (cross-network) and as an opt-in for set-and-forget. The native-messaging
> snap-path analysis here still applies *if* that optional fallback is built.

Don't make native messaging the daemon↔extension channel. Instead:

- Run the daemon as a normal **user service** (systemd `--user` / launchd),
  fully unconfined. It reads `storage.local` off disk directly — no browser
  involvement on the data path.
- The companion extension reaches the daemon via **`fetch`/WebSocket to
  `127.0.0.1:<port>`** (extension needs a `127.0.0.1` host permission). Firefox
  launches no binary, so snap confinement never applies — identical on snap,
  flatpak, deb, and macOS.

How little crosses that channel reinforces the choice: the daemon can **read**
peer manifests itself by opening `storage-sync-v2.sqlite` read-only (the same
`?immutable=1` read used above). The only thing it needs the extension for is
**publishing** its own manifest to `storage.sync` (only the extension can write
there) — a low-frequency, few-KB signal, trivial over localhost WS.

Tradeoff: localhost port/discovery handling + a `127.0.0.1` host permission,
versus uniform behavior across all install types. Keep native messaging as an
optional fallback, not the default. This demotes DELTA 4 from a snap-exec risk to
"pick a port." (The install-path table above still matters only for the optional
native-messaging fallback.)

---

## Assumption 5 — live consistent snapshot → CONFIRMED (with a WAL caveat)

Read uBO's `storage.local` sqlite successfully **while Firefox held it open**,
using `sqlite3 'file:<path>?immutable=1'` — full schema + 42 rows, no lock
error, no browser disruption.

### Caveat — `immutable=1` ignores the `-wal`

`immutable=1` reads the main db file and **skips any `-wal`**, so uncommitted
WAL changes are invisible (could read slightly stale data). Here the `-wal` was
0 bytes (checkpointed), so it was safe. For a guaranteed-consistent point-in-time
read, use the **sqlite online backup API** (`VACUUM INTO` or the backup
interface) against the live db — that respects WAL and produces a clean
standalone copy to chunk. Plan on backup-API, not `immutable=1`, for the real
snapshot path. Don't forget to snapshot the `.files/` dir at the same instant
(DELTA 1b).

---

## macOS — UNVERIFIED (no Mac on hand; verified facts are Linux-only)

Everything above was verified against a **live Linux (snap) profile**. The macOS
support added to `internal/profile` and `internal/ffctl` is built from Mozilla
source knowledge and cross-compiles clean (`GOOS=darwin go build ./...`), but has
**not** been run against a live macOS Firefox. What is and isn't certain:

- **Profile root** — `~/Library/Application Support/Firefox/` is well-established
  and already the darwin branch in `profile.firefoxRootCandidates`. Confidence:
  high. `profiles.ini`, `prefs.js`, the `extensions.webextensions.uuids` pref, and
  the `storage/default/moz-extension+++<UUID>…` layout are cross-platform Gecko
  internals, not OS-specific — the same code resolves them.
- **Process identity** (`ffctl.processStrings`) — `/proc` does not exist on macOS,
  so the darwin build shells out to `ps -p <pid> -o comm=,command=`. Confidence:
  high (POSIX `ps`), but the exact column output should be eyeballed once on a Mac.
- **Profile lock** — this is the one genuinely unverified fact. On Linux the lock
  is a `lock` **symlink** with target `<ip>:+<pid>` (nsProfileLock). macOS Firefox
  may instead (or additionally) use a `.parentlock` **fcntl** lock with no
  parseable symlink. `ffctl` reads only the `lock` symlink, so the macOS outcomes
  are: (a) if a `lock` symlink exists, `parseLockPID` takes the PID after the last
  `+` and works regardless of whether the prefix is an IP or a MAC; (b) if there is
  no `lock` symlink, `os.Readlink` returns ENOENT → "not running" → `Stop` is a
  no-op and `--restart-firefox` degrades to "please close Firefox yourself",
  identical to not passing the flag. **Both outcomes are safe** — the failure mode
  is reduced convenience, never a wrong SIGTERM (the `looksLikeFirefox` guard still
  gates every signal). The open question for a live-Mac check: *does macOS Firefox
  write a parseable `lock` symlink?* If not and auto-restart on macOS is wanted, add
  a `.parentlock`-aware holder lookup to the darwin build.
  - **Data-safety guard (`store.Apply`) covers both mechanisms.** Apply must never
    write under a live browser, so `profileLocked` refuses when *either* the `lock`
    symlink classifies as live/unparseable (`ffctl.InspectLock`) *or* a live process
    holds an fcntl lock on `.parentlock` (a `F_GETLK` probe — query-only, so a
    lingering `.parentlock` no process holds never causes a false refusal). Whichever
    primitive a given Firefox build uses, one of the two catches it. This is verified
    on Linux (the symlink path; the fcntl probe reads negative there because snap
    Firefox does not POSIX-lock `.parentlock`) and is the best-effort macOS path until
    a live-Mac check confirms which primitive macOS uses.
- **Relaunch binary** (`ffctl.defaultFirefoxBinary`) — darwin defaults to
  `/Applications/Firefox.app/Contents/MacOS/firefox` (the inner binary forwards
  `--profile`/`--new-instance`, unlike `open -a Firefox`). Overridable with
  `--firefox-bin` for non-standard install locations.

## Net effect on the package layout

- `internal/profile/` — profile-root probe must handle snap/flatpak/plain (DELTA
  0); native-host install path is confinement-dependent (DELTA 4).
- `internal/store/firefox.go` — must (a) select the `webExtensions-storage-local`
  db, not the cache db (DELTA 1a); (b) snapshot sqlite **+ `.files/`** together
  (DELTA 1b); (c) snapshot via backup API, not immutable (Assumption 5 caveat);
  (d) on apply, rewrite the UUID in dirname + `.metadata-v2` + `database.origin`
  (DELTA 2).
- `extension/` courier — writes small JSON to `storage_sync_data`; precondition
  is an active Sync account (DELTA 3). Talks to the daemon over localhost
  WebSocket, not native messaging (DELTA 4 decision).
- daemon↔extension channel — **localhost WS server in the daemon**, not a
  `native-host/` package, as the default. A native-messaging host stays a
  snap/flatpak-aware optional fallback only (DELTA 4 decision).
