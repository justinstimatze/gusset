import { useState } from "react";
import { browser } from "wxt/browser";
import { DEFAULT_WS_URL, INSTALL_URL } from "@/lib/settings";

// SettingsForm pairs the extension with the daemon: the loopback WebSocket
// address and the token from `gusset ws-token`. Used by both the popup and the
// dashboard settings.
export function SettingsForm({
  wsUrl,
  onSaved,
}: {
  wsUrl: string;
  onSaved?: () => void;
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
    onSaved?.();
  };

  return (
    <form onSubmit={save} className="space-y-3">
      <div className="space-y-1 rounded bg-[var(--panel)] px-3 py-2 text-xs text-[var(--ink-dim)]">
        <p>
          gusset has two parts: this extension and a small companion app that
          runs on this computer. The extension talks to it over a local
          connection — it can’t sync on its own, and (being sandboxed) can’t
          install or start the app for you.
        </p>
        <p>
          <strong className="font-medium text-[var(--ink)]">
            Don’t have the app yet?
          </strong>{" "}
          <a
            href={INSTALL_URL}
            target="_blank"
            rel="noreferrer"
            className="text-[var(--accent)] underline underline-offset-2"
          >
            Install gusset →
          </a>
        </p>
      </div>
      <p className="text-xs text-[var(--ink-dim)]">
        Already running it? Get the token with{" "}
        <code className="rounded bg-[var(--panel)] px-1">gusset ws-token</code>{" "}
        and paste both below.
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
        className="w-full rounded bg-[var(--accent)] py-2 font-medium text-[var(--on-accent)] hover:bg-[var(--accent-strong)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--accent)] disabled:opacity-60"
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
      <span className="mb-1 block text-xs font-medium text-[var(--ink-dim)]">
        {label}
      </span>
      <input
        type={password ? "password" : "text"}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded border border-[var(--line)] bg-[var(--canvas)] px-2 py-1.5 text-sm focus-visible:border-[var(--accent)] focus-visible:outline focus-visible:outline-1 focus-visible:outline-[var(--accent)]"
      />
    </label>
  );
}
