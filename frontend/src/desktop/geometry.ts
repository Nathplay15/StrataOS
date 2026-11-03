// Where a desktop window opens. The last geometry of each app is remembered
// with the size of the screen it was chosen on: a window made small on a
// laptop must not come back small on a large monitor.

export interface Geometry { x: number; y: number; w: number; h: number }
/** A remembered geometry and the area (screen minus taskbar) it was saved on. */
export interface SavedGeometry extends Geometry { sw?: number; sh?: number }

type Area = { w: number; h: number };

/** Minimum window size. */
export const MIN_W = 420, MIN_H = 280;

/** Screens this much larger (either way) than the saved one get the default size. */
const GROWTH = 1.25;

export function clamp(g: Geometry, a: Area): Geometry {
  const w = Math.min(Math.max(g.w, MIN_W), a.w);
  const h = Math.min(Math.max(g.h, MIN_H), a.h);
  return { w, h, x: Math.min(Math.max(g.x, 0), Math.max(0, a.w - w)), y: Math.min(Math.max(g.y, 0), Math.max(0, a.h - h)) };
}

/** Default place of the n-th open window: centred, then cascaded. */
export function defaultGeometry(size: [number, number], a: Area, n: number): Geometry {
  return { w: size[0], h: size[1], x: Math.max(16, (a.w - size[0]) / 2 + n * 28 - 60), y: Math.max(12, (a.h - size[1]) / 2 + n * 24 - 40) };
}

/**
 * Geometry of a window being opened: the saved one, unless the screen grew
 * much since it was saved (then the default size). Saved geometries of
 * older versions have no screen size: one smaller than the default is
 * taken as made on a smaller screen when the default now fits.
 */
export function startGeometry(saved: SavedGeometry | null, size: [number, number], a: Area, n: number): Geometry {
  const def = defaultGeometry(size, a, n);
  if (!saved) return clamp(def, a);
  const grown = saved.sw && saved.sh
    ? a.w > saved.sw * GROWTH || a.h > saved.sh * GROWTH
    : (saved.w < size[0] * 0.8 || saved.h < size[1] * 0.8) && a.w >= size[0] + 32 && a.h >= size[1] + 24;
  return clamp(grown ? def : { x: saved.x, y: saved.y, w: saved.w, h: saved.h }, a);
}
