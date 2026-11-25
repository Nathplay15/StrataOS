import { useEffect, useState } from "react";

// Look of the phone layout and of the desktop app (index.css, [data-style]):
// "glass" (translucent, blurred panels over a soft gradient) or "modern"
// (opaque surfaces). Kept by this browser. The desktop mode of the web
// interface keeps its own look: it clears the attribute.

export type UIStyle = "glass" | "modern";

const STYLE_KEY = "strata-style";
const SOLID_KEY = "strata-reduce-transparency";

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
    /* storage unavailable */
  }
};

export const uiStyle = (): UIStyle => (get(STYLE_KEY) === "modern" ? "modern" : "glass");
/** The user's "Reduce transparency" switch (the system's setting applies too). */
export const reduceTransparency = () => get(SOLID_KEY) === "1";

const prefersSolid = () => {
  try {
    if (matchMedia("(prefers-reduced-transparency: reduce)").matches) return true;
    return !(CSS.supports("backdrop-filter", "blur(2px)") || CSS.supports("-webkit-backdrop-filter", "blur(2px)"));
  } catch {
    return false;
  }
};

/** Sets the style attributes on <html>; off = the web desktop mode's own look. */
export function applyUIStyle(on = true) {
  const d = document.documentElement.dataset;
  if (!on) {
    delete d.style;
    delete d.transparency;
    return;
  }
  d.style = uiStyle();
  if (reduceTransparency() || prefersSolid()) d.transparency = "reduced";
  else delete d.transparency;
}

const EVENT = "strata:ui-style";

export function setUIStyle(s: UIStyle) {
  set(STYLE_KEY, s === "glass" ? null : s);
  window.dispatchEvent(new Event(EVENT));
}

export function setReduceTransparency(on: boolean) {
  set(SOLID_KEY, on ? "1" : null);
  window.dispatchEvent(new Event(EVENT));
}

/** Keeps <html> in the chosen style while on (and follows the system's transparency setting). */
export function useUIStyleRoot(on = true) {
  useEffect(() => {
    if (!on) {
      applyUIStyle(false);
      return;
    }
    const h = () => applyUIStyle(true);
    h();
    let mq: MediaQueryList | null = null;
    try {
      mq = matchMedia("(prefers-reduced-transparency: reduce)");
      mq.addEventListener("change", h);
    } catch {
      /* old browser */
    }
    window.addEventListener(EVENT, h);
    return () => {
      window.removeEventListener(EVENT, h);
      mq?.removeEventListener("change", h);
    };
  }, [on]);
}

/** The current choices, updated when they change. */
export function useUIStyle() {
  const [s, setS] = useState({ style: uiStyle(), solid: reduceTransparency() });
  useEffect(() => {
    const h = () => setS({ style: uiStyle(), solid: reduceTransparency() });
    window.addEventListener(EVENT, h);
    return () => window.removeEventListener(EVENT, h);
  }, []);
  return s;
}
