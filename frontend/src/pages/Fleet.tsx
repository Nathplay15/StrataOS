import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Copy, Download, ExternalLink, KeyRound, Network, Plus, RefreshCw, Server, Trash2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { ago, bytes, dateTime, pct } from "../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Switch, useUI } from "../components/ui";
import { srv, tr } from "../lib/i18n";

type Vol = { name: string; total: number; used: number; health: string };
type Updates = { version: string; installed: string; candidate: string; system: number; rebootRequired: boolean; aptBusy: boolean; checked?: string };
type Summary = {
  hostname: string; version: string; uptimeDays: number; sample?: { cpu: number; memUsed: number; memTotal: number }; volumes: Vol[]; alerts: { level: string; message: string }[];
  backups: { ok: number; failed: number; lastFailure?: string }; containers: { running: number; total: number };
  updates?: Updates; ha?: { role: string; peers: string[] }; access?: { updates: boolean; reboot: boolean };
};
type Row = { id: string; name: string; host: string; online: boolean; error?: string; summary?: Summary; haWith?: string[] };
type Manager = { id: string; name: string; created: string; lastSeen?: string; allowUpdates?: boolean; allowReboot?: boolean };
type Item = { member: string; name: string; host: string; ha?: string; state: string; progress: number; from?: string; target?: string; version?: string; note?: string; error?: string };
type Run = {
  id: string; kind: "strata" | "system"; strategy: "parallel" | "rolling"; reboot?: boolean; dryRun?: boolean; cancel?: boolean; user: string; status: string;
  started: string; finished?: string; items: Item[]; totals: { success: number; failed: number; skipped: number; active: number; pending: number; progress: number };
};
type UpdateForm = { kind: "strata" | "system"; strategy: "parallel" | "rolling"; reboot: boolean; dryRun: boolean };

const nasURL = (host: string) => `https://${host.includes(":") ? `[${host}]` : host}:7443/`;
const stateLabel: Record<string, string> = {
  pending: tr("Waiting"), running: tr("Updating"), restarting: tr("Service restarting"), rebooting: tr("Restarting the NAS"), verifying: tr("Checking"),
  success: tr("Done"), failed: tr("Failed"), skipped: tr("Skipped"),
};
const stateTone = (s: string) => (s === "success" ? "ok" : s === "failed" ? "danger" : s === "skipped" || s === "pending" ? "neutral" : "accent");

function UpdateBadges({ u }: { u?: Updates }) {
  if (!u) return null;
  const none = !u.candidate && !u.system && !u.rebootRequired;
  return (
    <div className="mt-3 flex flex-wrap items-center gap-1.5">
      {u.candidate && <Badge tone="accent">{tr("StrataOS {0} available", u.candidate)}</Badge>}
      {u.system > 0 && <Badge>{tr("{0} system update(s)", u.system)}</Badge>}
      {u.rebootRequired && <Badge tone="warn">{tr("Restart required")}</Badge>}
      {u.aptBusy && <Badge tone="accent" dot>{tr("Updating")}</Badge>}
      {none && <span className="flex items-center gap-1 text-ok text-[12px]"><CheckCircle2 size={13} />{tr("Up to date")}</span>}
      {u.checked && <span className="text-muted text-[11.5px]">{tr("checked {0}", ago(u.checked))}</span>}
    </div>
  );
}

function NASCard({ r, onRemove, selected, onSelect }: { r: Row; onRemove?: () => void; selected?: boolean; onSelect?: (v: boolean) => void }) {
  const s = r.summary;
  const errors = s?.alerts.filter((a) => a.level === "error").length ?? 0;
  return (
    <Card pad={false}>
      <div className="p-5">
        <div className="flex items-start gap-3">
          {onSelect && <div className="pt-2.5"><Checkbox checked={!!selected} onChange={onSelect} /></div>}
          <div className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-xl ${!r.online ? "bg-danger/10 text-danger" : errors ? "bg-warn/10 text-warn" : "bg-ok/10 text-ok"}`}><Server size={18} /></div>
          <div className="min-w-0 flex-1">
            <div className="truncate font-semibold">{s?.hostname ?? r.name}{r.id === "local" && <span className="ml-2 text-muted text-[12px] font-normal">{tr("(this NAS)")}</span>}</div>
            <div className="mono truncate text-muted text-[12px]">{r.host || location.host}</div>
          </div>
          {!r.online ? <Badge tone="danger" dot>{tr("Unreachable")}</Badge> : errors ? <Badge tone="warn" dot>{tr("{0} alert(s)", errors)}</Badge> : <Badge tone="ok" dot>{tr("Healthy")}</Badge>}
        </div>
        {!r.online && <div className="mt-3 break-words text-danger text-[12.5px]">{r.error}</div>}
        {s && <>
          <div className="mt-3 text-muted text-[12px]">StrataOS {s.version} · {tr("running for {0} day(s)", s.uptimeDays)} · {tr("{0}/{1} container(s)", s.containers.running, s.containers.total)}</div>
          <UpdateBadges u={s.updates} />
          {s.ha && <div className="mt-2 text-muted text-[12px]">{s.ha.role === "primary" ? tr("High availability: primary") : tr("High availability: secondary")}</div>}
          {r.id !== "local" && s.access && !s.access.updates && <div className="mt-2 text-muted text-[12px]">{tr("This NAS does not allow the console to install updates.")}</div>}
          {s.sample && (
            <div className="mt-3 grid grid-cols-2 gap-3 text-[12px]">
              <div><div className="mb-1 flex justify-between"><span className="text-muted">{tr("Processor")}</span><span>{s.sample.cpu.toFixed(0)} %</span></div><Progress value={s.sample.cpu} /></div>
              <div><div className="mb-1 flex justify-between"><span className="text-muted">{tr("Memory")}</span><span>{bytes(s.sample.memUsed)}</span></div><Progress value={pct(s.sample.memUsed, s.sample.memTotal)} /></div>
            </div>
          )}
          <div className="mt-3 space-y-2">
            {s.volumes.map((v) => (
              <div key={v.name} className="text-[12px]">
                <div className="mb-1 flex justify-between"><span>{v.name}{v.health !== "healthy" && <span className="ml-1 text-danger">({srv(v.health)})</span>}</span><span className="text-muted">{bytes(v.used)} / {bytes(v.total)}</span></div>
                <Progress value={pct(v.used, v.total)} tone={pct(v.used, v.total) >= 90 ? "danger" : pct(v.used, v.total) >= 80 ? "warn" : "ok"} />
              </div>
            ))}
          </div>
          <div className="mt-3 flex items-center gap-2 text-[12.5px]">
            {s.backups.failed ? <XCircle size={14} className="text-danger" /> : <CheckCircle2 size={14} className="text-ok" />}
            <span>{tr("Backups: {0} succeeded, {1} failed", s.backups.ok, s.backups.failed)}{s.backups.lastFailure ? ` (${s.backups.lastFailure})` : ""}</span>
          </div>
          {s.alerts.slice(0, 3).map((a, i) => <div key={i} className="mt-2 flex items-start gap-2 text-[12px] text-warn"><AlertTriangle size={13} className="mt-0.5 shrink-0" />{srv(a.message)}</div>)}
        </>}
      </div>
      <div className="flex justify-end gap-1 border-t border-line bg-panel-2/50 px-3 py-2">
        {r.id !== "local" && <a href={nasURL(r.host)} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost" icon={<ExternalLink size={14} />}>{tr("Open")}</Button></a>}
        {onRemove && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={onRemove} />}
      </div>
    </Card>
  );
}

function RunCard({ run, onCancel }: { run: Run; onCancel: () => void }) {
  const t = run.totals;
  const running = run.status === "running";
  return (
    <div className="rounded-xl border border-line p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{run.kind === "strata" ? tr("StrataOS update") : tr("Full system update")}</span>
        <Badge>{run.strategy === "rolling" ? tr("One at a time") : tr("All at once")}</Badge>
        {run.dryRun && <Badge>{tr("Simulation")}</Badge>}
        {run.reboot && <Badge>{tr("Restart if required")}</Badge>}
        {running ? <Badge tone="accent" dot>{run.cancel ? tr("Cancelling") : tr("In progress")}</Badge>
          : run.status === "success" ? <Badge tone="ok" dot>{tr("Succeeded")}</Badge>
          : run.status === "cancelled" ? <Badge tone="warn" dot>{tr("Cancelled")}</Badge>
          : <Badge tone="danger" dot>{tr("Failed")}</Badge>}
        <span className="text-muted text-[12px]">{dateTime(run.started)}{run.user ? ` · ${run.user}` : ""}</span>
        <span className="flex-1" />
        <span className="text-muted text-[12px]">{tr("{0} succeeded, {1} failed, {2} skipped", t.success, t.failed, t.skipped)}</span>
        {running && !run.cancel && <Button size="sm" variant="ghost" onClick={onCancel}>{tr("Cancel")}</Button>}
      </div>
      {running && <Progress className="mt-3" value={t.progress} tone="accent" />}
      <table className="table mt-2"><tbody>
        {run.items.map((it) => (
          <tr key={it.member}>
            <td className="w-[30%]">
              <a className="hover:underline" href={nasURL(it.host)} target="_blank" rel="noreferrer">{it.name}</a>
              {it.ha && <span className="ml-2 text-muted text-[11.5px]">{it.ha === "primary" ? tr("HA primary") : tr("HA secondary")}</span>}
            </td>
            <td className="w-[22%]"><Badge tone={stateTone(it.state)} dot={!["success", "failed", "skipped", "pending"].includes(it.state)}>{stateLabel[it.state] ?? it.state}</Badge></td>
            <td className="text-[12px]">
              {!["success", "failed", "skipped", "pending"].includes(it.state) && <Progress className="mb-1" value={it.progress} tone="accent" />}
              {it.from && <span className="mono text-muted">{it.version && it.version !== it.from ? `${it.from} → ${it.version}` : it.target && it.state !== "success" ? `${it.from} → ${it.target}` : it.from}</span>}
              {it.note && <span className="ml-2 text-muted">{srv(it.note)}</span>}
              {it.error && <div className="break-words text-danger">{srv(it.error)}</div>}
            </td>
          </tr>
        ))}
      </tbody></table>
    </div>
  );
}

export default function Fleet() {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["fleet"], queryFn: () => api.get<Row[]>("/api/fleet"), refetchInterval: 30000 });
  const managers = useQuery({ queryKey: ["fleet-managers"], queryFn: () => api.get<Manager[]>("/api/fleet/managers") });
  const runs = useQuery({ queryKey: ["fleet-updates"], queryFn: () => api.get<Run[]>("/api/fleet/updates"), refetchInterval: (q) => (q.state.data?.some((r) => r.status === "running") ? 3000 : 30000) });
  const [add, setAdd] = useState<{ host: string; code: string } | null>(null);
  const [code, setCode] = useState("");
  const [sel, setSel] = useState<string[]>([]);
  const [upd, setUpd] = useState<UpdateForm | null>(null);
  const create = useAction(() => api.post("/api/fleet", add), { success: tr("NAS added to the console"), onDone: () => setAdd(null) });
  const remove = useAction((id: string) => api.del(`/api/fleet/${id}`), { success: tr("NAS removed from the console") });
  const mkCode = useAction(() => api.post<{ code: string }>("/api/fleet/code"), { onDone: (r) => setCode(r.code) });
  const revoke = useAction((id: string) => api.del(`/api/fleet/managers/${id}`), { success: tr("Access removed") });
  const perms = useAction((m: Manager) => api.put(`/api/fleet/managers/${m.id}`, { allowUpdates: !!m.allowUpdates, allowReboot: !!m.allowReboot }), { success: tr("Permissions saved") });

  const manageable = (data ?? []).filter((r) => r.id !== "local" && r.online && r.summary?.access?.updates);
  const selected = manageable.filter((r) => sel.includes(r.id));
  const check = useAction(() => api.post("/api/fleet/check", { ids: (selected.length ? selected : manageable).map((r) => r.id) }));
  const start = useAction(() => api.post("/api/fleet/updates", { ids: selected.map((r) => r.id), ...upd }), { success: tr("Grouped update started"), onDone: () => { setUpd(null); setSel([]); } });
  const cancel = useAction((id: string) => api.post(`/api/fleet/updates/${id}/cancel`), { success: tr("No other NAS will start; those updating finish their update") });
  const running = runs.data?.some((r) => r.status === "running");
  // HA pairs among the selection: updated one after the other, whatever the strategy.
  const pairs = selected.filter((r) => r.haWith?.some((id) => sel.includes(id))).map((r) => r.summary?.hostname ?? r.name);

  return (
    <div>
      <PageHeader title={tr("Multi-NAS Console")} subtitle={tr("The status of all your StrataOS NAS in one place")} icon={<Network size={20} />} actions={<>
        {manageable.length > 0 && <Button icon={<RefreshCw size={15} />} loading={check.isPending} onClick={() => check.mutate()}>{selected.length ? tr("Check for updates ({0})", selected.length) : tr("Check for updates")}</Button>}
        {manageable.length > 0 && <Button icon={<Download size={15} />} disabled={!selected.length || running} onClick={() => setUpd({ kind: "strata", strategy: "rolling", reboot: false, dryRun: false })}>{tr("Update selected ({0})", selected.length)}</Button>}
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setAdd({ host: "", code: "" })}>{tr("Add a NAS")}</Button>
      </>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data?.map((r) => <NASCard key={r.id} r={r} selected={sel.includes(r.id)} onSelect={manageable.includes(r) ? (v) => setSel(v ? [...sel, r.id] : sel.filter((x) => x !== r.id)) : undefined}
            onRemove={r.id === "local" ? undefined : async () => { if (await confirm({ title: tr("Remove {0} from the console?", r.name), text: tr("This NAS is no longer monitored from here. Also remove access on that NAS (Multi-NAS Console → Authorized access)."), danger: true })) remove.mutate(r.id); }} />)}
        </div>
      )}
      {data && data.length === 1 && <div className="mt-4"><Card><Empty icon={<Network />} title={tr("Add your other NAS")} text={tr("On the other NAS, open Multi-NAS Console and display an authorization code, then click “Add a NAS” here.")} /></Card></div>}

      {!!runs.data?.length && (
        <Card className="mt-6" title={<span className="flex items-center gap-2"><Download size={16} />{tr("Grouped updates")}</span>}>
          <div className="space-y-3">{runs.data.slice(0, 5).map((r) => <RunCard key={r.id} run={r} onCancel={() => cancel.mutate(r.id)} />)}</div>
        </Card>
      )}

      <Card className="mt-6" title={<span className="flex items-center gap-2"><KeyRound size={16} />{tr("Authorized access to this NAS")}</span>} actions={<Button size="sm" loading={mkCode.isPending} onClick={() => mkCode.mutate()}>{tr("Authorization code")}</Button>}>
        <p className="mb-3 text-muted text-[12.5px]">{tr("The NAS listed here can see the status of this NAS in their console. They can install its updates only if you allow it.")}</p>
        {code && <div className="mb-3 flex items-center gap-2"><code className="mono rounded-lg bg-panel-2 px-3 py-2 text-[18px] tracking-widest">{code}</code><Button size="sm" icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(code); toast(tr("Code copied")); }}>{tr("Copy")}</Button><span className="text-muted text-[12px]">{tr("valid for 15 minutes, one time only")}</span></div>}
        {!managers.data?.length ? <div className="text-muted text-[13px]">{tr("No NAS has access to this one.")}</div> : (
          <table className="table"><tbody>
            {managers.data.map((m) => <tr key={m.id}>
              <td>{m.name}</td>
              <td className="text-muted text-[12px]">{m.lastSeen ? tr("Last seen {0}", ago(m.lastSeen)) : tr("Never")}</td>
              <td className="space-y-2 py-2">
                <Switch checked={!!m.allowUpdates} onChange={(v) => perms.mutate({ ...m, allowUpdates: v, allowReboot: v && !!m.allowReboot })} label={tr("Allow this console to install updates")} />
                <Switch checked={!!m.allowUpdates && !!m.allowReboot} disabled={!m.allowUpdates} onChange={(v) => perms.mutate({ ...m, allowReboot: v })} label={tr("Allow it to restart this NAS when an update requires it")} />
              </td>
              <td className="text-right"><Button size="sm" variant="ghost" onClick={() => revoke.mutate(m.id)}>{tr("Remove")}</Button></td>
            </tr>)}
          </tbody></table>
        )}
      </Card>

      <Modal open={!!upd} onClose={() => setUpd(null)} title={tr("Update {0} NAS", selected.length)} footer={<><Button onClick={() => setUpd(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={start.isPending} disabled={!selected.length} onClick={() => start.mutate()}>{upd?.dryRun ? tr("Simulate") : tr("Update")}</Button></>}>
        {upd && <div className="space-y-4">
          <div className="flex flex-wrap gap-1.5">{selected.map((r) => <Badge key={r.id}>{r.summary?.hostname ?? r.name}</Badge>)}</div>
          <Field label={tr("Update")}>
            <Select value={upd.kind} onChange={(e) => setUpd({ ...upd, kind: e.target.value as UpdateForm["kind"] })}>
              <option value="strata">{tr("StrataOS only")}</option>
              <option value="system">{tr("Full system update (all packages)")}</option>
            </Select>
          </Field>
          <Field label={tr("Strategy")} hint={upd.strategy === "rolling" ? tr("The next NAS starts once the previous one is back online and healthy with the new version. The update stops at the first failure.") : tr("All the NAS update at the same time.")}>
            <Select value={upd.strategy} onChange={(e) => setUpd({ ...upd, strategy: e.target.value as UpdateForm["strategy"] })}>
              <option value="rolling">{tr("One at a time")}</option>
              <option value="parallel">{tr("All at once")}</option>
            </Select>
          </Field>
          {pairs.length > 0 && <Notice tone="warn">{tr("{0} form a high-availability pair: they are never updated at the same time (secondary first).", pairs.join(", "))}</Notice>}
          <Checkbox checked={upd.reboot} onChange={(v) => setUpd({ ...upd, reboot: v })} label={tr("Restart the NAS that require it (if they allow it)")} />
          <div><Checkbox checked={upd.dryRun} onChange={(v) => setUpd({ ...upd, dryRun: v })} label={tr("Simulation: show what would be installed, change nothing")} /></div>
          <p className="text-muted text-[12px]">{tr("Each NAS takes a restore point before updating, when its system disk allows it.")}</p>
          <ErrorBox error={start.error} />
        </div>}
      </Modal>

      <Modal open={!!add} onClose={() => setAdd(null)} title={tr("Add a NAS")} footer={<><Button onClick={() => setAdd(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={create.isPending} disabled={!add?.host || !add.code} onClick={() => create.mutate()}>{tr("Add")}</Button></>}>
        {add && <div className="space-y-4">
          <Field label={tr("NAS address")}><Input value={add.host} onChange={(e) => setAdd({ ...add, host: e.target.value.trim() })} placeholder="192.168.1.30" /></Field>
          <Field label={tr("Authorization code")} hint={tr("Shown by the other NAS in Multi-NAS Console")}><Input className="mono uppercase" value={add.code} onChange={(e) => setAdd({ ...add, code: e.target.value })} placeholder="ABCDE-12345" /></Field>
          <ErrorBox error={create.error} />
        </div>}
      </Modal>
    </div>
  );
}
