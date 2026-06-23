# gusset

A small daemon that syncs **Firefox extension settings** (the data extensions
keep in `storage.local`) across your machines — the one thing Firefox Sync does
*not* carry. Firefox Sync already moves bookmarks, history, logins, open tabs,
the *list* of installed add-ons, and an allowlist of prefs. It does not move an
extension's own stored settings, so a fresh Firefox reinstalls uBlock Origin but
leaves it with default filters. gusset fills exactly that seam.

The name is the metaphor: a gusset is the small inserted piece that joins two
parts and keeps the seam from failing. gusset joins your machines at the one
joint Firefox leaves open.

Target: **Firefox first**, on **macOS and Linux**. See [HANDOFF.md](HANDOFF.md)
for the full design and [docs/firefox-internals-verified.md](docs/firefox-internals-verified.md)
for the load-bearing internals, verified against a live profile.

## Status

Early. The build tooling and the Firefox profile resolver
(`internal/profile`) are in place; the store, chunker, transport, sync engine,
and companion extension are not yet built.

## Build

```sh
make build      # ./gusset, version stamped from the git tag
make test       # go test -race ./...
make lint       # golangci-lint
```

## Usage

```sh
gusset version  # build version
gusset doctor   # resolve the active Firefox profile, list installed extensions
```

`doctor` is read-only — it touches nothing, and is the quickest way to confirm
gusset can find your profile.

## License

MIT — see [LICENSE](LICENSE).

Contact: justin@justinstimatze.com
