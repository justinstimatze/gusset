// Shared presentation for connection and sync states, so the popup and the
// dashboard render the same vocabulary and colors. Colors come from the Firefox
// semantic tokens in assets/theme.css (light-dark aware), not Tailwind defaults.

import type { ConnState } from "./daemon";
import type { ExtSync, Peer, PeerState, SyncState } from "./protocol";

export const connMeta: Record<
  ConnState,
  { dot: string; label: string; hint?: string }
> = {
  idle: {
    dot: "bg-[var(--neutral)]",
    label: "Not set up",
    hint: "Add the daemon address and token to connect.",
  },
  connecting: { dot: "bg-[var(--info)]", label: "Connecting…" },
  connected: { dot: "bg-[var(--ok)]", label: "Connected" },
  offline: {
    dot: "bg-[var(--err)]",
    label: "Daemon not running",
    hint: "Start it with `gusset sync --ws` — or install the gusset app if you don’t have it yet.",
  },
  "auth-failed": {
    dot: "bg-[var(--err)]",
    label: "Token rejected",
    hint: "Re-check the token from `gusset ws-token`.",
  },
};

export const peerDot: Record<PeerState, string> = {
  connected: "bg-[var(--ok)]",
  discovering: "bg-[var(--info)]",
  signaling: "bg-[var(--info)]",
  "hole-punching": "bg-[var(--info)]",
  unreachable: "bg-[var(--warn)]",
};

// syncMeta gives each per-extension sync state a short label and a badge style
// (Firefox semantic foreground + a translucent tint that works on either theme).
export const syncMeta: Record<SyncState, { label: string; cls: string }> = {
  "in-sync": { label: "in sync", cls: "text-[var(--ok)] bg-[var(--ok-bg)]" },
  pushing: { label: "pushing", cls: "text-[var(--info)] bg-[var(--info-bg)]" },
  pulling: { label: "pulling", cls: "text-[var(--info)] bg-[var(--info-bg)]" },
  pending: { label: "pending", cls: "text-[var(--warn)] bg-[var(--warn-bg)]" },
  stale: {
    label: "stale",
    cls: "text-[var(--neutral)] bg-[var(--neutral-bg)]",
  },
  blocked: { label: "blocked", cls: "text-[var(--warn)] bg-[var(--warn-bg)]" },
  error: { label: "error", cls: "text-[var(--err)] bg-[var(--err-bg)]" },
};

// isTransferring marks the states that have an in-flight chunk transfer — the
// ones worth a progress bar so the user can see a slow sync is still moving.
export function isTransferring(e: ExtSync): boolean {
  return e.state === "pushing" || e.state === "pulling";
}

// progressFraction returns 0..1 when the total is known, or null for an
// indeterminate transfer (show motion, not a percentage).
export function progressFraction(e: ExtSync): number | null {
  if (!e.total || e.total <= 0) return null;
  const done = e.total - (e.remaining ?? 0);
  return Math.min(1, Math.max(0, done / e.total));
}

// peerWhy and extWhy mirror the daemon's "never sync silently" rule: a
// non-converged state always reads with a reason.
export function peerWhy(p: Peer): string {
  if (p.state === "connected")
    return p.link ? `connected (${p.link})` : "connected";
  const base =
    p.reason === "auth-failed"
      ? "wrong passphrase"
      : p.reason === "nat-traversal-failed"
        ? "couldn't punch a route"
        : p.reason === "peer-offline"
          ? "peer offline"
          : p.state;
  return p.detail ? `${base} — ${p.detail}` : base;
}

export function extWhy(e: ExtSync): string {
  const m = syncMeta[e.state];
  if (isTransferring(e)) {
    const n = e.remaining ?? 0;
    return (
      e.detail ||
      `${n} chunk${n === 1 ? "" : "s"} left to ${e.state === "pushing" ? "push" : "pull"}`
    );
  }
  return e.detail || m.label;
}
