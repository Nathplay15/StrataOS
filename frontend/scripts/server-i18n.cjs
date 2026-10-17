// Writes the server texts (src/i18n/server.ts + server.<lang>.json) for the
// backend, which translates notifications and reports with them:
//   node scripts/server-i18n.cjs ../backend/internal/i18n/texts.json
const ts = require("typescript"); const fs = require("fs"); const path = require("path");
const dir = path.join(__dirname, "../src/i18n");
const js = ts.transpileModule(fs.readFileSync(path.join(dir, "server.ts"), "utf8"), { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText;
const mod = { exports: {} };
new Function("module", "exports", js)(mod, mod.exports);
const out = { pairs: mod.exports.serverTexts };
for (const f of fs.readdirSync(dir).sort()) {
  const m = f.match(/^server\.([a-z]{2})\.json$/);
  if (!m) continue;
  out[m[1]] = JSON.parse(fs.readFileSync(path.join(dir, f), "utf8"));
  const miss = out.pairs.filter(([fr]) => !out[m[1]][fr]).length;
  if (miss) console.error(`${m[1]}: ${miss} server texts untranslated (English is used instead)`);
}
fs.writeFileSync(process.argv[2] || "/dev/stdout", JSON.stringify(out) + "\n");
