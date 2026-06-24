import { useState } from "react";
import { browser } from "wxt/browser";
import { connMeta, peerDot } from "@/lib/display";
import { DEFAULT_WS_URL } from "@/lib/settings";
import { type BgState, useDaemonState } from "@/lib/use-daemon-state";

function App() {
  const { state, refresh } = useDaemonState(1500);
  const [editing, setEditing] = useState(false);

  // Drop into the settings form automatically until configured.
  const showForm = editing || (state !== null && !state.configured);

  return (
    <main className="min-h-[180px] bg-white p-4 text-sm text-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
      <header className="mb-3 flex items-center justify-between">
        <h1 className="text-base font-semibold tracking-tight">gusset</h1>
        {state?.configured && (
          <button
            type="button"
            onClick={() => setEditing((e) => !e)}
            className="rounded px-2 py-1 text-xs text-zinc-500 hover:bg-zinc-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 dark:text-zinc-400 dark:hover:bg-zinc-800"
          >
            {editing ? "Done" : "Settings"}
          </button>
        )}
      </header>

      {state === null ? (
        <p className="text-zinc-500">Loading…</p>
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
  const inFlight = extensions.filter((e) => e.state !== "in-sync").length;

  return (
    <div className="space-y-3">
      <div aria-live="polite" className="flex items-start gap-2">
        <span
          className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${meta.dot} motion-reduce:animate-none`}
        />
        <div>
          <div className="font-medium">{meta.label}</div>
          {meta.hint && (
            <div className="text-xs text-zinc-500 dark:text-zinc-400">
              {meta.hint}
            </div>
          )}
        </div>
      </div>

      {state.connState === "connected" && (
        <>
          <dl className="grid grid-cols-2 gap-2">
            <Stat
              n={peers.length}
              label={peers.length === 1 ? "device" : "devices"}
            />
            <Stat
              n={inFlight}
              label="in flight"
              sub={`${extensions.length} tracked`}
            />
          </dl>

          {peers.length > 0 ? (
            <ul className="space-y-1">
              {peers.map((p) => (
                <li
                  key={p.device_id}
                  className="flex items-center gap-2 rounded bg-zinc-50 px-2 py-1.5 dark:bg-zinc-800"
                >
                  <span
                    className={`h-2 w-2 shrink-0 rounded-full ${peerDot[p.state]}`}
                  />
                  <span className="truncate font-medium">
                    {p.name || p.device_id}
                  </span>
                  <span className="ml-auto text-xs text-zinc-500 dark:text-zinc-400">
                    {p.state}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="rounded bg-zinc-50 px-2 py-2 text-xs text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
              No devices paired yet. Run gusset on another machine with the same
              passphrase.
            </p>
          )}

          <button
            type="button"
            onClick={() => void browser.runtime.openOptionsPage()}
            className="w-full rounded border border-zinc-200 py-1.5 text-xs font-medium hover:bg-zinc-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 dark:border-zinc-700 dark:hover:bg-zinc-800"
          >
            Open dashboard
          </button>
        </>
      )}
    </div>
  );
}

function Stat({ n, label, sub }: { n: number; label: string; sub?: string }) {
  return (
    <div className="rounded bg-zinc-50 px-2 py-1.5 dark:bg-zinc-800">
      <div className="text-lg font-semibold tabular-nums">{n}</div>
      <div className="text-xs text-zinc-500 dark:text-zinc-400">{label}</div>
      {sub && <div className="text-[10px] text-zinc-400">{sub}</div>}
    </div>
  );
}

function SettingsForm({
  wsUrl,
  onSaved,
}: {
  wsUrl: string;
  onSaved: () => void;
}) {
  const [url, setUrl] = useState(wsUrl || DEFAULT_WS_URL);
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    await browser.runtime.sendMessage({
      type: "save-settings",
      settings: { wsUrl: url.trim(), token: token.trim() },
    });
    setSaving(false);
    onSaved();
  };

  return (
    <form onSubmit={save} className="space-y-3">
      <p className="text-xs text-zinc-500 dark:text-zinc-400">
        Pair this with your running daemon. Get the token with{" "}
        <code className="rounded bg-zinc-100 px-1 dark:bg-zinc-800">
          gusset ws-token
        </code>
        .
      </p>
      <Field
        label="Daemon address"
        value={url}
        onChange={setUrl}
        placeholder={DEFAULT_WS_URL}
      />
      <Field
        label="Pairing token"
        value={token}
        onChange={setToken}
        placeholder="paste from gusset ws-token"
        password
      />
      <button
        type="submit"
        disabled={saving}
        className="w-full rounded bg-blue-600 py-2 font-medium text-white hover:bg-blue-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-500 disabled:opacity-60"
      >
        {saving ? "Saving…" : "Save & connect"}
      </button>
    </form>
  );
}

function Field({
  label,
  value,
  onChange,
  placeholder,
  password,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  password?: boolean;
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs font-medium text-zinc-600 dark:text-zinc-300">
        {label}
      </span>
      <input
        type={password ? "password" : "text"}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded border border-zinc-300 bg-white px-2 py-1.5 text-sm focus-visible:border-blue-500 focus-visible:outline focus-visible:outline-1 focus-visible:outline-blue-500 dark:border-zinc-700 dark:bg-zinc-800"
      />
    </label>
  );
}

export default App;
