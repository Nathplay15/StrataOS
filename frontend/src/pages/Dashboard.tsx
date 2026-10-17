import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Container, Cpu, Database, HardDrive, Share2, Thermometer, XCircle } from "lucide-react";
import { AppLink } from "../components/AppLink";
import { api } from "../lib/api";
import type { Me, Overview, Sample, SystemInfo } from "../lib/types";
import { bytes, duration, pct, rate } from "../lib/format";
import { Card, Empty, Health, PageHeader, Progress, Spinner, Stat } from "../components/ui";
import { AreaChart, Ring } from "../components/charts";
import { tr, srv } from "../lib/i18n";

export default function Dashboard({ me }: { me: Me }) {
  const info = useQuery({ queryKey: ["sysinfo"], queryFn: () => api.get<SystemInfo>("/api/system/info"), refetchInterval: 30000 });
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => api.get<{ history: Sample[]; cores: number[] }>("/api/system/stats"), refetchInterval: 2000 });
  const ov = useQuery({ queryKey: ["overview"], queryFn: () => api.get<Overview>("/api/system/overview"), refetchInterval: 15000 });

  const h = stats.data?.history ?? [];
  const last = h[h.length - 1];
  const i = info.data;
  const o = ov.data;
  const hottest = i?.temps.reduce((m, t) => Math.max(m, t.value), 0) ?? 0;

  return (
    <div>
      <PageHeader title={tr("Bonjour {0}", me.fullName || me.username)} subtitle={i ? tr("{0} · {1} · actif depuis {2}", i.hostname, i.os, duration(i.uptime)) : tr("Chargement…")} />

      {o && o.alerts.length > 0 && (
        <div className="mb-5 space-y-2">
          {o.alerts.map((a) => (
            <div key={a.message} className={`flex items-center gap-2.5 rounded-xl border px-4 py-3 text-[13.5px] ${a.level === "error" ? "border-danger/30 bg-danger/10 text-danger" : "border-warn/30 bg-warn/10 text-warn"}`}>
              {a.level === "error" ? <XCircle size={17} /> : <AlertTriangle size={17} />}
              {srv(a.message)}
            </div>
          ))}
        </div>
      )}

      <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label={tr("Disques")} value={o?.disks ?? "—"} sub={tr("{0} RAID · {1} pool ZFS", o?.raids ?? 0, o?.pools ?? 0)} icon={<HardDrive size={20} />} />
        <Stat label={tr("Partages")} value={o?.shares ?? "—"} sub={tr("{0} utilisateurs", o?.users ?? 0)} icon={<Share2 size={20} />} tone="ok" />
        <Stat label={tr("Conteneurs")} value={o ? `${o.containers.running}/${o.containers.total}` : "—"} sub={tr("en cours d'exécution")} icon={<Container size={20} />} tone="accent" />
        <Stat label={tr("Température max")} value={hottest ? `${hottest.toFixed(0)} °C` : "—"} sub={i?.cpuModel} icon={<Thermometer size={20} />} tone={hottest > 80 ? "danger" : hottest > 65 ? "warn" : "ok"} />
      </div>

      <div className="grid gap-5 xl:grid-cols-3">
        <Card title={tr("Ressources")} className="xl:col-span-1">
          {!last ? (
            <Spinner />
          ) : (
            <div className="flex flex-wrap justify-around gap-6 py-2">
              <Ring value={last.cpu} label={tr("Processeur")} sub={tr("{0} cœurs · charge {1}", i?.cores ?? "?", i?.load[0].toFixed(2) ?? "—")} />
              <Ring value={pct(last.memUsed, last.memTotal)} label={tr("Mémoire")} sub={`${bytes(last.memUsed)} / ${bytes(last.memTotal)}`} color="var(--accent-2)" />
            </div>
          )}
        </Card>
        <Card title={tr("Activité réseau")} className="xl:col-span-2">
          <AreaChart
            height={150}
            format={rate}
            series={[
              { label: tr("Réception"), color: "var(--accent)", values: h.map((s) => s.netRx) },
              { label: tr("Émission"), color: "var(--accent-2)", values: h.map((s) => s.netTx) },
            ]}
          />
        </Card>
        <Card title={tr("Processeur")} className="xl:col-span-2">
          <AreaChart height={150} max={100} format={(v) => `${v.toFixed(0)} %`} series={[{ label: "CPU", color: "var(--accent)", values: h.map((s) => s.cpu) }]} />
        </Card>
        <Card title={tr("Disques (E/S)")}>
          <AreaChart
            height={150}
            format={rate}
            series={[
              { label: tr("Lecture"), color: "var(--ok)", values: h.map((s) => s.diskRead) },
              { label: tr("Écriture"), color: "var(--warn)", values: h.map((s) => s.diskWrite) },
            ]}
          />
        </Card>
      </div>

      <Card title={tr("Volumes de stockage")} className="mt-5" actions={me.role === "admin" && <AppLink to="/storage/volumes" className="text-accent text-[13px]">{tr("Gérer")}</AppLink>}>
        {!o ? (
          <Spinner />
        ) : o.volumes.length === 0 ? (
          <Empty icon={<Database size={22} />} title={tr("Aucun volume")} text={tr("Créez un RAID ou un pool, puis un volume pour commencer à stocker des données.")} action={me.role === "admin" && <AppLink to="/storage/volumes" className="text-accent">{tr("Créer un volume →")}</AppLink>} />
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {o.volumes.map((v) => {
              const p = pct(v.used, v.total);
              return (
                <div key={v.name} className="rounded-xl border border-line bg-panel-2 p-4">
                  <div className="mb-3 flex items-center justify-between gap-2">
                    <div className="flex min-w-0 items-center gap-2.5">
                      <Database size={17} className="text-accent" />
                      <div className="min-w-0">
                        <div className="truncate font-medium">{v.name}</div>
                        <div className="text-muted text-[11.5px]">{v.kind.toUpperCase()} · {v.mountPoint}</div>
                      </div>
                    </div>
                    <Health value={v.health} />
                  </div>
                  <Progress value={p} />
                  <div className="mt-2 flex justify-between text-[12px] text-muted">
                    <span>{bytes(v.used)} {" "}{tr("utilisés")}</span>
                    <span>{bytes(v.free)} {" "}{tr("libres sur")}{" "}{bytes(v.total)}</span>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </Card>

      {i && (
        <Card title={tr("Système")} className="mt-5">
          <dl className="grid gap-x-8 gap-y-3 text-[13px] sm:grid-cols-2 xl:grid-cols-4">
            {[
              [tr("Matériel"), [i.vendor, i.product].filter(Boolean).join(" ") || "—"],
              [tr("Processeur"), i.cpuModel],
              [tr("Noyau"), i.kernel],
              [tr("Fuseau"), i.timezone],
              [tr("Mémoire"), bytes(i.memTotal)],
              [tr("Swap"), `${bytes(i.swapTotal - i.swapFree)} / ${bytes(i.swapTotal)}`],
              [tr("Charge"), i.load.map((l) => l.toFixed(2)).join(" · ")],
              ["StrataOS", i.version],
            ].map(([k, v]) => (
              <div key={k}>
                <dt className="text-muted text-[12px]">{k}</dt>
                <dd className="truncate">{v}</dd>
              </div>
            ))}
          </dl>
          {i.temps.length > 0 && (
            <div className="mt-4 flex flex-wrap gap-2 border-t border-line pt-4">
              {i.temps.slice(0, 12).map((t, k) => (
                <span key={k} className="flex items-center gap-1.5 rounded-lg bg-panel-2 px-2.5 py-1 text-[12px]">
                  <Cpu size={13} className="text-muted" /> {t.label} <b>{t.value.toFixed(0)}°C</b>
                </span>
              ))}
            </div>
          )}
        </Card>
      )}
    </div>
  );
}
