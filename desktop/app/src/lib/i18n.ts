// Texts of the app. Like the NAS web interface, they are written in English
// and wrapped in t("…"): the app's own dictionaries (src/i18n/<lang>.json)
// come first, then the web interface's (frontend/src/i18n), whose texts the
// app shares for everything about the NAS. Texts of the sync engine use the
// engine's catalogs by key (desktop/internal/i18n/catalog, tk("state.paused")).
import { initI18n, lang, languages, locale, srv, tr, type Lang } from "@nas/lib/i18n";
import goEn from "../../../internal/i18n/catalog/en.json";

export { lang, languages, locale, srv, type Lang };

let dict: Record<string, string> = {};
let goDict: Record<string, string> = {};

const appLoaders = import.meta.glob<{ default: Record<string, string> }>("../i18n/*.json");
const goLoaders = import.meta.glob<{ default: Record<string, string> }>(["../../../internal/i18n/catalog/*.json", "!../../../internal/i18n/catalog/en.json"]);

export async function initTexts() {
  await initI18n();
  if (lang !== "en") {
    const a = appLoaders[`../i18n/${lang}.json`];
    const g = goLoaders[`../../../internal/i18n/catalog/${lang}.json`];
    [dict, goDict] = await Promise.all([a ? a().then((m) => m.default) : {}, g ? g().then((m) => m.default) : {}]);
  }
}

const fill = (s: string, args: (string | number | null | undefined)[]) => {
  args.forEach((a, i) => { s = s.split(`{${i}}`).join(String(a ?? "")); });
  return s;
};

/** Translates an English text of the interface; {0}, {1}… take the arguments. */
export function t(en: string, ...args: (string | number | null | undefined)[]): string {
  const s = dict[en];
  return s ? fill(s, args) : tr(en, ...args);
}

/** A text of the sync engine's catalog, by key. */
export function tk(id: string, ...args: (string | number | null | undefined)[]): string {
  return fill(goDict[id] || (goEn as Record<string, string>)[id] || id, args);
}
