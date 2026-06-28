import { useCallback, useEffect, useState } from "react";
import { browser } from "wxt/browser";
import type { ConnState } from "./daemon";
import type { Snapshot } from "./protocol";

export interface BgState {
  connState: ConnState;
  snapshot: Snapshot;
  configured: boolean;
  wsUrl: string;
}

// useDaemonState polls the background page for the cached connection state and
// status snapshot. The background owns the WebSocket; surfaces just render what
// it reports.
export function useDaemonState(pollMs = 1500) {
  const [state, setState] = useState<BgState | null>(null);

  const refresh = useCallback(async (): Promise<BgState> => {
    const res = (await browser.runtime.sendMessage({
      type: "get-state",
    })) as BgState;
    setState(res);
    return res;
  }, []);

  useEffect(() => {
    void refresh();
    const t = setInterval(() => void refresh(), pollMs);
    return () => clearInterval(t);
  }, [refresh, pollMs]);

  return { state, refresh };
}
