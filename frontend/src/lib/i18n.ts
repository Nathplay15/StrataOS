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
type Rule = { re: RegExp; from: string; to: string };
let rules: Rule[] = [];

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

// Server texts come in French or English: both are matched and rendered in
// the current language, most specific pattern first.
function buildRules(): Rule[] {
  const out: Rule[] = [];
  for (const [fr, en] of serverTexts) {
    const to = lang === "fr" ? fr : lang === "en" ? en : (serverDict[fr] || en);
    for (const from of [fr, en]) {
      if (from !== to) out.push({ re: new RegExp("^" + escape(from).replace(/\\\{(\d)\\\}/g, "(.*?)") + "$", "s"), from, to });
    }
  }
  return out.sort((a, b) => b.from.length - a.from.length);
}

/** Translates a text produced by the server (job title, history, alert…). */
export function srv(text: string | undefined | null, depth = 0): string {
  if (!text) return text ?? "";
  for (const r of rules) {
    const m = r.re.exec(text);
    // Variable parts may themselves be server texts ("Backup: X" + ": finished").
    if (m) return r.to.replace(/\{(\d)\}/g, (_, i) => (depth < 3 ? srv(m[+i + 1], depth + 1) : m[+i + 1]) ?? "");
  }
  return text;
}
