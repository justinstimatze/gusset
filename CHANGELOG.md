# Changelog

## Unreleased

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
- Design: codified "Be a good Firefox Sync citizen" in HANDOFF.md — bulk data
  never rides `storage.sync`, no forced syncs or polling of Mozilla's servers.
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
- `internal/chunk`: content-defined chunking (restic/chunker / FastCDC) →
  per-chunk keyed addressing + AEAD, plus signed manifest types. The chunker
  polynomial is derived per-user from the key (`crypto.Stream`), so boundaries
  are deterministic across the user's machines (dedup works) but not globally
  predictable. `Reconstruct` enforces the M2 invariant (open with address-AAD +
  re-verify address) and the manifest carries a keyed signature so reordering or
  dropping chunks is detected. `Missing` supports resumable, fetch-only-what's-
  absent transfer.
