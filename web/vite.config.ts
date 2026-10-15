import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The build lands where the Go binary embeds it (internal/webui, -tags webui).
// In development, `npm run dev` serves the app with live reload and forwards
// API calls to `forgerun serve` on :8080.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
  },
});
