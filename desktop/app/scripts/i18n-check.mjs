// Lists the texts of the app (t("…") in src/) that neither the app's own
// dictionaries (src/i18n/<lang>.json) nor the web interface's
// (frontend/src/i18n/<lang>.json) translate. Exits with an error when a
// French text is missing (the other languages are reported).
//   node scripts/i18n-check.mjs [--json LANG]   (--json: the missing texts as a JSON object to fill)
import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const app = join(dirname(fileURLToPath(import.meta.url)), "..");
const nas = join(app, "../../frontend/src/i18n");
const langs = ["fr", "de", "es", "it", "nl", "pt", "zh"];

const files = [];
(function walk(d) {
  for (const f of readdirSync(d)) {
    const p = join(d, f);
    if (statSync(p).isDirectory()) walk(p);
    else if (/\.tsx?$/.test(f)) files.push(p);
  }
})(join(app, "src"));

const texts = new Set();
const re = /\bt\(\s*"((?:[^"\\]|\\.)*)"/g;
for (const f of files) {
  for (const m of readFileSync(f, "utf8").matchAll(re)) texts.add(JSON.parse(`"${m[1]}"`));
}

const load = (p) => (existsSync(p) ? JSON.parse(readFileSync(p, "utf8")) : {});
const out = process.argv[2] === "--json" ? process.argv[3] : "";
let failed = false;
for (const l of langs) {
  const own = load(join(app, "src/i18n", `${l}.json`));
  const web = load(join(nas, `${l}.json`));
  const missing = [...texts].filter((s) => !own[s] && !web[s]).sort();
  if (out === l) {
    console.log(JSON.stringify(Object.fromEntries(missing.map((s) => [s, ""])), null, 2));
    continue;
  }
  if (out) continue;
  console.log(`${l}: ${missing.length} missing of ${texts.size}`);
  if (l === "fr" && missing.length) {
    failed = true;
    for (const s of missing) console.log(`  ${s}`);
  }
}
process.exit(failed ? 1 : 0);
