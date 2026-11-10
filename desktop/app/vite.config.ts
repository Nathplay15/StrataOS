import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";
import path from "node:path";

// Parts of the NAS web interface are shared (frontend/src: design tokens,
// buttons and dialogs, types, translations, jobs). Their API client
// (frontend/src/lib/api.ts) is replaced by src/lib/nasapi.ts, which sends
// the same calls to the NAS of the current account through the agent.
const nasSrc = fileURLToPath(new URL("../../frontend/src", import.meta.url));
const nasApi = path.join(nasSrc, "lib", "api.ts");
const shim = fileURLToPath(new URL("./src/lib/nasapi.ts", import.meta.url));

function nasApiShim(): Plugin {
  return {
    name: "strataos-nas-api",
    enforce: "pre",
    async resolveId(source, importer) {
      if (!importer || !importer.startsWith(nasSrc) || !/(^|\/)api$/.test(source)) return null;
      const r = await this.resolve(source, importer, { skipSelf: true });
      return r && r.id === nasApi ? shim : null;
    },
  };
}

export default defineConfig({
  plugins: [nasApiShim(), react(), tailwindcss()],
  resolve: {
    alias: {
      "@nas": nasSrc,
      "@catalog": fileURLToPath(new URL("../internal/i18n/catalog", import.meta.url)),
    },
    // One copy of each library for the app and the shared NAS files.
    dedupe: ["react", "react-dom", "@tanstack/react-query", "lucide-react"],
  },
  clearScreen: false,
  server: { port: 1420, strictPort: true, host: "localhost", fs: { allow: ["..", "../../frontend"] } },
  build: { target: "es2022", chunkSizeWarningLimit: 4000 },
});
