// Unit test of the desktop icon grid: node --experimental-strip-types frontend/scripts/desktop-layout-test.ts
import { layoutIcons, moveIcon, cellAt } from "../src/desktop/layout.ts";
const eq = (a: unknown, b: unknown, m: string) => { if (JSON.stringify(a) !== JSON.stringify(b)) { console.error("FAIL", m, JSON.stringify(a)); process.exitCode = 1; } };
eq(layoutIcons(["a","b","c"], {}, 2, 2), {a:[0,0],b:[0,1],c:[1,0]}, "default column order");
eq(layoutIcons(["a","b","c"], {b:[0,0]}, 2, 2), {b:[0,0],a:[0,1],c:[1,0]}, "saved first");
const l = layoutIcons(["a","b","c","d","e"], {}, 2, 2);
eq(Object.keys(l).length, 5, "overflow gets a cell");
eq(new Set(Object.values(l).map(String)).size, 5, "no overlap on overflow");
eq(moveIcon({a:[0,0],b:[0,1]}, "a", [0,1]), {a:[0,1],b:[0,0]}, "swap");
eq(moveIcon({a:[0,0],b:[0,1]}, "a", [3,3]), {a:[3,3],b:[0,1]}, "move to free");
eq(cellAt(5, 5, 4, 4), [0,0], "cellAt clamp");
eq(cellAt(12+96*2+10, 12+112+1, 4, 4), [2,1], "cellAt");
const c = layoutIcons(["a","b"], {a:[9,0], b:[1,0]}, 2, 2);
eq(c.b, [1,0], "clamped icon does not displace an in-grid one");
console.log("done");

// Widgets of the side panel.
import { DEFAULT_WIDGETS, dropWidget, moveWidget, shownWidgets, toggleWidget } from "../src/desktop/widgetList.ts";
eq(shownWidgets(null, true), DEFAULT_WIDGETS, "defaults");
eq(shownWidgets(null, false), ["status", "resources", "storage"], "admin widgets hidden for users");
eq(shownWidgets([], true), [], "empty list hides the panel");
eq(shownWidgets(["temps", "nope", "temps", "network"], true), ["temps", "network"], "unknown and repeated ids dropped");
eq(shownWidgets(["docker", "network"], false), ["network"], "forbidden ids dropped");
eq(moveWidget(["status", "resources", "storage"], "storage", -1), ["status", "storage", "resources"], "move up");
eq(moveWidget(["status", "resources"], "status", -1), ["status", "resources"], "clamped");
eq(dropWidget(["status", "resources", "storage"], "status", "storage"), ["resources", "storage", "status"], "drop onto");
eq(toggleWidget(["status"], "ups", true), ["status", "ups"], "add at the end");
eq(toggleWidget(["status", "ups"], "status", false), ["ups"], "remove");
console.log("widgets done");
