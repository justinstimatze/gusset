// Mock of wxt/browser for the standalone preview: returns realistic sample data
// so the real popup/dashboard components render without an extension runtime.
import type { Snapshot } from "@/lib/protocol";

const now = Math.floor(Date.now() / 1000);

const snapshot: Snapshot = {
  peers: [
    { device_id: "laptop-7f3a", name: "Work Laptop", state: "connected", link: "lan", since: 0 },
    { device_id: "phone-22b1", name: "Phone", state: "hole-punching", since: 0 },
    { device_id: "desktop-9c4d", name: "Home Desktop", state: "unreachable", reason: "peer-offline", since: 0 },
  ],
  extensions: [
    { extension: "uBlock0@raymondhill.net", device_id: "laptop-7f3a", state: "in-sync", since: 0 },
    { extension: "uBlock0@raymondhill.net", device_id: "phone-22b1", state: "pushing", remaining: 142, total: 366, since: 0 },
    { extension: "uBlock0@raymondhill.net", device_id: "desktop-9c4d", state: "stale", since: 0 },
    { extension: "{446900e4-71c2-419f-a6a7-df9c091e268b}", device_id: "laptop-7f3a", state: "in-sync", since: 0 },
    { extension: "{446900e4-71c2-419f-a6a7-df9c091e268b}", device_id: "phone-22b1", state: "pulling", remaining: 8, since: 0 },
    { extension: "sponsorBlocker@ajay.app", device_id: "laptop-7f3a", state: "pending", detail: "restart Firefox to apply", since: 0 },
  ],
  log: [
    { time: now - 4, level: "info", message: "pushing uBlock0@raymondhill.net to Phone (366 chunks)" },
    { time: now - 9, level: "ok", message: "applied 42 keys for uBlock0@raymondhill.net from Work Laptop" },
    { time: now - 11, level: "info", message: "connected to Work Laptop over lan" },
    { time: now - 30, level: "warn", message: "Home Desktop offline — waiting" },
    { time: now - 41, level: "error", message: "nat traversal to Phone failed — retrying via relay" },
    { time: now - 52, level: "info", message: "published beacon via the companion extension" },
  ],
};

const state = {
  connState: "connected" as const,
  snapshot,
  configured: true,
  wsUrl: "ws://127.0.0.1:8765",
};

export const browser = {
  runtime: {
    // biome-ignore lint/suspicious/noExplicitAny: preview stub
    sendMessage: async (msg: any) =>
      msg?.type === "get-state" ? state : { ok: true },
    openOptionsPage: () => console.log("preview: openOptionsPage()"),
    onMessage: { addListener: () => {} },
  },
  storage: {
    local: { get: async () => ({}), set: async () => {} },
    sync: { get: async () => ({}), set: async () => {}, onChanged: { addListener: () => {} } },
  },
};
