import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

const dir = import.meta.dirname;

// Standalone preview of the real popup/dashboard components with mocked browser
// APIs. Not part of the extension build — a dev-only visual harness.
export default defineConfig({
  root: dir,
  plugins: [tailwindcss()],
  esbuild: { jsx: "automatic" },
  resolve: {
    alias: {
      "wxt/browser": resolve(dir, "browser-mock.ts"),
      "@": resolve(dir, ".."),
    },
  },
});
