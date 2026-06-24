import { useState } from "react";
import { browser } from "wxt/browser";
import { DEFAULT_WS_URL } from "@/lib/settings";

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
      <p className="text-xs text-[var(--ink-dim)]">
        Pair with your running daemon. Get the token with{" "}
        <code className="rounded bg-[var(--panel)] px-1">gusset ws-token</code>.
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
