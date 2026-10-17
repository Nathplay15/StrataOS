// Codemod: wraps the French UI texts of the given .tsx files in t("…").
// Usage: node scripts/i18n-wrap.cjs src/pages/Foo.tsx …   (rewrites the files)
const ts = require("typescript");
const fs = require("fs");
const path = require("path");

const SKIP_ATTRS = new Set(["className", "key", "href", "src", "type", "value", "id", "to", "path", "method", "autoComplete", "rel", "target", "role", "variant", "tone", "size", "mode", "name", "htmlFor", "accept", "pattern", "step", "min", "max", "d", "viewBox", "fill", "stroke", "inputMode", "lang", "dir", "wide", "style", "colors", "icon", "download", "sandbox", "allow", "spellCheck", "form", "media"]);
const TEXT_KEYS = new Set(["label", "sub", "title", "text", "success", "confirm", "hint", "subtitle", "message", "placeholder", "description", "caption", "error", "help", "tip", "desc", "unit", "short", "long", "name"]);

function human(s) {
  if (!/[A-Za-zÀ-ÿ]/.test(s)) return false;
  if (/^(https?:|\/|#|\.|\$)/.test(s.trim())) return false;
  if (/^[a-z0-9_.:\-\/*@]+$/.test(s)) return false;
  if (/^[A-Z0-9_.:\-\/]+$/.test(s) && !/\s/.test(s)) return false; // RAID, SMB…
  return /\s/.test(s) || /[À-ÿ]/.test(s) || /^[A-ZÀ-Ý][a-zà-ÿ]+[.…!?:]?$/.test(s);
}

const entities = { "&gt;": ">", "&lt;": "<", "&amp;": "&", "&quot;": '"', "&apos;": "'", "&#39;": "'", "&nbsp;": " " };
const decode = (s) => s.replace(/&(gt|lt|amp|quot|apos|nbsp|#39);/g, (m) => entities[m]);
const lit = (s) => JSON.stringify(s);

function allowedParent(node) {
  let p = node.parent;
  // Climb through wrappers that keep the value a value.
  while (p && (ts.isParenthesizedExpression(p) || ts.isAsExpression(p) || ts.isSatisfiesExpression?.(p))) p = p.parent;
  if (!p) return false;
  if (ts.isJsxAttribute(p)) return !SKIP_ATTRS.has(p.name.getText());
  if (ts.isJsxExpression(p)) {
    if (p.parent && ts.isJsxAttribute(p.parent)) return !SKIP_ATTRS.has(p.parent.name.getText());
    return true;
  }
  if (ts.isConditionalExpression(p)) return p.condition !== node && allowedParent(p);
  if (ts.isBinaryExpression(p)) {
    const op = p.operatorToken.kind;
    if (op === ts.SyntaxKind.BarBarToken || op === ts.SyntaxKind.QuestionQuestionToken) return p.left !== node || allowedParent(p);
    if (op === ts.SyntaxKind.PlusToken) return allowedParent(p);
    return false;
  }
  if (ts.isArrayLiteralExpression(p)) {
    // Tuples like ["802.3ad", "LACP …"] or ["Batterie faible", "danger"].
    return true;
  }
  if (ts.isPropertyAssignment(p)) {
    if (p.name === node) return false;
    const key = p.name.getText().replace(/["']/g, "");
    return TEXT_KEYS.has(key);
  }
  if (ts.isReturnStatement(p)) return true;
  if (ts.isCallExpression(p)) {
    const callee = p.expression.getText();
    return /(^|\.)toast$/.test(callee) || callee === "setStatus";
  }
  return false;
}

function transform(file) {
  const src = fs.readFileSync(file, "utf8");
  const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const edits = [];
  function visit(node) {
    if (ts.isImportDeclaration(node) || ts.isTypeNode(node)) return;
    if (ts.isCallExpression(node) && node.expression.getText() === "tr") return; // already wrapped
    if (ts.isJsxText(node)) {
      const raw = node.getFullText();
      const text = raw.trim();
      if (text && human(decode(text))) {
        const lead = raw.match(/^\s*/)[0], trail = raw.match(/\s*$/)[0];
        const sp = (w) => (w && !w.includes("\n") ? '{" "}' : w);
        edits.push([node.getStart(), node.getEnd(), sp(lead) + "{tr(" + lit(decode(text).replace(/\s+/g, " ")) + ")}" + sp(trail)]);
      }
      return;
    }
    if ((ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) && human(node.text) && allowedParent(node)) {
      const inAttr = node.parent && ts.isJsxAttribute(node.parent);
      const call = "tr(" + lit(node.text) + ")";
      edits.push([node.getStart(), node.getEnd(), inAttr ? "{" + call + "}" : call]);
      return;
    }
    if (ts.isTemplateExpression(node) && allowedParent(node)) {
      const parts = [node.head.text, ...node.templateSpans.map((s) => s.literal.text)];
      if (human(parts.join(" "))) {
        let key = node.head.text;
        const args = [];
        node.templateSpans.forEach((s, i) => { key += "{" + i + "}" + s.literal.text; args.push(s.expression.getText()); });
        edits.push([node.getStart(), node.getEnd(), "tr(" + lit(key) + ", " + args.join(", ") + ")"]);
        return;
      }
    }
    ts.forEachChild(node, visit);
  }
  visit(sf);
  if (!edits.length) return 0;
  let out = src;
  edits.sort((a, b) => b[0] - a[0]);
  for (const [s, e, r] of edits) out = out.slice(0, s) + r + out.slice(e);
  if (!/import \{[^}]*\btr\b[^}]*\} from "[^"]*lib\/i18n"/.test(out)) {
    let rel = path.relative(path.dirname(file), path.join(__dirname, "../src/lib/i18n")).replace(/\\/g, "/");
    if (!rel.startsWith(".")) rel = "./" + rel;
    const imports = [...out.matchAll(/^import .*;$/gm)];
    const at = imports.length ? imports[imports.length - 1].index + imports[imports.length - 1][0].length : 0;
    out = out.slice(0, at) + `\nimport { tr } from "${rel}";` + out.slice(at);
  }
  fs.writeFileSync(file, out);
  return edits.length;
}

let total = 0;
for (const f of process.argv.slice(2)) {
  const n = transform(f);
  total += n;
  if (n) console.log(`${n}\t${f}`);
}
console.log(`total ${total}`);
