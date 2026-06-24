// The background event page owns the single daemon connection (the popup is too
// short-lived to hold a reliable WebSocket). It bridges the daemon and
// storage.sync — publishing the daemon's beacon and reporting peer beacons back
// — caches the latest status + connection state, and answers the popup's queries.

import { browser } from "wxt/browser";
import { publishBeacon, readPeerBeacons } from "@/lib/carrier";
import { type ConnState, DaemonClient } from "@/lib/daemon";
import { EMPTY_SNAPSHOT, type Snapshot } from "@/lib/protocol";
import {
  isConfigured,
  loadSettings,
  type Settings,
  saveSettings,
} from "@/lib/settings";

// PopupMsg is the UI -> background message protocol.
type PopupMsg =
  | { type: "get-state" }
  | { type: "save-settings"; settings: Settings }
  | { type: "set-device-name"; name: string };

export default defineBackground(() => {
  let client: DaemonClient | null = null;
  let connState: ConnState = "idle";
  let snapshot: Snapshot = EMPTY_SNAPSHOT;

  // reportPeers reads the peer beacons Firefox Sync has delivered and hands them
  // to the daemon. Called on connect and whenever storage.sync changes.
  async function reportPeers() {
    if (!client) return;
    try {
      client.sendPeers(await readPeerBeacons());
    } catch {
      // storage.sync can be transiently unavailable; the next change re-reports.
    }
  }

  async function start(settings: Settings) {
    client?.stop();
    if (!isConfigured(settings)) {
      connState = "idle";
      return;
    }
    client = new DaemonClient(settings.wsUrl, settings.token, {
      onStatus: (s) => {
        snapshot = s;
      },
      onBeacon: async (beacon) => {
        try {
          await publishBeacon(beacon);
        } catch {
          // best-effort; the daemon re-publishes on its next pass.
        }
        await reportPeers();
      },
      onState: (s) => {
        connState = s;
        if (s === "connected") void reportPeers();
      },
    });
    client.start();
  }

  // Re-report peers whenever Firefox Sync brings a change to storage.sync.
  browser.storage.sync.onChanged.addListener(() => void reportPeers());

  // Popup queries. get-state returns a token-free view; save-settings reconnects.
  browser.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (!message || typeof message !== "object") return false;
    const msg = message as PopupMsg;
    if (msg.type === "get-state") {
      void loadSettings().then((s) => {
        sendResponse({
          connState,
          snapshot,
          configured: isConfigured(s),
          wsUrl: s.wsUrl,
        });
      });
      return true; // keep the channel open for the async response
    }
    if (msg.type === "save-settings") {
      void saveSettings(msg.settings).then(async () => {
        await start(msg.settings);
        sendResponse({ ok: true });
      });
      return true;
    }
    if (msg.type === "set-device-name") {
      sendResponse({ ok: client?.sendName(msg.name) ?? false });
      return true;
    }
    return false;
  });

  void loadSettings().then(start);
});
