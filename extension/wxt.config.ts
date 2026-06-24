import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "wxt";

// See https://wxt.dev/api/config.html
export default defineConfig({
  modules: ["@wxt-dev/module-react"],
  manifestVersion: 3,
  vite: () => ({
    plugins: [tailwindcss()],
  }),
  manifest: {
    name: "gusset",
    description:
      "Sync your Firefox extension settings across your own devices.",
    // storage covers storage.local (settings) and storage.sync (beacon carrier).
    permissions: ["storage"],
    browser_specific_settings: {
      gecko: {
        id: "gusset@justinstimatze.com",
        // gusset is peer-to-peer and collects nothing — no telemetry, no
        // accounts, no server. Declared explicitly per Firefox's data-consent
        // requirement for new extensions.
        data_collection_permissions: { required: ["none"] },
      },
    },
    // The popup/background connect to the daemon's loopback WebSocket; everything
    // else stays self-only.
    content_security_policy: {
      extension_pages:
        "script-src 'self'; object-src 'self'; connect-src 'self' ws://127.0.0.1:* ws://localhost:*",
    },
  },
});
