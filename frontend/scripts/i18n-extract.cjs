// Extracts every tr("…") key into a JSON object {fr: existing English or ""}.
const ts = require("typescript"); const fs = require("fs"); const path = require("path");
const enPath = path.join(__dirname, "../src/i18n/en.json");
const en = JSON.parse(fs.readFileSync(enPath, "utf8"));
const keys = new Set();
function walk(d) { for (const f of fs.readdirSync(d)) { const p = path.join(d, f); if (fs.statSync(p).isDirectory()) walk(p); else if (/\.tsx?$/.test(f) && !f.endsWith(".d.ts")) scan(p); } }
function scan(f) {
  const sf = ts.createSourceFile(f, fs.readFileSync(f, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  (function v(n) { if (ts.isCallExpression(n) && n.expression.getText() === "tr" && n.arguments[0] && ts.isStringLiteralLike(n.arguments[0])) keys.add(n.arguments[0].text); ts.forEachChild(n, v); })(sf);
}
walk(path.join(__dirname, "../src"));
const out = {};
for (const k of [...keys].sort()) out[k] = en[k] ?? "";
const missing = Object.values(out).filter((v) => !v).length;
fs.writeFileSync(process.argv[2] || "/dev/stdout", JSON.stringify(out, null, 1));
console.error(`${keys.size} keys, ${missing} untranslated (en)`);
for (const f of fs.readdirSync(path.dirname(enPath))) {
  const m = f.match(/^([a-z]{2})\.json$/);
  if (!m || m[1] === "en") continue;
  const d = JSON.parse(fs.readFileSync(path.join(path.dirname(enPath), f), "utf8"));
  const miss = [...keys].filter((k) => !d[k]).length;
  if (miss) console.error(`${m[1]}: ${miss} untranslated (English is shown instead)`);
}
