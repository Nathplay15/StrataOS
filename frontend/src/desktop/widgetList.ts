// Widgets of the desktop side panel: which ones exist, the defaults, and the
// list kept per user (no imports: tested by
// node --experimental-strip-types frontend/scripts/desktop-layout-test.ts).

export const WIDGET_IDS = ["status", "resources", "storage", "apps", "network", "docker", "vms", "ups", "backups", "temps", "notifications"] as const;
export type WidgetId = (typeof WIDGET_IDS)[number];

/** Widgets reading data only administrators may see. */
export const ADMIN_WIDGETS: ReadonlySet<string> = new Set(["apps", "docker", "vms", "ups", "backups", "temps"]);

export const DEFAULT_WIDGETS: WidgetId[] = ["status", "resources", "storage", "apps"];

const known = (id: string): id is WidgetId => (WIDGET_IDS as readonly string[]).includes(id);

/** The widgets to show: the saved list (null = defaults) without unknown, repeated or forbidden ids. */
export function shownWidgets(saved: string[] | null | undefined, admin: boolean): WidgetId[] {
  const out: WidgetId[] = [];
  for (const id of saved ?? DEFAULT_WIDGETS) {
    if (known(id) && !out.includes(id) && (admin || !ADMIN_WIDGETS.has(id))) out.push(id);
  }
  return out;
}

/** Moves a widget by delta places (clamped). */
export function moveWidget(list: WidgetId[], id: WidgetId, delta: number): WidgetId[] {
  const i = list.indexOf(id);
  if (i < 0) return list;
  const j = Math.min(list.length - 1, Math.max(0, i + delta));
  if (i === j) return list;
  const out = list.filter((x) => x !== id);
  out.splice(j, 0, id);
  return out;
}

/** Moves a widget to the place of another (drag and drop). */
export function dropWidget(list: WidgetId[], id: WidgetId, onto: WidgetId): WidgetId[] {
  const j = list.indexOf(onto);
  return j < 0 ? list : moveWidget(list, id, j - list.indexOf(id));
}

/** Shows or hides a widget; a shown widget goes to the end. */
export function toggleWidget(list: WidgetId[], id: WidgetId, on: boolean): WidgetId[] {
  const without = list.filter((x) => x !== id);
  return on ? [...without, id] : without;
}
