import { useState } from "react";
import { browser } from "wxt/browser";
import { SettingsForm } from "@/components/settings-form";
import { Progress } from "@/components/ui/progress";
import { connMeta, peerDot } from "@/lib/display";
import { type BgState, useDaemonState } from "@/lib/use-daemon-state";

function App() {
  const { state, refresh } = useDaemonState(1500);
  const [editing, setEditing] = useState(false);
  const showForm = editing || (state !== null && !state.configured);

  return (
    <main className="min-h-[180px] bg-[var(--canvas)] p-4 text-sm text-[var(--ink)]">
      <header className="mb-3 flex items-center justify-between">
        <h1 className="text-base font-semibold tracking-tight">gusset</h1>
        {state?.configured && (
          <button
            type="button"
            onClick={() => setEditing((e) => !e)}
            className="rounded px-2 py-1 text-xs text-[var(--ink-dim)] hover:bg-[var(--panel)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--accent)]"
          >
            {editing ? "Done" : "Settings"}
          </button>
        )}
      </header>

      {state === null ? (
        <p className="text-[var(--ink-dim)]">Loading…</p>
      ) : showForm ? (
        <SettingsForm
          wsUrl={state.wsUrl}
          onSaved={async () => {
            setEditing(false);
            await refresh();
          }}
        />
      ) : (
        <Status state={state} />
      )}
    </main>
  );
}

function Status({ state }: { state: BgState }) {
  const meta = connMeta[state.connState];
  const { peers, extensions } = state.snapshot;
  const inFlight = extensions.filter(
    (e) => e.state === "pushing" || e.state === "pulling",
  ).length;

  return (
    <div className="space-y-3">
      <div aria-live="polite" className="flex items-start gap-2">
        <span
          className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${meta.dot} motion-reduce:animate-none`}
        />
        <div>
          <div className="font-medium">{meta.label}</div>
          {meta.hint && (
            <div className="text-xs text-[var(--ink-dim)]">{meta.hint}</div>
          )}
        </div>
      </div>

      {state.connState === "connected" && (
        <>
          {inFlight > 0 && (
            <div className="space-y-1">
              <div className="flex justify-between text-xs text-[var(--ink-dim)]">
                <span>Syncing {inFlight}…</span>
                <span>still working</span>
              </div>
              <Progress fraction={null} />
            </div>
          )}

          {peers.length > 0 ? (
            <ul className="space-y-1">
              {peers.map((p) => (
                <li
                  key={p.device_id}
                  className="flex items-center gap-2 rounded bg-[var(--panel)] px-2 py-1.5"
                >
                  <span
                    className={`h-2 w-2 shrink-0 rounded-full ${peerDot[p.state]}`}
                  />
                  <span className="truncate font-medium">
                    {p.name || p.device_id}
                  </span>
                  <span className="ml-auto text-xs text-[var(--ink-dim)]">
                    {p.state}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="rounded bg-[var(--panel)] px-2 py-2 text-xs text-[var(--ink-dim)]">
              No devices paired yet. Run gusset on another machine with the same
              passphrase.
            </p>
          )}

          <button
            type="button"
            onClick={() => void browser.runtime.openOptionsPage()}
            className="w-full rounded border border-[var(--line)] py-1.5 text-xs font-medium hover:bg-[var(--panel)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--accent)]"
          >
            Open dashboard
          </button>
        </>
      )}
    </div>
  );
}

export default App;
