// Preferences of this computer kept by the window (localStorage): theme,
// language (shared with the web interface's i18n module), last account.
import type { Lang } from "@nas/lib/i18n";

const get = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};
const set = (k: string, v: string | null) => {
  try {
    if (v === null) localStorage.removeItem(k);
    else localStorage.setItem(k, v);
  } catch {
    /* ignore */
  }
};

export type Theme = "system" | "light" | "dark";

export const theme = (): Theme => (get("strata-desktop-theme") as Theme) || "system";

const media = window.matchMedia("(prefers-color-scheme: dark)");

/** Applies the theme (the system's by default, following its changes). */
export function applyTheme() {
  const t = theme();
  document.documentElement.dataset.theme = t === "system" ? (media.matches ? "dark" : "light") : t;
}
media.addEventListener("change", applyTheme);

export function setTheme(t: Theme) {
  set("strata-desktop-theme", t);
  applyTheme();
}

/** "" = the system's language. */
export const chosenLang = () => (get("strata-lang") as Lang | null) ?? "";

export function setChosenLang(l: Lang | "") {
  set("strata-lang", l || null);
}

export const lastAccount = () => get("strata-desktop-account") ?? "";
export const setLastAccount = (id: string) => set("strata-desktop-account", id);

export const pref = (k: string) => get(`strata-desktop-${k}`);
export const setPref = (k: string, v: string | null) => set(`strata-desktop-${k}`, v);
