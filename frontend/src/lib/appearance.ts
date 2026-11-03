import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import { accentVars } from "./accent";
import type { Layout } from "../desktop/layout";

// Personal preferences kept on the NAS (GET/PUT /api/me/desktop): desktop
// icons and widgets, wallpaper, accent colour.

export interface DesktopPrefs {
  icons: Layout;
  /** null = the default widgets. */
  widgets: string[] | null;
  wallpaper: string;
  accent: string;
  /** Version of the uploaded wallpaper, absent when there is none. */
  customWallpaper?: number;
}

const KEY = ["desktop-prefs"];
const ACCENT_KEY = "strata-accent";

export function usePrefs() {
  return useQuery({ queryKey: KEY, queryFn: () => api.get<DesktopPrefs>("/api/me/desktop"), staleTime: Infinity });
}

/** Saves some preferences (the others are left as they are on the NAS). */
export function useSavePrefs() {
  const qc = useQueryClient();
  return (p: Partial<Omit<DesktopPrefs, "customWallpaper">>) => {
    qc.setQueryData<DesktopPrefs>(KEY, (old) => (old ? { ...old, ...p } : old));
    return api.put("/api/me/desktop", p).catch((e) => {
      qc.invalidateQueries({ queryKey: KEY });
      throw e;
    });
  };
}

/** Applies an accent colour to both themes ("" = the default of index.css). */
export function applyAccent(hex: string) {
  let el = document.getElementById("strata-accent") as HTMLStyleElement | null;
  if (!hex) {
    el?.remove();
  } else {
    if (!el) {
      el = document.createElement("style");
      el.id = "strata-accent";
      document.head.appendChild(el);
    }
    const css = (sel: string, t: "light" | "dark") => {
      const v = accentVars(hex, t);
      return `${sel}{--accent:${v.accent};--accent-2:${v.accent2};--accent-soft:${v.soft}}`;
    };
    el.textContent = css(":root", "light") + css(':root[data-theme="dark"]', "dark");
  }
  try {
    if (hex) localStorage.setItem(ACCENT_KEY, hex);
    else localStorage.removeItem(ACCENT_KEY);
  } catch {
    /* storage unavailable */
  }
}

/** Applies the accent remembered by this browser, before the first paint. */
export function applySavedAccent() {
  try {
    const v = localStorage.getItem(ACCENT_KEY);
    if (v) applyAccent(v);
  } catch {
    /* storage unavailable */
  }
}

/** Keeps the interface in the user's accent colour. */
export function useAccent() {
  const prefs = usePrefs();
  const accent = prefs.data?.accent;
  useEffect(() => {
    if (accent !== undefined) applyAccent(accent);
  }, [accent]);
}
