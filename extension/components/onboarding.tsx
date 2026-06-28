import { useState } from "react";
import { browser } from "wxt/browser";
import type { ConnState } from "@/lib/daemon";
import { SETUP_STEPS } from "@/lib/onboarding";

// CopyCmd shows a command with a one-click copy, so a new user never retypes a
// long curl line. Clipboard write runs from the click (a user gesture), which
// extensions are allowed to do.
function CopyCmd({ cmd }: { cmd: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(cmd);
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        } catch {
          /* clipboard blocked — the command is still visible to copy by hand */
        }
      }}
      className="group mt-1 flex w-full items-center gap-2 rounded bg-[var(--panel)] px-2 py-1 text-left font-mono text-xs hover:bg-[var(--neutral-bg)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--accent)]"
      title="Copy to clipboard"
    >
      <code className="flex-1 overflow-x-auto whitespace-nowrap">{cmd}</code>
      <span className="shrink-0 text-[var(--ink-dim)] group-hover:text-[var(--ink)]">
        {copied ? "copied" : "copy"}
      </span>
    </button>
  );
}

function headline(connState: ConnState): string {
  switch (connState) {
    case "offline":
      return "Almost there — start the daemon to connect";
    case "auth-failed":
      return "Token rejected — re-pair below";
    default:
      return "Get your devices syncing";
  }
}

// Onboarding renders the same six steps as `gusset setup`. In compact mode (the
// popup) it lists the steps tersely with a button to the full guide; in full
// mode (the dashboard) each step carries its copy-paste command.
export function Onboarding({
  connState,
  compact = false,
}: {
  connState: ConnState;
  compact?: boolean;
}) {
  return (
    <section className="space-y-2 rounded-lg border border-[var(--line)] bg-[var(--canvas)] p-3">
      <div>
        <h2 className="text-sm font-semibold">{headline(connState)}</h2>
        <p className="text-xs text-[var(--ink-dim)]">
          The same steps as <code>gusset setup</code> in your terminal.
        </p>
      </div>

      <ol className="space-y-2">
        {SETUP_STEPS.map((s) => {
          const done = s.doneInExtension;
          return (
            <li key={s.n} className="text-sm">
              <div className="flex items-baseline gap-2">
                <span
                  className={`shrink-0 font-mono text-xs ${done ? "text-[var(--ok)]" : "text-[var(--ink-dim)]"}`}
                >
                  {done ? "✓" : s.n}
                </span>
                <span
                  className={done ? "text-[var(--ink-dim)]" : "font-medium"}
                >
                  {s.title}
                </span>
              </div>
              {!compact && (
                <p className="ml-6 text-xs text-[var(--ink-dim)]">{s.detail}</p>
              )}
              {!compact && s.cmd && (
                <div className="ml-6">
                  <CopyCmd cmd={s.cmd} />
                </div>
              )}
            </li>
          );
        })}
      </ol>

      {compact && (
        <button
          type="button"
          onClick={() => void browser.runtime.openOptionsPage()}
          className="w-full rounded border border-[var(--line)] py-1.5 text-xs font-medium hover:bg-[var(--panel)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--accent)]"
        >
          Open the full setup guide →
        </button>
      )}
    </section>
  );
}
