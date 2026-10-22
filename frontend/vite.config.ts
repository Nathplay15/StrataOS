/// <reference types="node" />
import { readdirSync, readFileSync, existsSync, statSync } from "node:fs";
import { join } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// During development the UI talks to a StrataOS backend (the test VM by default).
const target = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env.STRATA_API ?? "http://localhost:7070";

// The PDF viewer's data files (CJK character maps, standard fonts, JPEG 2000 /
// JBIG2 decoders, colour profiles) are served by the NAS itself under
// /pdfjs/: no CDN, works offline.
const pdfjsDirs = ["cmaps", "standard_fonts", "wasm", "iccs"];
const pdfjsRoot = join(import.meta.dirname, "node_modules/pdfjs-dist");
function pdfjsAssets(): Plugin {
  return {
    name: "strata-pdfjs-assets",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const m = req.url?.match(/^\/pdfjs\/([a-z_]+)\/([\w.-]+)$/);
        const file = m && pdfjsDirs.includes(m[1]) ? join(pdfjsRoot, m[1], m[2]) : "";
        if (!file || !existsSync(file)) return next();
        if (file.endsWith(".wasm")) res.setHeader("Content-Type", "application/wasm");
        res.end(readFileSync(file));
      });
    },
    generateBundle() {
      for (const dir of pdfjsDirs) {
        for (const f of readdirSync(join(pdfjsRoot, dir))) {
          const p = join(pdfjsRoot, dir, f);
          if (statSync(p).isFile() && !f.startsWith("LICENSE")) this.emitFile({ type: "asset", fileName: `pdfjs/${dir}/${f}`, source: readFileSync(p) });
        }
      }
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), pdfjsAssets()],
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
