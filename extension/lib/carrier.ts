// The storage.sync carrier is the browser end of gusset's rendezvous: it writes
// this device's sealed beacon to storage.sync (which Firefox Sync carries to the
// user's other devices, end-to-end encrypted) and reads back the peers' beacons.
// The daemon cannot use the storage.sync API — only an extension can — so this is
// the courier the daemon proxies through over the localhost WebSocket.
//
// Each device writes under its own key so devices never overwrite each other
// (the namespacing the design calls for). Only tiny sealed beacons ride
// storage.sync — never bulk data — to stay a good Firefox Sync citizen.

import { browser } from "wxt/browser";

const BEACON_PREFIX = "gusset:beacon:";
const INSTALL_ID_KEY = "gusset:install-id";

// BEACON_MAX_AGE_MS bounds how long a peer's beacon lives in storage.sync before
// readPeerBeacons prunes it. A peer that stopped publishing (a finished test run,
// a decommissioned device) should not linger in the synced store forever — it
// would clutter every device's view and count against the small storage.sync
// quota. Generous relative to the daemon's 15-minute freshness window, so a peer
// that is merely between sync passes is never dropped.
const BEACON_MAX_AGE_MS = 30 * 60 * 1000;

// installId returns this extension install's stable id, generating and
// persisting one in local storage on first use. It namespaces this device's
// storage.sync beacon key.
export async function installId(): Promise<string> {
  const got = await browser.storage.local.get(INSTALL_ID_KEY);
  const existing = got[INSTALL_ID_KEY] as string | undefined;
  if (existing) return existing;
  const id = crypto.randomUUID();
  await browser.storage.local.set({ [INSTALL_ID_KEY]: id });
  return id;
}

interface BeaconRecord {
  beacon: string; // base64 sealed beacon
  t: number; // unix millis written, for housekeeping
}

// publishBeacon writes this device's sealed beacon to its own storage.sync key,
// replacing any previous one. now is injectable for tests.
export async function publishBeacon(
  beacon: string,
  now: number = Date.now(),
): Promise<void> {
  const id = await installId();
  const rec: BeaconRecord = { beacon, t: now };
  await browser.storage.sync.set({ [BEACON_PREFIX + id]: rec });
}

// readPeerBeacons returns every other device's sealed beacon from storage.sync,
// excluding this device's own. Beacons older than BEACON_MAX_AGE_MS are skipped
// and deleted from storage.sync, so a peer that stopped publishing does not haunt
// the synced store or every other device's peer list. (The daemon still re-checks
// freshness when it opens each beacon; this is the storage-side cleanup that the
// daemon, which cannot touch storage.sync, relies on the extension to do.) now is
// injectable for tests.
export async function readPeerBeacons(
  now: number = Date.now(),
): Promise<string[]> {
  const id = await installId();
  const ownKey = BEACON_PREFIX + id;
  const all = await browser.storage.sync.get(null);
  const out: string[] = [];
  const stale: string[] = [];
  for (const [key, value] of Object.entries(all)) {
    if (!key.startsWith(BEACON_PREFIX) || key === ownKey) continue;
    const rec = value as BeaconRecord;
    if (!rec || typeof rec.beacon !== "string") continue;
    if (typeof rec.t === "number" && now - rec.t > BEACON_MAX_AGE_MS) {
      stale.push(key);
      continue;
    }
    out.push(rec.beacon);
  }
  if (stale.length > 0) {
    // Best-effort: a failed removal just means we try again next read.
    try {
      await browser.storage.sync.remove(stale);
    } catch {
      /* ignore — the beacon stays, but the daemon's own freshness check still ignores it */
    }
  }
  return out;
}
