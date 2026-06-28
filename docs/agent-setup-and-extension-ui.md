# gusset — agent-friendly setup & extension UI (forward-looking notes)

Not committed design — captured early because both concerns shape interfaces
we're about to build (the CLI surface and the daemon↔extension status channel).

## Agent-friendly setup

Assume the person setting gusset up on a new machine is driving an agent (likely
Claude). Optimize the CLI so an agent can do the whole thing without a human at
a TTY, and can *verify and explain* what happened.

- **Non-interactive everything.** No required interactive prompts. The passphrase
  is suppliable as `--passphrase-file`, `--passphrase-env`, or stdin — never a
  forced TTY prompt, so the agent never has to echo a secret into a terminal.
  `gusset keygen` emits a fresh 8-word phrase for the first machine.
- **One idempotent `gusset init`.** Detects the profile (snap/flatpak/plain),
  sets up the key, writes config, installs the user service
  (`systemd --user` / launchd), and prints next steps. Safe to re-run.
- **`--json` on everything that reports** (`doctor`, `status`, errors).
  Structured output + meaningful exit codes let an agent branch instead of
  scraping prose. An agent should be able to read `gusset status --json` and tell
  the user "synced, last converged 2m ago" or "waiting — your laptop is offline."
- **`doctor` recommends an allowlist.** It already lists installed extensions; it
  should also flag which are denylisted-sensitive, so the agent can propose a
  sensible allowlist and explain the password-manager exclusion.
- **Pairing is scriptable.** `gusset pair` on machine B with the same passphrase;
  status reports `paired` so the agent can confirm both ends before declaring
  done. No QR-code-only flows that block automation.
- **Dry run / plan.** `gusset sync --plan` shows what would move (which
  extensions, how many chunks) without doing it — lets an agent preview and the
  user approve.
- **Self-explaining failures.** Every error an agent might hit (peer offline, not
  same-LAN, extension denylisted, passphrase mismatch) is a typed, JSON-able
  reason, matching the never-sync-silently status model.

The throughline: the same status source that powers the UI also powers the
agent. Build the machine-readable status once; humans and agents both read it.

## Extension UI

The companion WebExtension is a **control panel + status dashboard**, not a
worker — the daemon does the work; the extension talks to it over the localhost
WebSocket and renders what it reports. What it benefits from:

- **Per-extension × per-device status grid.** The heart of it: each allowlisted
  extension × each paired device, showing `in-sync (2m ago)` / `pushing 3` /
  `waiting — peer offline` / `blocked — sensitive`. Direct rendering of the §6
  status model.
- **Allowlist management.** List installed extensions, toggle sync per extension.
  Sensitive ones are visibly flagged with the deny-with-override gate behind an
  explicit confirm. A "what will be synced" preview before enabling.
- **Pairing flow.** Show this device's pairing state, accept the 8-word
  passphrase, list paired peers, and show which connection tier is live
  (LAN-direct / NAT / offline) so the user understands their reachability.
- **Trust transparency.** Surface that data is E2E-encrypted, peers are
  authenticated by the passphrase, and nothing sits on a third-party server — the
  security posture is a feature; show it.
- **Restore** (only if durable history ever returns; not v1).

Scope discipline: keep it small. If it grows past a trivial popup, build it as a
proper Vite + React + Tailwind + shadcn options page rather than hand-rolled
HTML/CSS (house frontend default), consuming the daemon's JSON status — same data
the agent reads.
