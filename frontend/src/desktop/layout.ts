// Grid of the desktop icons: every icon sits in one cell [column, row];
// positions are saved as grid coordinates so they survive a resize.

export type Cell = [number, number];
export type Layout = Record<string, Cell>;

export const CELL_W = 96;
export const CELL_H = 112;
export const GRID_PAD = 12;

const key = (c: Cell) => `${c[0]},${c[1]}`;

/** Free cells in reading order of the desktop: down the first column, then the next. */
function* cellsByColumn(cols: number, rows: number): Generator<Cell> {
  for (let c = 0; c < cols; c++) for (let r = 0; r < rows; r++) yield [c, r];
}

/** The free cell nearest to c (the cell itself when free); null when the grid is full. */
export function nearestFree(c: Cell, used: Set<string>, cols: number, rows: number): Cell | null {
  let best: Cell | null = null;
  let bestD = Infinity;
  for (const x of cellsByColumn(cols, rows)) {
    if (used.has(key(x))) continue;
    const d = (x[0] - c[0]) ** 2 + (x[1] - c[1]) ** 2;
    if (d < bestD) {
      best = x;
      bestD = d;
    }
  }
  return best;
}

/**
 * Places the icons (in their default order) on a cols × rows grid: saved
 * positions are clamped to the visible grid (moved to the nearest free cell
 * when taken), the others go to the first free cell. When the grid is full,
 * extra columns are added to the right rather than stacking icons.
 */
export function layoutIcons(ids: string[], saved: Layout, cols: number, rows: number): Layout {
  cols = Math.max(1, cols);
  rows = Math.max(1, rows);
  const need = Math.max(cols, Math.ceil(ids.length / rows));
  const used = new Set<string>();
  const out: Layout = {};
  // Icons whose saved cell is visible first, so that an icon pulled back
  // from outside the grid never takes their place.
  const inGrid = (c: Cell) => c[0] >= 0 && c[1] >= 0 && c[0] < cols && c[1] < rows;
  for (const pass of [true, false]) {
    for (const id of ids) {
      const s = saved[id];
      if (!s || inGrid(s) !== pass) continue;
      const c: Cell = [Math.min(Math.max(0, s[0]), cols - 1), Math.min(Math.max(0, s[1]), rows - 1)];
      const f = nearestFree(c, used, cols, rows) ?? nearestFree(c, used, need + 1, rows);
      if (f) {
        out[id] = f;
        used.add(key(f));
      }
    }
  }
  for (const id of ids) {
    if (out[id]) continue;
    for (const c of cellsByColumn(need + 1, rows)) {
      if (!used.has(key(c))) {
        out[id] = c;
        used.add(key(c));
        break;
      }
    }
  }
  return out;
}

/**
 * Moves icon id to cell `to`: an icon already there takes the old place of
 * the moved one (swap), so no two icons ever share a cell.
 */
export function moveIcon(layout: Layout, id: string, to: Cell): Layout {
  const from = layout[id];
  if (!from || (from[0] === to[0] && from[1] === to[1])) return layout;
  const out: Layout = { ...layout, [id]: to };
  const other = Object.keys(layout).find((k) => k !== id && layout[k][0] === to[0] && layout[k][1] === to[1]);
  if (other) out[other] = from;
  return out;
}

/** Grid size that fits a w × h area. */
export function gridSize(w: number, h: number): { cols: number; rows: number } {
  return { cols: Math.max(1, Math.floor((w - 2 * GRID_PAD) / CELL_W)), rows: Math.max(1, Math.floor((h - 2 * GRID_PAD) / CELL_H)) };
}

/** Cell under a point of the grid area (clamped to the grid). */
export function cellAt(x: number, y: number, cols: number, rows: number): Cell {
  return [Math.min(cols - 1, Math.max(0, Math.floor((x - GRID_PAD) / CELL_W))), Math.min(rows - 1, Math.max(0, Math.floor((y - GRID_PAD) / CELL_H)))];
}

export type Direction = "left" | "right" | "up" | "down";

/**
 * The icon reached from id with an arrow key: the nearest one in that
 * direction, preferring the same row (left/right) or column (up/down).
 * Undefined at the edge.
 */
export function iconToward(layout: Layout, id: string, dir: Direction): string | undefined {
  const from = layout[id];
  if (!from) return undefined;
  const horizontal = dir === "left" || dir === "right";
  const sign = dir === "right" || dir === "down" ? 1 : -1;
  let best: string | undefined;
  let bestKey: [number, number] = [Infinity, Infinity];
  for (const [k, c] of Object.entries(layout)) {
    if (k === id) continue;
    const along = (horizontal ? c[0] - from[0] : c[1] - from[1]) * sign;
    const across = Math.abs(horizontal ? c[1] - from[1] : c[0] - from[0]);
    if (along <= 0) continue;
    if (across < bestKey[0] || (across === bestKey[0] && along < bestKey[1])) {
      best = k;
      bestKey = [across, along];
    }
  }
  return best;
}
