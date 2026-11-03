// Accent colour of the interface, fitted to each theme so text and buttons
// stay readable (no imports: tested by
// node --experimental-strip-types frontend/scripts/accent-test.ts).

export const DEFAULT_ACCENT = "#4f6bff";

export const ACCENT_PRESETS = ["#4f6bff", "#8b5cf6", "#0d9488", "#16a34a", "#ea580c", "#e11d48", "#475569"];

type RGB = [number, number, number];

export function parseHex(hex: string): RGB | null {
  const m = /^#([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return null;
  const n = parseInt(m[1], 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

export function toHex(c: RGB): string {
  return "#" + c.map((x) => Math.round(Math.min(255, Math.max(0, x))).toString(16).padStart(2, "0")).join("");
}

/** WCAG relative luminance. */
export function luminance(c: RGB): number {
  const [r, g, b] = c.map((v) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast ratio between two colours. */
export function contrast(a: RGB, b: RGB): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

const mix = (a: RGB, b: RGB, t: number): RGB => [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t];

/** Moves c toward black or white (keeping its hue) until its luminance is within [lo, hi]. */
function fit(c: RGB, lo: number, hi: number): RGB {
  const l = luminance(c);
  if (l >= lo && l <= hi) return c;
  const to: RGB = l > hi ? [0, 0, 0] : [255, 255, 255];
  const goal = l > hi ? hi : lo;
  let a = 0, b = 1;
  for (let i = 0; i < 24; i++) {
    const m = (a + b) / 2;
    const lm = luminance(mix(c, to, m));
    if (l > hi ? lm > goal : lm < goal) a = m;
    else b = m;
  }
  return mix(c, to, b);
}

function rotateHue(c: RGB, deg: number): RGB {
  const [r, g, b] = c.map((v) => v / 255);
  const max = Math.max(r, g, b), min = Math.min(r, g, b), l = (max + min) / 2, d = max - min;
  if (d === 0) return c;
  const s = d / (1 - Math.abs(2 * l - 1));
  let h = max === r ? ((g - b) / d) % 6 : max === g ? (b - r) / d + 2 : (r - g) / d + 4;
  h = (h * 60 + deg + 360) % 360;
  const C = (1 - Math.abs(2 * l - 1)) * s, X = C * (1 - Math.abs(((h / 60) % 2) - 1)), m = l - C / 2;
  const [r1, g1, b1] = h < 60 ? [C, X, 0] : h < 120 ? [X, C, 0] : h < 180 ? [0, C, X] : h < 240 ? [0, X, C] : h < 300 ? [X, 0, C] : [C, 0, X];
  return [(r1 + m) * 255, (g1 + m) * 255, (b1 + m) * 255];
}

export interface AccentVars { accent: string; accent2: string; soft: string }

/**
 * CSS values for an accent in a theme. Light theme: dark enough for white
 * button text and for accent text on white panels (contrast ≥ 4.5). Dark
 * theme: light enough for accent text on the dark panels (≥ 4.5) while white
 * button text stays legible (≥ 3).
 */
export function accentVars(hex: string, theme: "light" | "dark"): AccentVars {
  const base = parseHex(hex) ?? parseHex(DEFAULT_ACCENT)!;
  const [lo, hi] = theme === "light" ? [0, 0.18] : [0.22, 0.29];
  const a = fit(base, lo, hi);
  const a2 = fit(rotateHue(base, -35), theme === "light" ? 0.1 : 0.3, theme === "light" ? 0.45 : 0.6);
  const [r, g, b] = a.map(Math.round);
  return { accent: toHex(a), accent2: toHex(a2), soft: `rgba(${r}, ${g}, ${b}, ${theme === "light" ? 0.12 : 0.16})` };
}

/** Panel colours of the themes (index.css), used to check contrasts. */
export const PANELS = { light: "#ffffff", dark: "#121829" };
