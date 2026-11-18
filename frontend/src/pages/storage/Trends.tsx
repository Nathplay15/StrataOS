import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { TrendingUp } from "lucide-react";
import { api, q } from "../../lib/api";
import { bytes } from "../../lib/format";
import { AreaChart } from "../../components/charts";
import { Badge, Card, Empty, ErrorBox, PageHeader, Progress, Select, Spinner, Stat } from "../../components/ui";
import { locale, tr } from "../../lib/i18n";

type Trend = { name: string; path?: string; used: number; total?: number; growth7d?: number; growth30d?: number; fullIn?: number };
type Point = { t: number; used: number };

const growth = (g?: number) => (g === undefined ? <span className="text-muted">—</span> : <span className={g > 0 ? "text-warn" : "text-ok"}>{g > 0 ? "+" : ""}{bytes(g)}</span>);

// A volume fills up in ~N days: warn under a month, critical under ten days.
const fullIn = (days?: number) => {
  if (days === undefined) return null;
  const danger = days < 10;
  return <Badge tone={danger ? "danger" : "warn"}>{tr("Full in ~{0} days", Math.round(days))}</Badge>;
};

export default function Trends() {
  const [name, setName] = useState("");
  const trends = useQuery({ queryKey: ["st-trends"], queryFn: () => api.get<{ volumes: Trend[]; shares: Trend[]; users: Trend[] }>("/api/storage/trends") });
  const vol = trends.data?.volumes.find((v) => v.name === name) ?? trends.data?.volumes[0];
  const history = useQuery({
    queryKey: ["st-trends-history", vol?.name],
    queryFn: () => api.get<{ points: Point[] }>(`/api/storage/trends/history${q({ name: vol?.name ?? "" })}`),
    enabled: !!vol,
    retry: false,
  });
  const pts = history.data?.points ?? [];
  const row = (t: Trend) => (
    <tr key={t.path ?? t.name}>
      <td className="max-w-[320px] truncate text-[12.5px]" title={t.path ?? t.name}>{t.name}</td>
      <td className="text-right whitespace-nowrap">{bytes(t.used)}{t.total ? <span className="text-muted"> / {bytes(t.total)}</span> : null}</td>
      <td className="text-right whitespace-nowrap">{growth(t.growth7d)}</td>
      <td className="text-right whitespace-nowrap">{growth(t.growth30d)}</td>
    </tr>
  );
  const table = (rows: Trend[], title: string) => (
    <Card title={title}>
      {rows.length ? (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>{tr("Name")}</th><th className="text-right">{tr("Used")}</th><th className="text-right">{tr("Growth (7 days)")}</th><th className="text-right">{tr("Growth (30 days)")}</th></tr></thead>
            <tbody>{rows.map(row)}</tbody>
          </table>
        </div>
      ) : (
        <Empty title={tr("Nothing yet")} />
      )}
    </Card>
  );

  return (
    <div>
      <PageHeader title={tr("Storage trends")} subtitle={tr("How your volumes, shared folders and accounts grow over time")} icon={<TrendingUp size={20} />} />
      <ErrorBox error={trends.error} />
      {trends.isLoading ? <Spinner /> : !trends.data ? null : (
        <div className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {trends.data.volumes.map((v) => {
              const pctUsed = v.total ? Math.min(100, Math.round((v.used / v.total) * 100)) : 0;
              return (
                <Card key={v.path ?? v.name} title={v.name} actions={fullIn(v.fullIn)}>
                  <Stat label={tr("Used")} value={`${bytes(v.used)}${v.total ? ` / ${bytes(v.total)}` : ""}`} />
                  {v.total ? <Progress value={pctUsed} className="mt-3" /> : null}
                  <div className="mt-2 flex gap-4 text-[12.5px]">
                    <span className="text-muted">{tr("7 days")}: {growth(v.growth7d)}</span>
                    <span className="text-muted">{tr("30 days")}: {growth(v.growth30d)}</span>
                  </div>
                </Card>
              );
            })}
          </div>
          {vol && (
            <Card title={tr("Volume history")} actions={
              <Select value={vol.name} onChange={(e) => setName(e.target.value)} className="max-w-xs">
                {trends.data.volumes.map((v) => <option key={v.name} value={v.name}>{v.name}</option>)}
              </Select>
            }>
              {pts.length < 2 ? <Empty title={tr("Not enough history yet")} /> : (
                <>
                  <AreaChart height={170} max={vol.total} format={bytes} series={[{ label: tr("Used"), color: "var(--accent)", values: pts.map((p) => p.used) }]} />
                  <p className="mt-2 text-right text-[12px] text-muted">{new Date(pts[0].t * 1000).toLocaleDateString(locale)} → {new Date(pts[pts.length - 1].t * 1000).toLocaleDateString(locale)}</p>
                </>
              )}
            </Card>
          )}
          <div className="grid gap-4 lg:grid-cols-2">
            {table(trends.data.shares, tr("Shared folders"))}
            {table(trends.data.users, tr("Accounts"))}
          </div>
          <p className="text-muted text-[12px]">{tr("Shared folders are measured once a day at low priority (recycle bins and snapshots left out, volumes whose disks sleep wait for another day); accounts come from the user quotas, on the volumes where they are on. The projections assume the last two weeks keep their pace.")}</p>
        </div>
      )}
    </div>
  );
}
