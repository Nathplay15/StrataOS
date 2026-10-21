// Markdown rendered in a sandboxed frame with no script and no network: the
// file's HTML can never run on the interface origin.
import { useEffect, useState } from "react";
import { marked } from "marked";

const style = `body{font:14px/1.6 ui-sans-serif,system-ui,sans-serif;margin:0;padding:16px 20px;color:%fg;background:%bg;word-wrap:break-word}
pre,code{font-family:ui-monospace,monospace;font-size:12.5px}pre{background:%soft;padding:10px;border-radius:8px;overflow:auto}
code{background:%soft;padding:1px 4px;border-radius:4px}pre code{background:none;padding:0}
table{border-collapse:collapse}td,th{border:1px solid %line;padding:4px 8px}blockquote{margin:0;padding-left:12px;border-left:3px solid %line;color:%muted}
img{max-width:100%}a{color:%accent}h1,h2{border-bottom:1px solid %line;padding-bottom:4px}`;

export default function MarkdownPreview({ text }: { text: string }) {
  const [html, setHtml] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setHtml(marked.parse(text, { async: false, gfm: true }) as string), 150);
    return () => clearTimeout(t);
  }, [text]);
  const css = getComputedStyle(document.documentElement);
  const v = (n: string) => css.getPropertyValue(n).trim();
  const st = style.replace(/%fg/g, v("--text")).replace(/%bg/g, v("--panel")).replace(/%soft/g, v("--panel-2")).replace(/%line/g, v("--border")).replace(/%muted/g, v("--muted")).replace(/%accent/g, v("--accent"));
  const doc = `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:"><base target="_blank"><style>${st}</style></head><body>${html}</body></html>`;
  return <iframe title="preview" sandbox="" srcDoc={doc} className="h-full w-full border-0 bg-panel" />;
}
