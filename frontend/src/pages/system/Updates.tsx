import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CalendarClock, CheckCircle2, FlaskConical, History, Plus, RefreshCcw, Trash2, Undo2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Badge, Button, Card, Empty, ErrorBox, Field, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { dateTime } from "../../lib/format";
import { locale, tr } from "../../lib/i18n";

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
      <div className="mb-5 grid gap-5 lg:grid-cols-2">
        <ChannelCard />
        <AutoUpdatesCard />
      </div>
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

type Mode = "" | "notify" | "install";
type AutoRun = {
  window: string; started: string; finished?: string; status: "running" | "success" | "failed" | "skipped"; dryRun?: boolean; message?: string;
  security?: number; others?: number; strataTo?: string; available?: number; offered?: string; restart?: "" | "required" | "done";
};
type AutoData = {
  strata: Mode; security: "" | "install"; system: Mode; days: number[]; hour: number; reboot: boolean;
  channel: "stable" | "beta"; repository: boolean; installed: string; beta: boolean;
  nextWindow?: string; last?: AutoRun; latestStable?: string; downgrade?: string;
};

function useAutoUpdates() {
  return useQuery({ queryKey: ["updates-auto"], queryFn: () => api.get<AutoData>("/api/system/updates/auto") });
}

function ChannelCard() {
  const { confirm } = useUI();
  const { data } = useAutoUpdates();
  const setChannel = useAction((channel: string) => api.put("/api/system/updates/channel", { channel }), { success: tr("Update channel changed") });
  const downgrade = useAction((version: string) => api.post("/api/system/updates/downgrade", { version }));
  if (!data) return null;
  const choose = async (ch: "stable" | "beta") => {
    if (ch === data.channel) return;
    if (ch === "beta" && !(await confirm({ title: tr("Switch to the beta channel?"), text: tr("Beta versions bring new features before everyone else, but they may have bugs, and an update may need a manual fix. Keep backups of your data, and don't use the beta channel on a NAS you can't afford to lose for a while."), confirm: tr("Use beta versions"), danger: true }))) return;
    if (ch === "stable" && data.beta && !(await confirm({ title: tr("Go back to the stable channel?"), text: tr("StrataOS {0} (beta) stays installed: the NAS moves to the next stable version newer than it, as soon as it is published. You can also go back to the latest stable version right away below.", data.installed) }))) return;
    setChannel.mutate(ch);
  };
  return (
    <Card title={<span className="flex items-center gap-2"><FlaskConical size={16} className="text-accent" />{tr("Update channel")}</span>}>
      {!data.repository ? (
        <div className="text-muted text-[13px]">{tr("No StrataOS repository configured: updates are installed by uploading a new .deb package.")}</div>
      ) : (
        <div className="space-y-3">
          <div className="flex gap-2">
            {(["stable", "beta"] as const).map((ch) => (
              <button key={ch} disabled={setChannel.isPending} onClick={() => choose(ch)} className={`flex-1 rounded-xl border px-3 py-2 text-left text-[13px] ${data.channel === ch ? "border-accent bg-accent-soft" : "border-line"}`}>
                <div className="font-semibold">{ch === "stable" ? tr("Stable") : tr("Beta")}</div>
                <div className="text-muted text-[12px]">{ch === "stable" ? tr("Final versions only (recommended)") : tr("Also the beta and release-candidate versions")}</div>
              </button>
            ))}
          </div>
          {data.channel === "beta" && <Notice tone="warn">{tr("Beta channel: pre-release versions are installed as soon as they are published. They may have bugs; keep backups.")}</Notice>}
          {data.channel === "stable" && data.beta && (
            <Notice>
              <div>{tr("StrataOS {0} is a beta version. It stays installed until a stable version newer than it is published.", data.installed)}</div>
              {data.downgrade && (
                <div className="mt-2">
                  <Button size="sm" loading={downgrade.isPending} onClick={async () => { if (await confirm({ title: tr("Go back to StrataOS {0}?", data.downgrade!), text: tr("StrataOS {0} replaces {1} now, after a restore point. Settings made with features of the beta version may not work in {0}. The service restarts: the interface reloads.", data.downgrade!, data.installed), confirm: tr("Downgrade"), danger: true, typeToConfirm: data.downgrade })) downgrade.mutate(data.downgrade!); }}>{tr("Go back to the latest stable version ({0})", data.downgrade)}</Button>
                </div>
              )}
            </Notice>
          )}
        </div>
      )}
    </Card>
  );
}

function AutoUpdatesCard() {
  const { data } = useAutoUpdates();
  const [c, setC] = useState<AutoData | null>(null);
  useEffect(() => { if (data && !c) setC(data); }, [data, c]);
  const save = useAction(() => api.put("/api/system/updates/auto", { strata: c!.strata, security: c!.security, system: c!.system, days: c!.days, hour: c!.hour, reboot: c!.reboot }), { success: tr("Automatic update settings saved"), onDone: () => setC(null) });
  if (!data || !c) return null;
  const set = (p: Partial<AutoData>) => setC({ ...c, ...p });
  const days = Array.from({ length: 7 }, (_, i) => (i + 1) % 7); // Monday first
  const dayName = (d: number) => new Intl.DateTimeFormat(locale, { weekday: "short" }).format(new Date(2026, 0, 4 + d));
  const everyDay = c.days.length === 0;
  const toggleDay = (d: number) => {
    const cur = everyDay ? [0, 1, 2, 3, 4, 5, 6] : c.days;
    const next = cur.includes(d) ? cur.filter((x) => x !== d) : [...cur, d];
    if (next.length > 0) set({ days: next.length === 7 ? [] : next });
  };
  const installs = c.strata === "install" || c.security === "install" || c.system === "install";
  const enabled = c.strata !== "" || c.security !== "" || c.system !== "";
  const modes = (withNotify: boolean) => <>
    <option value="">{tr("Off")}</option>
    {withNotify && <option value="notify">{tr("Notify only")}</option>}
    <option value="install">{tr("Install automatically")}</option>
  </>;
  const last = data.last;
  const lastText = (r: AutoRun) => {
    const n = (r.security ?? 0) + (r.others ?? 0) + (r.strataTo ? 1 : 0);
    if (r.status === "running") return tr("In progress");
    if (r.status === "skipped") return tr("Skipped: a task was running ({0})", r.message ?? "");
    if (r.status === "failed") return tr("Failed: {0}", r.message ?? "");
    if (r.dryRun) return tr("Simulation: {0} package(s) would be updated", n);
    return n ? tr("{0} package(s) updated", n) : tr("Nothing to install");
  };
  return (
    <Card title={<span className="flex items-center gap-2"><CalendarClock size={16} className="text-accent" />{tr("Automatic updates")}</span>} actions={<Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
      <div className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label={tr("StrataOS")}><Select value={c.strata} onChange={(e) => set({ strata: e.target.value as Mode })}>{modes(true)}</Select></Field>
          <Field label={tr("Debian security updates")}><Select value={c.security} onChange={(e) => set({ security: e.target.value as "" | "install" })}>{modes(false)}</Select></Field>
          <Field label={tr("Other system updates")}><Select value={c.system} onChange={(e) => set({ system: e.target.value as Mode })}>{modes(true)}</Select></Field>
        </div>
        {enabled && <>
          <div>
            <div className="mb-1.5 text-[12.5px] font-medium text-muted">{tr("Maintenance window")}</div>
            <div className="flex flex-wrap items-center gap-1.5">
              {days.map((d) => {
                const on = everyDay || c.days.includes(d);
                return <button key={d} onClick={() => toggleDay(d)} className={`rounded-lg border px-2.5 py-1 text-[12.5px] capitalize ${on ? "border-accent bg-accent-soft" : "border-line text-muted"}`}>{dayName(d)}</button>;
              })}
              <Select className="ml-2 w-auto" value={c.hour} onChange={(e) => set({ hour: +e.target.value })}>
                {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{tr("{0} to {1}", `${String(h).padStart(2, "0")}:00`, `${String((h + 3) % 24).padStart(2, "0")}:00`)}</option>)}
              </Select>
            </div>
            <div className="mt-1 text-muted text-[11.5px]">{tr("A restore point is taken first. A backup, scrub or other task in progress postpones the updates by 10 minutes, until the end of the window.")}</div>
          </div>
          {installs && <Switch checked={c.reboot} onChange={(v) => set({ reboot: v })} label={tr("Restart the NAS when an update requires it")} hint={tr("Otherwise you are notified that a restart is required.")} />}
        </>}
        <div className="divide-y divide-line rounded-xl border border-line text-[13px]">
          <div className="flex justify-between gap-3 px-4 py-2"><span className="text-muted">{tr("Next window")}</span><span>{data.nextWindow ? dateTime(data.nextWindow) : "—"}</span></div>
          <div className="flex justify-between gap-3 px-4 py-2">
            <span className="text-muted">{tr("Last automatic run")}</span>
            {last ? (
              <span className="text-right">
                <Badge tone={last.status === "success" ? "ok" : last.status === "failed" ? "danger" : last.status === "skipped" ? "warn" : "accent"}>{dateTime(last.finished ?? last.started)}</Badge>
                <span className="block text-[12.5px]">{lastText(last)}</span>
                {last.offered && <span className="block text-[12px] text-muted">{tr("StrataOS {0} is available", last.offered)}</span>}
                {!!last.available && <span className="block text-[12px] text-muted">{tr("{0} system update(s) available", last.available)}</span>}
                {last.restart === "required" && <span className="block text-[12px] text-warn">{tr("A restart is required")}</span>}
              </span>
            ) : <span>—</span>}
          </div>
        </div>
      </div>
    </Card>
  );
}
