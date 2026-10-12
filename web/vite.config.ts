import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/",
  plugins: [react()],
  // The control API reads .vite/manifest.json to tell which files carry a
  // content hash in their name and may be cached for a year.
  build: { manifest: true },
  server: { proxy: { "/api": "http://127.0.0.1:18080" } },
  test: { environment: "jsdom", setupFiles: "./src/test/setup.ts", css: true },
});
