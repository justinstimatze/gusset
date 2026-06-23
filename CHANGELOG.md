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
