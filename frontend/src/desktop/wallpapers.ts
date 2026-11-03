import { tr } from "../lib/i18n";

// Built-in wallpapers: CSS gradients and one small inline SVG, nothing to
// download. Their ids are checked by the server (api/h_desktop.go).

// Layered hills, like the strata of the logo.
const strataSVG = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 1600 900' preserveAspectRatio='xMidYMid slice'>
<defs><linearGradient id='s' x1='0' y1='0' x2='0' y2='1'><stop offset='0' stop-color='#1e293b'/><stop offset='1' stop-color='#0f172a'/></linearGradient></defs>
<rect width='1600' height='900' fill='url(#s)'/>
<path d='M0 520 C300 440 520 600 820 520 S1350 420 1600 500 V900 H0Z' fill='#334155' opacity='.55'/>
<path d='M0 620 C260 560 560 700 900 620 S1380 560 1600 640 V900 H0Z' fill='#475569' opacity='.5'/>
<path d='M0 720 C340 660 620 790 960 720 S1400 680 1600 740 V900 H0Z' fill='#6366f1' opacity='.35'/>
<path d='M0 810 C380 770 700 860 1040 800 S1440 790 1600 830 V900 H0Z' fill='#0ea5e9' opacity='.3'/>
</svg>`;

export const WALLPAPERS: Record<string, { label: string; css: string }> = {
  aurora: { label: tr("Dawn"), css: "radial-gradient(1200px 700px at 15% 10%, #3b4cca 0%, transparent 60%), radial-gradient(900px 600px at 85% 30%, #0e7490 0%, transparent 55%), radial-gradient(1000px 800px at 60% 100%, #7c3aed 0%, transparent 60%), #0b1022" },
  dusk: { label: tr("Dusk"), css: "radial-gradient(1100px 700px at 80% 0%, #f97316 0%, transparent 55%), radial-gradient(1000px 700px at 10% 90%, #be185d 0%, transparent 55%), linear-gradient(160deg, #1e1b4b, #0f172a)" },
  ocean: { label: tr("Ocean"), css: "radial-gradient(1200px 800px at 50% -10%, #38bdf8 0%, transparent 55%), radial-gradient(900px 700px at 0% 100%, #0f766e 0%, transparent 60%), linear-gradient(180deg, #0c4a6e, #082f49)" },
  forest: { label: tr("Forest"), css: "radial-gradient(1000px 700px at 20% 20%, #16a34a 0%, transparent 55%), radial-gradient(900px 700px at 90% 80%, #0f766e 0%, transparent 55%), linear-gradient(160deg, #052e16, #022c22)" },
  graphite: { label: tr("Graphite"), css: "radial-gradient(1000px 700px at 70% 20%, #334155 0%, transparent 60%), linear-gradient(160deg, #111827, #030712)" },
  strata: { label: tr("Strata"), css: `url("data:image/svg+xml,${encodeURIComponent(strataSVG)}") center / cover no-repeat, #0f172a` },
};

export const DEFAULT_WALLPAPER = "aurora";

/** CSS background of a wallpaper choice ("custom" = the image uploaded by the user). */
export function wallpaperCSS(id: string, customVersion?: number): string {
  if (id === "custom" && customVersion) return `url("/api/me/wallpaper?v=${customVersion}") center / cover no-repeat, #0b1022`;
  return (WALLPAPERS[id] ?? WALLPAPERS[DEFAULT_WALLPAPER]).css;
}

/** Wallpaper chosen before it was kept on the NAS (this browser only). */
export function legacyWallpaper(): string {
  try {
    return localStorage.getItem("strata-wallpaper") ?? "";
  } catch {
    return "";
  }
}
