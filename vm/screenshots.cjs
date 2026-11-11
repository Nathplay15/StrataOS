// Takes the web screenshots of docs/screenshots/ from a demo VM (vm/demo-data.sh)
// with the Chromium of the Playwright Docker image (nothing to install on the host):
//   mkdir -p /tmp/pw && (cd /tmp/pw && npm i --no-save playwright@1.48.2)
//   docker run --rm --network host --ipc=host -u "$(id -u):$(id -g)" -e HOME=/tmp \
//     -e STRATA_URL=http://localhost:7070 -v /tmp/pw/node_modules:/pw/node_modules:ro \
//     -v $PWD/vm/screenshots.cjs:/pw/screenshots.cjs:ro -v $PWD/docs/screenshots:/out \
//     -v /usr/share/fonts/truetype/noto:/usr/share/fonts/truetype/noto-host:ro \
//     -w /pw mcr.microsoft.com/playwright:v1.48.2-noble node screenshots.cjs
// ONLY=desktop,backup retakes some. The image has no Inter: Noto Sans from the host
// (fonts-noto-core) avoids a CJK fallback font. Then shrink the files (pngquant,
// optipng). Credentials: the test-only demo administrator.
const { chromium } = require("playwright");
const path = require("path");
const B = process.env.STRATA_URL || "http://localhost:7070";
const OUT = process.env.OUT || "/out";
const ONLY = (process.env.ONLY || "").split(",").filter(Boolean);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function login(page, { mode = "desktop", windows = [], geometry = {}, theme = "dark" }) {
  await page.goto(B + "/", { waitUntil: "load" });
  await page.evaluate(async ([mode, windows, geometry, theme]) => {
    localStorage.clear();
    localStorage.setItem("strata-lang", "en");
    localStorage.setItem("strata-theme", theme);
    for (const [app, g] of Object.entries(geometry)) localStorage.setItem(`strata-win-${app}`, JSON.stringify({ ...g, sw: window.innerWidth, sh: window.innerHeight - 48 }));
    localStorage.setItem("strata-ui-mode", mode);
    sessionStorage.setItem("strata-open-windows", JSON.stringify(windows));
    await fetch("/api/login", { method: "POST", headers: { "X-Strata": "1", "Content-Type": "application/json" }, body: JSON.stringify({ username: "admin", password: "Strata-Test-2026" }) });
    await fetch("/api/me/2fa/postpone", { method: "POST", headers: { "X-Strata": "1" } });
  }, [mode, windows, geometry, theme]);
  await page.goto(B + "/", { waitUntil: "load" });
  await page.addStyleTag({ content: ':root{--font-sans:"Inter","Noto Sans",sans-serif !important}' });
  await sleep(5000); // charts and live data
}

const next = (p) => p.getByRole("button", { name: "Next", exact: true }).click();
const tab = (name) => async (p) => {
  await p.getByRole("button", { name, exact: true }).first().click();
  await sleep(2500);
};

const shots = {
  desktop: { windows: [{ app: "storage", section: "volumes" }], geometry: { storage: { x: 150, y: 60, w: 960, h: 600 } } },
  dashboard: { windows: [{ app: "overview", maximized: true }] },
  shares: { windows: [{ app: "control", section: "shares", maximized: true }] },
  appcenter: { windows: [{ app: "appcenter", maximized: true }] },
  backup: { windows: [{ app: "backup", maximized: true }] },
  "security-advisor": { windows: [{ app: "control", section: "security", maximized: true }] },
  datastores: {
    windows: [{ app: "backup", maximized: true }],
    after: async (p) => {
      await p.getByText("Backup repositories", { exact: true }).first().evaluate((e) => e.parentElement.scrollIntoView({ block: "start" }));
      await sleep(1500);
    },
  },
  firewall: { windows: [{ app: "control", section: "network", maximized: true }], after: tab("Firewall") },
  "virtual-networks": { windows: [{ app: "control", section: "network", maximized: true }], after: tab("Virtual networks") },
  restore: {
    windows: [{ app: "backup", maximized: true }],
    after: async (p) => {
      // Source → point in time → Documents → two folders → the restore step with its preview.
      await p.getByRole("button", { name: "Restore…" }).first().click();
      await sleep(2500);
      await p.getByText("Family files → repository").last().click();
      await next(p);
      await sleep(3000);
      await p.getByText(/\d\d:\d\d (AM|PM)/).first().click();
      await next(p);
      await sleep(3000);
      await p.getByText("Documents", { exact: true }).first().click();
      await sleep(2500);
      for (const n of ["Recipes", "School"]) {
        const bb = await p.getByText(n, { exact: true }).last().boundingBox();
        await p.mouse.click(252, bb.y + bb.height / 2); // its checkbox
      }
      await next(p);
      await sleep(2500);
      await p.getByRole("button", { name: /Preview/ }).first().click().catch(() => {});
      await sleep(4000);
    },
  },
  "fleet-health": { windows: [{ app: "fleet", maximized: true }], wait: 8000 },
  notifications: {
    windows: [{ app: "overview" }],
    geometry: { overview: { x: 120, y: 50, w: 900, h: 640 } },
    after: async (p) => {
      await p.locator('button[aria-haspopup="dialog"][aria-label^="Notifications"]').first().click();
      await sleep(1500);
    },
  },
  "desktop-light": { theme: "light", windows: [{ app: "files" }, { app: "storage", section: "disks" }], geometry: { files: { x: 90, y: 50, w: 820, h: 520 }, storage: { x: 330, y: 200, w: 900, h: 560 } } },
  mobile: { width: 390, height: 844, mode: "classic", scale: 2 },
};

(async () => {
  const browser = await chromium.launch();
  try {
    for (const [name, o] of Object.entries(shots)) {
      if (ONLY.length && !ONLY.includes(name)) continue;
      const phone = !!o.width && o.width < 600;
      const ctx = await browser.newContext({
        viewport: { width: o.width || 1440, height: o.height || 900 }, deviceScaleFactor: o.scale || 1, isMobile: phone, hasTouch: phone,
        locale: "en-US", timezoneId: process.env.TZ_NAS || "Europe/Paris", colorScheme: o.theme === "light" ? "light" : "dark",
      });
      const page = await ctx.newPage();
      try {
        await login(page, o);
        if (o.wait) await sleep(o.wait);
        if (o.after) await o.after(page);
        await page.screenshot({ path: path.join(OUT, name + ".png") });
        console.log("  " + name + ".png");
      } catch (e) {
        console.log("  FAILED " + name + ": " + e.message.split("\n")[0]);
      }
      await ctx.close();
    }
  } finally {
    await browser.close();
  }
})();
