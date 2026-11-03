// Unit test of the global search ranking: node --experimental-strip-types frontend/scripts/search-rank-test.ts
import { norm, rank, score } from "../src/lib/searchRank.ts";
const eq = (a: unknown, b: unknown, m: string) => { if (JSON.stringify(a) !== JSON.stringify(b)) { console.error("FAIL", m, JSON.stringify(a)); process.exitCode = 1; } };
const items = [
  { id: "net", labels: ["Réseau", "Network"], keywords: ["ip", "dhcp", "bridge"] },
  { id: "netsvc", labels: ["Services réseau", "Network services"] },
  { id: "storage", labels: ["Gestionnaire de stockage", "Storage Manager"], keywords: ["disk", "raid"] },
  { id: "disks", labels: ["Disques", "Disks"], keywords: ["smart"] },
  { id: "files", labels: ["Fichiers", "Files"] },
  { id: "profile", labels: ["Profil"] },
];
const ids = (q: string, n?: number) => rank(q, items, n).map((x) => x.id);
eq(norm("  RÉSEAU "), "reseau", "norm strips accents and case");
eq(ids("reseau"), ["net", "netsvc"], "accents ignored, exact name first");
eq(ids("network")[0], "net", "English name matches");
eq(ids("disk"), ["disks", "storage"], "name prefix beats keyword");
eq(ids("bridge"), ["net"], "keyword only");
eq(ids("net serv"), ["netsvc"], "every word must match");
eq(ids("fi"), ["files", "profile"], "prefix beats inside of a word");
eq(ids("zzz"), [], "no match");
eq(ids(""), [], "empty query");
eq(ids("e", 2).length, 2, "limit");
eq(score("raid", items[2]) > 0 && score("raid", items[2]) < score("disks", items[3]), true, "keyword weighs less than a name");
console.log("done");
