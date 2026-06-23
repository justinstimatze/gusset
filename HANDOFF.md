# gusset — HANDOFF

Status: **design stage, no code yet.** This document is the spine. Read it
top to bottom before writing anything.

## What gusset is

A small daemon that syncs **Firefox extension settings** (the data extensions
keep in `storage.local`) across your machines — the one thing Firefox Sync
does *not* carry. Firefox Sync already syncs bookmarks, history, logins, open
tabs, the *list* of installed add-ons, and an allowlist of `about:config`
prefs. It does not sync an extension's own stored settings. So you sign into a
fresh Firefox, uBlock Origin reinstalls itself automatically, and then sits
there with default filters — your custom rules, whitelist, and dashboard
config did not come along. gusset fills exactly that seam and nothing else.

The name is the metaphor: a gusset is the small inserted piece that joins two
parts and keeps the seam from failing (sewing; also the load-carrying plate in
aircraft/bridge joints). gusset joins your machines at the one joint Firefox
leaves open.

Target: **Firefox first**, on **macOS and Linux**. Chrome is a possible later
backend but is explicitly out of scope for v1 (see "Why Firefox first").

## Why this can't be a normal extension (the constraint that shapes everything)

WebExtensions are sandboxed from each other. There is **no API for one
extension to read another extension's storage.** uBO's settings are readable
only by uBO. So a hypothetical "sync all my extensions" extension is
architecturally impossible — it would have no way to see any other extension's
data. (This is a good wall: it's what stops a sketchy extension from looting
your password manager. We do not want to break it.)

The only thing that can read another extension's storage is a program *outside*
the sandbox: a native app with disk access. That is why gusset is a native
daemon, not (only) an extension.

`storage.sync` exists and Firefox Sync *does* carry it — but it is an opt-in
API the extension itself must choose to use, and it is quota-capped (~100 KB
total per extension). uBO deliberately keeps its filter lists in
`storage.local`, not `storage.sync`, precisely because they blow past that
quota. So "just use storage.sync" is not available to us for the bulk data.

## Architecture: split the control plane from the data plane

The key design move. We do **not** sync the whole profile folder (heavyweight,
corruption-prone, drags along history/cookies/sessions you don't want moving).
Instead:

### Control plane = Firefox Sync itself

Ship a **tiny companion extension** whose only job is to hold *manifests* in
its own `storage.sync`. A manifest is small — for even a multi-megabyte uBO
store it's just a list of content-hashes plus metadata, a few KB, well under
the 100 KB quota. Firefox Sync then carries that manifest across your machines
for free: end-to-end encrypted, no server for us to run, no auth for us to
build, already running and already trusted by the user.

Each machine publishes its own state under its **own namespaced key**
(`manifest:<machine-id>:<extension-id>` → chunk-set + version + timestamp), and
only *reads* peers' keys to learn what to fetch. Because every machine owns its
own key, there is no write-conflict on the manifest store.

The companion extension is also the natural home for the **control-panel UI**:
pick which extensions to sync, see status, trigger a restore. It earns its keep
beyond being a manifest courier.

This also removes a fragility we considered and rejected: having the daemon
write directly into `storage-sync-v2.sqlite` (Firefox's storage.sync backing
store) out from under the browser. That's undocumented and racy. Going through
a companion extension means manifests are written via the *supported*
`storage.sync` API and Firefox handles syncing them. No fragile injection.

#### Be a good Firefox Sync citizen

Firefox Sync is a free, end-to-end-encrypted service Mozilla runs for users, and
gusset rides it for the control plane. We treat that as a courtesy to use
lightly, not a resource to exploit. Concretely, this is a design constraint, not
a nicety:

- **Never put bulk data on `storage.sync`.** Only tiny manifests (a few KB of
  content-hashes) ride Sync; the megabyte-scale chunks go through gusset's own
  transport. This is the whole point of the control-plane / data-plane split —
  keep it that way. Anything that would grow the `storage.sync` payload toward
  the ~100 KB quota is a smell.
- **Don't talk to Mozilla's servers directly, and don't force syncs.** gusset
  reads and writes only *local* state (the companion extension's `storage.sync`,
  surfaced locally in `storage-sync-v2.sqlite`) and lets Firefox sync on its own
  cadence. No polling Mozilla's sync endpoints, no programmatic "sync now" loops,
  no hammering. The daemon's network traffic is with *our* transport only.
- **Stay within the documented, supported surface.** Manifests go through the
  `storage.sync` API; we do not reverse-engineer or abuse Sync internals.

If a feature would make gusset a heavier guest on Mozilla's infrastructure,
that's a reason to redesign it, not to ship it.

### Data plane = content-addressed chunks, moved peer-to-peer

> **Superseded.** The original sketch (this section) sent chunks through a dumb
> store-and-forward substrate with **git as the default backend**. That was
> replaced after a privacy/security pass — see
> **docs/transport-and-security.md** for the authoritative design. Summary of
> the change: chunks now move **directly device-to-device** (v1: same-LAN
> direct, signaled through Firefox Sync; NAT traversal and relay are later
> tiers), encrypted with a key derived from a single 8-word passphrase. No
> transport account, no server holding data, no durable store. The chunking and
> manifest design below still stands; only the *transport* changed. The
> paragraphs below are kept for the chunking rationale and marked where they no
> longer hold.

The bulk chunks (the actual extension settings) move between the user's own
machines over a direct, encrypted connection. The transport is pluggable behind
an interface; the **v1 backend is Tier-0 same-LAN direct** (see the security
doc). ~~git is the default backend~~ — dropped.

The daemon runs each extension's store through a content-defined chunker
(FastCDC / rolling hash) so the store splits into variable-size chunks keyed by
content hash. Two payoffs:

- **Only changed chunks ship.** A settings tweak moves a few KB, not the whole
  store. This makes a narrow / slow / metered channel a non-problem.
- **Dedup for free.** Identical chunks (across versions, or across extensions)
  transfer once.

Do not hand-roll the chunker. Use `restic/chunker` (BSD-licensed Go FastCDC).

### Opportunistic, resumable, eventually-consistent

Sync does **not** need to be immediate. Two of the user's machines converge
whenever they are **both online** (the assumed common case: both up, same WiFi).
There is no store-and-forward queue parked on a server — if no two devices are up
together, nothing moves, and status shows "waiting — peer offline" rather than
failing silently. Because chunks are immutable and content-addressed,
interruption is always safe — a half-finished transfer just means "not all
chunks present yet," never a corrupt state. Resume = re-request the missing
(keyed) hashes.

**Atomic apply-on-complete:** the receiving side reconstructs the new blob only
when every chunk in the manifest is present and hash-verified, then swaps it
into the store atomically. Until then the old settings stay live. A flaky
channel can dribble for an hour; you see "old" until you see "new," never torn
state.

### ~~Free version history~~ — dropped with the durable store

> **Superseded.** This bonus depended on keeping old manifests + chunks in a
> durable store. The P2P pivot has **no durable store** (the user explicitly does
> not want history — "we don't care about history as long as syncs keep things up
> to date"), so rollback is **not** a v1 feature. It could return later behind the
> same interface via an optional encrypted durable store. See
> docs/transport-and-security.md §5.

## v1 scope decision: blob-level, not key-level

Each extension's storage is treated as **one opaque blob.** Snapshot it, chunk
it, sync it atomically per-extension, last-writer-wins per extension by
timestamp.

This sidesteps the single hardest engineering problem (see "Storage formats"):
Firefox encodes `storage.local` values in Mozilla's StructuredClone binary
format, not JSON. Blob-level sync never parses those values — it moves the
store intact and re-homes it via UUID resolution on the other side. CDC still
gives incremental transfer because it finds the changed *byte regions* of the
opaque file without understanding them.

Key-level sync (decode to individual key/value pairs, diff per key, true
minimal deltas, smarter merge) is **v2** and requires writing a
StructuredClone decoder. Deferred on purpose. The blob+CDC path gets ~90% of
the incrementality with 0% of the decoding.

Merge policy v1: **last-writer-wins per extension**, by timestamp. Acceptable
for settings. Per-key merge is a v2 concern that rides on the decoder.

## VERIFY FIRST — load-bearing assumptions to confirm against current Firefox

These claims are from general WebExtensions / Firefox knowledge, **not** yet
verified against current Firefox source or a live profile on this machine. They
are version-sensitive and the whole design rests on them. Confirm each before
implementing the corresponding piece. (Read the source / inspect a real profile
— don't trust this doc as settled fact.)

1. **storage.local backend + on-disk path.** Believed: IndexedDB-backed sqlite
   at `<profile>/storage/default/moz-extension+++<UUID>/idb/*.sqlite`, values
   in StructuredClone format. Historically it was a plain JSON file at
   `<profile>/browser-extension-data/<extension-id>/storage.js`; migration to
   IDB is/was gated by the pref `extensions.webextensions.ExtensionStorageIDB.enabled`.
   VERIFY: current default backend, exact path, and whether both can coexist.
   (If a given extension is still on the JSON-file backend, that's the *easy*
   case — plain JSON, no decoder needed.)

2. **Per-install random UUID.** Believed: the `moz-extension+++<UUID>` internal
   UUID is generated fresh per install and differs across machines, stored in
   the `extensions.webextensions.uuids` pref in `prefs.js` as an
   extension-id → uuid JSON map. This is why we key on the stable extension ID
   and resolve the on-disk path locally on each machine. VERIFY: pref name and
   value format.

3. **storage.sync engine + quota.** Believed: `storage.sync` is backed by
   `storage-sync-v2.sqlite` and synced by Firefox Sync's "extension-storage"
   engine; quota ~100 KB total, ~8 KB/item, ~512 items (Firefox matched
   Chrome's numbers for parity). VERIFY: current quota constants and that the
   sync engine is enabled by default.

4. **Native messaging host registration.** Believed: a JSON manifest pointing
   at the daemon binary must live in an OS-specific dir —
   `~/Library/Application Support/Mozilla/NativeMessagingHosts/` (macOS) and
   `~/.mozilla/native-messaging-hosts/` (Linux). VERIFY: exact paths and
   manifest schema for current Firefox.

5. **Live consistent snapshot.** Believed: sqlite's online backup API gives a
   consistent read of the IDB store while Firefox holds it open, so for Firefox
   we may not need to wait for the browser to quit. VERIFY on a live profile.
   (Standard sqlite feature; low risk, but confirm against the IDB store
   specifically.)

## Cross-platform: it's just path resolution

macOS and Linux differ only in the profile root. Everything below is identical.
Abstract this behind one resolver; the rest of the code is OS-agnostic
(`os.UserHomeDir()` + a `runtime.GOOS` switch).

| | Linux | macOS |
|---|---|---|
| Firefox profile root | snap / flatpak / plain — probe in that order † | `~/Library/Application Support/Firefox/` |
| (Chrome, later) | `~/.config/google-chrome/` | `~/Library/Application Support/Google/Chrome/` |

† Linux is **not** one path: an Ubuntu-default Firefox is a snap and its profile
lives under `~/snap/firefox/common/.mozilla/firefox/`, not `~/.mozilla`. See
DELTA 0 in docs/firefox-internals-verified.md; implemented in
`internal/profile`. Resolve the active profile by parsing `profiles.ini`, not by
hardcoding a profile name.

## Why Firefox first

- It's the target audience's daily driver.
- The whole control-plane trick is Firefox-Sync-specific. Chrome would need its
  own coordination channel (it has no equivalent free-ride for arbitrary
  manifest data), so Chrome is not a small delta — it's a second design.
- The hard work (UUID resolution, StructuredClone, the IDB sqlite reader) all
  lives on the Firefox side anyway. Do the hard part first for the platform
  that matters.

If/when Chrome arrives: storage is LevelDB at
`<profile>/Local Extension Settings/<extension-id>/`, values are JSON (easy),
extension IDs are stable across machines (no UUID problem). Read with
`syndtr/goleveldb`. The catch is LevelDB holds an exclusive `LOCK` while Chrome
runs, so Chrome likely needs build-on-quiesce rather than live snapshots, and a
non-Firefox-Sync coordination channel.

## Proposed package layout (Go)

```
cmd/gusset/          main; version from git tag (see Versioning)        [done: version, doctor]
internal/profile/    OS + browser path discovery, profiles.ini parser,  [done]
                     prefs.js / uuids resolver (snap/flatpak/plain)
internal/store/      Backend interface; Firefox snapshot + apply        [done: Snapshot; TODO: Apply]
  firefox.go         IDB reader (by db name) + VACUUM-INTO snapshot +
                     .files capture; Apply does UUID rewrite (DELTA 2)
  (chrome.go later)  leveldb
internal/policy/     allowlist (empty default) + sensitive denylist     [TODO]
                     (deny-with-override). See transport-and-security.md §3
internal/crypto/     passphrase -> Argon2id -> K_enc/K_addr/peer-auth;  [TODO]
                     AEAD chunk encryption, keyed content-addressing
internal/chunk/      FastCDC wrapper (restic/chunker); keyed-hash +      [TODO]
                     encrypt per chunk; manifest types
internal/sync/       delta detection, atomic apply, LWW conflict policy [TODO]
internal/transport/  Transport interface; v1 = p2p Tier-0 LAN-direct,   [TODO]
                     signaled via storage.sync (NAT/relay tiers later)
internal/status/     single status source; never-sync-silently model    [TODO]
extension/           companion WebExtension: manifest + signaling        [TODO]
                     courier (storage.sync), status UI, localhost-WS client
```

Authoritative design for the data plane, encryption, policy, and status lives in
**docs/transport-and-security.md**. The daemon↔extension channel is a **localhost
WebSocket**, not native messaging (see DELTA 4 in
docs/firefox-internals-verified.md) — so there is no `native-host/` package by
default.

The `store.Backend` interface keeps macOS/Linux *and* Firefox/Chrome
differences from leaking past `internal/store` and `internal/profile`.

## Versioning / build (house style)

- Do **not** hand-maintain a `const version`. Use `var version = "dev"`
  overridden at release via `-ldflags "-X main.version=$(git describe --tags --always --dirty)"`,
  with a `buildVersion()` fallback chain using `runtime/debug.ReadBuildInfo()`
  (ldflags → `bi.Main.Version` → vcs.revision → "dev"). Reference: hindcast
  `cmd/hindcast/main.go`.
- Ship a `Makefile` with `VERSION := $(shell git describe --tags --always --dirty)`
  and `build`/`install` targets passing the ldflags.
- Keep a `CHANGELOG.md` (prose, not derivable from tags).
- New repo is **private**, default branch **main**.
- Stage explicit paths, never `git add -A`. Scope `gofmt` to edited files.

## Open questions / risks

- **StructuredClone is the v2 gate.** If per-key merge ever becomes necessary,
  someone has to write/port a StructuredClone decoder in Go. It's
  reverse-engineered and version-sensitive. Keep blob-level as long as possible.
- **CDC dedup on raw store files is best-effort.** sqlite reorganizes pages and
  (for Chrome) LevelDB compaction rewrites whole files, so a small logical
  change can look like a large byte change and degrade the dedup ratio. Still
  strictly better than whole-file transfer, and the async queue makes a
  post-compaction re-sync a background non-event. Note it; don't over-engineer.
- ~~**Native messaging host install friction.**~~ **Resolved** — the
  daemon↔extension channel is a localhost WebSocket, not native messaging, so
  there is no host to register (DELTA 4, docs/firefox-internals-verified.md).
- **Concurrent-use safety.** Live snapshot via `VACUUM INTO` reads the store
  consistently while Firefox holds it open — verified on a live uBO store
  (`internal/store`). Apply (the write side) still needs care: do not write a
  store out from under a running Firefox; atomic swap + verify on a quiesced
  target, or while the extension is idle.
- **Which extensions to sync → settled as policy.** Opt-in allowlist (empty by
  default) plus a **sensitive denylist with deny-with-override** for credential
  managers. Device-specific tokens / absolute paths can't be stripped at
  blob-level (that's the v2 key-level decoder), so allowlisting is a per-extension
  judgement the user makes, with warnings. See docs/transport-and-security.md §3.
- **P2P reachability is best-effort.** v1 assumes both machines online, usually
  same-LAN. Cross-NAT (Tier 1) and relay (Tier 2) are deferred. Every
  non-converged state is surfaced via status, never silent.
- **Lost-device revocation.** A paired device holds the passphrase-derived key;
  v1 revocation = rotate the passphrase everywhere. No forward secrecy. Noted,
  not solved.

## Progress & next steps

**Done:**
1. ✅ Verified the five "VERIFY FIRST" assumptions against a live snap Firefox
   profile (+ Sync sign-in confirming the `extension-storage` engine).
   docs/firefox-internals-verified.md.
2. ✅ Scaffolded the Go module with house tooling + `internal/profile` resolver
   (snap/flatpak/plain) + `gusset doctor`.
3. ✅ `internal/store` Firefox **snapshot** path against the live uBO store
   (VACUUM-INTO + `.files` capture, store identified by db name). 42 keys, 10
   external files.

4. ✅ `internal/policy` — allowlist (empty default) + sensitive denylist
   (deny-with-override) + name heuristic.
5. ✅ `internal/crypto` — passphrase → Argon2id → keys; XChaCha20 AEAD; keyed
   addressing; AAD binding; per-user salt; strength floor.
6. ✅ `internal/chunk` — FastCDC (restic/chunker) → keyed-hash + encrypt per
   chunk; signed manifests; per-user deterministic boundaries; resumable.
7. ✅ `internal/store` **Apply** path — 3-place UUID rewrite + atomic swap,
   verified re-homing a live store onto a different-UUID target.

**Next:**
8. `internal/transport` — Tier-0 LAN-direct backend, signaled via `storage.sync`.
9. `internal/sync` — serialize a snapshot dir ⇄ stream (the store⇄chunk seam),
   delta detection, LWW; wire snapshot→chunk→transport→reconstruct→apply.
10. Status plumbing (`gusset status` + localhost WS JSON), then the companion
    extension (manifest/signaling courier + status UI).
