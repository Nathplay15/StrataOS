import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// During development the UI talks to a StrataOS backend (the test VM by default).
const target = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env.STRATA_API ?? "http://localhost:7070";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../backend/web/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target,
        changeOrigin: true,
        ws: true,
        // The backend rejects cross-origin writes: present requests as same-origin.
        headers: { origin: target },
      },
    },
  },
});
