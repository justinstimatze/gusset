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
// excluding this device's own. Staleness is judged by the daemon after it opens
// each beacon, so this returns them all.
export async function readPeerBeacons(): Promise<string[]> {
  const id = await installId();
  const ownKey = BEACON_PREFIX + id;
  const all = await browser.storage.sync.get(null);
  const out: string[] = [];
  for (const [key, value] of Object.entries(all)) {
    if (!key.startsWith(BEACON_PREFIX) || key === ownKey) continue;
    const rec = value as BeaconRecord;
    if (rec && typeof rec.beacon === "string") out.push(rec.beacon);
  }
  return out;
}
