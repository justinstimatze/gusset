// Settings the user pastes from the daemon: the localhost WebSocket URL and the
// pairing token (`gusset ws-token`). Stored locally (never synced — the token is
// a secret) under storage.local.

import { browser } from "wxt/browser";

export interface Settings {
  wsUrl: string; // e.g. ws://127.0.0.1:8765
  token: string;
}

const KEY = "gusset:settings";

export const DEFAULT_WS_URL = "ws://127.0.0.1:8765";

export async function loadSettings(): Promise<Settings> {
  const got = await browser.storage.local.get(KEY);
  const s = got[KEY] as Partial<Settings> | undefined;
  return { wsUrl: s?.wsUrl || DEFAULT_WS_URL, token: s?.token || "" };
}

export async function saveSettings(s: Settings): Promise<void> {
  await browser.storage.local.set({ [KEY]: s });
}

export function isConfigured(s: Settings): boolean {
  return s.wsUrl.trim() !== "" && s.token.trim() !== "";
}
