// Takes the screenshots of docs/screenshots/ from a demo VM (vm/demo-data.sh)
// with Firefox, driven by puppeteer-core (not a project dependency):
//   npm i --no-save puppeteer-core   (PUPPETEER_SKIP_DOWNLOAD=1: Firefox is used)
//   STRATA_URL=http://localhost:7140 node vm/screenshots.cjs
// Credentials: the test-only demo administrator.
const puppeteer = require("puppeteer-core");
const path = require("path");
const B = process.env.STRATA_URL || "http://localhost:7070";
const OUT = path.join(__dirname, "../docs/screenshots");
const FIREFOX = process.env.FIREFOX || "/usr/bin/firefox";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function prepare(page, { mode, windows = [], geometry = {} }) {
  await page.goto(B + "/", { waitUntil: "load" });
  await page.evaluate(async (mode, windows, geometry) => {
    localStorage.setItem("strata-lang", "en");
    for (const [app, g] of Object.entries(geometry)) localStorage.setItem(`strata-win-${app}`, JSON.stringify(g));
    localStorage.setItem("strata-ui-mode", mode);
    sessionStorage.setItem("strata-open-windows", JSON.stringify(windows));
    await fetch("/api/login", { method: "POST", headers: { "X-Strata": "1", "Content-Type": "application/json" }, body: JSON.stringify({ username: "admin", password: "Strata-Test-2026" }) });
  }, mode, windows, geometry);
  await page.goto(B + "/", { waitUntil: "load" });
  await sleep(4000); // charts and live data
}

async function shot(browser, name, { width = 1600, height = 1000, mode = "desktop", windows, geometry, url }) {
  const page = await browser.newPage();
  await page.setViewport({ width, height });
  await prepare(page, { mode, windows, geometry });
  if (url) {
    await page.goto(B + url, { waitUntil: "load" });
    await sleep(4000);
  }
  await page.screenshot({ path: path.join(OUT, name + ".png") });
  console.log("  " + name + ".png");
  await page.close();
}

(async () => {
  require("fs").mkdirSync(OUT, { recursive: true });
  const browser = await puppeteer.launch({ browser: "firefox", executablePath: FIREFOX, headless: true });
  try {
    await shot(browser, "desktop", { windows: [{ app: "storage", section: "volumes" }], geometry: { storage: { x: 240, y: 70, w: 1010, h: 600 } } });
    await shot(browser, "shares", { windows: [{ app: "control", section: "shares", maximized: true }] });
    await shot(browser, "appcenter", { windows: [{ app: "appcenter", maximized: true }] });
    await shot(browser, "dashboard", { windows: [{ app: "overview", maximized: true }] });
    await shot(browser, "backup", { windows: [{ app: "backup", maximized: true }] });
    await shot(browser, "mobile", { width: 390, height: 844, mode: "classic" });
  } finally {
    await browser.close();
  }
})();
