// The NAS at a glance: its system, volumes, alerts, and the sync state of
// this computer with it.
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Box, Cpu, FolderSync, HardDrive, Server, Users } from "lucide-react";
import { Badge, Button, Card, ErrorBox, Spinner, Stat, Health } from "@nas/components/ui";
import { bytes, duration } from "@nas/lib/format";
import type { Overview, SystemInfo } from "@nas/lib/types";
import { api } from "../lib/nasapi";
import { useApp } from "../lib/state";
import { useNav } from "../lib/nav";
import { srv, t, tk } from "../lib/i18n";
import { summary } from "../lib/shell";
import { PageHeader, Section, UsageBar, WebButton } from "../components/kit";

export function OverviewPage() {
  const { account, info, isAdmin, state } = useApp();
  const { go } = useNav();
  const sys = useQuery({ queryKey: ["system-info"], queryFn: () => api.get<SystemInfo>("/api/system/info"), refetchInterval: 30_000 });
  const ov = useQuery({ queryKey: ["overview"], queryFn: () => api.get<Overview>("/api/system/overview"), refetchInterval: 30_000, enabled: isAdmin });
  const s = summary(state ? { ...state, accounts: state.accounts.filter((a) => a.id === account?.id) } : undefined);
  const pairs = account?.config.pairs ?? [];
  return (
    <>
      <PageHeader title={account?.config.nasName || t("Overview")} subtitle={`${account?.config.server ?? ""} · ${info?.user?.fullName || account?.config.user || ""}`} actions={<WebButton path="/" />} />
      <ErrorBox error={sys.error || ov.error} />
      {sys.isLoading && <Spinner />}
      {sys.data && (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Stat label={t("System")} value={`StrataOS ${sys.data.version}`} sub={sys.data.os} icon={<Server size={18} />} />
          <Stat label={t("Processor")} value={t("{0} cores", sys.data.cores)} sub={`${t("Load")} ${sys.data.load.map((x) => x.toFixed(2)).join(" · ")}`} icon={<Cpu size={18} />} />
          <Stat label={t("Memory")} value={bytes(sys.data.memTotal)} sub={`${t("Uptime")} ${duration(sys.data.uptime)}`} icon={<Box size={18} />} />
          <Stat label={t("Folder sync")} value={pairs.length ? s.text : t("None")} sub={t("{0} folder(s) of this computer", pairs.length)} tone={s.state === "error" ? "danger" : s.state === "syncing" ? "accent" : "ok"} icon={<FolderSync size={18} />} />
        </div>
      )}
      {ov.data && (
        <>
          {ov.data.alerts.length > 0 && (
            <Section title={t("Alerts")}>
              <Card pad={false}>
                {ov.data.alerts.map((a, i) => (
                  <div key={i} className="flex items-start gap-2.5 border-b border-line px-4 py-3 text-[13px] last:border-0">
                    <AlertTriangle size={16} className={a.level === "error" ? "text-danger" : "text-warn"} />
                    <span className="flex-1">{srv(a.message)}</span>
                    {a.replace && <Button size="sm" onClick={() => go("storage")}>{t("Storage")}</Button>}
                  </div>
                ))}
              </Card>
            </Section>
          )}
          <Section title={t("Volumes")} actions={<Button size="sm" variant="ghost" onClick={() => go("storage")}>{t("Storage")} →</Button>}>
            <div className="grid gap-3 sm:grid-cols-2">
              {ov.data.volumes.map((v) => (
                <Card key={v.mountPoint}>
                  <div className="mb-2 flex items-center gap-2"><HardDrive size={16} className="text-accent" /><span className="font-medium">{v.name}</span><Badge>{v.kind}</Badge><span className="ml-auto"><Health value={v.health} /></span></div>
                  <UsageBar used={v.used} total={v.total} />
                  <div className="mt-1.5 text-[12px] text-muted">{t("{0} used of {1}, {2} free", bytes(v.used), bytes(v.total), bytes(v.free))}</div>
                </Card>
              ))}
            </div>
          </Section>
          <Section title={t("At a glance")}>
            <div className="grid gap-3 sm:grid-cols-4">
              <Stat label={t("Disks")} value={ov.data.disks} icon={<HardDrive size={18} />} />
              <Stat label={t("Shared folders")} value={ov.data.shares} icon={<FolderSync size={18} />} />
              <Stat label={t("Users")} value={ov.data.users} icon={<Users size={18} />} />
              <Stat label={t("Containers")} value={`${ov.data.containers.running} / ${ov.data.containers.total}`} icon={<Box size={18} />} />
            </div>
          </Section>
        </>
      )}
      {!pairs.length && <div className="mt-6"><Card><div className="flex items-center gap-3"><FolderSync className="text-accent" /><div className="flex-1 text-[13px]">{tk("status.nofolder")}</div><Button variant="primary" onClick={() => go("sync")}>{tk("folders.add")}</Button></div></Card></div>}
    </>
  );
}
