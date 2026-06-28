// DaemonClient is the extension's connection to the gusset daemon's localhost
// WebSocket. It owns the connection lifecycle: first-frame token auth, parsing
// the status/beacon stream, reporting peer beacons, and reconnecting with
// exponential backoff when the daemon is down. The browser WebSocket API has no
// native heartbeat, so a dead connection is surfaced as a state, not a hang.
//
// Connection state is explicit so the UI can show an actionable status (notably
// "daemon not running") rather than a silent spinner.

import type {
  AuthMsg,
  PeersMsg,
  ServerMsg,
  SetNameMsg,
  Snapshot,
} from "./protocol";

export type ConnState =
  | "idle" // no settings configured yet
  | "connecting"
  | "connected"
  | "auth-failed" // bad token — reconnecting won't help
  | "offline"; // daemon not reachable; will retry

export interface DaemonHandlers {
  onStatus(snapshot: Snapshot): void;
  onBeacon(beacon: string): void; // publish this sealed beacon to storage.sync
  onState(state: ConnState): void;
}

const BACKOFF_MIN_MS = 500;
const BACKOFF_MAX_MS = 15_000;

// IDLE_TIMEOUT_MS is how long the client tolerates silence before treating the
// link as dead. The daemon sends an app-level heartbeat every 20s, so 50s allows
// one missed beat before forcing a reconnect. Without this, a half-open TCP
// connection (daemon killed with no clean close) leaves the UI stuck on
// "connected" forever — onclose never fires, so nothing else would notice.
export const IDLE_TIMEOUT_MS = 50_000;

// closeCodePolicyViolation is the WebSocket close code the daemon uses to reject
// a bad token (RFC 6455 1008). Reconnecting with the same token is pointless, so
// we stop and surface auth-failed.
const closeCodePolicyViolation = 1008;

export class DaemonClient {
  private url: string;
  private token: string;
  private handlers: DaemonHandlers;

  private ws: WebSocket | null = null;
  private backoff = BACKOFF_MIN_MS;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private watchdog: ReturnType<typeof setTimeout> | null = null;
  private stopped = false;
  private state: ConnState = "idle";

  constructor(url: string, token: string, handlers: DaemonHandlers) {
    this.url = url;
    this.token = token;
    this.handlers = handlers;
  }

  start(): void {
    this.stopped = false;
    this.open();
  }

  stop(): void {
    this.stopped = true;
    this.clearWatchdog();
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
    if (this.ws) {
      this.ws.onclose = null;
      this.ws.close();
      this.ws = null;
    }
    this.setState("idle");
  }

  // sendPeers reports the sealed peer beacons read from storage.sync. A no-op
  // when not connected (the daemon re-fetches on the next pass anyway).
  sendPeers(beacons: string[]): void {
    if (this.ws?.readyState !== WebSocket.OPEN) return;
    const msg: PeersMsg = { type: "peers", beacons };
    this.ws.send(JSON.stringify(msg));
  }

  // sendName asks the daemon to rename this device. Returns false if not
  // connected (the user must be connected to rename).
  sendName(name: string): boolean {
    if (this.ws?.readyState !== WebSocket.OPEN) return false;
    const msg: SetNameMsg = { type: "set-name", name };
    this.ws.send(JSON.stringify(msg));
    return true;
  }

  getState(): ConnState {
    return this.state;
  }

  private setState(s: ConnState): void {
    if (this.state === s) return;
    this.state = s;
    this.handlers.onState(s);
  }

  private open(): void {
    if (this.stopped) return;
    this.setState("connecting");
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.ws = ws;

    ws.onopen = () => {
      // First frame is the token. The daemon closes the socket if it is wrong.
      const auth: AuthMsg = { token: this.token };
      ws.send(JSON.stringify(auth));
      // Expect frames (a heartbeat at worst) now that we're open; arm the
      // watchdog so a daemon that opens then goes silent is caught.
      this.resetWatchdog();
    };

    ws.onmessage = (ev) => {
      // Any frame proves the link is alive: reset backoff and the watchdog, and
      // the first one proves auth succeeded.
      this.backoff = BACKOFF_MIN_MS;
      this.resetWatchdog();
      this.setState("connected");
      let msg: ServerMsg;
      try {
        msg = JSON.parse(ev.data as string) as ServerMsg;
      } catch {
        return; // ignore malformed frames rather than tear down the link
      }
      if (msg.type === "status") this.handlers.onStatus(msg.snapshot);
      else if (msg.type === "beacon") this.handlers.onBeacon(msg.beacon);
      // "ping" carries no payload — its arrival already reset the watchdog above.
    };

    ws.onclose = (ev) => {
      this.ws = null;
      this.clearWatchdog();
      if (this.stopped) return;
      if (ev.code === closeCodePolicyViolation) {
        // Bad token: do not spin reconnecting — the user must fix the token.
        this.setState("auth-failed");
        return;
      }
      this.setState("offline");
      this.scheduleReconnect();
    };

    ws.onerror = () => {
      // onerror is always followed by onclose, which drives reconnect.
    };
  }

  private scheduleReconnect(): void {
    if (this.stopped || this.retryTimer) return;
    const delay = this.backoff;
    this.backoff = Math.min(this.backoff * 2, BACKOFF_MAX_MS);
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.open();
    }, delay);
  }

  private resetWatchdog(): void {
    this.clearWatchdog();
    this.watchdog = setTimeout(() => this.onIdleTimeout(), IDLE_TIMEOUT_MS);
  }

  private clearWatchdog(): void {
    if (this.watchdog) {
      clearTimeout(this.watchdog);
      this.watchdog = null;
    }
  }

  // onIdleTimeout fires when no frame — not even a heartbeat — arrived within
  // IDLE_TIMEOUT_MS. The socket is silently dead (e.g. half-open TCP after the
  // daemon was killed): drop it ourselves and reconnect rather than wait for an
  // onclose the OS may never deliver.
  private onIdleTimeout(): void {
    this.watchdog = null;
    const ws = this.ws;
    if (!ws || this.stopped) return;
    ws.onclose = null; // we drive the transition; don't let close() double-handle
    ws.close();
    this.ws = null;
    this.setState("offline");
    this.scheduleReconnect();
  }
}
