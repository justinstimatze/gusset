# gusset — transport, privacy & security

Authoritative design for the data plane and its security model. This supersedes
the original "git store-and-forward" sketch in HANDOFF.md. Decisions here were
made deliberately with the threat of syncing sensitive extension data (the
canonical example: a password-manager extension) front of mind.

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

**Tier 1 — cross-network NAT traversal (later).** When the peers are on
different networks, add `pion` (`pion/ice` + `pion/stun`, pure-Go) to gather
server-reflexive candidates via public STUN (`stun.l.google.com:19302` — free,
stateless, data-blind, no account) and hole-punch. Firefox Sync remains the
signaling channel; we cache a peer's last-known direct endpoint to skip the
minutes-slow first rendezvous. Best-effort: symmetric-NAT-both-ends pairs still
won't connect, and that is shown as status, not hidden.

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
  the extension already uses to reach the daemon — see HANDOFF "daemon↔extension
  channel"), so the UI just renders what the daemon reports.

Design rule: there is no silent "nothing happened." If an allowlisted extension
is not converging, `gusset status` names the reason.

## 7. Local hygiene

- Snapshot temp files are `0600`, in a restricted dir, removed after chunking
  (the snapshot path already removes on failure; extend to success).
- Never log `storage.local` values or key material.
- The v1 blob-level "treat as opaque" stance helps: gusset never parses or
  inspects the secrets it moves.

## Build order implied by this design

1. `internal/policy` — allowlist (empty default) + sensitive denylist
   (deny-with-override).
2. `internal/crypto` — passphrase → Argon2id → `K_enc`/`K_addr`/peer-auth; AEAD;
   keyed addressing.
3. `internal/chunk` — FastCDC (restic/chunker) → keyed-hash + encrypt per chunk;
   manifest types.
4. `internal/transport` — `Transport` interface + **Tier 0 LAN-direct** backend
   (Sync-signaling adapter for endpoint exchange, mutual-TLS dial, peer-auth
   from the passphrase keypair). Tiers 1–2 (STUN/ICE, relay) are later.
5. `internal/store` Apply path — finish the other half (UUID rewrite, atomic
   swap).
6. Status plumbing — a single status source surfaced via `gusset status`, the
   localhost WS, and the extension UI.
