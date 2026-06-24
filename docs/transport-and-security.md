# gusset — transport, privacy & security

Authoritative design for the data plane and its security model; the architecture
overview is in [design.md](design.md). Decisions here were made deliberately with
the threat of syncing sensitive extension data (the canonical example: a
password-manager extension) front of mind.

## The pivot, in one paragraph

gusset moves an extension's `storage.local` directly **device-to-device**, over
a peer-to-peer connection that is signaled through the control plane we already
have (the companion extension's `storage.sync`, carried by Firefox Sync). There
is **no third-party transport account and no server holding your data** — the
only account is the Firefox Account you already use. Chunks are encrypted with a
key derived from a single passphrase before they ever leave the machine. Sync is
**opportunistic**: two of your machines converge whenever they are online at the
same time. We do not keep durable history — convergence, not archival, is the
goal.

## 1. Threat model

**The data.** `storage.local` can hold OAuth/refresh tokens, session cookies,
API keys, decrypted secrets, push-subscription endpoints, device-bound tokens,
and password-manager material. Treat all of it as potentially secret.

**The bar.** Today this data lives only in the local profile, or — for the
manifest — on Firefox Sync, which is end-to-end encrypted (Mozilla cannot read
it). **gusset must not drop below that bar.** Anything we add must keep the data
either local-only or E2E-encrypted.

**Adversaries considered:**

| Adversary | Mitigation |
|---|---|
| Anyone on the network path between devices | Connection is DTLS-encrypted (pion) *and* chunks are AEAD-encrypted app-side. |
| Whoever can read the Firefox Sync account (FxA password holder) | Sync carries only manifests (keyed HMAC hashes) + signaling blobs — opaque without the passphrase-derived keys. No plaintext, no usable secrets. |
| A STUN server | Sees only a device's public IP:port (which it is asking about) — never any data. |
| Local attacker with disk access | Already game over — they can read the profile directly. gusset adds only short-lived `0600` temp snapshots, cleaned promptly. |
| A lost/stolen paired device | Holds the passphrase-derived key. Revocation = rotate the passphrase on remaining devices. Documented limitation, not solved in v1. |

**Explicitly out of scope for v1:** protecting against a compromised local OS
account; TURN-relay anonymity; forward secrecy across passphrase rotation.

## 2. Encryption & keys — one passphrase is the root secret

The user holds a single secret: an **8-word passphrase** (diceware-style,
≈88 bits). It is never stored on any transport or in any manifest. From it,
Argon2id derives (via HKDF labels) everything:

- **`K_enc`** — per-chunk AEAD (XChaCha20-Poly1305 or AES-256-GCM), random nonce
  per chunk. This is app-layer encryption *on top of* the connection's DTLS —
  defense in depth, and the only thing protecting a chunk at rest if it is ever
  parked anywhere.
- **`K_addr`** — keyed content-addressing: a chunk's id is
  `HMAC(K_addr, plaintext)`, not `hash(plaintext)`. Dedup across *your own*
  devices still works; the global confirmation-oracle and cross-user
  linkability of plain content-hashing are gone.
- **Peer authentication** — a keypair / PSK so a device only connects to devices
  that prove knowledge of the same passphrase. Pairing a new machine is just:
  type the 8 words. One secret = encryption + addressing + peer auth + no
  account.

Pipeline order matters: **CDC-chunk the plaintext first, then encrypt each
chunk.** Encrypting before chunking would destroy dedup. Chunk *sizes* remain
visible to an on-path observer (minor structural metadata leak); padding is a
later option, not v1.

**Hardening decisions (from the d09a9a0 security review):**

- **M1 — salt & passphrase strength.** The recommended derivation uses a
  **per-user random salt** (`crypto.NewSalt`), generated once and shared at first
  pairing alongside the passphrase. It restores Argon2id's per-target cost and
  removes cross-user key/address linkage. `crypto.AppSalt` (fixed) remains only
  for the no-shared-state "passphrase-alone" fallback. User-supplied passphrases
  must pass `crypto.ValidatePassphrase` **at setup** (a coarse structural floor —
  the real assurance is `GeneratePassphrase` over a quality wordlist); derivation
  thereafter is not re-gated.
- **M2 — bind ciphertext to its address.** `crypto.Seal/Open` take the chunk's
  content-address as **AAD**, so a ciphertext served from the wrong address fails
  `Open`. **Invariant the chunk layer MUST uphold:** seal each chunk with
  `aad = []byte(address)` where `address = K_addr-HMAC(plaintext)`, and after
  `Open` **re-verify** `Address(plaintext) == requested address` before trusting
  the bytes. Belt (AAD) and suspenders (post-decrypt address check) — a chunk
  must never be accepted from an address it wasn't sealed under.

## 3. Policy — safe by default, opt-in per extension

- **Empty allowlist by default. gusset syncs nothing until you add an extension
  ID.** There is no "sync everything" mode.
- **Sensitive denylist, deny-with-override.** A built-in list of known
  credential / 2FA extensions (1Password, Bitwarden, KeePassXC, LastPass, Authy,
  …) is refused by default. A loud, per-extension override exists for the user
  who truly means it — but the default is no. Rationale holds even with
  encryption: a password manager's `storage.local` is either its own
  E2E-encrypted vault (redundant with its own sync, often device-bound) or
  session/unlock tokens that are device-specific and dangerous to propagate.
- **Device-specific data caveat.** Blob-level v1 cannot strip per-device fields
  (absolute paths, push endpoints, device tokens) — that needs the v2 key-level
  decoder. So allowlisting is a per-extension judgement the user makes; gusset
  warns when an extension looks risky.

## 4. Transport — direct device-to-device, in tiers

The realistic deployment is two machines that are usually **on at the same time
and usually on the same WiFi**. So we build the simplest tier that serves that,
and keep the harder tiers as clean extensions of the same `Transport` interface.
Each tier is surfaced in status so the user always knows which one is in use.

**Tier 0 — same-LAN direct (this is v1).** Each device publishes its LAN
endpoint (`IP:port`) and peer-auth public key under its own `storage.sync` key;
peers read it and dial directly over the local network. (mDNS local discovery is
an optional convenience on top.) The connection is **mutual-auth TLS** with
certificates derived from the passphrase keypair — that gives peer
authentication and channel encryption with stdlib crypto. Encrypted chunks
(already AEAD'd with `K_enc`) stream over it. **No STUN, no ICE, no
hole-punching, no relay.** This is the lightest thing that works for the
same-WiFi case, and it always works there.

**Tier 1 — cross-network NAT traversal (in progress).** When the peers are on
different networks, each device gathers its server-reflexive candidate via public
STUN (`stun.l.google.com:19302` — free, stateless, data-blind, no account),
publishes it (plus its LAN candidates) in a **sealed beacon** over a signaling
channel, reads its peers' beacons, and connects. Firefox Sync is the signaling
carrier; we cache a peer's last-known direct endpoint to skip the minutes-slow
first rendezvous. Best-effort: symmetric-NAT-both-ends pairs still won't connect,
and that is shown as status, not hidden.

Tier 1 decomposes into four pieces; the foundation is built, the two hard
externally-dependent pieces are the next steps:

- **Reflexive discovery — `internal/stunc` (done).** A minimal RFC 8489 Binding
  client: open a UDP socket, ask a public STUN server "what is my public
  IP:port?", get back the XOR-MAPPED-ADDRESS. Hand-rolled (~100 lines, no
  dependency) rather than pulling the full `pion` tree, because reflexive
  discovery is all that is needed until hole-punching exists. The caller owns the
  socket so the same local port can later carry data.
- **Signaling contract + beacons — `internal/rendezvous` (done).** A `Beacon`
  (DeviceID, Instance, LAN endpoints, reflexive candidate, issued-at) is
  AEAD-**sealed** with the passphrase-derived key before publication — the carrier
  sees only ciphertext, and `Open` succeeding *is* the authentication (a beacon
  that opens came from a passphrase-holder; domain-separated by AAD from chunk
  ciphertext). A `Signaling` interface abstracts the carrier; `DirSignaling` (a
  shared directory) is the test/manual-rendezvous impl, mirroring how the
  transport ran over loopback before mDNS.
- **Companion extension + storage.sync carrier (next).** The production
  `Signaling`: the companion extension writes each device's sealed beacon to its
  own `storage.sync` key (carried E2E-encrypted by Firefox Sync) and reads peers'.
  Browser-side; needs a live Sync round-trip to validate. Stays a *good Sync
  citizen* — a beacon is a few hundred bytes, low-frequency, never bulk.
- **NAT hole-punching (next).** An ICE agent (`pion/ice` earns its place here)
  doing the simultaneous-open over the gathered candidates, for the NAT pairs a
  direct dial to the reflexive/LAN endpoints cannot reach. The cached-known-
  endpoint and easy-NAT cases work from the beacon endpoints alone before this.

Reusing the same socket the beacon advertised is why `stunc.Reflexive` takes a
caller-owned `net.PacketConn`: the reflexive mapping is per-socket, so the data
path must use the socket STUN measured.

**Tier 2 — relay (later, only if wanted).** An optional user-run relay for the
pairs Tier 1 can't punch. A server, so it's opt-in and off by default; the
"no account, no server" default stays intact for everyone who doesn't need it.

**Signaling, all tiers.** Devices exchange reachability (LAN/public endpoints +
pubkey) by writing their own namespaced `storage.sync` key and reading peers'.
Firefox Sync is, in effect, the signaling server — none to run.

## 5. Convergence model

- **Opportunistic, eventually-consistent.** Peers converge whenever both are
  online (the assumed common case: both up, same WiFi). There is no
  store-and-forward: if no two devices are up together, nothing moves — by
  design, and acceptable because we sync settings (rare changes), not a live
  feed. Status always shows "waiting — peer offline" rather than failing
  silently.
- **No durable history.** We dropped the durable chunk store, so the "free
  version history / rollback" bonus from the original sketch is **gone**. This is
  a deliberate trade for "no account, no server." If rollback is wanted later it
  needs a durable (encrypted) store behind the same interface.
- **Atomic apply-on-complete, resumable.** The receiver reconstructs and swaps a
  new blob only when every chunk in the manifest is present and hash-verified;
  until then the old settings stay live. Interrupted transfers just resume —
  re-request the missing keyed hashes. (Apply also does the Firefox UUID rewrite;
  see docs/firefox-internals-verified.md DELTA 2.)
- **Conflict policy.** Last-writer-wins per extension by timestamp (v1,
  blob-level), unchanged from the original design.

## 6. Status — never sync silently

A P2P system is legitimately "not synced" often (peer asleep, NAT failed,
extension denylisted). That is only acceptable if gusset **always shows why.**
Every non-converged state carries a visible, human-readable reason.

**Peer connectivity** (per paired device):
`discovering → signaling → hole-punching → connected (lan|direct-nat) |
unreachable(reason)`, where reason ∈ {peer-offline, nat-traversal-failed,
auth-failed}.

**Per-extension sync state** (per peer):
`in-sync(since T) | pushing(n left) | pulling(n left) |
stale(peer-offline) | blocked(denylisted; override with …) | error(detail)`.

**Surfaced through three channels, all reading one status source:**
- `gusset status` — rich CLI, sibling to `gusset doctor`.
- The companion extension UI — per-extension × per-device grid, the natural home.
- A machine-readable JSON status over the localhost WebSocket (the same channel
  the extension already uses to reach the daemon — see the design's
  daemon↔extension channel), so the UI just renders what the daemon reports.

Design rule: there is no silent "nothing happened." If an allowlisted extension
is not converging, `gusset status` names the reason.

## 7. Local hygiene

- Snapshot temp files are `0600`, in a restricted dir, removed after chunking
  (the snapshot path already removes on failure; extend to success).
- Never log `storage.local` values or key material.
- The v1 blob-level "treat as opaque" stance helps: gusset never parses or
  inspects the secrets it moves.

## 8. Daemon lifetime — on-demand by default

gusset is **one binary with a caller-chosen lifetime**, not a mandatory
background service. The "daemon" is not a separate program; it is `gusset sync`
told to *stay reachable* instead of *sync once and exit*. The same transport,
reconcile, and status code runs in every mode — only how long the listener stays
open changes. This means the privacy-conscious path is the default, and nobody
is forced into a resident process.

**Three lifetimes:**

| invocation | behavior | for |
|---|---|---|
| `gusset sync` | advertise + browse mDNS, converge with any peer that appears within a short grace, then **exit** | the default — run it on both machines, it syncs, it's gone |
| `gusset sync --for D` | hold the listener open for a bounded **setup window** (e.g. `--for 10m`), serving and syncing, then exit | one-time setup / onboarding a new machine; convenience of a daemon with a guaranteed end |
| `gusset sync --watch` | stay reachable indefinitely (optionally under `systemd --user` / launchd) | opt-in set-and-forget only |

`--peer host:port` skips mDNS and dials a named address (manual rendezvous).

**Why on-demand is the stronger security/privacy posture** (the reason it is the
default, not just an option):

- The **listening socket and mDNS advertisement exist only during the window** —
  no persistent open port for other local processes to probe, no always-on
  service to compromise.
- **Key material lives only for the run.** The passphrase → Argon2id derivation
  happens per-invocation (a few hundred ms), so decryption-capable keys are not
  resident in a long-lived process's memory between syncs.
- **Nothing auto-starts and nothing touches `storage.local` unless invoked.**
  "Never sync silently" (§6) sharpens to "never sync unless you ran it";
  `--for`/`--watch` are an explicit, opt-in escalation, never the default.

The `--for` window is the sweet spot for one-time setup: the convenience of a
daemon (the peer can connect whenever it is ready during the window) with the
bounded lifetime of a one-shot (guaranteed gone afterward — no residue).

**Supersedes the DELTA 4 "run as a user service" default for v1.** That default
(docs/firefox-internals-verified.md) assumed a persistent daemon reached over a
localhost WS by the extension. v1 instead is the on-demand binary above,
discovered on the LAN by **mDNS** (`_gusset._tcp`), so it needs no always-on
service, no localhost WS, no companion extension, and no Firefox Sync — peer auth
still comes from the passphrase (§2). The companion extension + Firefox-Sync
signaling move to **Tier 1** (cross-network rendezvous, where mDNS cannot reach);
the user-service daemon stays as an opt-in for set-and-forget.

**Apply still needs Firefox closed on the receiving side** (`store.Apply` refuses
a locked profile — DELTA 2 / `ErrProfileLocked`). A one-shot run surfaces this
immediately and legibly; snapshotting the source works with Firefox open, only
the write does not.

## Build order implied by this design

1. `internal/policy` — allowlist (empty default) + sensitive denylist
   (deny-with-override).
2. `internal/crypto` — passphrase → Argon2id → `K_enc`/`K_addr`/peer-auth; AEAD;
   keyed addressing.
3. `internal/chunk` — FastCDC (restic/chunker) → keyed-hash + encrypt per chunk;
   manifest types.
4. `internal/transport` — **Tier 0 LAN-direct** backend: pinned mutual-TLS
   (peer-auth from the passphrase keypair) + the chunk request/response protocol.
   Tiers 1–2 (STUN/ICE via Firefox-Sync signaling, relay) are later. ✅ done.
5. `internal/store` Apply path — UUID rewrite + atomic swap. ✅ done.
6. Status plumbing — `internal/status`, a single source rendered by `gusset
   status` (and, at Tier 1, the WS + extension UI). ✅ done.
7. **`internal/discovery` + `gusset sync`** — mDNS advertise/browse for LAN
   rendezvous, and the on-demand `sync` command (§8) wiring discover → mutual-TLS
   connect → exchange manifests → pull-missing → apply → report, with
   `--for`/`--watch`/`--peer` selecting the listener lifetime. This is v1's
   primary surface; no daemon, extension, or Firefox Sync required.
8. **Tier 1 cross-network rendezvous** — foundation done: `internal/stunc`
   (reflexive candidate via STUN) + `internal/rendezvous` (sealed beacons + a
   `Signaling` interface, with a filesystem `DirSignaling` for tests/manual
   rendezvous). Next: the companion extension as the `storage.sync` `Signaling`
   carrier, NAT hole-punching (`pion/ice`) for the hard NAT pairs, and the
   `gusset sync` wiring (gather → seal+publish → fetch → dial), plus a status-grid
   UI; opt-in user-service daemon for set-and-forget.
