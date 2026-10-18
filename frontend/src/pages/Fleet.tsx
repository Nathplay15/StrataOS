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
            <div className="truncate font-semibold">{s?.hostname ?? r.name}{r.id === "local" && <span className="ml-2 text-muted text-[12px] font-normal">{tr("(ce NAS)")}</span>}</div>
            <div className="mono truncate text-muted text-[12px]">{r.host || location.host}</div>
          </div>
          {!r.online ? <Badge tone="danger" dot>{tr("Injoignable")}</Badge> : errors ? <Badge tone="warn" dot>{tr("{0} alerte(s)", errors)}</Badge> : <Badge tone="ok" dot>{tr("En bonne santé")}</Badge>}
        </div>
        {!r.online && <div className="mt-3 break-words text-danger text-[12.5px]">{r.error}</div>}
        {s && <>
          <div className="mt-3 text-muted text-[12px]">StrataOS {s.version} · {tr("en marche depuis {0} jour(s)", s.uptimeDays)} · {tr("{0}/{1} conteneur(s)", s.containers.running, s.containers.total)}</div>
          {s.sample && (
            <div className="mt-3 grid grid-cols-2 gap-3 text-[12px]">
              <div><div className="mb-1 flex justify-between"><span className="text-muted">{tr("Processeur")}</span><span>{s.sample.cpu.toFixed(0)} %</span></div><Progress value={s.sample.cpu} /></div>
              <div><div className="mb-1 flex justify-between"><span className="text-muted">{tr("Mémoire")}</span><span>{bytes(s.sample.memUsed)}</span></div><Progress value={pct(s.sample.memUsed, s.sample.memTotal)} /></div>
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
            <span>{tr("Sauvegardes : {0} réussie(s), {1} en échec", s.backups.ok, s.backups.failed)}{s.backups.lastFailure ? ` (${s.backups.lastFailure})` : ""}</span>
          </div>
          {s.alerts.slice(0, 3).map((a, i) => <div key={i} className="mt-2 flex items-start gap-2 text-[12px] text-warn"><AlertTriangle size={13} className="mt-0.5 shrink-0" />{srv(a.message)}</div>)}
        </>}
      </div>
      <div className="flex justify-end gap-1 border-t border-line bg-panel-2/50 px-3 py-2">
        {r.id !== "local" && <a href={`https://${r.host.includes(":") ? `[${r.host}]` : r.host}:7443/`} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost" icon={<ExternalLink size={14} />}>{tr("Ouvrir")}</Button></a>}
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
  const create = useAction(() => api.post("/api/fleet", add), { success: tr("NAS ajouté à la console"), onDone: () => setAdd(null) });
  const remove = useAction((id: string) => api.del(`/api/fleet/${id}`), { success: tr("NAS retiré de la console") });
  const mkCode = useAction(() => api.post<{ code: string }>("/api/fleet/code"), { onDone: (r) => setCode(r.code) });
  const revoke = useAction((id: string) => api.del(`/api/fleet/managers/${id}`), { success: tr("Accès retiré") });

  return (
    <div>
      <PageHeader title={tr("Console multi-NAS")} subtitle={tr("L'état de tous vos NAS StrataOS au même endroit")} icon={<Network size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setAdd({ host: "", code: "" })}>{tr("Ajouter un NAS")}</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data?.map((r) => <NASCard key={r.id} r={r} onRemove={r.id === "local" ? undefined : async () => { if (await confirm({ title: tr("Retirer {0} de la console ?", r.name), text: tr("Ce NAS n'est plus supervisé d'ici. Retirez aussi l'accès sur ce NAS (Console multi-NAS → Accès autorisés)."), danger: true })) remove.mutate(r.id); }} />)}
        </div>
      )}
      {data && data.length === 1 && <div className="mt-4"><Card><Empty icon={<Network />} title={tr("Ajoutez vos autres NAS")} text={tr("Sur l'autre NAS, ouvrez Console multi-NAS et affichez un code d'autorisation, puis cliquez sur « Ajouter un NAS » ici.")} /></Card></div>}

      <Card className="mt-6" title={<span className="flex items-center gap-2"><KeyRound size={16} />{tr("Accès autorisés à ce NAS")}</span>} actions={<Button size="sm" loading={mkCode.isPending} onClick={() => mkCode.mutate()}>{tr("Code d'autorisation")}</Button>}>
        <p className="mb-3 text-muted text-[12.5px]">{tr("Les NAS listés ici voient l'état de ce NAS (lecture seule) dans leur console.")}</p>
        {code && <div className="mb-3 flex items-center gap-2"><code className="mono rounded-lg bg-panel-2 px-3 py-2 text-[18px] tracking-widest">{code}</code><Button size="sm" icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(code); toast(tr("Code copié")); }}>{tr("Copier")}</Button><span className="text-muted text-[12px]">{tr("valable 15 minutes, une seule fois")}</span></div>}
        {!managers.data?.length ? <div className="text-muted text-[13px]">{tr("Aucun NAS n'a accès à celui-ci.")}</div> : (
          <table className="table"><tbody>
            {managers.data.map((m) => <tr key={m.id}><td>{m.name}</td><td className="text-muted text-[12px]">{m.lastSeen ? tr("Dernière consultation {0}", ago(m.lastSeen)) : tr("Jamais")}</td><td className="text-right"><Button size="sm" variant="ghost" onClick={() => revoke.mutate(m.id)}>{tr("Retirer")}</Button></td></tr>)}
          </tbody></table>
        )}
      </Card>

      <Modal open={!!add} onClose={() => setAdd(null)} title={tr("Ajouter un NAS")} footer={<><Button onClick={() => setAdd(null)}>{tr("Annuler")}</Button><Button variant="primary" loading={create.isPending} disabled={!add?.host || !add.code} onClick={() => create.mutate()}>{tr("Ajouter")}</Button></>}>
        {add && <div className="space-y-4">
          <Field label={tr("Adresse du NAS")}><Input value={add.host} onChange={(e) => setAdd({ ...add, host: e.target.value.trim() })} placeholder="192.168.1.30" /></Field>
          <Field label={tr("Code d'autorisation")} hint={tr("Affiché par l'autre NAS dans Console multi-NAS")}><Input className="mono uppercase" value={add.code} onChange={(e) => setAdd({ ...add, code: e.target.value })} placeholder="ABCDE-12345" /></Field>
          <ErrorBox error={create.error} />
        </div>}
      </Modal>
    </div>
  );
}
