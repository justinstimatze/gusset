import { defineConfig } from "vitest/config";
import { WxtVitest } from "wxt/testing";

// WxtVitest wires WXT's auto-imports and redirects `wxt/browser` to the in-memory
// fakeBrowser, so storage.local / storage.sync work in tests.
export default defineConfig({
  plugins: [WxtVitest()],
  test: {
    environment: "happy-dom",
  },
});
