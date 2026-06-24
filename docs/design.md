# gusset — design

This is the design rationale: what gusset is, the constraints that shape it, and
why it is built the way it is. The load-bearing Firefox internals it relies on
are recorded, verified against a live profile, in
[firefox-internals-verified.md](firefox-internals-verified.md); the data plane,
encryption, policy, and status model are specified in
[transport-and-security.md](transport-and-security.md).

## What gusset is

A small daemon that syncs **Firefox extension settings** (the data extensions
keep in `storage.local`) across a user's machines — the one thing Firefox Sync
does *not* carry. Firefox Sync already syncs bookmarks, history, logins, open
tabs, the *list* of installed add-ons, and an allowlist of `about:config` prefs.
It does not sync an extension's own stored settings. So you sign into a fresh
Firefox, uBlock Origin reinstalls itself automatically, and then sits there with
default filters — your custom rules, whitelist, and dashboard config did not come
along. gusset fills exactly that seam and nothing else.

The name is the metaphor: a gusset is the small inserted piece that joins two
parts and keeps the seam from failing (sewing; also the load-carrying plate in
aircraft and bridge joints). gusset joins your machines at the one joint Firefox
leaves open.

Target: **Firefox first**, on **macOS and Linux**. Chrome is a possible later
backend but is out of scope for v1 (see "Why Firefox first").

## Why this can't be a normal extension

WebExtensions are sandboxed from each other. There is **no API for one extension
to read another extension's storage** — uBO's settings are readable only by uBO.
So a "sync all my extensions" extension is architecturally impossible: it would
have no way to see any other extension's data. (This is a good wall — it's what
stops a sketchy extension from looting your password manager, and gusset does not
want to break it.)

The only thing that can read another extension's storage is a program *outside*
the sandbox: a native app with disk access. That is why gusset is a native
daemon, not (only) an extension.

`storage.sync` exists and Firefox Sync *does* carry it — but it is an opt-in API
the extension itself must choose to use, and it is quota-capped (~100 KB total
per extension). uBO deliberately keeps its filter lists in `storage.local`, not
`storage.sync`, precisely because they blow past that quota. So "just use
storage.sync" is not available for the bulk data.

## Architecture: split the control plane from the data plane

The key design move. gusset does **not** sync the whole profile folder
(heavyweight, corruption-prone, and it drags along history, cookies, and sessions
that should not move). Instead it separates coordination from bulk transfer.

### Control plane — Firefox Sync itself

A **tiny companion extension** holds *manifests* in its own `storage.sync`. A
manifest is small — even for a multi-megabyte uBO store it is just a list of
content-hashes plus metadata, a few KB, well under the 100 KB quota. Firefox Sync
then carries that manifest across machines for free: end-to-end encrypted, no
server to run, no auth to build, already trusted by the user.

Each machine publishes its own state under its **own namespaced key**
(`manifest:<machine-id>:<extension-id>` → chunk-set + version + timestamp) and
only *reads* peers' keys to learn what to fetch. Because every machine owns its
own key, there is no write-conflict on the manifest store.

The companion extension is also the natural home for the **control-panel UI**:
pick which extensions to sync, see status, trigger a restore.

Going through a companion extension (the supported `storage.sync` API) rather than
writing `storage-sync-v2.sqlite` directly avoids a real fragility: poking
Firefox's storage.sync backing store out from under the browser is undocumented
and racy. Firefox handles syncing the manifests; gusset never injects.

#### Be a good Firefox Sync citizen

Firefox Sync is a free, end-to-end-encrypted service Mozilla runs for users, and
gusset rides it for the control plane. That is a courtesy to use lightly, and it
is a design constraint, not a nicety:

- **Never put bulk data on `storage.sync`.** Only tiny manifests (a few KB of
  content-hashes) ride Sync; the megabyte-scale chunks go through gusset's own
  transport. This is the whole point of the control-plane / data-plane split.
  Anything that would grow the `storage.sync` payload toward the ~100 KB quota is
  a smell.
- **Don't talk to Mozilla's servers directly, and don't force syncs.** gusset
  reads and writes only *local* state (the companion extension's `storage.sync`,
  surfaced locally in `storage-sync-v2.sqlite`) and lets Firefox sync on its own
  cadence — no polling sync endpoints, no programmatic "sync now" loops. The
  daemon's network traffic is with gusset's own transport only.
- **Stay within the documented, supported surface.** Manifests go through the
  `storage.sync` API; gusset does not reverse-engineer Sync internals.

If a feature would make gusset a heavier guest on Mozilla's infrastructure,
that's a reason to redesign it, not to ship it.

### Data plane — content-addressed chunks, moved peer-to-peer

The bulk chunks (the actual extension settings) move directly between a user's
own machines over an encrypted connection, keyed by a single 8-word passphrase.
There is no transport account, no server holding data, and no durable store. The
transport is pluggable behind an interface; the tiers are: same-LAN direct
(signaled by mDNS or a shared folder), then NAT hole-punching, then a relay for
the NAT pairs hole-punching cannot cross. See
[transport-and-security.md](transport-and-security.md) for the authoritative
design.

The daemon runs each extension's store through a content-defined chunker (FastCDC
/ rolling hash, via `restic/chunker`) so the store splits into variable-size
chunks keyed by content hash. Two payoffs:

- **Only changed chunks ship.** A settings tweak moves a few KB, not the whole
  store, so a narrow or metered channel is a non-problem.
- **Dedup for free.** Identical chunks (across versions, or across extensions)
  transfer once.

### Opportunistic, resumable, eventually-consistent

Sync does **not** need to be immediate. Two machines converge whenever they are
both online (the common case: both up, same WiFi). There is no store-and-forward
queue parked on a server — if no two devices are up together, nothing moves, and
status shows "waiting — peer offline" rather than failing silently. Because chunks
are immutable and content-addressed, interruption is always safe: a half-finished
transfer just means "not all chunks present yet," never a corrupt state. Resume is
re-requesting the missing (keyed) hashes.

**Atomic apply-on-complete:** the receiving side reconstructs the new blob only
once every chunk in the manifest is present and hash-verified, then swaps it into
the store atomically. Until then the old settings stay live. A flaky channel can
dribble for an hour; you see "old" until you see "new," never torn state.

gusset keeps **no durable history** — convergence, not archival, is the goal, so
rollback is not a feature. It could return later behind the same interface via an
optional encrypted durable store.

## v1 scope: blob-level, not key-level

Each extension's storage is treated as **one opaque blob**: snapshot it, chunk it,
sync it atomically per-extension, last-writer-wins per extension by timestamp.

This sidesteps the single hardest engineering problem. Firefox encodes
`storage.local` values in Mozilla's StructuredClone binary format, not JSON.
Blob-level sync never parses those values — it moves the store intact and re-homes
it via UUID resolution on the other side. Content-defined chunking still gives
incremental transfer, because it finds the changed *byte regions* of the opaque
file without understanding them.

Key-level sync (decode to individual key/value pairs, diff per key, true minimal
deltas, smarter merge) is a v2 concern: it requires a StructuredClone decoder in
Go, which is reverse-engineered and version-sensitive. Deferred on purpose — the
blob+CDC path gets most of the incrementality with none of the decoding.

## Cross-platform: mostly path resolution

macOS and Linux differ in two places: the profile root (one resolver,
`os.UserHomeDir()` + a `runtime.GOOS` switch) and, in `internal/ffctl`, how a PID
is identified and how Firefox is relaunched (build-tagged
`ffctl_linux.go` / `ffctl_darwin.go`). Everything else — snapshot, apply, chunk,
crypto, transport, rendezvous — is OS-agnostic and cross-compiles for both.

| | Linux | macOS |
|---|---|---|
| Firefox profile root | snap / flatpak / plain — probed in that order † | `~/Library/Application Support/Firefox/` |
| (Chrome, later) | `~/.config/google-chrome/` | `~/Library/Application Support/Google/Chrome/` |

† Linux is **not** one path: an Ubuntu-default Firefox is a snap and its profile
lives under `~/snap/firefox/common/.mozilla/firefox/`, not `~/.mozilla`. The
active profile is resolved by parsing `profiles.ini`, not by hardcoding a name
(implemented in `internal/profile`).

## Why Firefox first

- It is the target audience's daily driver.
- The control-plane trick is Firefox-Sync-specific. Chrome has no equivalent
  free ride for arbitrary manifest data, so Chrome is not a small delta — it is a
  second design.
- The hard work (UUID resolution, StructuredClone, the IDB sqlite reader) all
  lives on the Firefox side anyway. Do the hard part first for the platform that
  matters.

If/when Chrome arrives: storage is LevelDB at
`<profile>/Local Extension Settings/<extension-id>/`, values are JSON (easy), and
extension IDs are stable across machines (no UUID problem). The catch is LevelDB
holds an exclusive `LOCK` while Chrome runs, so Chrome likely needs
build-on-quiesce rather than live snapshots, and a non-Firefox-Sync coordination
channel.

## Package layout

```
cmd/gusset/          CLI entry point; version from the git tag
internal/profile/    OS + browser path discovery, profiles.ini parser, prefs.js / uuids resolver
internal/store/      Backend interface; Firefox snapshot + apply (3-place UUID rewrite)
internal/policy/     allowlist (empty default) + sensitive denylist (deny-with-override)
internal/crypto/     passphrase -> Argon2id -> enc/addr/peer-auth keys; AEAD chunk encryption
internal/chunk/      FastCDC wrapper; keyed-hash + encrypt per chunk; signed manifests
internal/syncx/      snapshot serialization, export/import wiring, last-writer-wins
internal/transport/  pinned mutual-TLS peer auth + the chunk request/response protocol
internal/discovery/  mDNS LAN rendezvous (_gusset._tcp)
internal/rendezvous/ sealed beacons over a Signaling carrier (shared folder today, the extension later)
internal/icewire/    ICE NAT hole-punch + QUIC reliable stream, reusing the pinned identity
internal/converge/   the reconcile loop (build offer, pull, apply)
internal/status/     single status source; the "never sync silently" model
internal/statusws/   token-gated loopback WebSocket that streams the status Snapshot to the extension
```

The daemon↔extension channel is a **localhost WebSocket**, not native messaging,
so there is no native-messaging host to register. It is loopback-only and gated
by a token derived from the passphrase (`gusset ws-token`) — localhost is not a
trust boundary, so an unauthenticated socket is closed before it sees any status.

## Versioning and build

The git tag is the single source of truth — there is no hand-edited version
constant. `var version = "dev"` is overridden at release via
`-ldflags "-X main.version=$(git describe --tags --always --dirty)"`, with a
`buildVersion()` fallback chain using `runtime/debug.ReadBuildInfo()` (ldflags →
module version → VCS revision → `dev`). The [`Makefile`](../Makefile) wires this
up; [`CHANGELOG.md`](../CHANGELOG.md) carries the prose that tags can't.

## Open questions and known limits

- **StructuredClone is the v2 gate.** Per-key merge needs a StructuredClone
  decoder in Go — reverse-engineered and version-sensitive. Keep blob-level as
  long as possible.
- **CDC dedup on raw store files is best-effort.** sqlite reorganizes pages and
  (for Chrome) LevelDB compaction rewrites whole files, so a small logical change
  can look like a large byte change and degrade the dedup ratio. Still strictly
  better than whole-file transfer; note it, don't over-engineer.
- **Apply needs a quiesced profile.** Live snapshot via `VACUUM INTO` reads the
  store consistently while Firefox holds it open, but the write side must not
  write a store out from under a running Firefox — Apply refuses a locked profile,
  and `--restart-firefox` is the opt-in that closes and relaunches the browser.
- **Which extensions to sync is a user decision.** Opt-in allowlist (empty by
  default) plus a sensitive denylist with deny-with-override for credential
  managers. Device-specific tokens and absolute paths can't be stripped at
  blob-level (that's the v2 decoder), so allowlisting is a per-extension judgement
  the user makes, with warnings.
- **Cross-NAT reachability is best-effort.** Same-LAN works directly; harder NAT
  pairs need hole-punching, and symmetric↔symmetric needs a relay. Every
  non-converged state is surfaced via status, never silent.
- **Lost-device revocation.** A paired device holds the passphrase-derived key;
  revocation means rotating the passphrase everywhere. No forward secrecy. Noted,
  not solved.
