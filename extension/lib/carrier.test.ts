import { beforeEach, describe, expect, it } from "vitest";
import { fakeBrowser } from "wxt/testing";
import { installId, publishBeacon, readPeerBeacons } from "./carrier";

beforeEach(() => {
  fakeBrowser.reset();
});

describe("installId", () => {
  it("generates a UUID and persists it across calls", async () => {
    const a = await installId();
    const b = await installId();
    expect(a).toBe(b);
    expect(a).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    );
  });
});

describe("publishBeacon / readPeerBeacons", () => {
  it("writes this device's beacon under its own namespaced key", async () => {
    await publishBeacon("my-sealed-beacon", 1000);
    const id = await installId();
    const stored = await fakeBrowser.storage.sync.get(null);
    expect(stored[`gusset:beacon:${id}`]).toEqual({
      beacon: "my-sealed-beacon",
      t: 1000,
    });
  });

  it("returns peers' beacons but never this device's own", async () => {
    await publishBeacon("mine", 1000);
    // A peer's beacon arrives via Firefox Sync under a different key.
    await fakeBrowser.storage.sync.set({
      "gusset:beacon:other-device": { beacon: "peer-sealed", t: 1000 },
    });

    const peers = await readPeerBeacons(1000);
    expect(peers).toEqual(["peer-sealed"]);
  });

  it("ignores keys that are not beacons", async () => {
    await fakeBrowser.storage.sync.set({
      unrelated: "noise",
      "gusset:beacon:p": { beacon: "b", t: 1000 },
    });
    const peers = await readPeerBeacons(1000);
    expect(peers).toEqual(["b"]);
  });

  it("returns nothing when no peers have published", async () => {
    await publishBeacon("mine", 1000);
    expect(await readPeerBeacons(1000)).toEqual([]);
  });

  it("skips and deletes beacons older than the max age", async () => {
    const now = 60 * 60 * 1000; // 1h in ms
    await fakeBrowser.storage.sync.set({
      "gusset:beacon:fresh": { beacon: "fresh-sealed", t: now - 60_000 }, // 1 min old
      "gusset:beacon:stale": { beacon: "stale-sealed", t: now - 45 * 60_000 }, // 45 min old
    });

    const peers = await readPeerBeacons(now);
    expect(peers).toEqual(["fresh-sealed"]);

    // The stale beacon is pruned from storage.sync, not just hidden, so it stops
    // syncing back to every device.
    const stored = await fakeBrowser.storage.sync.get(null);
    expect(stored["gusset:beacon:stale"]).toBeUndefined();
    expect(stored["gusset:beacon:fresh"]).toBeDefined();
  });
});
