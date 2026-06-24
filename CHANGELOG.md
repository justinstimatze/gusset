# Changelog

## Unreleased

- The extension's TypeScript runs under a strict regime: `tsconfig` adds
  `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `verbatimModuleSyntax`,
  `noUnusedLocals`/`noUnusedParameters`, `noImplicitOverride`/`noImplicitReturns`,
  and `noFallthroughCasesInSwitch` on top of `strict`; Biome's `noExplicitAny`,
  `noNonNullAssertion`, and unused-symbol rules are errors (test fakes may use
  `any`). The codebase is clean under all of them — the React roots are
  null-checked, buttons carry an explicit type, and array accesses are guarded.
- Companion Firefox WebExtension under `extension/` (WXT + React + Tailwind,
  Manifest V3): the production `storage.sync` beacon courier and the status UI.
  It owns the single daemon connection in its background event page, bridges the
  daemon's beacon to `storage.sync` (writing under a per-install namespaced key)
  and reports peers back, and renders the live status in a popup with a pairing
  form. The WebSocket client is a state machine with first-frame token auth and
  backoff reconnect — a rejected token surfaces "token rejected", a missing
  daemon "daemon not running", never a silent spinner. The toolbar popup is the
  glanceable launcher; a full dashboard page (options_ui) renders the
  per-extension × per-device sync grid with reasons, as an accessible native
  table. Unit-tested (vitest) for the connection state machine and the carrier's
  namespacing; an `extension` CI job typechecks, tests, and builds it.
- The localhost WebSocket is now the production beacon carrier as well as the
  status stream. The daemon cannot touch the `storage.sync` API — only the
  extension can — so the WS server implements `rendezvous.Signaling` by proxy:
  it pushes this device's sealed beacon to the extension to publish, and takes
  the peer beacons the extension reports from `storage.sync`. `gusset sync --ws`
  without a `--rendezvous-dir` now uses the companion extension as its
  cross-network rendezvous carrier (the shared folder remains the alternative
  when there is no extension). The daemon↔extension protocol is a small
  typed-envelope WebSocket, contract-tested against a mock extension client.
- `internal/statusws` + `gusset sync --ws host:port`: the daemon can now stream
  live status to the companion extension over a localhost WebSocket. It is
  loopback-only (a non-loopback bind is refused) and gated by a token derived
  from the passphrase — localhost is not a trust boundary, so an unauthenticated
  socket is closed before it sees any status. The status model gained change
  subscriptions, so the socket pushes a fresh Snapshot the moment anything
  changes (with an app-level heartbeat to drop dead clients), and `gusset
  ws-token` prints the pairing token to paste into the extension once. This is
  the daemon-side substrate the companion extension connects to.
- Security: TLS peer pinning now also covers resumed sessions. The mutual-TLS
  configs gained a `VerifyConnection` pin (invoked on resumed TLS 1.3 handshakes,
  which skip `VerifyPeerCertificate`) and the server disables session tickets, so
  a resumed connection cannot bypass the passphrase-derived key pin.
- Security default: `gusset init` now generates a per-user salt by default (it
  prints a one-line command to pair other devices); `--no-salt` opts back into
  passphrase-only derivation. Salting Argon2id per user means a weak
  bring-your-own passphrase is not precomputation-attackable and keys never link
  across users.
- Removed the server-reflexive beacon candidate (and the now-unused
  `internal/stunc`): it was usually undialable and leaked the device's public IP
  and an internal port into the beacon. Cross-NAT reachability rides the ICE
  hole-punch endpoint instead.
- Tooling: pinned the Go toolchain to a patched release and added `govulncheck`,
  `gosec`, `errorlint`, and CodeQL to CI; pinned all GitHub Actions to commit
  SHAs; added a macOS test matrix (so the darwin `ffctl` path is built), release
  build-provenance attestation, and the usual community-health files
  (`CONTRIBUTING.md`, `CODEOWNERS`, issue/PR templates).
- Docs: replaced the working-notes `HANDOFF.md` with a trimmed
  [`docs/design.md`](docs/design.md) written for readers rather than as a tracker.
- Security: `store.Apply` now validates the path-bearing fields of a snapshot's
  `meta.json` (`idb_file_base`, `origin_suffix`, `source_uuid`) before building
  any filesystem path. These arrive from a remote peer and were joined into
  paths under the Firefox profile; a crafted snapshot with `..` segments could
  escape the staging directory and write outside the profile. Apply now fails
  closed on anything that isn't the expected shape (a bare basename, a
  QuotaManager origin suffix, a canonical UUID).
- Security: `gusset sync` refuses a passphrase file that is readable by group or
  other (mode `& 0o077 != 0`) instead of silently loading the root secret from a
  world-readable file. The error names the file and suggests `chmod 600`.
- Tests: added direct coverage for the two STUN address decoders (the only
  untrusted-input parsers that lacked a named test — truncated values at every
  family boundary now must fail closed without panicking) and for `crypto.Stream`
  (the cross-machine determinism the chunker polynomial depends on).
- `internal/status` hardening: the renderer now sanitizes peer-supplied strings
  (beacon/mDNS labels, device ids, and free-text detail) before they reach the
  terminal — any non-printable rune (ANSI escapes, carriage returns, bells)
  becomes a visible replacement character instead of executing as a control
  sequence. Closes the terminal-injection note from the Tier-1 security review;
  ordinary printable Unicode (non-ASCII hostnames) is unaffected.
- NAT hole-punching wired into `gusset sync`. When `--stun` is set on the
  rendezvous path, the device now also gathers an ICE endpoint (creds +
  candidates) and advertises it, sealed, inside its beacon
  (`rendezvous.Beacon.ICE`). When every direct LAN dial to a peer fails and that
  peer published an ICE endpoint, gusset punches a hole with `internal/icewire`
  and reconciles over it. The punched QUIC connection is
  bidirectional, so one expensive path carries a full reconcile both ways (we
  serve our offer on the stream the peer opens, and pull theirs on a stream we
  open) rather than the LAN model's two separate connections. The ICE controlling
  side (and QUIC client) is chosen by the greater device id, so both peers agree
  on opposite roles; status reflects `hole-punching` → `connected (direct-nat)` or
  `unreachable (nat-traversal-failed)`. The ICE agent is gathered once per run and
  spent on the first peer that needs it. Coverage boundary unchanged:
  symmetric↔symmetric still needs a TURN relay (Tier-2).
- `internal/icewire` — the NAT-traversal data path. Adopts pion/ice for
  hole-punching and quic-go for a reliable, ordered stream
  over the punched UDP path (ICE alone yields datagrams; the chunk protocol needs
  a stream). QUIC reuses the *same* passphrase-derived pinned-mutual-TLS identity
  as the LAN transport (`transport.Identity`'s configs + a `gusset-chunk/1` ALPN),
  and a stream is just the `io.ReadWriteCloser` `transport.NewClient`/`Serve`
  already consume — so the chunk and reconcile layers are unchanged. `Gather`
  produces a small JSON `Endpoint` (ICE creds + candidates) to carry sealed inside
  a rendezvous beacon; `Connect` punches and brings up QUIC; `Conn.OpenStream`/
  `AcceptStream` drive pull/serve. Verified end-to-end over pion's virtual network
  (vnet) — two peers behind simulated port-restricted-cone NATs punch and run a
  real chunk `Get`, in-process, no hardware (the approach was de-risked first in
  spikes/icepunch).
- Scaffolded the Go module with the house tooling: git-tag-derived versioning
  (`buildVersion()` fallback chain + `Makefile`), golangci-lint v2, CI (vet,
  gofmt check, `go test -race`, build) and goreleaser release plumbing.
- `internal/profile`: resolves the active Firefox profile across snap, flatpak,
  and plain Linux installs (and macOS), parses `profiles.ini`, and reads the
  `extensions.webextensions.uuids` map from `prefs.js`. Encodes the facts
  verified in `docs/firefox-internals-verified.md`.
- `gusset doctor`: read-only command that resolves the profile and lists
  installed extensions with their per-install UUIDs.
- `internal/store`: blob-level `Backend` interface and a Firefox implementation
  of the read/snapshot path. It locates the `webExtensions-storage-local` IDB by
  database name, takes a consistent copy via `VACUUM INTO` while Firefox holds
  the store open (pure-Go `modernc.org/sqlite`, so `CGO_ENABLED=0` builds), and
  captures the out-of-line external value files alongside. Tested against a live
  uBlock Origin store (skips cleanly when no profile is present).
- Design: codified "Be a good Firefox Sync citizen" — bulk data never rides
  `storage.sync`, no forced syncs or polling of Mozilla's servers.
- Design pivot (docs/transport-and-security.md): the data plane moves from a
  git/store-and-forward transport to **direct device-to-device** sync, signaled
  through Firefox Sync. v1 transport is Tier-0 same-LAN direct (NAT traversal and
  relay are later tiers). Chunks are encrypted with a key derived from a single
  8-word passphrase; the transport only ever sees ciphertext. No transport
  account, no server holding data, no durable history. Policy: opt-in allowlist
  (empty default) + sensitive denylist (deny-with-override). New requirement:
  never sync silently — every non-converged state carries a visible reason
  (status surfaced via `gusset status`, localhost WS JSON, and the extension UI).
- Notes: docs/agent-setup-and-extension-ui.md captures agent-driven setup (one
  machine-readable status source for humans and agents alike) and the extension's
  control-panel/status-dashboard UI.
- `internal/policy`: the safe-by-default sync gate. Opt-in allowlist (empty
  default) + a built-in sensitive denylist (credential/2FA extensions) with
  deny-with-override. Every `Evaluate` carries a human-readable reason.
- `internal/crypto`: one passphrase → Argon2id → HKDF subkeys. XChaCha20-Poly1305
  chunk encryption, HMAC-SHA256 keyed content-addressing, and label-scoped
  `Subkey` derivation for peer auth. Keys are reproducible across machines from
  the passphrase alone (fixed app salt; per-user random salt supported).
- Security review hardening (crypto + policy):
  - **M2** — `Seal`/`Open` now take the content-address as AAD, binding a
    ciphertext to its address so it cannot be served from another; documented the
    matching post-decrypt re-verification invariant for the chunk layer.
  - **M1** — per-user random salt is the recommended default (`NewSalt`); added
    `ValidatePassphrase` (structural strength floor) and `EntropyBits`.
  - **L1** — `Subkey` returns an error instead of panicking past the HKDF limit.
  - **L2** — policy gains `EvaluateNamed` + `LooksSensitiveName`: a name
    heuristic that deny-with-overrides credential-looking extensions not on the
    built-in denylist.
- `internal/syncx`: the integration seam. Deterministic, reversible snapshot
  directory ⇄ stream serialization (timestamp-free, so identical content packs
  identically and dedups; unpack rejects path-traversal/absolute entries), plus
  `Export`/`Import` wiring store → chunk → transport → reconstruct → store.Apply,
  and `Newer` (last-writer-wins by timestamp). Verified end-to-end: a live uBO
  store snapshots, packs, chunks+encrypts (366 chunks), round-trips through an
  opaque address→ciphertext map, and applies onto a different-UUID target with
  all 42 keys intact; a wrong key fails the import.
- `internal/store` Apply (the write path): installs a snapshot into a target
  profile, re-homing it onto that machine's per-install UUID by rewriting the
  UUID in all three places it is embedded (origin dir name, `.metadata-v2`,
  sqlite `database.origin` — DELTA 2). Stages on the same filesystem and swaps
  the IDB dir in with a rename, keeping a backup until success. Refuses to write
  a locked (running) Firefox profile and an uninstalled extension. Snapshot now
  also records the name-derived IDB file base and captures `.metadata-v2` so a
  store can be re-homed onto a machine where the origin dir does not yet exist.
  Verified end-to-end: a live uBO snapshot applies onto a synthetic profile with
  a different UUID, data intact.
- Two-container LAN test (real mDNS over a bridge). A Docker-gated integration
  test (`go test -tags docker_integration -run TestTwoBox_Docker ./cmd/gusset/`)
  runs two gusset containers on a user-defined Docker bridge — each in its own
  network namespace with its own IP, two genuinely separate network stacks, not
  loopback. The source serves a copy of the live uBlock store; the target
  resolves the source by container name over the bridge, dials it, pulls, and
  applies all 42 keys. A second phase confirms **mDNS discovery works over the
  bridge** — the target finds the source with no `--peer` at all. gusset is a
  static CGO-free binary, so the image is FROM scratch with no base pull. The
  test is behind a build tag (needs Docker, ~25s), so the default `go test ./...`
  stays fast and dependency-free; it skips cleanly when Docker is absent.
- Two-process end-to-end test + a bug it found. A new integration test runs two
  real `gusset sync` processes — each with its own HOME (its own Firefox profile)
  and config — over a real loopback TCP/TLS connection: the source serves a copy
  of the live uBlock store, the target (a separate profile, a different UUID, no
  running Firefox) dials it, pulls, and applies all 42 keys re-homed onto its
  UUID. It exercises the production binary end to end across two OS processes —
  the closest thing to a two-box LAN run without two machines. A `--listen
  host:port` flag was added for the source side (the manual-rendezvous companion
  to `--peer`). The test surfaced a real bug, now fixed: an extension installed
  but with no `storage.local` yet (e.g. freshly installed) made `BuildOffer`
  abort the whole sync. `store.Snapshot` now returns a sentinel `ErrNoStore` for
  that case, and `converge.BuildOffer` skips such an extension (offers nothing)
  while still letting the machine receive it from a peer.
- `internal/config` + `gusset init`/`allow`/`disallow`: persisted per-user
  settings (XDG config dir, overridable via `GUSSET_CONFIG_DIR`), so routine
  syncs need no flags or environment. Holds the allowlist, sensitive overrides,
  an optional per-user salt, and a pointer to the passphrase file; the passphrase
  itself is never stored in the config. `gusset sync` now reads all of these:
  salt via `SaltOrApp` (a configured per-user salt, else `crypto.AppSalt`),
  allowlist/overrides merged with the `--extensions`/`--override` flags, and the
  passphrase resolved from the config's file, then `<config-dir>/passphrase`,
  then `GUSSET_PASSPHRASE_FILE`/`GUSSET_PASSPHRASE`. `gusset init` defaults to the
  passphrase-alone path (the same 8 words reproduce keys everywhere, zero extra
  sharing); `--new-salt` opts into the per-user-salt hardening (M1) and prints
  the `init --salt <b64>` command to pair other devices. `init` refuses to
  overwrite an existing config so a shared salt is never clobbered.
- `internal/discovery`: LAN rendezvous over mDNS (`_gusset._tcp`). `Advertise`
  announces this device's transport listener (returning a stop func so the
  announcement lives only as long as the sync window — the §8 privacy posture);
  `Browse` finds peers, skips self, and returns dialable `host:port`. mDNS only
  announces presence; who may connect is still gated by the passphrase-derived
  mutual TLS. Pure-Go (`grandcat/zeroconf`), so `CGO_ENABLED=0` holds.
- `internal/converge`: the reconcile layer `gusset sync` runs. `BuildOffer`
  snapshots+exports each allowlisted extension into one `transport.StaticSource`
  (union of encrypted chunks + a JSON catalog of per-extension manifests) plus
  the local catalog; `Pull` fetches a peer's catalog, and for each extension it
  offers that is allowlisted locally and strictly newer (LWW), reconstructs and
  applies it. Per-extension apply failures are reported, not fatal, so one bad
  extension does not abort the run. Kept free of policy/discovery (takes an
  `allow` predicate and a connection), so it unit-tests over an in-memory pipe;
  verified end-to-end against a live uBO store (offer → serve → pull → apply onto
  a different-UUID target), plus the LWW-skip, not-allowlisted-blocked, and
  Firefox-running (locked) paths. Also verified as a two-peer convergence over
  real loopback mutual-TLS: peer A serves its live store, peer B (a separate
  profile, an identity independently derived from the same passphrase) dials over
  the socket, fetches the catalog via opOffer, pulls the chunks, and applies all
  42 keys re-homed onto its own UUID — the full networked path the pipe tests
  cannot exercise.
- `internal/ffctl` + `gusset sync --restart-firefox`: opt-in "close and reopen
  Firefox for you." Applying incoming settings needs Firefox not running (it
  locks the profile and caches the store in memory), so this stops the exact
  Firefox holding the target profile — the PID is read straight from the `lock`
  symlink target, not guessed — waits for a clean shutdown, lets the run apply,
  then relaunches it (detached, so the session restores). Conservative by design:
  it sends SIGTERM (never SIGKILL — a clean exit flushes the store and saves the
  session), and refuses to signal a PID it cannot confirm is Firefox via
  `/proc/<pid>/{comm,cmdline}` — verified in practice when it declined to kill a
  recycled PID behind a stale lock. Off by default; closing the browser is
  destructive, so it only runs when the user passes the flag. Linux for v1.
  - Stale-lock recovery: a lingering `lock` symlink (a crash, or a recycled PID)
    would otherwise block apply forever even with no Firefox running. `ffctl`
    gains `InspectLock` (Unlocked / LockedLive / LockedStale / LockUnknown) and
    `ClearStale`, which removes a lock only when no live Firefox holds it,
    re-checking liveness immediately before removal to close the check-then-act
    race; an unparseable lock is left untouched. `gusset sync` clears a stale
    lock automatically (provably safe — `store.Apply` independently re-checks the
    lock as a backstop), and `gusset doctor` now reports lock status.
  - macOS support: the two OS-specific seams (identifying a PID as Firefox, and
    the default relaunch binary) move into build-tagged `ffctl_linux.go` /
    `ffctl_darwin.go`. The darwin build identifies processes via `ps` (there is no
    `/proc`) and defaults to `/Applications/Firefox.app/Contents/MacOS/firefox`.
    Cross-compiles clean (`GOOS=darwin go build ./...`); the lock-symlink handling
    degrades safely where macOS may instead use a `.parentlock` fcntl lock (no
    parseable symlink → "not running" → `--restart-firefox` falls back to a manual
    close, never a wrong signal). The one fact that needs a live Mac to confirm is
    documented under "macOS — UNVERIFIED" in `docs/firefox-internals-verified.md`.
- Receiver activation is now communicated clearly. Firefox loads `storage.local`
  at startup and locks the profile while running, so applying incoming settings
  has two user-visible preconditions, both stated plainly rather than left as a
  raw error: (a) after a successful apply, `gusset sync` prints "restart Firefox
  here to load them — on disk but not yet live"; (b) when the receiver's Firefox
  is running, the apply is reported as a distinct `converge.Locked` outcome (the
  data was fetched and verified; only the on-disk write was deferred) and the
  command prints "close Firefox, re-run, then reopen it". Both render in the
  status grid as a new `status.Pending` state (data on disk, action needed to go
  live) rather than as `in-sync` or `error`.
- `gusset sync`: the on-demand sync command (docs/transport-and-security.md §8).
  Serves this device's allowlisted extensions and pulls a peer's newer ones over
  passphrase-authed mutual TLS, discovered on the LAN by mDNS (or `--peer
  host:port` direct). Listener lifetime is the user's choice: a default one-shot
  window, `--for D` (bounded setup window), or `--watch` (indefinite, until
  Ctrl-C) — nobody is forced into an always-on process. Allowlist via
  `--extensions` (+ `--override` for sensitive ones); passphrase from
  `GUSSET_PASSPHRASE_FILE` or `GUSSET_PASSPHRASE`. Reconcile outcomes render
  through `internal/status` as an end-of-run summary, so every peer and extension
  carries a reason. v1 needs no daemon, no extension, and no Firefox Sync.
- `internal/status`: the single status source behind the never-sync-silently
  rule (docs/transport-and-security.md §6). A concurrency-safe model of paired
  peers (`discovering → … → connected(lan) | unreachable(reason)`) and
  per-extension × per-peer sync state (`in-sync | pushing | pulling | stale |
  blocked | error`), producing one sorted, JSON-marshalable `Snapshot` that all
  three surfaces render. The "never silent" invariant is enforced at render:
  every non-converged state yields a non-empty reason, and one that arrived
  without an explanation renders a loud fallback rather than a blank. Decoupled
  from the data plane (the daemon will bridge a `transport.ConnError`
  PhaseHandshake into an `auth-failed` peer), so it imports nothing of transport;
  timestamps are caller-supplied for deterministic tests.
- `gusset status`: renders peers and per-extension sync state with reasons. The
  live model is owned by the daemon (read over the localhost WS later); until the
  daemon exists this reports honestly that none is running and renders the empty
  model explicitly — "nothing configured" is shown, not hidden.
- `internal/transport`: Tier-0 LAN-direct data plane, in three testable layers.
  (1) A chunk request/response wire protocol (`Has`/`Get`, length-framed, with
  bounded reads) over any `io.ReadWriteCloser` — `Client.Get` is the callback
  `chunk.Reconstruct`/`syncx.Import` consume; a `MapSource` serves the
  address→ciphertext map `chunk.Split` produces. (2) A passphrase-derived
  Ed25519 **peer identity** that becomes a **pinned mutual-TLS** config: both
  devices derive the same keypair from the passphrase, and each pins the peer's
  cert to that public key — so authentication is "proves knowledge of the shared
  passphrase," with no CA, no enrollment, and no transport account (TLS 1.3's
  CertificateVerify supplies the private-key-possession proof). (3) A LAN
  server/client over loopback TCP wiring the two. The transport only ever sees
  opaque ciphertext. Verified end-to-end over real mutual-TLS, including the full
  `chunk.Split` → serve → `Client.Get` → `chunk.Reconstruct` seam and that a
  wrong-passphrase dialer is rejected at the handshake. The listener swallows
  per-connection failures (one bad peer must not take it down) but reports them
  to an optional `WithConnErrorHandler` observer — so a failed handshake (the
  status layer's auth-failed) or mid-serve error is visible, not black-holed.
  The `storage.sync` signaling adapter (endpoint + pubkey exchange) lands later
  with the extension.
- Tier 1 cross-network rendezvous — foundation (transport doc §4, build step 8):
  - `internal/stunc`: a minimal RFC 8489 STUN Binding client that answers "what
    is my public IP:port?" — open a UDP socket, query a public STUN server, decode
    the XOR-MAPPED-ADDRESS (legacy MAPPED-ADDRESS as fallback). Hand-rolled,
    dependency-free (~100 lines) rather than pulling the full `pion` tree, since
    reflexive discovery is all that is needed until hole-punching is built; `pion`
    earns its place at the ICE step. The caller owns the `net.PacketConn` so the
    socket STUN measured is the one the data path later reuses. Tested hermetically
    against an in-process STUN responder (XOR + legacy forms, stray-datagram skip,
    context timeout).
  - `internal/rendezvous`: the signaling contract. A `Beacon` (device id, label,
    LAN endpoints, reflexive candidate, caller-supplied issued-at) is AEAD-sealed
    with the passphrase-derived key before publication — the carrier (Firefox Sync)
    sees only ciphertext, and a beacon that `Open`s is thereby proven to come from
    a passphrase-holder (no separate signature; domain-separated by AAD from chunk
    ciphertext). A `Signaling` interface abstracts the carrier; `DirSignaling` (a
    shared directory, atomic 0600 writes, self-excluding fetch) is the test and
    manual-rendezvous impl. The production carrier (the companion extension over
    `storage.sync`) and NAT hole-punching (`pion/ice`) are the next Tier-1 steps.
  - macOS note: both packages are OS-agnostic and cross-compile clean for darwin
    alongside the ffctl split above.
- `gusset sync` Tier-1 wiring (`--rendezvous-dir`, transport doc §4): two machines
  off the same LAN now sync by trading sealed beacons through a shared folder, no
  companion extension required yet. Each pass gathers this device's beacon (every
  non-loopback IPv4 at the listener port, plus a STUN reflexive candidate when
  `--stun host:port` is given), publishes it via `rendezvous.DirSignaling`, and
  fetches peers' beacons — opening only those sealed by the same passphrase and
  dropping the stale. A peer's candidates are dialed in order (LAN first, then the
  reflexive candidate as a best-effort direct-NAT attempt), reusing the existing
  mutual-TLS dial+reconcile path; the connected candidate's kind sets the status
  `Link` (`lan` / `direct-nat`). Rendezvous merges with mDNS so a peer reachable
  both ways is pulled once. `--device-id` overrides the default per-device id
  (hostname). The reflexive candidate is our public IP at the listener port —
  honest for the port-forward / easy-NAT case; robust hole-punching for harder NAT
  pairs is the deferred ICE step. The shared folder only ever holds opaque sealed
  beacons.
- `internal/rendezvous` hardening: `DirSignaling.Fetch` now bounds what it reads
  from the carrier (≤64 KiB per beacon via a limited reader, not a TOCTOU-racy
  stat; ≤256 beacons per fetch). The carrier is an untrusted courier — a writer
  with folder access but no passphrase cannot forge a beacon that Opens, but could
  otherwise exhaust memory with one giant file or a flood of small ones; the caps
  close that while staying generous for honest use (a real beacon is a few hundred
  bytes, and you sync a handful of your own devices).
- `internal/chunk`: content-defined chunking (restic/chunker / FastCDC) →
  per-chunk keyed addressing + AEAD, plus signed manifest types. The chunker
  polynomial is derived per-user from the key (`crypto.Stream`), so boundaries
  are deterministic across the user's machines (dedup works) but not globally
  predictable. `Reconstruct` enforces the M2 invariant (open with address-AAD +
  re-verify address) and the manifest carries a keyed signature so reordering or
  dropping chunks is detected. `Missing` supports resumable, fetch-only-what's-
  absent transfer.
