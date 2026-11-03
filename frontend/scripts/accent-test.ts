// Unit test of the accent colours: node --experimental-strip-types frontend/scripts/accent-test.ts
import { ACCENT_PRESETS, PANELS, accentVars, contrast, parseHex } from "../src/lib/accent.ts";
let fails = 0;
const check = (ok: boolean, m: string) => { if (!ok) { console.error("FAIL", m); fails++; } };
const white = parseHex("#ffffff")!;
const colours = [...ACCENT_PRESETS, "#ffff00", "#00ff00", "#000000", "#ffffff", "#0000ff", "#ff0000", "#777777", "#00ffff"];
for (const c of colours) {
  const l = parseHex(accentVars(c, "light").accent)!;
  check(contrast(l, white) >= 4.5, `${c} light: white text on accent ${contrast(l, white).toFixed(2)}`);
  check(contrast(l, parseHex(PANELS.light)!) >= 4.5, `${c} light: accent text on panel`);
  const d = parseHex(accentVars(c, "dark").accent)!;
  check(contrast(d, parseHex(PANELS.dark)!) >= 4.5, `${c} dark: accent text on panel ${contrast(d, parseHex(PANELS.dark)!).toFixed(2)}`);
  check(contrast(d, white) >= 3, `${c} dark: white text on accent ${contrast(d, white).toFixed(2)}`);
  check(/^rgba\(\d+, \d+, \d+, 0\.1[26]\)$/.test(accentVars(c, "dark").soft), `${c} soft`);
}
check(accentVars("bogus", "light").accent === accentVars("#4f6bff", "light").accent, "invalid colour falls back to the default");
console.log(fails ? `${fails} failures` : "done");
if (fails) process.exitCode = 1;
