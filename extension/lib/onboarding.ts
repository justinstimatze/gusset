// SETUP_STEPS is the single source of truth for the first-run walkthrough shown
// in both the popup and the dashboard. It mirrors `gusset setup` on the CLI
// step-for-step, so the terminal and the extension guide the user through the
// identical journey and reinforce each other: the CLI names the extension, the
// extension names the CLI commands.

export interface SetupStep {
  n: number;
  title: string;
  detail: string;
  cmd?: string;
  // doneInExtension marks the step the user has necessarily completed already by
  // virtue of looking at this UI (installing the extension).
  doneInExtension?: boolean;
}

// The permanent, version-independent install link for the signed extension.
export const INSTALL_XPI_URL =
  "https://github.com/justinstimatze/gusset/releases/latest/download/gusset.xpi";

export const SETUP_STEPS: SetupStep[] = [
  {
    n: 1,
    title: "Install the gusset app",
    detail:
      "the small daemon that does the syncing — run on every machine (Windows: use install.ps1)",
    cmd: "curl -fsSL https://raw.githubusercontent.com/justinstimatze/gusset/main/install.sh | sh",
  },
  {
    n: 2,
    title: "Create the config",
    detail:
      "on every machine — the passphrase is the only thing you carry across",
    cmd: "gusset init",
  },
  {
    n: 3,
    title: "Set the shared passphrase",
    detail:
      "first machine generates and prints it; paste those same words on the others",
    cmd: "gusset passphrase new",
  },
  {
    n: 4,
    title: "Choose what to sync",
    detail: "opt an extension in (the allowlist is empty by default)",
    cmd: "gusset allow uBlock0@raymondhill.net",
  },
  {
    n: 5,
    title: "Install this extension",
    detail: "you're looking at it — already done",
    doneInExtension: true,
  },
  {
    n: 6,
    title: "Start the daemon, then pair it below",
    detail:
      "run it with a status socket, then paste the token from `gusset ws-token` into the form below",
    cmd: "gusset sync --watch --ws 127.0.0.1:8765",
  },
];
