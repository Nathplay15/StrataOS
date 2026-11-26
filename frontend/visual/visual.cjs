// Visual regression of the main pages: phone and desktop widths, Glass and
// Modern, light and dark, rendered from the built interface
// (backend/web/dist) against a mocked API (fixtures.json), compared with the
// pictures of baselines/ (at half size; a pixel differs when a channel moves by more than
// 40; a page fails above 0.5 % of its pixels).
//
//   MODE=record STRATA_URL=http://localhost:7070  record fixtures.json from a demo VM
//   MODE=update                                     write baselines/
//   MODE=check (default)                            compare; diffs in $OUT (visual-diff/)
//
// Run with the Chromium of the Playwright image (see vm/screenshots.cjs):
//   docker run --rm --network host --ipc=host -u "$(id -u):$(id -g)" -e HOME=/tmp \
//     -v /tmp/pw/node_modules:/pw/node_modules:ro -v $PWD:/src -w /src/frontend/visual \
//     -e NODE_PATH=/pw/node_modules:/src/frontend/node_modules \
//     mcr.microsoft.com/playwright:v1.48.2-noble node visual.cjs
const fs = require("fs");
const path = require("path");
const http = require("http");
const { chromium } = require("playwright");
const { PNG } = require("pngjs");

const MODE = process.env.MODE || "check";
const DIR = __dirname;
const DIST = path.resolve(DIR, "../../backend/web/dist");
const FIX = path.join(DIR, "fixtures.json");
const BASE = path.join(DIR, "baselines");
const OUT = process.env.OUT || path.join(DIR, "../../visual-diff");
const NOW = Date.parse("2026-11-20T10:00:00Z");

const pages = [
  { name: "home", path: "/" },
  { name: "files", path: "/files" },
  { name: "storage", path: "/storage" },
  { name: "updates", path: "/system/updates" },
];
const sizes = [
  { name: "phone", width: 390, height: 844, mode: "classic" },
  { name: "desktop", width: 1280, height: 800, mode: "classic" },
];
const looks = [
  { name: "glass-light", style: "glass", theme: "light" },
  { name: "glass-dark", style: "glass", theme: "dark" },
  { name: "modern-light", style: "modern", theme: "light" },
  { name: "modern-dark", style: "modern", theme: "dark" },
];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const key = (method, url) => method + " " + url.replace(/^https?:\/\/[^/]+/, "").replace(/([?&])(_|t|ts)=\d+/g, "$1");

// The built interface and the mocked API on one port.
function serve(fixtures) {
  const types = { ".js": "text/javascript", ".css": "text/css", ".html": "text/html", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2", ".json": "application/json", ".webmanifest": "application/manifest+json" };
  const srv = http.createServer((req, res) => {
    const u = req.url || "/";
    if (u.startsWith("/api/")) {
      const k = key(req.method, u);
      const hit = fixtures[k] ?? fixtures[key(req.method, u.split("?")[0])];
      res.writeHead(hit ? hit.status : 404, { "Content-Type": "application/json" });
      res.end(hit ? hit.body : '{"error":"not mocked"}');
      return;
    }
    let p = path.join(DIST, decodeURIComponent(u.split("?")[0]));
    if (!p.startsWith(DIST) || !fs.existsSync(p) || fs.statSync(p).isDirectory()) p = path.join(DIST, "index.html");
    if (path.basename(p) === "sw.js") { res.writeHead(404); res.end(); return; }
    res.writeHead(200, { "Content-Type": types[path.extname(p)] || "application/octet-stream" });
    fs.createReadStream(p).pipe(res);
  });
  return new Promise((r) => srv.listen(0, "127.0.0.1", () => r(srv)));
}

// Half size (2×2 averages): small baselines in the repository, and the
// anti-aliasing noise of one pixel left out.
function half(src) {
  const w = src.width >> 1, h = src.height >> 1;
  const out = new PNG({ width: w, height: h });
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      for (let c = 0; c < 4; c++) {
        const at = (yy, xx) => src.data[((yy * src.width) + xx) * 4 + c];
        out.data[(y * w + x) * 4 + c] = (at(2 * y, 2 * x) + at(2 * y, 2 * x + 1) + at(2 * y + 1, 2 * x) + at(2 * y + 1, 2 * x + 1)) >> 2;
      }
    }
  }
  return out;
}

function diff(a, b) {
  if (a.width !== b.width || a.height !== b.height) return { ratio: 1, png: null };
  const out = new PNG({ width: a.width, height: a.height });
  let n = 0;
  for (let i = 0; i < a.data.length; i += 4) {
    const d = Math.max(Math.abs(a.data[i] - b.data[i]), Math.abs(a.data[i + 1] - b.data[i + 1]), Math.abs(a.data[i + 2] - b.data[i + 2]));
    const hit = d > 40;
    if (hit) n++;
    out.data[i] = hit ? 255 : a.data[i] >> 2;
    out.data[i + 1] = hit ? 0 : a.data[i + 1] >> 2;
    out.data[i + 2] = hit ? 0 : a.data[i + 2] >> 2;
    out.data[i + 3] = 255;
  }
  return { ratio: n / (a.width * a.height), png: out };
}

async function prepare(page, base, size, look) {
  await page.goto(base + "/api/setup", { waitUntil: "load" }).catch(() => {});
  await page.evaluate(([look, mode]) => {
    localStorage.clear();
    localStorage.setItem("strata-lang", "en");
    localStorage.setItem("strata-theme", look.theme);
    if (look.style !== "glass") localStorage.setItem("strata-style", look.style);
    localStorage.setItem("strata-reduce-transparency", "0");
    localStorage.setItem("strata-ui-mode", mode);
  }, [look, size.mode]);
}

async function main() {
  const browser = await chromium.launch();
  if (MODE === "record") {
    // Signs in on a demo VM (test credentials) and keeps every API answer.
    const B = process.env.STRATA_URL || "http://localhost:7070";
    const fixtures = {};
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await ctx.newPage();
    page.on("response", async (r) => {
      const u = r.url();
      if (!u.includes("/api/") || r.request().method() !== "GET") return;
      try { fixtures[key("GET", u)] = { status: r.status(), body: await r.text() }; } catch { /* streamed */ }
    });
    await page.goto(B + "/", { waitUntil: "load" });
    await page.evaluate(async () => {
      await fetch("/api/login", { method: "POST", headers: { "X-Strata": "1", "Content-Type": "application/json" }, body: JSON.stringify({ username: "admin", password: "Strata-Test-2026" }) });
      await fetch("/api/me/2fa/postpone", { method: "POST", headers: { "X-Strata": "1" } });
      localStorage.setItem("strata-lang", "en");
      localStorage.setItem("strata-ui-mode", "classic");
    });
    for (const size of sizes) {
      await page.setViewportSize({ width: size.width, height: size.height });
      for (const p of pages) { await page.goto(B + p.path, { waitUntil: "load" }); await sleep(4000); }
    }
    fs.writeFileSync(FIX, JSON.stringify(fixtures, null, 0) + "\n");
    console.log(`${Object.keys(fixtures).length} answers recorded`);
    await browser.close();
    return;
  }
  const fixtures = JSON.parse(fs.readFileSync(FIX, "utf8"));
  const srv = await serve(fixtures);
  const base = `http://127.0.0.1:${srv.address().port}`;
  fs.mkdirSync(MODE === "update" ? BASE : OUT, { recursive: true });
  let failed = 0;
  for (const size of sizes) {
    for (const look of looks) {
      const ctx = await browser.newContext({ viewport: { width: size.width, height: size.height }, deviceScaleFactor: 1, reducedMotion: "reduce", colorScheme: look.theme });
      const page = await ctx.newPage();
      await page.clock.install({ time: NOW });
      await prepare(page, base, size, look);
      for (const p of pages) {
        const name = `${p.name}-${size.name}-${look.name}.png`;
        await page.goto(base + p.path, { waitUntil: "load" });
        await page.clock.runFor(4000);
        await sleep(1500);
        const shot = half(PNG.sync.read(await page.screenshot({ animations: "disabled", caret: "hide" })));
        if (MODE === "update") { fs.writeFileSync(path.join(BASE, name), PNG.sync.write(shot, { deflateLevel: 9 })); continue; }
        const ref = path.join(BASE, name);
        if (!fs.existsSync(ref)) { console.log(`NEW  ${name}`); fs.writeFileSync(path.join(OUT, name), PNG.sync.write(shot)); failed++; continue; }
        const d = diff(PNG.sync.read(fs.readFileSync(ref)), shot);
        const bad = d.ratio > 0.005;
        console.log(`${bad ? "DIFF" : "ok  "} ${name} ${(d.ratio * 100).toFixed(2)} %`);
        if (bad) {
          failed++;
          fs.writeFileSync(path.join(OUT, name.replace(".png", "-new.png")), PNG.sync.write(shot));
          if (d.png) fs.writeFileSync(path.join(OUT, name.replace(".png", "-diff.png")), PNG.sync.write(d.png));
        }
      }
      await ctx.close();
    }
  }
  await browser.close();
  srv.close();
  if (failed) { console.log(`${failed} page(s) differ: see ${OUT}`); process.exit(1); }
}

main().catch((e) => { console.error(e); process.exit(2); });
