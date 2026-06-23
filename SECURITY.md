# Security Policy

gusset moves your Firefox extension settings between your own machines over a
passphrase-derived encrypted channel. A flaw here can expose those settings or
let an unpaired device connect, so security reports are taken seriously.

## Supported versions

gusset is pre-1.0 and under active development. Only the latest commit on `main`
is supported; please confirm an issue reproduces there before reporting.

## Reporting a vulnerability

Please report privately rather than opening a public issue:

- GitHub **private vulnerability reporting**: the "Report a vulnerability" button
  under this repository's **Security** tab, or
- email **justin@justinstimatze.com**.

Include what you did, what you expected, and what happened — a proof of concept
helps. You'll get an acknowledgement, and a fix or explanation once the report is
triaged. Please give a reasonable window to address the issue before any public
disclosure.

## Threat model (what gusset defends)

The authoritative design and threat model live in
[docs/transport-and-security.md](docs/transport-and-security.md). In short:

- The only shared secret is an 8-word passphrase; all keys derive from it.
  Connecting requires proving knowledge of it (passphrase-derived mutual TLS) —
  there is no account, server, or CA to compromise.
- Carriers that move data on gusset's behalf (the device-to-device transport, and
  the Tier-1 rendezvous folder / Firefox Sync) only ever see ciphertext.
- Out of scope for v1: forward secrecy and per-device revocation — a lost device
  is handled by rotating the passphrase everywhere.
