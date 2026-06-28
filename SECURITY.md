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

## Privacy: what is exposed, and to whom

gusset is built to need no account and no server, and to keep your settings — and
who-syncs-with-whom — out of any third party's hands. What that does and does not
hide:

**Kept private by design**

- Every beacon that crosses a carrier (a `--rendezvous-dir` folder, or Firefox
  Sync's `storage.sync`) is sealed: the carrier sees only ciphertext, never your
  endpoints, device names, or settings. The settings themselves move
  device-to-device, never through the carrier.
- No telemetry, analytics, or update pings — the daemon makes no outbound
  connection you did not ask for, and the extension talks only to the local daemon
  over loopback (enforced by its content-security policy; it requests only the
  `storage` permission and declares no data collection).
- The passphrase is never written to the config, logged, sent, or placed in a
  certificate or beacon. The mutual-TLS certificate carries a fixed name
  (`gusset-peer`) and nothing identifying, and TLS 1.3 keeps even that off the wire.

**Metadata that is visible, and to whom**

- **On a local network**, gusset announces itself over mDNS so your devices can
  find each other. Anyone on that network can see that a gusset device is present
  and its random device id — which is **opaque, not derived from your hostname**,
  so it does not reveal your machine's name.
- **Through Firefox Sync**, the per-device beacon keys let your *own* Sync account
  see that gusset is installed and roughly how many devices you have. The values
  stay encrypted; Mozilla cannot read them.
- **A shared rendezvous folder** holds one sealed file per device, so its file
  count reveals your device count to anyone who can read the folder. The contents
  stay sealed.
- **STUN is opt-in.** If you pass `--stun`, the STUN server you name learns your
  public IP. To sync across networks without contacting any third party, use a
  shared folder (`--rendezvous-dir`) instead.
