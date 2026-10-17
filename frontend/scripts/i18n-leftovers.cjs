// Lists French-looking string literals that are not inside tr(…).
const ts = require("typescript"); const fs = require("fs");
const fr = /[éèêàùçôîâœ]|\b(le|la|les|des|du|une|un|est|sont|pour|avec|dans|sur|pas|aucun|aucune|cette|votre|vos)\b/i;
for (const f of process.argv.slice(2)) {
  const src = fs.readFileSync(f, "utf8");
  const sf = ts.createSourceFile(f, src, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  (function visit(n) {
    if (ts.isCallExpression(n) && n.expression.getText() === "tr") return;
    if (ts.isImportDeclaration(n)) return;
    if ((ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isJsxText(n)) && fr.test(n.text ?? n.getText()) && /\s/.test((n.text ?? n.getText()).trim())) {
      const { line } = sf.getLineAndCharacterOfPosition(n.getStart());
      console.log(`${f}:${line + 1}: ${JSON.stringify((n.text ?? n.getText()).trim()).slice(0, 110)}`);
    }
    if (ts.isTemplateExpression(n) && fr.test(n.getText())) {
      const { line } = sf.getLineAndCharacterOfPosition(n.getStart());
      console.log(`${f}:${line + 1}: TEMPLATE ${n.getText().slice(0, 110)}`);
      return;
    }
    ts.forEachChild(n, visit);
  })(sf);
}
