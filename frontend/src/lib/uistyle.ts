import { useEffect, useState } from "react";

// Look of the phone layout and of the desktop app (index.css, [data-style]):
// "glass" (translucent, blurred panels over a soft gradient) or "modern"
// (opaque surfaces). Kept by this browser. The desktop mode of the web
// interface keeps its own look: it clears the attribute.

export type UIStyle = "glass" | "modern";

const STYLE_KEY = "strata-style";
const SOLID_KEY = "strata-reduce-transparency"; // "1" on, "0" off, none: automatic
const SLOW_KEY = "strata-slow-frames"; // frames measured slow on this device

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
/** A modest device: few cores, little memory, or frames measured slow
 * (the blur of the glass style costs most there). */
export function modestDevice(): boolean {
  if (get(SLOW_KEY) === "1") return true;
  const cores = navigator.hardwareConcurrency ?? 8;
  const mem = (navigator as Navigator & { deviceMemory?: number }).deviceMemory ?? 8;
  return cores <= 4 || mem <= 4;
}

/** The "Reduce transparency" switch: the user's choice, else on for a
 * modest device (the system's setting applies too). */
export const reduceTransparency = () => {
  const v = get(SOLID_KEY);
  return v === "1" || (v !== "0" && modestDevice());
};

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
  else {
    delete d.transparency;
    if (d.style === "glass") measureFrames();
  }
}

const EVENT = "strata:ui-style";

export function setUIStyle(s: UIStyle) {
  set(STYLE_KEY, s === "glass" ? null : s);
  window.dispatchEvent(new Event(EVENT));
}

export function setReduceTransparency(on: boolean) {
  set(SOLID_KEY, on ? "1" : "0");
  window.dispatchEvent(new Event(EVENT));
}

let measured = false;

/** Measures the frame rate once, a little after the glass style shows: a
 * device that cannot keep up gets the opaque panels (unless the user chose). */
function measureFrames() {
  if (measured || get(SOLID_KEY) !== null || get(SLOW_KEY) !== null || typeof requestAnimationFrame === "undefined") return;
  measured = true;
  setTimeout(() => {
    if (document.hidden) {
      measured = false;
      return;
    }
    const times: number[] = [];
    let last = performance.now();
    const tick = (t: number) => {
      times.push(t - last);
      last = t;
      if (times.length < 90) {
        requestAnimationFrame(tick);
        return;
      }
      times.sort((a, b) => a - b);
      const median = times[times.length >> 1];
      const slow = times.filter((x) => x > 50).length;
      if (median > 25 || slow > 15) {
        set(SLOW_KEY, "1");
        window.dispatchEvent(new Event(EVENT));
      } else set(SLOW_KEY, "0");
    };
    requestAnimationFrame((t) => { last = t; requestAnimationFrame(tick); });
  }, 3000);
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
