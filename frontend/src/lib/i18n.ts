// Interface language. Texts are written in English in the code and wrapped in
// tr("…"); src/i18n/<lang>.json maps them to the other languages (missing
// entries fall back to English). Dictionaries are loaded on demand by
// initI18n(), before the interface modules are imported.
import { serverTexts } from "../i18n/server";

export type Lang = "en" | "fr" | "es" | "de" | "it" | "pt" | "nl" | "zh";

/** Languages offered in the interface, with their own name and date locale. */
export const languages: { id: Lang; name: string; locale: string }[] = [
  { id: "en", name: "English", locale: "en-GB" },
  { id: "fr", name: "Français", locale: "fr-FR" },
  { id: "es", name: "Español", locale: "es-ES" },
  { id: "de", name: "Deutsch", locale: "de-DE" },
  { id: "it", name: "Italiano", locale: "it-IT" },
  { id: "pt", name: "Português (Brasil)", locale: "pt-BR" },
  { id: "nl", name: "Nederlands", locale: "nl-NL" },
  { id: "zh", name: "中文（简体）", locale: "zh-CN" },
];

const known = (l: string | null): l is Lang => languages.some((x) => x.id === l);

function initial(): Lang {
  try {
    const saved = localStorage.getItem("strata-lang");
    if (known(saved)) return saved;
  } catch {
    /* private mode */
  }
  for (const pref of navigator.languages ?? [navigator.language]) {
    const l = (pref || "").toLowerCase().slice(0, 2);
    if (known(l)) return l;
  }
  return "en";
}

export const lang: Lang = initial();
export const locale = languages.find((l) => l.id === lang)!.locale;
document.documentElement.lang = lang;

let dict: Record<string, string> = {};
let serverDict: Record<string, string> = {};
type Rule = { re: RegExp; from: string; to: string; weak: boolean };
let rules: Rule[] = [];
let exact = new Map<string, string>();
// English key of a server text → its text in the current language.
let byKey: Map<string, string> | null = null;

const loaders = import.meta.glob<{ default: Record<string, string> }>("../i18n/*.json");
const load = async (name: string) => (loaders[`../i18n/${name}.json`] ? (await loaders[`../i18n/${name}.json`]()).default : {});

/** Loads the dictionaries of the current language (call before rendering). */
export async function initI18n() {
  if (lang !== "en") {
    [dict, serverDict] = await Promise.all([load(lang), lang === "fr" ? Promise.resolve({}) : load(`server.${lang}`)]);
  }
  rules = buildRules();
}

/** Translates an English UI text; {0}, {1}… are replaced by the arguments. */
export function tr(en: string, ...args: (string | number | null | undefined)[]): string {
  let s = dict[en] || en;
  args.forEach((a, i) => { s = s.split(`{${i}}`).join(String(a ?? "")); });
  return s;
}

/** Changes the language (the interface reloads to apply it everywhere). */
export function setLang(l: Lang) {
  try {
    localStorage.setItem("strata-lang", l);
  } catch {
    /* ignore */
  }
  location.reload();
}

const escape = (x: string) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// Words that do not make a template specific (backend/internal/i18n keeps
// the same list): a template made only of them ("{0} to {1}", "{0}, {1}",
// "error: {0}") is used by key only, never to recognise a text.
const connectors = new Set(["to", "vers", "and", "et", "or", "ou", "of", "de", "du", "des", "for", "pour", "in", "dans", "on", "sur", "at", "by", "par", "from", "with", "avec", "the", "le", "la", "les", "à", "au", "error", "erreur", "invalid", "invalide", "warning", "avertissement"]);

/** "exact" (no placeholder), "connector" (never matched), "weak" (one
 * word of its own: applies only to short names or known texts) or "strong". */
export function templateKind(tpl: string): "exact" | "connector" | "weak" | "strong" {
  if (!/\{\d\}/.test(tpl)) return "exact";
  const words = tpl.replace(/\{\d\}/g, " ").match(/\p{L}{2,}/gu) ?? [];
  if (!words.some((w) => !connectors.has(w.toLowerCase()))) return "connector";
  return words.length <= 1 ? "weak" : "strong";
}

// Server texts come in French or English: both are recognised and rendered
// in the current language. Whole texts only: an unknown text is shown as it
// is, never translated piece by piece.
function buildRules(): Rule[] {
  const out: Rule[] = [];
  exact = new Map();
  for (const [fr, en] of serverTexts) {
    const to = lang === "fr" ? fr : lang === "en" ? en : (serverDict[fr] || en);
    for (const from of [fr, en]) {
      const kind = templateKind(from);
      if (kind === "exact") {
        if (!exact.has(from)) exact.set(from, to);
      } else if (kind !== "connector" && from !== to) {
        out.push({ re: new RegExp("^" + escape(from).replace(/\\\{(\d)\\\}/g, "(.*?)") + "$", "s"), from, to, weak: kind === "weak" });
      }
    }
  }
  return out.sort((a, b) => b.from.length - a.from.length);
}

const short = (s: string) => s.length <= 40 && s.trim().split(/\s+/).length <= 2;

function match(text: string, depth: number): string | null {
  if (!text) return null;
  const e = exact.get(text);
  if (e !== undefined) return e;
  if (depth > 3) return null;
  for (const r of rules) {
    const m = r.re.exec(text);
    if (!m) continue;
    const parts: string[] = [];
    let ok = true;
    for (const c of m.slice(1)) {
      const v = match(c, depth + 1);
      if (v === null && r.weak && !short(c)) { ok = false; break; }
      parts.push(v ?? c);
    }
    if (ok) return r.to.replace(/\{(\d)\}/g, (_, i) => parts[+i] ?? "");
  }
  return null;
}

/** Translates a text produced by the server (job title, history, alert…). */
export function srv(text: string | undefined | null): string {
  if (!text) return text ?? "";
  return match(text, 0) ?? text;
}

/** A server text given as its English key ({0}, {1}… placeholders) and its
 * arguments, in the current language (arguments that are server texts
 * themselves are translated too). */
export function srvKey(key: string, args: string[] = []): string {
  if (!byKey) {
    byKey = new Map();
    for (const [fr, en] of serverTexts) byKey.set(en, lang === "fr" ? fr : lang === "en" ? en : (serverDict[fr] || en));
  }
  const to = byKey.get(key) ?? key;
  return to.replace(/\{(\d+)\}/g, (_, i) => srv(args[+i] ?? ""));
}

/** A job log in the current language: the lines the server wrote with a
 * key are rendered from it, command output stays as it is. */
export function jobLog(log: string | undefined, keys?: { line: number; lines?: number; key: string; args?: string[] }[]): string {
  if (!log || !keys?.length || lang === "en") return log ?? "";
  const lines = log.split("\n");
  const at = new Map(keys.map((k) => [k.line, k]));
  const out: string[] = [];
  for (let i = 0; i < lines.length; i++) {
    const k = at.get(i);
    if (k) {
      out.push(srvKey(k.key, k.args));
      i += (k.lines ?? 1) - 1;
    } else out.push(lines[i]);
  }
  return out.join("\n");
}

let reverse: Map<string, string> | null = null;

/** The English text behind a translated one (the text itself when unknown), for searching in both languages. */
export function english(text: string): string {
  if (lang === "en") return text;
  if (!reverse) {
    reverse = new Map();
    for (const [en, t] of Object.entries(dict)) if (t && !reverse.has(t)) reverse.set(t, en);
  }
  return reverse.get(text) ?? text;
}
