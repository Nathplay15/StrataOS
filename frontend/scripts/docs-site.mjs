#!/usr/bin/env node
// Builds the StrataOS website from the repository's Markdown, so the docs
// have one source of truth: the download page (packaging/pages/index.html)
// at the root and the documentation under docs/ (user guide split into one
// page per section, FAQ, hardware, release notes, developers), in English
// and French, plus a client-side search index and a 404 page.
//   node frontend/scripts/docs-site.mjs [output folder, default dist/site]
// Env: VERSION (default: the VERSION file), REPO (owner/name, for links),
// SITE_BASE (path the site is served under, for the 404 page).
// Only the generated parts of the output folder are replaced, so the APT
// repository (/apt) can live next to them.
import { Marked } from "marked";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const OUT = path.resolve(process.argv[2] || path.join(ROOT, "dist/site"));
const VERSION = process.env.VERSION || fs.readFileSync(path.join(ROOT, "VERSION"), "utf8").trim();
const REPO = process.env.REPO || "Nathplay15/StrataOS";
const BASE = process.env.SITE_BASE || `/${REPO.split("/")[1]}/`;
const GH = `https://github.com/${REPO}`;
const LANGS = ["en", "fr"];

const UI = {
  en: {
    docs: "Documentation", overview: "Overview", guide: "User guide", reference: "Reference", project: "Project",
    faq: "FAQ", hardware: "Hardware", notes: "Release notes", developers: "Developers", contributing: "Contributing and security",
    download: "Download", source: "Source code", menu: "Menu", skip: "Skip to content", search: "Search the documentation",
    searchPh: "Search…", onPage: "On this page", edit: "Edit this page on GitHub", prev: "Previous", next: "Next",
    anchor: "Link to this section", other: "Français", otherLang: "fr", onlyEnglish: "",
    docsTitle: "StrataOS documentation", guideSections: "The user guide, step by step", more: "More",
    releaseOn: "Release on GitHub", license: "Apache License 2.0", version: "Version {0}",
    faqDesc: "Short answers to common questions", hardwareDesc: "Tested machines and how to check yours",
    notesDesc: "What changed in each version", developersDesc: "Build, test and release StrataOS",
    contributingDesc: "Report a bug or a vulnerability, translate, contribute code",
  },
  fr: {
    docs: "Documentation", overview: "Présentation", guide: "Guide d'utilisation", reference: "Référence", project: "Projet",
    faq: "FAQ", hardware: "Matériel", notes: "Notes de version", developers: "Développeurs", contributing: "Contribuer et sécurité",
    download: "Télécharger", source: "Code source", menu: "Menu", skip: "Aller au contenu", search: "Rechercher dans la documentation",
    searchPh: "Rechercher…", onPage: "Sur cette page", edit: "Modifier cette page sur GitHub", prev: "Précédent", next: "Suivant",
    anchor: "Lien vers cette section", other: "English", otherLang: "en",
    onlyEnglish: "Cette page n'existe qu'en anglais.",
    docsTitle: "Documentation de StrataOS", guideSections: "Le guide d'utilisation, étape par étape", more: "Et aussi",
    releaseOn: "Version sur GitHub", license: "Licence Apache 2.0", version: "Version {0}",
    faqDesc: "Des réponses courtes aux questions fréquentes", hardwareDesc: "Les machines testées et comment vérifier la vôtre",
    notesDesc: "Ce qui change à chaque version", developersDesc: "Compiler, tester et publier StrataOS",
    contributingDesc: "Signaler un bug ou une faille, traduire, contribuer au code",
  },
};

// Screenshots shown in guide sections (by section number).
const SHOTS = {
  3: ["desktop.png", "The StrataOS desktop with the Storage Manager open", "Le bureau de StrataOS avec le Gestionnaire de stockage ouvert"],
  5: ["shares.png", "Shared folders with SMB, NFS and Time Machine", "Les dossiers partagés avec SMB, NFS et Time Machine"],
  9: ["backup.png", "Backup tasks, versioned and encrypted", "Les tâches de sauvegarde, versionnées et chiffrées"],
  14: ["appcenter.png", "The App Center and its ready-to-run apps", "L'App Center et ses applications prêtes à l'emploi"],
  19: ["dashboard.png", "Overview: resources, network and disk activity", "Vue d'ensemble : ressources, réseau et activité des disques"],
};

const read = (p) => fs.readFileSync(path.join(ROOT, p), "utf8");
const exists = (p) => fs.existsSync(path.join(ROOT, p));
const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
const fmt = (s, ...a) => s.replace(/\{(\d)\}/g, (_, i) => a[i]);
const slugify = (s) => s.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "section";
const plain = (html) => html.replace(/<a class="anchor"[^>]*>.*?<\/a>/g, "").replace(/<\/?(p|li|h\d|t[rdh]|pre|div|[uo]l|br|figure|table|blockquote|section)\b[^>]*>/g, " ").replace(/<[^>]+>/g, "").replace(/&nbsp;/g, " ").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, "&").replace(/\s+/g, " ").trim();
const rel = (from, to) => path.posix.relative(path.posix.dirname(from), to) || path.posix.basename(to);

// Splits a Markdown file into its "# title", the text before the first
// "## " heading, and the "## " sections.
function parseDoc(md) {
  const lines = md.replace(/\r/g, "").split("\n");
  let title = "", intro = [], sections = [], cur = null, fence = false;
  for (const l of lines) {
    if (/^(```|~~~)/.test(l)) fence = !fence;
    if (!fence && !title && /^# /.test(l)) { title = l.slice(2).trim(); continue; }
    if (!fence && /^## /.test(l)) { cur = { title: l.slice(3).trim(), body: [] }; sections.push(cur); continue; }
    (cur ? cur.body : intro).push(l);
  }
  return { title, intro: intro.join("\n").trim(), sections: sections.map((s) => ({ ...s, body: s.body.join("\n").trim() })) };
}

// ---- Pages ----

// key -> output file under docs/ (the French one is under docs/fr/).
const FILES = { overview: "index.html", faq: "faq.html", hardware: "hardware.html", notes: "release-notes.html", developers: "developers.html", contributing: "contributing.html" };
const out = (key, lang) => `docs/${lang === "fr" ? "fr/" : ""}${FILES[key]}`;

const guide = { en: parseDoc(read("docs/GUIDE.md")), fr: parseDoc(read("docs/GUIDE.fr.md")) };
const sectionNum = (t) => Number((t.match(/^(\d+)\./) || [])[1]);
const sectionTitle = (t) => t.replace(/^\d+\.\s*/, "");
guide.en.sections.forEach((s) => {
  s.num = sectionNum(s.title);
  FILES[`guide-${s.num}`] = `guide/${slugify(sectionTitle(s.title))}.html`;
});
guide.fr.sections.forEach((s) => (s.num = sectionNum(s.title)));

// Repository file -> page, for internal links ("lang" pins a language).
const LINKS = {
  "README.md": { key: "overview" }, "docs/GUIDE.md": { key: "overview", lang: "en" }, "docs/GUIDE.fr.md": { key: "overview", lang: "fr" },
  "docs/FAQ.md": { key: "faq", lang: "en" }, "docs/FAQ.fr.md": { key: "faq", lang: "fr" }, "docs/HARDWARE.md": { key: "hardware" },
  "docs/DEVELOPMENT.md": { key: "developers" }, "CONTRIBUTING.md": { key: "contributing" }, "SECURITY.md": { key: "contributing", hash: "security-policy" },
};

// ---- Markdown ----

let ctx; // the page being rendered: { src, out, lang, shift, toc, ids }

function rewrite(href) {
  if (!href || /^(https?:|mailto:)/.test(href)) return href;
  const m = href.match(/^#guide-(\d+)$/);
  if (m && FILES[`guide-${m[1]}`]) return rel(ctx.out, out(`guide-${m[1]}`, ctx.lang));
  if (href.startsWith("#")) return href;
  const [p, hash] = href.split("#");
  const target = path.posix.normalize(path.posix.join(path.posix.dirname(ctx.src), p));
  if (target.startsWith("docs/screenshots/")) return rel(ctx.out, target.slice(5));
  if (target.startsWith("packaging/release-notes/")) return rel(ctx.out, out("notes", ctx.lang)) + "#v" + path.posix.basename(target, ".md");
  const l = LINKS[target];
  if (l) {
    const h = hash || l.hash;
    return rel(ctx.out, out(l.key, l.lang || ctx.lang)) + (h ? "#" + h : "");
  }
  return `${GH}/${exists(target) && fs.statSync(path.join(ROOT, target)).isDirectory() ? "tree" : "blob"}/main/${target}${hash ? "#" + hash : ""}`;
}

function pngSize(file) {
  try {
    const b = fs.readFileSync(file);
    if (b.toString("ascii", 1, 4) === "PNG") return [b.readUInt32BE(16), b.readUInt32BE(20)];
  } catch {}
  return null;
}

function img(src, alt, eager = false) {
  const size = pngSize(path.join(OUT, path.posix.normalize(path.posix.join(path.posix.dirname(ctx.out), src))));
  return `<img src="${esc(src)}" alt="${esc(alt)}"${size ? ` width="${size[0]}" height="${size[1]}"` : ""}${eager ? "" : ' loading="lazy"'} decoding="async">`;
}

const marked = new Marked({
  gfm: true,
  walkTokens(t) {
    if (t.type === "link" || t.type === "image") t.href = rewrite(t.href);
    if (t.type === "heading" && ctx.shift) t.depth = Math.min(6, t.depth + ctx.shift);
  },
  renderer: {
    heading({ tokens, depth }) {
      const html = this.parser.parseInline(tokens);
      const base = (ctx.idPrefix || "") + slugify(plain(html));
      let id = base;
      for (let i = 2; ctx.ids.has(id); i++) id = `${base}-${i}`;
      ctx.ids.add(id);
      if (depth === 2) ctx.toc.push({ id, html });
      return `<h${depth} id="${id}">${html}<a class="anchor" href="#${id}" aria-label="${esc(UI[ctx.lang].anchor)}">#</a></h${depth}>\n`;
    },
    image({ href, text }) {
      return img(href, text);
    },
  },
});

function render(md, opts) {
  ctx = { toc: [], ids: new Set(), shift: 0, ...opts };
  // Wide tables scroll on their own on small screens.
  const html = marked.parse(md).replace(/<table>/g, '<div class="table-wrap"><table>').replace(/<\/table>/g, "</table></div>");
  return { html, toc: ctx.toc };
}

// ---- Layout ----

function nav(lang, current) {
  const u = UI[lang];
  const link = (key, label) => {
    const o = out(key, lang);
    return `<li><a href="${rel(current, o)}"${o === current ? ' aria-current="page"' : ""}>${label}</a></li>`;
  };
  const sections = guide[lang].sections.length === guide.en.sections.length ? guide[lang].sections : guide.en.sections;
  return `<nav id="sidebar" class="sidebar" aria-label="${esc(u.docs)}">
<ul class="nav-group">${link("overview", u.overview)}</ul>
<h2 class="nav-title">${u.guide}</h2>
<ol class="nav-group guide">${sections.map((s) => link(`guide-${s.num}`, `<span class="n">${s.num}.</span> ${esc(sectionTitle(s.title))}`)).join("")}</ol>
<h2 class="nav-title">${u.reference}</h2>
<ul class="nav-group">${link("faq", u.faq)}${link("hardware", u.hardware)}${link("notes", u.notes)}</ul>
<h2 class="nav-title">${u.project}</h2>
<ul class="nav-group">${link("developers", u.developers)}${link("contributing", u.contributing)}</ul>
</nav>`;
}

function header(lang, current, { otherHref, docs = true } = {}) {
  const u = UI[lang];
  const root = rel(current, "index.html").replace(/index\.html$/, "") || "./";
  const docsHome = rel(current, out("overview", lang));
  const searchIndex = rel(current, `docs/search-${lang}.js`);
  const docsBase = rel(current, "docs/index.html").replace(/index\.html$/, "");
  return `<a class="skip" href="#content">${u.skip}</a>
<header class="top">
<a class="brand" href="${root}index.html"><img src="${root}assets/logo.svg" alt="" width="28" height="28"><span>StrataOS</span></a>
<a class="brand-docs" href="${docsHome}">${u.docs}</a>
${docs ? `<button class="menu-btn" type="button" aria-expanded="false" aria-controls="sidebar">${u.menu}</button>` : ""}
<form class="search" role="search" action="${docsHome}" data-index="${searchIndex}" data-base="${docsBase}">
<label class="sr" for="q">${u.search}</label>
<input id="q" type="search" placeholder="${esc(u.searchPh)}" autocomplete="off" spellcheck="false" aria-controls="search-results">
<ul id="search-results" class="results" hidden></ul>
<p class="sr" aria-live="polite" id="search-status"></p>
</form>
<nav class="top-links" aria-label="StrataOS">
<a href="${root}index.html#download">${u.download}</a>
${otherHref ? `<a href="${otherHref}" hreflang="${u.otherLang}" lang="${u.otherLang}">${u.other}</a>` : ""}
<a href="${GH}">GitHub</a>
</nav>
</header>`;
}

function head(lang, current, title, desc, extra = "") {
  const root = rel(current, "index.html").replace(/index\.html$/, "");
  return `<!doctype html>
<html lang="${lang}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(title)}</title>
<meta name="description" content="${esc(desc)}">
<meta name="color-scheme" content="light dark">
${extra}<link rel="icon" href="${root}assets/logo.svg" type="image/svg+xml">
<link rel="stylesheet" href="${root}assets/site.css">
<script>document.documentElement.classList.add("js")</script>
<script src="${root}assets/site.js" defer></script>
</head>`;
}

function describe(html) {
  const p = (html.match(/<p>([\s\S]*?)<\/p>/) || [])[1] || "";
  const t = plain(p);
  return t.length > 160 ? t.slice(0, 157).replace(/\s+\S*$/, "") + "…" : t;
}

const search = { en: [], fr: [] };

// One search entry per page and per h2/h3 heading; "nest" names the h2
// above an h3 too (release notes: "Release notes › StrataOS 0.6.1 › Fixes").
function indexPage(lang, o, title, html, nest) {
  const u = o.slice("docs/".length);
  let h2 = "";
  html.split(/(?=<h[23] id=")/).forEach((part) => {
    const m = part.match(/^<h([23]) id="([^"]+)">([\s\S]*?)<\/h[23]>/);
    const x = plain(m ? part.slice(m[0].length) : part);
    if (!m) {
      if (x) search[lang].push({ t: title, u, x: x.slice(0, 4000) });
      return;
    }
    const h = plain(m[3]);
    if (m[1] === "2") h2 = h;
    search[lang].push({ t: [title, nest && m[1] === "3" ? h2 : "", h].filter(Boolean).join(" › "), u: `${u}#${m[2]}`, x: x.slice(0, 4000) });
  });
}

const pages = []; // every page written, for the summary

function writePage(p, i, list) {
  const u = UI[p.lang];
  const other = LANGS.find((l) => l !== p.lang);
  const otherOut = out(p.key, other);
  const prev = list[i - 1], next = list[i + 1];
  const toc = p.toc && p.toc.length >= 3
    ? `<nav class="toc" aria-labelledby="toc-${p.key}"><h2 id="toc-${p.key}">${u.onPage}</h2><ul>${p.toc.map((t) => `<li><a href="#${t.id}">${plain(t.html)}</a></li>`).join("")}</ul></nav>`
    : "";
  const alt = LANGS.map((l) => `<link rel="alternate" hreflang="${l}" href="${rel(p.out, out(p.key, l))}">\n`).join("");
  const html = `${head(p.lang, p.out, `${p.title} · StrataOS`, p.desc || describe(p.html), alt)}
<body>
${header(p.lang, p.out, { otherHref: rel(p.out, otherOut) })}
<div class="layout">
${nav(p.lang, p.out)}
<main id="content" class="content" tabindex="-1">
${p.fallback ? `<p class="note" lang="fr">${u.onlyEnglish}</p>` : ""}
<article${p.fallback ? ' lang="en"' : ""}${p.cls ? ` class="${p.cls}"` : ""}>
${p.eyebrow ? `<p class="eyebrow">${p.eyebrow}</p>` : ""}<h1>${esc(p.title)}</h1>
${p.html.startsWith("<p>") ? p.html.replace(/<\/p>\n?/, `</p>\n${toc}\n`) : toc + "\n" + p.html}
</article>
<nav class="pager" aria-label="${u.prev} / ${u.next}">
${prev ? `<a class="prev" href="${rel(p.out, prev.out)}" rel="prev"><span>${u.prev}</span>${esc(prev.title)}</a>` : "<span></span>"}
${next ? `<a class="next" href="${rel(p.out, next.out)}" rel="next"><span>${u.next}</span>${esc(next.title)}</a>` : "<span></span>"}
</nav>
<footer class="foot">
<a href="${GH}/edit/main/${p.edit}">${u.edit}</a>
<span>StrataOS ${esc(VERSION)} · <a href="${GH}/blob/main/LICENSE">${u.license}</a></span>
</footer>
</main>
</div>
</body>
</html>
`;
  fs.mkdirSync(path.dirname(path.join(OUT, p.out)), { recursive: true });
  fs.writeFileSync(path.join(OUT, p.out), html);
  indexPage(p.lang, p.out, p.title, p.html, p.key === "notes");
}

// A Markdown page, French when a .fr.md exists.
function mdPage(key, src, lang, opts = {}) {
  const frSrc = src.replace(/\.md$/, ".fr.md");
  const useFr = lang === "fr" && exists(frSrc);
  const file = useFr ? frSrc : src;
  const doc = parseDoc(read(file));
  const o = out(key, lang);
  const body = (opts.prepend || "") + read(file).replace(/\r/g, "").replace(/^# .*\n/m, "") + (opts.append || "");
  const r = render(body, { src: file, out: o, lang });
  return { key, lang, out: o, title: opts.title || doc.title, html: r.html, toc: r.toc, edit: file, fallback: lang === "fr" && !useFr };
}

function semver(a, b) {
  const pa = a.split(".").map(Number), pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pb[i] - pa[i];
  return 0;
}

function build() {
  // Clean only what this script owns.
  for (const p of ["docs", "assets", "screenshots", "index.html", "404.html"]) fs.rmSync(path.join(OUT, p), { recursive: true, force: true });
  fs.mkdirSync(path.join(OUT, "assets"), { recursive: true });
  fs.cpSync(path.join(ROOT, "docs/screenshots"), path.join(OUT, "screenshots"), { recursive: true });
  for (const f of ["site.css", "site.js"]) fs.copyFileSync(path.join(ROOT, "packaging/pages", f), path.join(OUT, "assets", f));
  fs.copyFileSync(path.join(ROOT, "frontend/public/logo.svg"), path.join(OUT, "assets/logo.svg"));

  const notes = fs.readdirSync(path.join(ROOT, "packaging/release-notes")).filter((f) => /^\d+\.\d+\.\d+\.md$/.test(f)).map((f) => f.slice(0, -3)).sort(semver);

  for (const lang of LANGS) {
    const u = UI[lang];
    const g = guide[lang].sections.length === guide.en.sections.length ? guide[lang] : guide.en;
    const gsrc = g === guide.fr ? "docs/GUIDE.fr.md" : "docs/GUIDE.md";
    const list = [];

    // Overview: the guide's introduction, a screenshot and a map of the docs.
    {
      const o = out("overview", lang);
      const r = render(g.intro, { src: gsrc, out: o, lang });
      const card = (key, title, desc) => `<li><a href="${rel(o, out(key, lang))}"><strong>${esc(title)}</strong><span>${esc(desc)}</span></a></li>`;
      ctx = { out: o };
      const html = `${r.html}
<figure class="hero">${img(rel(o, "screenshots/desktop.png"), SHOTS[3][lang === "fr" ? 2 : 1], true)}</figure>
<h2 id="guide">${u.guideSections}</h2>
<ol class="cards guide-cards">${g.sections.map((s) => `<li><a href="${rel(o, out(`guide-${s.num}`, lang))}"><span class="n">${s.num}</span><strong>${esc(sectionTitle(s.title))}</strong></a></li>`).join("")}</ol>
<h2 id="more">${u.more}</h2>
<ul class="cards">${card("faq", u.faq, u.faqDesc)}${card("hardware", u.hardware, u.hardwareDesc)}${card("notes", u.notes, u.notesDesc)}${card("developers", u.developers, u.developersDesc)}${card("contributing", u.contributing, u.contributingDesc)}</ul>`;
      list.push({ key: "overview", lang, out: o, title: u.docsTitle, html, edit: gsrc, cls: "overview" });
    }

    // One page per guide section; "section N" in the text links to it.
    for (const s of g.sections) {
      const key = `guide-${s.num}`;
      const o = out(key, lang);
      const body = s.body.replace(/\b([Ss]ection) (\d{1,2})\b/g, (m, w, n) => (FILES[`guide-${n}`] ? `[${w} ${n}](#guide-${n})` : m));
      const r = render(body, { src: gsrc, out: o, lang });
      let html = r.html;
      if (SHOTS[s.num]) {
        const [f, en, fr] = SHOTS[s.num];
        ctx = { out: o };
        const fig = `<figure>${img(rel(o, "screenshots/" + f), lang === "fr" ? fr : en)}</figure>`;
        html = html.replace(/<\/p>/, `</p>\n${fig}`);
      }
      list.push({ key, lang, out: o, title: sectionTitle(s.title), eyebrow: `${u.guide} · ${s.num}`, html, toc: r.toc, edit: gsrc });
    }

    list.push(mdPage("faq", "docs/FAQ.md", lang));
    list.push(mdPage("hardware", "docs/HARDWARE.md", lang));

    // Release notes, newest first, each file's headings one level down.
    {
      const o = out("notes", lang);
      let html = "";
      const toc = [];
      for (const v of notes) {
        const r = render(read(`packaging/release-notes/${v}.md`), { src: `packaging/release-notes/${v}.md`, out: o, lang, shift: 1, idPrefix: `v${v}-` });
        toc.push({ id: `v${v}`, html: `StrataOS ${v}` });
        html += `<section class="release" aria-labelledby="v${v}"><h2 id="v${v}">StrataOS ${v}<a class="anchor" href="#v${v}" aria-label="${esc(u.anchor)}">#</a></h2>
<p class="release-link"><a href="${GH}/releases/tag/v${v}">${u.releaseOn}</a></p>
${r.html}</section>\n`;
      }
      list.push({ key: "notes", lang, out: o, title: u.notes, html, toc, edit: notes.length ? `packaging/release-notes/${notes[0]}.md` : "packaging/release-notes", fallback: lang === "fr" });
    }

    list.push(mdPage("developers", "docs/DEVELOPMENT.md", lang));
    // CONTRIBUTING.md then SECURITY.md (its title becomes a section).
    list.push(mdPage("contributing", "CONTRIBUTING.md", lang, { title: u.contributing, append: "\n\n" + read("SECURITY.md").replace(/^# /m, "## ") }));

    list.forEach((p, i) => writePage(p, i, list));
    pages.push(...list);
    fs.writeFileSync(path.join(OUT, `docs/search-${lang}.js`), `window.STRATA_SEARCH=${JSON.stringify(search[lang])};\n`);
  }

  // Download page: the template with the version, the repository and the
  // shared header.
  const tpl = read("packaging/pages/index.html");
  fs.writeFileSync(path.join(OUT, "index.html"), tpl
    .replace("@HEADER@", header("en", "index.html", { docs: false }))
    .replace(/@VERSION@/g, VERSION).replace(/@REPO@/g, REPO));

  // 404 page: served for any missing path, so its links go through <base>.
  ctx = { out: "404.html" };
  const nf = `${head("en", "404.html", "Page not found · StrataOS", "This page does not exist.", `<base href="${esc(BASE)}">\n`)}
<body>
${header("en", "404.html", { docs: false })}
<main id="content" class="content narrow" tabindex="-1">
<article>
<p class="eyebrow">404</p>
<h1>Page not found</h1>
<p>This page doesn't exist, or it moved. Try the search above, or start from one of these pages:</p>
<ul class="cards">
<li><a href="docs/index.html"><strong>${UI.en.docsTitle}</strong><span>${UI.en.guideSections}</span></a></li>
<li><a href="docs/fr/index.html" lang="fr"><strong>${UI.fr.docsTitle}</strong><span>Page introuvable : repartez d'ici</span></a></li>
<li><a href="index.html#download"><strong>${UI.en.download}</strong><span>${esc(fmt(UI.en.version, VERSION))}</span></a></li>
</ul>
</article>
</main>
</body>
</html>
`;
  fs.writeFileSync(path.join(OUT, "404.html"), nf);
  console.log(`docs site: ${pages.length} pages (${LANGS.join(", ")}) in ${path.relative(process.cwd(), OUT) || "."}`);
}

build();
