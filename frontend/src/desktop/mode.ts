import { useEffect, useState } from "react";

export type UIMode = "auto" | "desktop" | "classic";
const KEY = "strata-ui-mode";

function read(): UIMode {
  try {
    const v = localStorage.getItem(KEY);
    return v === "desktop" || v === "classic" ? v : "auto";
  } catch {
    return "auto";
  }
}

export function setUIMode(m: UIMode) {
  try {
    localStorage.setItem(KEY, m);
  } catch {
    /* storage unavailable */
  }
  window.dispatchEvent(new Event("strata:ui-mode"));
}

/** Desktop on large screens unless the user chose otherwise; windows are unusable on phones. */
export function useDesktopMode(): boolean {
  const compute = () => {
    const m = read();
    const large = window.innerWidth >= 1024 && window.innerHeight >= 600;
    return m === "desktop" ? large : m === "classic" ? false : large;
  };
  const [on, setOn] = useState(compute);
  useEffect(() => {
    const h = () => setOn(compute());
    window.addEventListener("resize", h);
    window.addEventListener("strata:ui-mode", h);
    return () => {
      window.removeEventListener("resize", h);
      window.removeEventListener("strata:ui-mode", h);
    };
  }, []);
  return on;
}
