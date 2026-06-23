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
