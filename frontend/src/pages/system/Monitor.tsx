import { useQuery } from "@tanstack/react-query";
import { Activity } from "lucide-react";
import { api } from "../../lib/api";
import type { Sample, SystemInfo } from "../../lib/types";
import { bytes, pct, rate } from "../../lib/format";
import { Card, PageHeader, Progress } from "../../components/ui";
import { AreaChart } from "../../components/charts";
import { tr } from "../../lib/i18n";

export default function Monitor() {
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => api.get<{ history: Sample[]; cores: number[] }>("/api/system/stats"), refetchInterval: 2000 });
  const info = useQuery({ queryKey: ["sysinfo"], queryFn: () => api.get<SystemInfo>("/api/system/info"), refetchInterval: 10000 });
  const h = stats.data?.history ?? [];
  const last = h[h.length - 1];
  return (
    <div>
      <PageHeader title={tr("Resource Monitor")} subtitle={tr("History of the last 6 minutes, refreshed every 2 seconds")} icon={<Activity size={20} />} />
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={tr("Processor")}>
          <AreaChart height={170} max={100} format={(v) => `${v.toFixed(0)} %`} series={[{ label: tr("Usage"), color: "var(--accent)", values: h.map((s) => s.cpu) }]} />
        </Card>
        <Card title={tr("Memory")}>
          <AreaChart height={170} max={last?.memTotal} format={bytes} series={[{ label: tr("Used"), color: "var(--accent-2)", values: h.map((s) => s.memUsed) }]} />
        </Card>
        <Card title={tr("Network")}>
          <AreaChart height={170} format={rate} series={[{ label: tr("Receive"), color: "var(--accent)", values: h.map((s) => s.netRx) }, { label: tr("Transmit"), color: "var(--accent-2)", values: h.map((s) => s.netTx) }]} />
        </Card>
        <Card title={tr("Disks")}>
          <AreaChart height={170} format={rate} series={[{ label: tr("Read"), color: "var(--ok)", values: h.map((s) => s.diskRead) }, { label: tr("Write"), color: "var(--warn)", values: h.map((s) => s.diskWrite) }]} />
        </Card>
        <Card title={tr("Cores ({0})", stats.data?.cores.length ?? 0)}>
          <div className="grid gap-3 sm:grid-cols-2">
            {stats.data?.cores.map((c, i) => (
              <div key={i}>
                <div className="mb-1 flex justify-between text-[12px] text-muted"><span>CPU {i}</span><span>{c.toFixed(0)} %</span></div>
                <Progress value={c} />
              </div>
            ))}
          </div>
        </Card>
        <Card title={tr("Temperature sensors")}>
          {!info.data?.temps.length ? <div className="text-muted">{tr("No sensors available.")}</div> : (
            <div className="grid gap-3 sm:grid-cols-2">
              {info.data.temps.map((t, i) => (
                <div key={i}>
                  <div className="mb-1 flex justify-between text-[12px] text-muted"><span>{t.label}</span><span>{t.value.toFixed(0)} °C</span></div>
                  <Progress value={pct(t.value, 100)} tone={t.value > 80 ? "danger" : t.value > 65 ? "warn" : "ok"} />
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}
