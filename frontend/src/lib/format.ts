export function bytes(n: number | undefined, digits = 1): string {
  if (n === undefined || n === null || isNaN(n)) return "—";
  if (n < 1024) return `${Math.round(n)} o`;
  const units = ["Ko", "Mo", "Go", "To", "Po"];
  let v = n;
  let i = -1;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : digits)} ${units[i]}`;
}

export const rate = (n: number) => `${bytes(n)}/s`;

export function pct(used: number, total: number): number {
  if (!total) return 0;
  return Math.min(100, Math.max(0, (used / total) * 100));
}

export function duration(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d} j ${h} h`;
  if (h > 0) return `${h} h ${m} min`;
  return `${m} min`;
}

export function dateTime(v: string | number | undefined): string {
  if (!v) return "—";
  const d = typeof v === "number" ? new Date(v) : new Date(v);
  return d.toLocaleString("fr-FR", { dateStyle: "short", timeStyle: "short" });
}

export function ago(v: string | number): string {
  const t = typeof v === "number" ? v : new Date(v).getTime();
  const s = (Date.now() - t) / 1000;
  if (s < 60) return "à l'instant";
  if (s < 3600) return `il y a ${Math.floor(s / 60)} min`;
  if (s < 86400) return `il y a ${Math.floor(s / 3600)} h`;
  return `il y a ${Math.floor(s / 86400)} j`;
}

/** Parses "10G", "500 MB", "2T" into bytes. */
export function parseSize(s: string): number {
  const m = s.trim().match(/^([\d.]+)\s*([kmgtp]?)(i?b|o)?$/i);
  if (!m) return NaN;
  const mult: Record<string, number> = { "": 1, k: 1024, m: 1024 ** 2, g: 1024 ** 3, t: 1024 ** 4, p: 1024 ** 5 };
  return Math.round(parseFloat(m[1]) * mult[m[2].toLowerCase()]);
}

export const cx = (...c: (string | false | null | undefined)[]) => c.filter(Boolean).join(" ");
