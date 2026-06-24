import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type ConnState, DaemonClient } from "./daemon";
import type { Snapshot } from "./protocol";

// FakeWS is a controllable WebSocket stand-in. Tests drive the "server" side via
// open()/message()/serverClose().
class FakeWS {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances: FakeWS[] = [];

  url: string;
  readyState = FakeWS.CONNECTING;
  onopen: ((e?: any) => void) | null = null;
  onmessage: ((e: any) => void) | null = null;
  onclose: ((e: any) => void) | null = null;
  onerror: ((e?: any) => void) | null = null;
  sent: string[] = [];

  constructor(url: string) {
    this.url = url;
    FakeWS.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    this.readyState = FakeWS.CLOSED;
  }

  // Test-side drivers.
  open() {
    this.readyState = FakeWS.OPEN;
    this.onopen?.();
  }
  message(obj: unknown) {
    this.onmessage?.({ data: JSON.stringify(obj) });
  }
  serverClose(code: number) {
    this.readyState = FakeWS.CLOSED;
    this.onclose?.({ code });
  }
}

function newClient() {
  const states: ConnState[] = [];
  const statuses: Snapshot[] = [];
  const beacons: string[] = [];
  const client = new DaemonClient("ws://127.0.0.1:8765", "the-token", {
    onStatus: (s) => statuses.push(s),
    onBeacon: (b) => beacons.push(b),
    onState: (s) => states.push(s),
  });
  return { client, states, statuses, beacons };
}

const last = <T>(a: T[]): T => {
  const v = a[a.length - 1];
  if (v === undefined) throw new Error("expected a non-empty array");
  return v;
};

beforeEach(() => {
  FakeWS.instances = [];
  (globalThis as any).WebSocket = FakeWS;
});

afterEach(() => {
  vi.useRealTimers();
});

describe("DaemonClient", () => {
  it("sends the token as the first frame on open", () => {
    const { client } = newClient();
    client.start();
    const ws = last(FakeWS.instances);
    ws.open();
    expect(ws.sent).toHaveLength(1);
    expect(JSON.parse(ws.sent[0] ?? "")).toEqual({ token: "the-token" });
  });

  it("delivers status and beacon messages and reports connected", () => {
    const { client, states, statuses, beacons } = newClient();
    client.start();
    const ws = last(FakeWS.instances);
    ws.open();

    const snap: Snapshot = {
      self: { device_id: "self" },
      peers: [],
      extensions: [],
      log: [],
    };
    ws.message({ type: "status", snapshot: snap });
    ws.message({ type: "beacon", beacon: "sealed" });

    expect(last(states)).toBe("connected");
    expect(statuses).toHaveLength(1);
    expect(beacons).toEqual(["sealed"]);
  });

  it("treats a 1008 close as auth-failed and does NOT reconnect", () => {
    vi.useFakeTimers();
    const { client, states } = newClient();
    client.start();
    last(FakeWS.instances).open();
    last(FakeWS.instances).serverClose(1008);

    expect(last(states)).toBe("auth-failed");
    vi.advanceTimersByTime(60_000);
    expect(FakeWS.instances).toHaveLength(1); // no reconnect attempt
  });

  it("treats any other close as offline and reconnects with backoff", () => {
    vi.useFakeTimers();
    const { client, states } = newClient();
    client.start();
    last(FakeWS.instances).open();
    last(FakeWS.instances).serverClose(1006); // abnormal closure (daemon down)

    expect(last(states)).toBe("offline");
    expect(FakeWS.instances).toHaveLength(1);
    vi.advanceTimersByTime(500); // first backoff
    expect(FakeWS.instances).toHaveLength(2); // reconnected
  });

  it("stop() prevents further reconnects", () => {
    vi.useFakeTimers();
    const { client } = newClient();
    client.start();
    last(FakeWS.instances).open();
    last(FakeWS.instances).serverClose(1006);
    client.stop();
    vi.advanceTimersByTime(60_000);
    expect(FakeWS.instances).toHaveLength(1);
  });

  it("sendPeers writes a peers frame only when open", () => {
    const { client } = newClient();
    client.start();
    const ws = last(FakeWS.instances);

    client.sendPeers(["a", "b"]); // not open yet -> dropped
    expect(ws.sent).toHaveLength(0);

    ws.open();
    ws.sent.length = 0; // drop the auth frame
    client.sendPeers(["a", "b"]);
    expect(JSON.parse(ws.sent[0] ?? "")).toEqual({
      type: "peers",
      beacons: ["a", "b"],
    });
  });
});
