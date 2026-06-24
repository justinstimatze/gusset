import { useMemo } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { connMeta, extWhy, peerDot, peerWhy, syncMeta } from "@/lib/display";
import type { Snapshot } from "@/lib/protocol";
import { type BgState, useDaemonState } from "@/lib/use-daemon-state";

function App() {
  const { state } = useDaemonState(1500);

  return (
    <main className="min-h-screen bg-zinc-50 text-zinc-900 dark:bg-zinc-950 dark:text-zinc-100">
      <div className="mx-auto max-w-3xl px-6 py-8">
        <header className="mb-6 flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">gusset</h1>
            <p className="text-sm text-zinc-500 dark:text-zinc-400">
              Extension settings, synced across your devices.
            </p>
          </div>
          {state && <ConnPill state={state} />}
        </header>

        {state === null ? (
          <p className="text-zinc-500">Loading…</p>
        ) : !state.configured ? (
          <NotConfigured />
        ) : (
          <div className="space-y-6">
            <Devices snapshot={state.snapshot} />
            <Grid snapshot={state.snapshot} />
          </div>
        )}
      </div>
    </main>
  );
}

function ConnPill({ state }: { state: BgState }) {
  const meta = connMeta[state.connState];
  return (
    <div
      aria-live="polite"
      className="flex items-center gap-2 rounded-full border border-zinc-200 bg-white px-3 py-1.5 text-sm dark:border-zinc-800 dark:bg-zinc-900"
    >
      <span
        className={`h-2 w-2 rounded-full ${meta.dot} motion-reduce:animate-none`}
      />
      <span className="font-medium">{meta.label}</span>
    </div>
  );
}

function NotConfigured() {
  return (
    <Card>
      <CardContent className="text-sm">
        <p className="mb-1 font-medium">Not paired yet.</p>
        <p className="text-zinc-500 dark:text-zinc-400">
          Open the gusset icon in your toolbar to enter the daemon address and
          the token from{" "}
          <code className="rounded bg-zinc-100 px-1 dark:bg-zinc-800">
            gusset ws-token
          </code>
          .
        </p>
      </CardContent>
    </Card>
  );
}

function Devices({ snapshot }: { snapshot: Snapshot }) {
  const { peers } = snapshot;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Devices ({peers.length})</CardTitle>
      </CardHeader>
      <CardContent>
        {peers.length === 0 ? (
          <p className="text-sm text-zinc-500 dark:text-zinc-400">
            No devices paired yet. Run gusset on another machine with the same
            passphrase.
          </p>
        ) : (
          <ul className="flex flex-wrap gap-2">
            {peers.map((p) => (
              <li
                key={p.device_id}
                className="flex items-center gap-2 rounded-md bg-zinc-100 px-2.5 py-1.5 text-sm dark:bg-zinc-800"
                title={peerWhy(p)}
              >
                <span className={`h-2 w-2 rounded-full ${peerDot[p.state]}`} />
                <span className="font-medium">{p.name || p.device_id}</span>
                <span className="text-xs text-zinc-500 dark:text-zinc-400">
                  {p.state}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

// deviceColumns returns the device ids/names to show as grid columns: every
// paired peer, plus any device that appears only in the extension rows.
function deviceColumns(snapshot: Snapshot): { id: string; name: string }[] {
  const byId = new Map<string, string>();
  for (const p of snapshot.peers) byId.set(p.device_id, p.name || p.device_id);
  for (const e of snapshot.extensions)
    if (!byId.has(e.device_id)) byId.set(e.device_id, e.device_id);
  return [...byId.entries()]
    .map(([id, name]) => ({ id, name }))
    .sort((a, b) => a.id.localeCompare(b.id));
}

function Grid({ snapshot }: { snapshot: Snapshot }) {
  const devices = useMemo(() => deviceColumns(snapshot), [snapshot]);
  const extensions = useMemo(
    () => [...new Set(snapshot.extensions.map((e) => e.extension))].sort(),
    [snapshot],
  );
  const cell = (ext: string, dev: string) =>
    snapshot.extensions.find((e) => e.extension === ext && e.device_id === dev);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Sync status</CardTitle>
      </CardHeader>
      <CardContent className="p-0">
        {extensions.length === 0 ? (
          <p className="p-4 text-sm text-zinc-500 dark:text-zinc-400">
            No extensions are syncing yet. Opt one in with{" "}
            <code className="rounded bg-zinc-100 px-1 dark:bg-zinc-800">
              gusset allow &lt;id&gt;
            </code>
            .
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <caption className="sr-only">
                Per-extension sync state on each device
              </caption>
              <thead>
                <tr className="border-b border-zinc-200 dark:border-zinc-800">
                  <th scope="col" className="px-4 py-2 text-left font-medium">
                    Extension
                  </th>
                  {devices.map((d) => (
                    <th
                      key={d.id}
                      scope="col"
                      className="px-4 py-2 text-left font-medium"
                    >
                      {d.name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {extensions.map((ext) => (
                  <tr
                    key={ext}
                    className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60"
                  >
                    <th
                      scope="row"
                      className="max-w-[16rem] truncate px-4 py-2 text-left font-normal"
                    >
                      {ext}
                    </th>
                    {devices.map((d) => {
                      const c = cell(ext, d.id);
                      return (
                        <td key={d.id} className="px-4 py-2">
                          {c ? (
                            <Badge
                              title={extWhy(c)}
                              className={syncMeta[c.state].cls}
                            >
                              {syncMeta[c.state].label}
                            </Badge>
                          ) : (
                            <span
                              className="text-zinc-300 dark:text-zinc-600"
                              aria-hidden="true"
                            >
                              —
                            </span>
                          )}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export default App;
