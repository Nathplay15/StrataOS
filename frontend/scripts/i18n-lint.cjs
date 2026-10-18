// Spots likely translation mistakes in src/i18n/*.json: placeholders ({0}…)
// lost or added, leading/trailing spaces or final punctuation that differ
// from the source text, and English sentences left untranslated.
//   node scripts/i18n-lint.cjs [--check] [lang…]
const fs = require("fs"); const path = require("path");
const dir = path.join(__dirname, "../src/i18n");
const args = process.argv.slice(2);
const only = args.filter((a) => !a.startsWith("--"));
const ph = (s) => (s.match(/\{\d\}/g) || []).sort().join(",");
const edge = (s) => [/^\s/.test(s), /\s$/.test(s)].join();
// Final punctuation, ignoring the different spacing of French (" :", " ?").
const end = (s) => { const m = s.trim().match(/[.:?!…]$/); return m ? m[0].replace(/[.!]/, ".") : ""; };
let problems = 0;
for (const f of fs.readdirSync(dir).sort()) {
  const m = f.match(/^(server\.)?([a-z]{2})\.json$/);
  if (!m || (only.length && !only.includes(m[2]))) continue;
  const d = JSON.parse(fs.readFileSync(path.join(dir, f), "utf8"));
  const out = [];
  for (const [src, t] of Object.entries(d)) {
    if (ph(src) !== ph(t)) out.push(`placeholders ${JSON.stringify(src)} → ${JSON.stringify(t)}`);
    else if (m[2] !== "zh" && edge(src) !== edge(t) && !/^[,:]/.test(t)) out.push(`spaces ${JSON.stringify(src)} → ${JSON.stringify(t)}`);
    else if (m[2] !== "zh" && !/\s$/.test(src) && end(src) !== end(t) && !/[)\]}"»]$/.test(t.trim()) && !/(^|\s)\p{L}{1,5}\.$/u.test(t.trim())) out.push(`punctuation ${JSON.stringify(src)} → ${JSON.stringify(t)}`);
    else if (src === t && /\b(the|and|of|to|is|your|with|for|this|in|on)\b/i.test(src) && !m[1]) out.push(`untranslated ${JSON.stringify(src)}`);
  }
  if (out.length) console.log(`${f}: ${out.length}\n  ` + out.join("\n  "));
  problems += out.length;
}
console.error(`${problems} possible problem(s)`);
if (args.includes("--check") && problems) process.exit(1);
