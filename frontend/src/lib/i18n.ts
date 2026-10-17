// Interface language. Texts are written in French in the code and wrapped in
// tr("…"); the English dictionary maps them (missing entries stay in French).
import en from "../i18n/en.json";
import { serverTexts } from "../i18n/server";

export type Lang = "fr" | "en";

/** Languages offered in the interface, with their own name. */
export const languages: { id: Lang; name: string }[] = [
  { id: "fr", name: "Français" },
  { id: "en", name: "English" },
];

function initial(): Lang {
  try {
    const saved = localStorage.getItem("strata-lang");
    if (saved === "fr" || saved === "en") return saved;
  } catch {
    /* private mode */
  }
  return (navigator.language || "en").toLowerCase().startsWith("fr") ? "fr" : "en";
}

export const lang: Lang = initial();
export const locale = lang === "en" ? "en-GB" : "fr-FR";
document.documentElement.lang = lang;

const dict = en as Record<string, string>;

/** Translates a French UI text; {0}, {1}… are replaced by the arguments. */
export function tr(fr: string, ...args: (string | number | null | undefined)[]): string {
  let s = lang === "en" ? (dict[fr] ?? fr) : fr;
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
// [source pattern, target template] for the current language, most specific first.
const rules = serverTexts
  .map(([fr, en]) => (lang === "en" ? [fr, en] : [en, fr]))
  .map(([from, to]) => ({ re: new RegExp("^" + escape(from).replace(/\\\{(\d)\\\}/g, "(.*?)") + "$", "s"), from, to }))
  .sort((a, b) => b.from.length - a.from.length);

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
