// @vitest-environment node
// Node (not jsdom): this is a pure wire-format test with no DOM, and it needs
// import.meta.url to be a real file:// URL so it can read the checked-in
// fixtures from disk.
import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { type ConnState, DaemonClient } from "./daemon";
import {
  LINKS,
  LOG_LEVELS,
  PEER_STATES,
  REASONS,
  SERVER_MSG_TYPES,
  type Snapshot,
  SYNC_STATES,
} from "./protocol";

// This is the consumer half of the cross-language daemon↔extension contract. It
// reads the EXACT JSON fixtures the Go producer emits
// (internal/statusws/contract_test.go, regenerated with `-update`) and asserts
// the real client code decodes them and that the enum sets match. Because both
// sides are pinned to these bytes, renaming a Go json tag or changing/adding an
// enum value breaks a test here instead of silently breaking the live socket.
//
// The Go test asserts it PRODUCES these bytes; this test asserts the extension
// CONSUMES them. Together they replace the "keep them in sync" comment in
// protocol.ts with machine enforcement.

const fixture = (name: string): string =>
  readFileSync(new URL(`./testdata/protocol/${name}`, import.meta.url), "utf8");

const fixtureJSON = <T>(name: string): T => JSON.parse(fixture(name)) as T;

// RawWS feeds raw server bytes (a fixture string) straight into the client's
// onmessage, exactly as a real socket would — unlike the object-driven FakeWS in
// daemon.test.ts, which re-stringifies test-authored objects and so could never
// catch a Go shape change. The point is to exercise the real JSON.parse + decode
// path against real producer output.
class RawWS {
  static OPEN = 1;
  static instances: RawWS[] = [];
  readyState = 0;
  onopen: ((e?: unknown) => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  onerror: ((e?: unknown) => void) | null = null;
  sent: string[] = [];
  constructor(public url: string) {
    RawWS.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
  }
  // Test-side driver: deliver a raw server frame verbatim.
  deliver(raw: string) {
    this.readyState = RawWS.OPEN;
    this.onmessage?.({ data: raw });
  }
}

function sameSet(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && [...a].sort().join() === [...b].sort().join();
}

beforeEach(() => {
  RawWS.instances = [];
  (globalThis as { WebSocket?: unknown }).WebSocket = RawWS;
});

afterEach(() => {
  (globalThis as { WebSocket?: unknown }).WebSocket = undefined;
});

describe("daemon↔extension wire contract", () => {
  it("decodes the real status frame through DaemonClient onto a Snapshot", () => {
    const statuses: Snapshot[] = [];
    const states: ConnState[] = [];
    const client = new DaemonClient("ws://127.0.0.1:8765", "tok", {
      onStatus: (s) => statuses.push(s),
      onBeacon: () => {},
      onState: (s) => states.push(s),
    });
    client.start();
    const ws = RawWS.instances[0];
    if (!ws) throw new Error("client never opened a socket");

    ws.deliver(fixture("status_frame.json"));

    expect(states).toContain("connected");
    expect(statuses).toHaveLength(1);
    const snap = statuses[0];
    if (!snap) throw new Error("no snapshot delivered");

    // Self, and every peer/extension/log entry the producer put on the wire,
    // survived the round trip with the Go field names intact.
    expect(snap.self.device_id).toBe("rukh-2300de");
    expect(snap.peers).toHaveLength(5);
    expect(snap.extensions).toHaveLength(7);
    expect(snap.log).toHaveLength(4);

    // The optional progress fields decode (a rename of remaining/total would
    // null the progress bar silently in production — here it fails loudly).
    const pushing = snap.extensions.find((e) => e.state === "pushing");
    expect(pushing).toMatchObject({ remaining: 3, total: 10 });

    const connected = snap.peers.find((p) => p.device_id === "peer-connected");
    expect(connected).toMatchObject({ state: "connected", link: "direct-nat" });
  });

  it("decodes the real beacon frame onto the base64 beacon string", () => {
    const beacons: string[] = [];
    const client = new DaemonClient("ws://127.0.0.1:8765", "tok", {
      onStatus: () => {},
      onBeacon: (b) => beacons.push(b),
      onState: () => {},
    });
    client.start();
    const ws = RawWS.instances[0];
    if (!ws) throw new Error("client never opened a socket");

    ws.deliver(fixture("beacon_frame.json"));
    // Go marshals []byte as base64; the client hands it on verbatim for the
    // extension to write to storage.sync.
    expect(beacons).toEqual(["YS1zZWFsZWQtYmVhY29u"]);
  });

  it("matches the Go enum sets exactly (both directions)", () => {
    const goEnums = fixtureJSON<Record<string, string[]>>("enums.json");
    const pairs: [string, readonly string[]][] = [
      ["peer_states", PEER_STATES],
      ["links", LINKS],
      ["reasons", REASONS],
      ["sync_states", SYNC_STATES],
      ["log_levels", LOG_LEVELS],
      ["server_msg_types", SERVER_MSG_TYPES],
    ];
    for (const [key, tsValues] of pairs) {
      const goValues = goEnums[key];
      expect(goValues, `enums.json missing category ${key}`).toBeDefined();
      expect(
        sameSet(goValues ?? [], tsValues),
        `enum drift in "${key}": Go=${JSON.stringify(goValues)} TS=${JSON.stringify(tsValues)}`,
      ).toBe(true);
    }
  });

  it("carries no enum value outside the TS unions in the snapshot fixture", () => {
    const snap = fixtureJSON<Snapshot>("snapshot.json");
    for (const p of snap.peers) {
      expect(PEER_STATES).toContain(p.state);
      if (p.link) expect(LINKS).toContain(p.link);
      if (p.reason) expect(REASONS).toContain(p.reason);
    }
    for (const e of snap.extensions) expect(SYNC_STATES).toContain(e.state);
    for (const l of snap.log) expect(LOG_LEVELS).toContain(l.level);
  });
});
