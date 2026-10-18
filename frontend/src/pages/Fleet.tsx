import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Copy, ExternalLink, KeyRound, Network, Plus, Server, Trash2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { ago, bytes, pct } from "../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, PageHeader, Progress, Spinner, useUI } from "../components/ui";
import { srv, tr } from "../lib/i18n";

type Vol = { name: string; total: number; used: number; health: string };
type Summary = { hostname: string; version: string; uptimeDays: number; sample?: { cpu: number; memUsed: number; memTotal: number }; volumes: Vol[]; alerts: { level: string; message: string }[]; backups: { ok: number; failed: number; lastFailure?: string }; containers: { running: number; total: number } };
type Row = { id: string; name: string; host: string; online: boolean; error?: string; summary?: Summary };
type Manager = { id: string; name: string; created: string; lastSeen?: string };

function NASCard({ r, onRemove }: { r: Row; onRemove?: () => void }) {
  const s = r.summary;
  const errors = s?.alerts.filter((a) => a.level === "error").length ?? 0;
  return (
    <Card pad={false}>
      <div className="p-5">
        <div className="flex items-start gap-3">
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
        {r.id !== "local" && <a href={`https://${r.host.includes(":") ? `[${r.host}]` : r.host}:7443/`} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost" icon={<ExternalLink size={14} />}>{tr("Open")}</Button></a>}
        {onRemove && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={onRemove} />}
      </div>
    </Card>
  );
}

export default function Fleet() {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["fleet"], queryFn: () => api.get<Row[]>("/api/fleet"), refetchInterval: 30000 });
  const managers = useQuery({ queryKey: ["fleet-managers"], queryFn: () => api.get<Manager[]>("/api/fleet/managers") });
  const [add, setAdd] = useState<{ host: string; code: string } | null>(null);
  const [code, setCode] = useState("");
  const create = useAction(() => api.post("/api/fleet", add), { success: tr("NAS added to the console"), onDone: () => setAdd(null) });
  const remove = useAction((id: string) => api.del(`/api/fleet/${id}`), { success: tr("NAS removed from the console") });
  const mkCode = useAction(() => api.post<{ code: string }>("/api/fleet/code"), { onDone: (r) => setCode(r.code) });
  const revoke = useAction((id: string) => api.del(`/api/fleet/managers/${id}`), { success: tr("Access removed") });

  return (
    <div>
      <PageHeader title={tr("Multi-NAS Console")} subtitle={tr("The status of all your StrataOS NAS in one place")} icon={<Network size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setAdd({ host: "", code: "" })}>{tr("Add a NAS")}</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data?.map((r) => <NASCard key={r.id} r={r} onRemove={r.id === "local" ? undefined : async () => { if (await confirm({ title: tr("Remove {0} from the console?", r.name), text: tr("This NAS is no longer monitored from here. Also remove access on that NAS (Multi-NAS Console → Authorized access)."), danger: true })) remove.mutate(r.id); }} />)}
        </div>
      )}
      {data && data.length === 1 && <div className="mt-4"><Card><Empty icon={<Network />} title={tr("Add your other NAS")} text={tr("On the other NAS, open Multi-NAS Console and display an authorization code, then click “Add a NAS” here.")} /></Card></div>}

      <Card className="mt-6" title={<span className="flex items-center gap-2"><KeyRound size={16} />{tr("Authorized access to this NAS")}</span>} actions={<Button size="sm" loading={mkCode.isPending} onClick={() => mkCode.mutate()}>{tr("Authorization code")}</Button>}>
        <p className="mb-3 text-muted text-[12.5px]">{tr("The NAS listed here can see the status of this NAS (read-only) in their console.")}</p>
        {code && <div className="mb-3 flex items-center gap-2"><code className="mono rounded-lg bg-panel-2 px-3 py-2 text-[18px] tracking-widest">{code}</code><Button size="sm" icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(code); toast(tr("Code copied")); }}>{tr("Copy")}</Button><span className="text-muted text-[12px]">{tr("valid for 15 minutes, one time only")}</span></div>}
        {!managers.data?.length ? <div className="text-muted text-[13px]">{tr("No NAS has access to this one.")}</div> : (
          <table className="table"><tbody>
            {managers.data.map((m) => <tr key={m.id}><td>{m.name}</td><td className="text-muted text-[12px]">{m.lastSeen ? tr("Last seen {0}", ago(m.lastSeen)) : tr("Never")}</td><td className="text-right"><Button size="sm" variant="ghost" onClick={() => revoke.mutate(m.id)}>{tr("Remove")}</Button></td></tr>)}
          </tbody></table>
        )}
      </Card>

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
