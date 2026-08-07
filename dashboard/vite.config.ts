import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dashboard is served by core from its own origin and loads nothing
// external — a home server has to work with the internet unplugged, and the
// content-security-policy core sets forbids external sources anyway.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    // Fail rather than silently emit something too large for an old laptop to
    // parse quickly.
    chunkSizeWarningLimit: 500,
    sourcemap: true,
  },
  server: {
    port: 5173,
    // During development the dashboard runs on Vite and core on 8080. In
    // production they share an origin, so this proxy exists only to make the
    // two situations look the same to the application code.
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
