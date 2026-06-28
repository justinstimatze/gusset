# Contributing to gusset

Thanks for your interest. gusset is a small, security-sensitive tool — it moves
a user's extension data between their devices — so changes favor clarity and a
conservative threat model over features.

## Development

Requires the Go toolchain pinned in [`go.mod`](go.mod) (the `go` and `toolchain`
directives; `go` fetches the toolchain automatically).

```sh
make build   # ./gusset, version stamped from the git tag
make test    # go test -race ./...
make lint    # golangci-lint (errcheck, gosec, staticcheck, ...)
make fmt     # gofmt -w .
```

Before opening a pull request, `make test` and `make lint` should both pass.
CI additionally runs `govulncheck` and CodeQL.

## Conventions

- **Go is the default.** Match the surrounding code's style; `gofmt` is enforced
  in CI.
- **Tests are expected.** New behavior comes with tests; bug fixes come with a
  test that fails before the fix. Untrusted-input parsers and anything on the
  crypto/transport path especially.
- **Keep the trust model intact.** The passphrase is the only shared secret and
  is never written to the config or logged. Data derived from a peer is treated
  as hostile until validated. If a change touches authentication, key
  derivation, or how remote data reaches the filesystem, say so explicitly in
  the PR description.
- **Be a good Firefox Sync citizen.** Only tiny manifests/beacons ride
  `storage.sync`; bulk data moves over gusset's own transport. Don't add
  anything that polls Mozilla's servers or forces a sync.

## Reporting bugs and requesting features

Use the issue templates. For anything security-sensitive, do **not** open a
public issue — follow [SECURITY.md](SECURITY.md) instead.
