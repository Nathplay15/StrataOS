import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, History, Plus, RefreshCcw, Trash2, Undo2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Button, Card, Empty, ErrorBox, Notice, PageHeader, Spinner, useUI } from "../../components/ui";
import { dateTime } from "../../lib/format";
import { tr } from "../../lib/i18n";

type Up = { package: string; current: string; new: string; source: string };

type StrataVersion = { running: string; installed: string; candidate: string; repository: boolean };

function StrataCard() {
  const { toast, confirm } = useUI();
  const { data, refetch } = useQuery({ queryKey: ["strata-version"], queryFn: () => api.get<StrataVersion>("/api/system/updates/strata") });
  const [waiting, setWaiting] = useState(false);
  const upgrade = useAction(() => api.post("/api/system/updates/strata"), { onDone: () => setWaiting(true) });
  // The service restarts during the update: wait for a new version to answer.
  useEffect(() => {
    if (!waiting || !data) return;
    const from = data.running;
    const t = setInterval(async () => {
      try {
        const r = await fetch("/api/setup").then((x) => x.json());
        if (r.version && r.version !== from) {
          clearInterval(t);
          toast(tr("StrataOS updated: {0} → {1}", from, r.version));
          setTimeout(() => location.reload(), 1500);
        }
      } catch {
        /* restarting */
      }
    }, 3000);
    return () => clearInterval(t);
  }, [waiting, data, toast]);
  if (!data) return null;
  const available = data.candidate && data.candidate !== "(none)" && data.candidate !== data.installed;
  return (
    <Card title="StrataOS" className="mb-5">
      <div className="flex flex-wrap items-center gap-4">
        <div><div className="text-muted text-[12px]">{tr("Installed version")}</div><div className="mono text-[15px] font-semibold">{data.running}</div></div>
        {available && <div><div className="text-muted text-[12px]">{tr("Available")}</div><div className="mono text-[15px] font-semibold text-ok">{data.candidate}</div></div>}
        <div className="flex-1" />
        {waiting ? <Spinner label={tr("Updating and restarting the service…")} /> : available ? (
          <Button variant="primary" loading={upgrade.isPending} onClick={async () => { if (await confirm({ title: tr("Update to {0}?", data.candidate), text: tr("The StrataOS service restarts at the end: the interface reloads automatically. Shares and applications are not interrupted.") })) upgrade.mutate(); }}>{tr("Update StrataOS")}</Button>
        ) : <Button onClick={() => refetch()}>{data.repository ? tr("Up to date — check") : tr("Check")}</Button>}
      </div>
      {!data.repository && <div className="mt-3 text-muted text-[12.5px]">{tr("No StrataOS repository configured: updates are installed by uploading a new .deb package.")}</div>}
    </Card>
  );
}

export default function Updates() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["updates"], queryFn: () => api.get<Up[]>("/api/system/updates") });
  const refresh = useAction(() => api.post("/api/system/updates/refresh"));
  const upgrade = useAction(() => api.post("/api/system/updates/upgrade"));
  const security = data?.filter((u) => u.source.includes("security")).length ?? 0;
  return (
    <div>
      <PageHeader title={tr("Updates")} subtitle={tr("Debian and StrataOS packages")} icon={<RefreshCcw size={20} />} actions={<>
        <Button icon={<RefreshCcw size={15} />} loading={refresh.isPending} onClick={() => refresh.mutate()}>{tr("Search")}</Button>
        <Button variant="primary" disabled={!data?.length} loading={upgrade.isPending} onClick={async () => { if (await confirm({ title: tr("Install updates?"), text: tr("{0} package(s) will be updated. Some services may restart.", data?.length) })) upgrade.mutate(); }}>{tr("Update all")}</Button>
      </>} />
      <StrataCard />
      <ErrorBox error={error} />
      <Card pad={false} title={data ? tr("{0} update(s) available{1}", data.length, security ? tr(" including {0} security", security) : "") : undefined}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<CheckCircle2 />} title={tr("The system is up to date")} text={tr("Click \"Search\" to refresh the package list.")} /> : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Package")}</th><th>{tr("Current version")}</th><th>{tr("New version")}</th><th>{tr("Source")}</th></tr></thead>
              <tbody>{data.map((u) => <tr key={u.package}><td className="font-medium">{u.package}</td><td className="mono text-muted">{u.current}</td><td className="mono text-ok">{u.new}</td><td className="text-muted text-[12.5px]">{u.source}</td></tr>)}</tbody>
            </table>
          </div>
        )}
      </Card>
      <div className="mt-5"><RestorePoints /></div>
    </div>
  );
}

type Points = { supported: boolean; reason: string; mode?: string; root: { fsType: string; subvol: string }; points: { name: string; created: string; label: string; info?: string; invalid?: boolean }[] };

function RestorePoints() {
  const { confirm } = useUI();
  const { data } = useQuery({ queryKey: ["restore-points"], queryFn: () => api.get<Points>("/api/system/restore-points") });
  const create = useAction(() => api.post("/api/system/restore-points"), { success: tr("Restore point created") });
  const del = useAction((n: string) => api.del(`/api/system/restore-points/${n}`), { success: tr("Restore point deleted") });
  const rollback = useAction((n: string) => api.post(`/api/system/restore-points/${n}/rollback`), { success: tr("Rolled back to restore point: the NAS is restarting") });
  if (!data) return null;
  const label = (l: string) => (l === "before-update" || l === "avant-mise-a-jour" ? tr("Before an update") : l === "manual" || l === "manuel" ? tr("Manual") : l);
  return (
    <Card title={<span className="flex items-center gap-2"><History size={16} className="text-accent" />{tr("System restore points")}</span>} actions={data.supported && <Button size="sm" icon={<Plus size={14} />} loading={create.isPending} onClick={() => create.mutate()}>{tr("Create now")}</Button>}>
      {!data.supported ? (
        <Notice>{tr("Unavailable on this NAS: {0}", data.reason)}</Notice>
      ) : (
        <div className="space-y-3">
          <div className="text-muted text-[13px]">{tr("A restore point is created automatically before each update. Reverting to a point restores the system and its programs, then restarts; your data and StrataOS settings do not change.")}</div>
          <div className="text-muted text-[12px]">{data.mode === "btrfs" ? tr("System Btrfs snapshots (the last 6 are kept).") : data.mode === "lvm" ? tr("LVM snapshots of the system volume (the last 3 are kept; a full snapshot becomes unusable).") : tr("System without snapshots: the versions of the packages about to change, and of StrataOS, are kept before each update (the last 3 restore points are kept).")}</div>
          {data.points.length === 0 ? <div className="text-muted text-[13px]">{tr("No restore points yet.")}</div> : (
            <div className="divide-y divide-line rounded-xl border border-line">
              {data.points.map((p) => (
                <div key={p.name} className="flex flex-wrap items-center gap-3 px-4 py-2.5 text-[13px]">
                  <div className="flex-1"><b>{dateTime(p.created)}</b> <span className="text-muted">· {label(p.label)}{p.info ? ` · ${p.info}` : ""}</span>{p.invalid && <span className="ml-2 text-danger">{tr("unusable (snapshot full)")}</span>}</div>
                  <Button size="sm" icon={<Undo2 size={14} />} disabled={p.invalid} onClick={async () => { if (await confirm({ title: tr("Revert to this point?"), text: tr("The system returns to its state of {0} and the NAS restarts. Programs installed or updated since then will be rolled back; accounts created since then will need their password reset.", dateTime(p.created)), confirm: tr("Restore and restart"), danger: true })) rollback.mutate(p.name); }}>{tr("Restore")}</Button>
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={() => del.mutate(p.name)} />
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
