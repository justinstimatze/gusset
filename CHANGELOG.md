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
