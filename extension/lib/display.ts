// Shared presentation for connection and sync states, so the popup and the
// dashboard render the same vocabulary and colors.

import type { ConnState } from "./daemon";
import type { ExtSync, Peer, PeerState, SyncState } from "./protocol";

export const connMeta: Record<
  ConnState,
  { dot: string; label: string; hint?: string }
> = {
  idle: {
    dot: "bg-zinc-400",
    label: "Not set up",
    hint: "Add the daemon address and token to connect.",
  },
  connecting: { dot: "bg-amber-400 animate-pulse", label: "Connecting…" },
  connected: { dot: "bg-emerald-500", label: "Connected" },
  offline: {
    dot: "bg-red-500",
    label: "Daemon not running",
    hint: "Start it with `gusset sync --ws`.",
  },
  "auth-failed": {
    dot: "bg-red-500",
    label: "Token rejected",
    hint: "Re-check the token from `gusset ws-token`.",
  },
};

export const peerDot: Record<PeerState, string> = {
  connected: "bg-emerald-500",
  discovering: "bg-amber-400",
  signaling: "bg-amber-400",
  "hole-punching": "bg-amber-400",
  unreachable: "bg-red-500",
};

// syncMeta gives each per-extension sync state a short label and a badge style.
export const syncMeta: Record<SyncState, { label: string; cls: string }> = {
  "in-sync": {
    label: "in sync",
    cls: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  },
  pushing: {
    label: "pushing",
    cls: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  },
  pulling: {
    label: "pulling",
    cls: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  },
  pending: {
    label: "pending",
    cls: "bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300",
  },
  stale: {
    label: "stale",
    cls: "bg-zinc-200 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-300",
  },
  blocked: {
    label: "blocked",
    cls: "bg-orange-100 text-orange-800 dark:bg-orange-950 dark:text-orange-300",
  },
  error: {
    label: "error",
    cls: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
  },
};

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
  if (e.state === "pushing" || e.state === "pulling") {
    const n = e.remaining ?? 0;
    return (
      e.detail ||
      `${n} chunk${n === 1 ? "" : "s"} left to ${e.state === "pushing" ? "push" : "pull"}`
    );
  }
  return e.detail || m.label;
}
