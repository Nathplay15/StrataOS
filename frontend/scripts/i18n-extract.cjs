// Checks that every tr("…") text is translated in every src/i18n/<lang>.json
// (the English source is shown for missing entries):
//   node scripts/i18n-extract.cjs [--check] [missing.json]
// Without --check, writes the untranslated texts per language to missing.json
// (or stdout) so they can be filled in.
const ts = require("typescript"); const fs = require("fs"); const path = require("path");
const dir = path.join(__dirname, "../src/i18n");
const keys = new Set();
function walk(d) { for (const f of fs.readdirSync(d)) { const p = path.join(d, f); if (fs.statSync(p).isDirectory()) walk(p); else if (/\.tsx?$/.test(f) && !f.endsWith(".d.ts")) scan(p); } }
function scan(f) {
  const sf = ts.createSourceFile(f, fs.readFileSync(f, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  (function v(n) { if (ts.isCallExpression(n) && n.expression.getText() === "tr" && n.arguments[0] && ts.isStringLiteralLike(n.arguments[0])) keys.add(n.arguments[0].text); ts.forEachChild(n, v); })(sf);
}
walk(path.join(__dirname, "../src"));
console.error(`${keys.size} texts`);
const missing = {};
for (const f of fs.readdirSync(dir).sort()) {
  const m = f.match(/^([a-z]{2})\.json$/);
  if (!m) continue;
  const d = JSON.parse(fs.readFileSync(path.join(dir, f), "utf8"));
  const miss = [...keys].filter((k) => !d[k]).sort();
  if (miss.length) { console.error(`${m[1]}: ${miss.length} untranslated`); missing[m[1]] = miss; }
}
const check = process.argv.includes("--check");
const outFile = process.argv.slice(2).find((a) => !a.startsWith("--"));
if (!check) fs.writeFileSync(outFile || "/dev/stdout", JSON.stringify(missing, null, 1) + "\n");
// --check (CI): fail when a text is not translated in every language.
if (check && Object.keys(missing).length) process.exit(1);
