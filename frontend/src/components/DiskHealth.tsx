import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, q } from "../lib/api";
import { bytes, cx } from "../lib/format";
import { locale, srv, tr } from "../lib/i18n";
import { AreaChart } from "./charts";
import { Badge, ErrorBox, Progress, Spinner, Tabs } from "./ui";

/** One SMART reading of the health history (nil fields: not reported). */
export type HistoryPoint = { t: number; re?: number; pe?: number; un?: number; crc?: number; me?: number; tp?: number; poh?: number; us?: number; sp?: number; wr?: number };

export type Wear = { percentUsed?: number; spare?: number; written?: number; endurance?: number; writeRate?: number; rateDays?: number; remainingDays?: number };

export type SsdCache = { kind: "lvm" | "l2arc" | "slog"; name: string; mode?: string; hitRatio: number; reads: number; series?: { t: number; ratio: number; reads: number }[] };

export type SsdWear = { device: string; model: string; serial: string; kind: string; wear: Wear; caches: SsdCache[]; advice: string[] };

type History = { device: string; model: string; serial: string; kind: string; points: HistoryPoint[]; wear?: Wear };

/** Wear of the SSDs and the caches they hold (Disks, LVM). */
export function useSsdWear() {
  return useQuery({ queryKey: ["ssd-wear"], queryFn: () => api.get<SsdWear[]>("/api/storage/ssd-wear"), refetchInterval: 60000 });
}

export const wearTone = (p?: number) => (p === undefined ? "neutral" : p >= 90 ? "danger" : p >= 80 ? "warn" : "ok");

function remaining(days?: number): string {
  if (days === undefined) return "";
  if (days <= 0) return tr("rated endurance reached");
  const m = Math.max(1, Math.round(days / 30));
  if (m >= 120) return tr("more than 10 years left at this rate");
  if (m >= 24) return tr("about {0} years left at this rate", Math.floor(m / 12));
  return tr("about {0} months left at this rate", m);
}

const cacheLabel = (c: SsdCache) =>
  c.kind === "lvm" ? tr("LVM cache of {0}", c.name) : c.kind === "l2arc" ? tr("ZFS read cache (L2ARC) of {0}", c.name) : tr("ZFS log device (SLOG) of {0}", c.name);

/** Compact wear line of an SSD, for its disk card. */
export function WearLine({ w }: { w: SsdWear }) {
  const p = w.wear.percentUsed;
  return (
    <div className="mt-3 space-y-1.5 rounded-lg bg-panel-2/60 p-2.5 text-[12px]">
      <div className="flex items-center justify-between gap-2">
        <span className="text-muted">{tr("SSD wear")}</span>
        <span className="font-medium">{p === undefined ? "—" : `${p} %`}</span>
      </div>
      {p !== undefined && <Progress value={p} tone={wearTone(p) === "neutral" ? "accent" : (wearTone(p) as "ok" | "warn" | "danger")} />}
      <div className="text-muted">
        {[w.wear.written !== undefined && tr("{0} written", bytes(w.wear.written)), remaining(w.wear.remainingDays)].filter(Boolean).join(" · ")}
      </div>
      {w.caches.map((c) => <div key={c.kind + c.name}><Badge tone="accent">{cacheLabel(c)}</Badge></div>)}
      {w.caches.length > 0 && <ul className="list-disc space-y-0.5 pl-4 text-muted">{w.advice.map((a) => <li key={a}>{srv(a)}</li>)}</ul>}
    </div>
  );
}

/** Wear, cache efficiency and advice of the SSD holding a cache (LVM page). */
export function CacheWear({ w, cache }: { w: SsdWear; cache?: SsdCache }) {
  const p = w.wear.percentUsed;
  const series = (cache?.series ?? []).filter((s) => s.ratio >= 0);
  return (
    <div className="mt-2 space-y-2 rounded-lg border border-line p-3 text-[12.5px]">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <span className="font-medium">{w.model || w.device}</span>
        <span className="text-muted">{tr("Wear: {0}", p === undefined ? "—" : `${p} %`)}</span>
        {w.wear.written !== undefined && <span className="text-muted">{tr("Written: {0}", bytes(w.wear.written))}{w.wear.endurance ? ` / ${tr("about {0} rated (estimated)", bytes(w.wear.endurance))}` : ""}</span>}
        {w.wear.writeRate !== undefined && <span className="text-muted">{tr("{0} a day over {1} days", bytes(w.wear.writeRate), Math.round(w.wear.rateDays ?? 0))}</span>}
        {w.wear.remainingDays !== undefined && <span className="text-muted">{remaining(w.wear.remainingDays)}</span>}
        {cache && cache.hitRatio >= 0 && <span className="text-muted">{tr("{0} % of reads from the SSD over 30 days", Math.round(cache.hitRatio))}</span>}
      </div>
      {p !== undefined && <Progress value={p} tone={wearTone(p) === "neutral" ? "accent" : (wearTone(p) as "ok" | "warn" | "danger")} />}
      {series.length > 1 && (
        <div>
          <div className="mb-1 text-[11.5px] text-muted">{tr("Reads served by the SSD (%)")}</div>
          <AreaChart height={70} max={100} series={[{ label: tr("Hit ratio"), color: "var(--ok)", values: series.map((s) => s.ratio) }]} format={(v) => `${v.toFixed(0)} %`} />
        </div>
      )}
      <ul className="list-disc space-y-0.5 pl-5 text-muted">
        {w.advice.map((a) => <li key={a}>{srv(a)}</li>)}
      </ul>
    </div>
  );
}

const ranges = [
  { id: "7", label: tr("7 days"), days: 7 },
  { id: "30", label: tr("30 days"), days: 30 },
  { id: "365", label: tr("1 year"), days: 365 },
  { id: "all", label: tr("All"), days: 0 },
] as const;
type RangeId = (typeof ranges)[number]["id"];

const counters: { key: keyof HistoryPoint; label: string; color: string }[] = [
  { key: "re", label: tr("Reallocated sectors"), color: "var(--danger)" },
  { key: "pe", label: tr("Pending sectors"), color: "var(--warn)" },
  { key: "un", label: tr("Unreadable sectors"), color: "#a855f7" },
  { key: "crc", label: tr("Cable (CRC) errors"), color: "#0ea5e9" },
  { key: "me", label: tr("Media errors"), color: "#f97316" },
];

const day = (t: number) => new Date(t * 1000).toLocaleDateString(locale, { dateStyle: "medium" });

/** SMART history of a disk: counters, temperature, SSD wear. */
export function SmartHistory({ device }: { device: string }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["smart-history", device], queryFn: () => api.get<History>(`/api/storage/disks/history${q({ device })}`) });
  const [range, setRange] = useState<RangeId>("30");
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data || data.points.length === 0) return <div className="text-muted text-[13px]">{tr("No history yet: a reading is recorded every hour while the disk is awake (sleeping disks are not woken up).")}</div>;
  const days = ranges.find((r) => r.id === range)!.days;
  const from = days ? Date.now() / 1000 - days * 86400 : 0;
  const pts = data.points.filter((p) => p.t >= from);
  const has = (k: keyof HistoryPoint) => pts.some((p) => p[k] !== undefined);
  const vals = (k: keyof HistoryPoint) => pts.filter((p) => p[k] !== undefined).map((p) => p[k] as number);
  const first = pts[0], last = pts[pts.length - 1];
  const shownCounters = counters.filter((c) => has(c.key));
  const worse = shownCounters.filter((c) => first && last && (last[c.key] as number) > (first[c.key] as number));
  return (
    <div className="space-y-5">
      <Tabs tabs={ranges.map((r) => ({ id: r.id, label: r.label }))} value={range} onChange={setRange} />
      {pts.length === 0 ? <div className="text-muted text-[13px]">{tr("No reading in this period.")}</div> : <>
        <div className="text-[12px] text-muted">{pts.length === 1
          ? tr("One reading so far ({0}): the graphs fill in day after day.", day(first.t))
          : tr("{0} readings from {1} to {2}", pts.length, day(first.t), day(last.t))}</div>
        {worse.length > 0 && (
          <div className="rounded-xl border border-warn/30 bg-warn/10 px-4 py-3 text-[13px] text-warn">
            <b>{tr("Getting worse:")}</b>{" "}{worse.map((c) => tr("{0} {1} → {2}", c.label.toLowerCase(), first[c.key] as number, last[c.key] as number)).join(", ")}
          </div>
        )}
        {shownCounters.length > 0 && (
          <div>
            <div className="mb-1 text-[12.5px] font-medium">{tr("Error counters")}</div>
            {shownCounters.every((c) => vals(c.key).every((v) => v === 0))
              ? <div className="text-[12.5px] text-ok">{tr("All counters stayed at zero.")}</div>
              : <AreaChart height={110} series={shownCounters.map((c) => ({ label: c.label, color: c.color, values: vals(c.key) }))} format={(v) => v.toFixed(0)} />}
          </div>
        )}
        {has("tp") && (
          <div>
            <div className="mb-1 text-[12.5px] font-medium">{tr("Temperature")}</div>
            <AreaChart height={90} series={[{ label: "°C", color: "var(--accent)", values: vals("tp") }]} format={(v) => tr("{0} °C", v.toFixed(0))} />
            <div className="mt-1 text-[11.5px] text-muted">{tr("Min {0} °C, max {1} °C", Math.min(...vals("tp")), Math.max(...vals("tp")))}</div>
          </div>
        )}
        {(has("us") || has("wr")) && (
          <div className={cx("grid gap-4", has("us") && has("wr") && "sm:grid-cols-2")}>
            {has("us") && (
              <div>
                <div className="mb-1 text-[12.5px] font-medium">{tr("SSD wear (%)")}</div>
                <AreaChart height={90} max={100} series={[{ label: tr("Used"), color: "var(--warn)", values: vals("us") }]} format={(v) => `${v.toFixed(0)} %`} />
              </div>
            )}
            {has("wr") && (
              <div>
                <div className="mb-1 text-[12.5px] font-medium">{tr("Data written")}</div>
                <AreaChart height={90} series={[{ label: tr("Total"), color: "#0ea5e9", values: vals("wr") }]} format={(v) => bytes(v)} />
              </div>
            )}
          </div>
        )}
        {data.wear && (
          <div className="rounded-xl bg-panel-2 p-3 text-[12.5px]">
            {[
              data.wear.percentUsed !== undefined && tr("Wear: {0}", `${data.wear.percentUsed} %`),
              data.wear.spare !== undefined && tr("Spare cells: {0}", `${data.wear.spare} %`),
              data.wear.writeRate !== undefined && tr("{0} a day over {1} days", bytes(data.wear.writeRate), Math.round(data.wear.rateDays ?? 0)),
              remaining(data.wear.remainingDays),
            ].filter(Boolean).join(" · ")}
          </div>
        )}
      </>}
    </div>
  );
}
