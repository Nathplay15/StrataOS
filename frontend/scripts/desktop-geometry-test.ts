// Unit test of desktop window placement: node --experimental-strip-types frontend/scripts/desktop-geometry-test.ts
import { startGeometry } from "../src/desktop/geometry.ts";
const eq = (a: unknown, b: unknown, m: string) => { if (JSON.stringify(a) !== JSON.stringify(b)) { console.error("FAIL", m, JSON.stringify(a)); process.exitCode = 1; } };
const big = { w: 2560, h: 1392 }, laptop = { w: 1280, h: 672 };
const size: [number, number] = [1120, 760];
eq(startGeometry(null, size, big, 0), { w: 1120, h: 760, x: 660, y: 276 }, "default, centred");
eq(startGeometry(null, size, laptop, 0), { w: 1120, h: 672, x: 20, y: 0 }, "default clamped to a small screen");
// Saved on a laptop, opened on a large monitor: default size.
eq(startGeometry({ x: 10, y: 10, w: 700, h: 500, sw: 1280, sh: 672 }, size, big, 0).w, 1120, "screen grew: default size");
// Saved on the same screen, or a slightly larger one: kept.
eq(startGeometry({ x: 10, y: 10, w: 700, h: 500, sw: 2560, sh: 1392 }, size, big, 0), { w: 700, h: 500, x: 10, y: 10 }, "same screen: kept");
eq(startGeometry({ x: 10, y: 10, w: 700, h: 500, sw: 2400, sh: 1300 }, size, big, 0).w, 700, "slightly larger screen: kept");
// Saved on a large screen, opened on a small one: clamped, never larger than the screen.
eq(startGeometry({ x: 1500, y: 600, w: 1800, h: 1000, sw: 2560, sh: 1392 }, size, laptop, 0), { w: 1280, h: 672, x: 0, y: 0 }, "screen shrank: clamped");
// Before 0.10 (no screen size): smaller than the default and the default fits.
eq(startGeometry({ x: 10, y: 10, w: 600, h: 400 }, size, big, 0).w, 1120, "legacy small: default");
eq(startGeometry({ x: 10, y: 10, w: 1000, h: 700 }, size, big, 0).w, 1000, "legacy close to default: kept");
eq(startGeometry({ x: 10, y: 10, w: 600, h: 400 }, size, laptop, 0).w, 600, "legacy small on a small screen: kept");
console.log("geometry done");
