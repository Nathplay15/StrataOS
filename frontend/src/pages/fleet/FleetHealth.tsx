import { Fragment, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowDown, ArrowUp, CheckCircle2, ChevronDown, ChevronRight, Download, ExternalLink, HardDrive, RefreshCw, Server, ShieldCheck, Thermometer } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, pct } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Progress, Spinner } from "../../components/ui";
import { AppLink } from "../../components/AppLink";
import { srv, tr } from "../../lib/i18n";

export type FleetHealth = {
  hostname: string; version: string; uptimeDays: number;
  updates?: { version: string; candidate: string; system: number; rebootRequired: boolean; aptBusy: boolean };
  advisor?: { score: number; high: number; medium: number; time: string };
  disks: { total: number; failing: number; warning: number; maxWear?: number; issues?: string[] };
  arrays: { total: number; degraded: number; rebuilding: number; issues?: string[] };
  space: { total: number; used: number; fullest?: string; fullestPct: number; unmounted: number; volumes: { name: string; total: number; used: number; health: string }[] };
  backups: { total: number; failed: number; lastSuccess?: string; lastFailure?: string; lastFailureName?: string; stale?: string[] };
  temps: { cpu?: number; disk?: number; diskName?: string };
  alerts: { errors: number; warnings: number; messages?: { level: string; message: string }[] };
  access?: { updates: boolean; reboot: boolean; vms: boolean };
  partial?: boolean; at: string;
};
type Reason = { level: "info" | "warning" | "critical"; kind: string; text: string };
export type HealthRow = {
  id: string; name: string; host: string; online: boolean; error?: string; health?: FleetHealth; level: "ok" | "info" | "warning" | "critical";
  reasons: Reason[]; checked: string; seen?: string; pending?: boolean;
};
type Totals = { total: number; online: number; offline: number; critical: number; warning: number; attention: number; updates: number; spaceTotal: number; spaceUsed: number };
export type Dashboard = { rows: HealthRow[]; totals: Totals; refreshed?: string; refreshing: boolean };

export const nasURL = (host: string) => `https://${host.includes(":") ? `[${host}]` : host}:7443/`;
const rank: Record<string, number> = { ok: 0, info: 1, warning: 2, critical: 3 };
const tone = (l: string) => (l === "critical" ? "danger" : l === "warning" ? "warn" : l === "info" ? "accent" : "ok") as "danger" | "warn" | "accent" | "ok";
const levelLabel = (l: string) => ({ ok: tr("Healthy"), info: tr("Healthy"), warning: tr("Needs attention"), critical: tr("Critical") } as Record<string, string>)[l] ?? l;

type SortKey = "level" | "name" | "version" | "security" | "disks" | "space" | "backups" | "temps" | "alerts";
const sortValue: Record<SortKey, (r: HealthRow) => number | string> = {
  level: (r) => rank[r.level] * 10 + (r.online ? 0 : 5),
  name: (r) => (r.health?.hostname ?? r.name).toLowerCase(),
  version: (r) => (r.health?.updates?.candidate ? 2 : 0) + (r.health?.updates?.system ? 1 : 0),
  security: (r) => -(r.health?.advisor?.score ?? 101),
  disks: (r) => (r.health ? r.health.disks.failing * 100 + r.health.arrays.degraded * 100 + r.health.disks.warning * 10 + (r.health.disks.maxWear ?? 0) / 10 : -1),
  space: (r) => r.health?.space.fullestPct ?? -1,
  backups: (r) => (r.health ? r.health.backups.failed * 10 + (r.health.backups.stale?.length ?? 0) : -1),
  temps: (r) => Math.max(r.health?.temps.cpu ?? 0, (r.health?.temps.disk ?? 0) * 1.5),
  alerts: (r) => (r.health ? r.health.alerts.errors * 100 + r.health.alerts.warnings : -1),
};

function Th({ k, sort, setSort, children, className }: { k: SortKey; sort: { key: SortKey; desc: boolean }; setSort: (s: { key: SortKey; desc: boolean }) => void; children: React.ReactNode; className?: string }) {
  const active = sort.key === k;
  return (
    <th className={className}>
      <button className="inline-flex items-center gap-1 hover:text-fg" onClick={() => setSort({ key: k, desc: active ? !sort.desc : k !== "name" })} aria-sort={active ? (sort.desc ? "descending" : "ascending") : undefined}>
        {children}{active && (sort.desc ? <ArrowDown size={12} /> : <ArrowUp size={12} />)}
      </button>
    </th>
  );
}

function Advisor({ h }: { h?: FleetHealth }) {
  const a = h?.advisor;
  if (!a) return <span className="text-muted">—</span>;
  return <span className={a.high ? "text-danger" : a.score < 60 ? "text-warn" : a.score < 80 ? "" : "text-ok"}>{a.score}/100{a.high > 0 && <span className="ml-1 text-[11.5px]">({tr("{0} high", a.high)})</span>}</span>;
}

function Disks({ h }: { h?: FleetHealth }) {
  if (!h) return <span className="text-muted">—</span>;
  const d = h.disks, a = h.arrays;
  const bad = d.failing + a.degraded;
  return (
    <div className="space-y-0.5 text-[12px]">
      {bad > 0 ? <div className="text-danger">{d.failing > 0 && tr("{0} failing", d.failing)}{d.failing > 0 && a.degraded > 0 && ", "}{a.degraded > 0 && tr("{0} degraded", a.degraded)}</div>
        : d.warning > 0 ? <div className="text-warn">{tr("{0} with warnings", d.warning)}</div>
        : <div className="text-ok">{tr("{0} disk(s) OK", d.total)}</div>}
      {a.rebuilding > 0 && <div className="text-accent">{tr("{0} rebuilding", a.rebuilding)}</div>}
      {d.maxWear != null && <div className={d.maxWear >= 90 ? "text-warn" : "text-muted"}>{tr("SSD wear {0} %", d.maxWear)}</div>}
    </div>
  );
}

function Space({ h }: { h?: FleetHealth }) {
  if (!h || !h.space.total) return <span className="text-muted">—</span>;
  const p = pct(h.space.used, h.space.total);
  const worst = h.space.fullestPct;
  return (
    <div className="min-w-[120px] text-[12px]">
      <div className="mb-1 flex justify-between gap-2"><span>{bytes(h.space.used)} / {bytes(h.space.total)}</span></div>
      <Progress value={p} tone={worst >= 90 ? "danger" : worst >= 80 ? "warn" : "ok"} />
      {worst >= 80 && h.space.fullest && <div className={worst >= 90 ? "mt-0.5 text-danger" : "mt-0.5 text-warn"}>{tr("{0}: {1} %", h.space.fullest, Math.round(worst))}</div>}
      {h.space.unmounted > 0 && <div className="mt-0.5 text-warn">{tr("{0} not mounted", h.space.unmounted)}</div>}
    </div>
  );
}

function Backups({ h }: { h?: FleetHealth }) {
  if (!h) return <span className="text-muted">—</span>;
  const b = h.backups;
  if (!b.total) return <span className="text-muted text-[12px]">{tr("No backup task")}</span>;
  return (
    <div className="space-y-0.5 text-[12px]">
      {b.lastSuccess ? <div className="flex items-center gap-1"><CheckCircle2 size={12} className="text-ok" />{ago(b.lastSuccess)}</div> : <div className="text-muted">{tr("Never succeeded")}</div>}
      {b.failed > 0 && <div className="text-danger" title={b.lastFailureName}>{tr("{0} failed", b.failed)}{b.lastFailure ? ` · ${ago(b.lastFailure)}` : ""}</div>}
      {!!b.stale?.length && <div className="text-warn" title={b.stale.join(", ")}>{tr("{0} late", b.stale.length)}</div>}
    </div>
  );
}

function Temps({ h }: { h?: FleetHealth }) {
  const t = h?.temps;
  if (!t || (t.cpu == null && t.disk == null)) return <span className="text-muted">—</span>;
  return (
    <div className="space-y-0.5 whitespace-nowrap text-[12px]">
      {t.cpu != null && <div className={t.cpu >= 85 ? "text-danger" : t.cpu >= 75 ? "text-warn" : ""}>{tr("CPU {0} °C", t.cpu)}</div>}
      {t.disk != null && <div className={t.disk >= 55 ? "text-danger" : t.disk >= 50 ? "text-warn" : ""} title={t.diskName}>{tr("Disk {0} °C", t.disk)}</div>}
    </div>
  );
}

function Version({ h, partial }: { h?: FleetHealth; partial?: boolean }) {
  if (!h) return <span className="text-muted">—</span>;
  const u = h.updates;
  return (
    <div className="space-y-1 text-[12px]">
      <div className="mono">{h.version}</div>
      <div className="flex flex-wrap gap-1">
        {u?.candidate && <Badge tone="accent">{u.candidate}</Badge>}
        {!!u?.system && <Badge>{tr("{0} pkg", u.system)}</Badge>}
        {u?.rebootRequired && <Badge tone="warn">{tr("Restart")}</Badge>}
        {u?.aptBusy && <Badge tone="accent" dot>{tr("Updating")}</Badge>}
        {partial && <Badge>{tr("Partial")}</Badge>}
      </div>
    </div>
  );
}

function Details({ r }: { r: HealthRow }) {
  const h = r.health;
  return (
    <div className="grid gap-4 p-3 text-[12.5px] md:grid-cols-3">
      <div>
        <div className="mb-1 font-medium">{tr("Why")}</div>
        {r.reasons.length === 0 ? <div className="text-ok">{tr("Nothing to report")}</div> : r.reasons.map((x, i) => (
          <div key={i} className={x.level === "critical" ? "text-danger" : x.level === "warning" ? "text-warn" : "text-muted"}>{srv(x.text)}</div>
        ))}
        {!r.online && r.error && <div className="mt-1 break-words text-muted">{srv(r.error)}</div>}
        {!r.online && r.seen && <div className="mt-1 text-muted">{tr("Last answer {0}", ago(r.seen))}</div>}
      </div>
      <div>
        <div className="mb-1 font-medium">{tr("Alerts")}</div>
        {!h?.alerts.messages?.length && !h?.disks.issues?.length && !h?.arrays.issues?.length ? <div className="text-muted">{tr("None")}</div> : <>
          {[...(h?.arrays.issues ?? []), ...(h?.alerts.messages ?? []).map((m) => m.message)].filter((v, i, a) => a.indexOf(v) === i).slice(0, 6).map((m, i) => (
            <div key={i} className="flex items-start gap-1.5"><AlertTriangle size={12} className="mt-0.5 shrink-0 text-warn" />{srv(m)}</div>
          ))}
        </>}
      </div>
      <div>
        <div className="mb-1 font-medium">{tr("Volumes")}</div>
        {h?.space.volumes.map((v) => (
          <div key={v.name} className="mb-1.5">
            <div className="flex justify-between gap-2"><span>{v.name}{v.health !== "healthy" && <span className="ml-1 text-danger">({srv(v.health)})</span>}</span><span className="text-muted">{bytes(v.used)} / {bytes(v.total)}</span></div>
            <Progress value={pct(v.used, v.total)} tone={pct(v.used, v.total) >= 90 ? "danger" : pct(v.used, v.total) >= 80 ? "warn" : "ok"} />
          </div>
        ))}
        {h && <div className="mt-2 text-muted">{tr("running for {0} day(s)", h.uptimeDays)}{r.checked && ` · ${tr("checked {0}", ago(r.checked))}`}</div>}
      </div>
    </div>
  );
}

/** The health of every NAS of the console, sortable, with drill-down actions. */
export function FleetHealthBoard({ onUpdate }: { onUpdate: (id: string) => void }) {
  const q = useQuery({ queryKey: ["fleet-health"], queryFn: () => api.get<Dashboard>("/api/fleet/health"), refetchInterval: 30000 });
  const refresh = useAction(() => api.post<Dashboard>("/api/fleet/health/refresh"));
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: "level", desc: true });
  const [open, setOpen] = useState<string[]>([]);
  const rows = useMemo(() => {
    const list = [...(q.data?.rows ?? [])];
    const f = sortValue[sort.key];
    list.sort((a, b) => {
      const x = f(a), y = f(b);
      const c = x < y ? -1 : x > y ? 1 : 0;
      return (sort.desc ? -c : c) || (a.id === "local" ? -1 : b.id === "local" ? 1 : 0);
    });
    return list;
  }, [q.data, sort]);
  if (q.isLoading) return <Spinner label={tr("Asking every NAS…")} />;
  if (!q.data) return <ErrorBox error={q.error} />;
  const t = q.data.totals;
  const toggle = (id: string) => setOpen(open.includes(id) ? open.filter((x) => x !== id) : [...open, id]);

  return (
    <div className="space-y-4">
      <ErrorBox error={q.error || refresh.error} />
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div className={`rounded-xl border p-4 ${t.attention ? (t.critical ? "border-danger/40 bg-danger/5" : "border-warn/40 bg-warn/5") : "border-ok/40 bg-ok/5"}`}>
          <div className="flex items-center gap-2 font-semibold">
            {t.attention ? <AlertTriangle size={17} className={t.critical ? "text-danger" : "text-warn"} /> : <CheckCircle2 size={17} className="text-ok" />}
            {t.attention ? tr("{0} NAS need attention", t.attention) : tr("Every NAS is healthy")}
          </div>
          <div className="mt-1 text-muted text-[12px]">{t.critical ? tr("{0} critical", t.critical) : tr("Nothing critical")}</div>
        </div>
        <div className="rounded-xl border border-line p-4">
          <div className="flex items-center gap-2 font-semibold"><Server size={16} />{tr("{0}/{1} online", t.online, t.total)}</div>
          <div className="mt-1 text-muted text-[12px]">{t.offline ? tr("{0} unreachable", t.offline) : tr("All reachable")}</div>
        </div>
        <div className="rounded-xl border border-line p-4">
          <div className="flex items-center gap-2 font-semibold"><Download size={16} />{t.updates ? tr("{0} NAS with updates", t.updates) : tr("Up to date")}</div>
          <div className="mt-1 text-muted text-[12px]">{q.data.refreshed ? tr("checked {0}", ago(q.data.refreshed)) : tr("Not checked yet")}{q.data.refreshing && ` · ${tr("refreshing…")}`}</div>
        </div>
        <div className="rounded-xl border border-line p-4">
          <div className="flex items-center gap-2 font-semibold"><HardDrive size={16} />{bytes(t.spaceUsed)} / {bytes(t.spaceTotal)}</div>
          <Progress className="mt-2" value={pct(t.spaceUsed, t.spaceTotal)} />
        </div>
      </div>

      <Card pad={false} title={<span className="flex items-center gap-2"><ShieldCheck size={16} />{tr("Fleet health")}</span>}
        actions={<Button size="sm" icon={<RefreshCw size={14} />} loading={refresh.isPending} onClick={() => refresh.mutate()}>{tr("Refresh")}</Button>}>
        <div className="overflow-x-auto">
          <table className="table min-w-[980px]">
            <thead>
              <tr className="text-left text-muted text-[12px]">
                <th className="w-6" />
                <Th k="name" sort={sort} setSort={setSort}>{tr("NAS")}</Th>
                <Th k="level" sort={sort} setSort={setSort}>{tr("Status")}</Th>
                <Th k="version" sort={sort} setSort={setSort}>{tr("Version")}</Th>
                <Th k="security" sort={sort} setSort={setSort}>{tr("Security")}</Th>
                <Th k="disks" sort={sort} setSort={setSort}>{tr("Disks")}</Th>
                <Th k="space" sort={sort} setSort={setSort}>{tr("Space")}</Th>
                <Th k="backups" sort={sort} setSort={setSort}>{tr("Backups")}</Th>
                <Th k="temps" sort={sort} setSort={setSort}><Thermometer size={13} /></Th>
                <Th k="alerts" sort={sort} setSort={setSort}>{tr("Alerts")}</Th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const h = r.health;
                const expanded = open.includes(r.id);
                const local = r.id === "local";
                const top = r.reasons.find((x) => x.level !== "info");
                const canUpdate = !local && r.online && h?.access?.updates && (h.updates?.candidate || h.updates?.system);
                return (
                  <Fragment key={r.id}>
                    <tr className={!r.online && !r.pending ? "opacity-80" : ""}>
                      <td><button onClick={() => toggle(r.id)} aria-label={tr("Details")} className="text-muted hover:text-fg">{expanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}</button></td>
                      <td>
                        <div className="font-medium">{h?.hostname || r.name}{local && <span className="ml-1.5 text-muted text-[11.5px] font-normal">{tr("(this NAS)")}</span>}</div>
                        <div className="mono text-muted text-[11.5px]">{r.host || location.hostname}</div>
                      </td>
                      <td>
                        {r.pending ? <Badge>{tr("Checking")}</Badge> : !r.online ? <Badge tone="danger" dot>{tr("Unreachable")}</Badge> : <Badge tone={tone(r.level)} dot>{levelLabel(r.level)}</Badge>}
                        {top && <div className="mt-1 max-w-[200px] truncate text-[11.5px] text-muted" title={srv(top.text)}>{srv(top.text)}</div>}
                      </td>
                      <td><Version h={h} partial={h?.partial} /></td>
                      <td className="text-[12.5px]"><Advisor h={h} /></td>
                      <td><Disks h={h} /></td>
                      <td><Space h={h} /></td>
                      <td><Backups h={h} /></td>
                      <td><Temps h={h} /></td>
                      <td className="text-[12px]">
                        {!h ? <span className="text-muted">—</span> : h.alerts.errors + h.alerts.warnings === 0 ? <span className="text-ok">{tr("None")}</span> : <>
                          {h.alerts.errors > 0 && <div className="text-danger">{tr("{0} error(s)", h.alerts.errors)}</div>}
                          {h.alerts.warnings > 0 && <div className="text-warn">{tr("{0} warning(s)", h.alerts.warnings)}</div>}
                        </>}
                      </td>
                      <td className="whitespace-nowrap text-right">
                        {local ? <>
                          <AppLink to="/system/updates"><Button size="sm" variant="ghost" icon={<Download size={14} />}>{tr("Updates")}</Button></AppLink>
                          <AppLink to="/system/security"><Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />}>{tr("Advisor")}</Button></AppLink>
                        </> : <>
                          {canUpdate && <Button size="sm" variant="ghost" icon={<Download size={14} />} onClick={() => onUpdate(r.id)}>{tr("Update")}</Button>}
                          <a href={nasURL(r.host) + "system/security"} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />}>{tr("Advisor")}</Button></a>
                          <a href={nasURL(r.host)} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost" icon={<ExternalLink size={14} />}>{tr("Open")}</Button></a>
                        </>}
                      </td>
                    </tr>
                    {expanded && <tr><td colSpan={11} className="bg-panel-2/50"><Details r={r} /></td></tr>}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
