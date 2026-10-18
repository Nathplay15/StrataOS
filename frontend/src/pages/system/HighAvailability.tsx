import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftRight, Copy, KeyRound, Plug, Plus, RefreshCcw, Server, ServerCog, Unlink, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Notice, Modal, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type Config = { continuous?: boolean; maintenance?: boolean; autoReturn?: boolean; role: "" | "primary" | "secondary"; peerHost: string; peerName: string; vip: string; interface: string; vrid: number; interval: number; lastSync: string; lastResult: string; pairedAt: string };
type Sec = { host: string; name: string; priority: number; pathMap?: Record<string, string>; lastSync: string; lastResult: string; state: string };
type Cont = { pending: number; last?: string; error?: string; watchLimit?: boolean; active: boolean };
type Data = { installed: boolean; config: Config; continuous?: Cont; secondaries?: Sec[]; state: string; pairingCode?: string; pairingExpires?: string; interfaces?: Record<string, string> };

/** Volume mapping: the volume of the secondary for each volume of this NAS. */
function VolumeMap({ value, onChange }: { value: Record<string, string>; onChange: (m: Record<string, string>) => void }) {
  const locs = useQuery({ queryKey: ["locations"], queryFn: () => api.get<{ path: string; label: string }[]>("/api/storage/locations") });
  const vols = (locs.data ?? []).filter((l) => l.path.split("/").length === 4); // /srv/strata/<volume>
  if (!vols.length) return null;
  return (
    <div className="space-y-2">
      <div className="text-muted text-[12.5px]">{tr("Matching volume on the secondary (keep the same name if it exists there):")}</div>
      {vols.map((v) => {
        const name = v.path.split("/").pop()!;
        const cur = (value[v.path] ?? v.path).split("/").pop()!;
        return (
          <div key={v.path} className="flex items-center gap-2 text-[13px]">
            <span className="mono w-40 truncate">{name}</span><span className="text-muted">→</span>
            <Input className="mono max-w-[200px]" value={cur} onChange={(e) => { const n = e.target.value.trim().replace(/[^A-Za-z0-9_.-]/g, ""); const m = { ...value }; if (!n || n === name) delete m[v.path]; else m[v.path] = `/srv/strata/${n}`; onChange(m); }} />
          </div>
        );
      })}
    </div>
  );
}

function Secondaries({ data }: { data: Data }) {
  const { confirm } = useUI();
  const [f, setF] = useState<{ peerHost: string; code: string; pathMap: Record<string, string> } | null>(null);
  const add = useAction(() => api.post("/api/ha/pair", f), { success: tr("Secondary NAS added: replication is starting"), onDone: () => setF(null) });
  const remove = useAction((host: string) => api.del<{ warning?: string } | null>(`/api/ha/secondaries/${encodeURIComponent(host)}`), { success: (r) => r?.warning ?? tr("Secondary NAS removed") });
  const list = data.secondaries ?? [];
  return (
    <Card title={tr("Secondary NAS")} actions={<Button size="sm" icon={<Plus size={14} />} disabled={data.state !== "MASTER"} onClick={() => setF({ peerHost: "", code: "", pathMap: {} })}>{tr("Add a secondary NAS")}</Button>}>
      <div className="space-y-2">
        {list.map((x, i) => (
          <div key={x.host} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
            <div className="min-w-0 flex-1">
              <div><b>{x.name || x.host}</b> <span className="mono text-muted">{x.host}</span> <span className="text-muted">· {tr("failover order {0}", i + 1)}</span></div>
              <div className="text-muted text-[12px]">{x.lastSync && !x.lastSync.startsWith("0001") ? tr("Last replication {0}", ago(x.lastSync)) : tr("Not replicated yet")}{x.lastResult && x.lastResult !== "OK" ? <span className="text-danger"> · {srv(x.lastResult)}</span> : ""}</div>
              {x.pathMap && Object.keys(x.pathMap).length > 0 && <div className="mono text-muted text-[11.5px]">{Object.entries(x.pathMap).map(([a, b]) => `${a.split("/").pop()} → ${b.split("/").pop()}`).join(", ")}</div>}
            </div>
            <Badge tone={x.state === "BACKUP" ? "ok" : x.state === "MASTER" ? "warn" : "danger"} dot>{x.state === "BACKUP" ? tr("Ready") : x.state === "MASTER" ? tr("Active") : tr("Unreachable")}</Badge>
            {list.length > 1 && <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove {0}?", x.name || x.host), text: tr("This NAS no longer receives replications and no longer takes over. Its data stays in place."), danger: true })) remove.mutate(x.host); }} />}
          </div>
        ))}
        <p className="text-muted text-[12px]">{tr("On failure, the first available secondary in failover order takes the shared address.")}</p>
      </div>
      <Modal open={!!f} onClose={() => setF(null)} title={tr("Add a secondary NAS")} footer={<><Button onClick={() => setF(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={add.isPending} disabled={!f?.peerHost || !f.code} onClick={() => add.mutate()}>{tr("Pair")}</Button></>}>
        {f && <div className="space-y-4">
          <Notice>{tr("On the new NAS, open High availability and display a pairing code.")}</Notice>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Secondary NAS address")}><Input className="mono" value={f.peerHost} onChange={(e) => setF({ ...f, peerHost: e.target.value.trim() })} placeholder="192.168.1.22" /></Field>
            <Field label={tr("Pairing code")}><Input className="mono uppercase" value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} placeholder="ABCDE-FGHIJ" /></Field>
          </div>
          <VolumeMap value={f.pathMap} onChange={(m) => setF({ ...f, pathMap: m })} />
          <ErrorBox error={add.error} />
        </div>}
      </Modal>
    </Card>
  );
}

export default function HighAvailability() {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["ha"], queryFn: () => api.get<Data>("/api/ha"), refetchInterval: 3000 });
  const install = useAction(() => api.post("/api/system/components/ha/install"));
  const prepare = useAction(() => api.post("/api/ha/prepare"));
  const [f, setF] = useState<{ peerHost: string; vip: string; interface: string; interval: number; code: string; continuous: boolean; pathMap: Record<string, string> }>({ peerHost: "", vip: "", interface: "", interval: 15, code: "", continuous: true, pathMap: {} });
  const [mode, setMode] = useState<{ continuous: boolean; interval: number } | null>(null);
  const saveMode = useAction(() => api.put("/api/ha/settings", mode), { success: tr("Replication mode saved"), onDone: () => setMode(null) });
  const pair = useAction(() => api.post("/api/ha/pair", f), { success: tr("NAS paired: the first replication is starting") });
  const sync = useAction(() => api.post("/api/ha/sync"));
  const back = useAction(() => api.post("/api/ha/switchback"));
  const maint = useAction(() => api.post("/api/ha/maintenance"));
  const endMaint = useAction(() => api.post("/api/ha/maintenance/end"), { success: tr("Maintenance finished: control automatically returns to this NAS") });
  const unpair = useAction(() => api.post<{ warning?: string } | null>("/api/ha/unpair"), { success: (r) => r?.warning ?? tr("Pairing removed") });
  useEffect(() => {
    if (data?.interfaces && !f.interface) {
      const first = Object.keys(data.interfaces)[0];
      if (first) setF((x) => ({ ...x, interface: first }));
    }
  }, [data, f.interface]);

  const header = <PageHeader title={tr("High availability")} subtitle={tr("Two NAS, one address: the secondary takes over if the primary fails")} icon={<ServerCog size={20} />} />;
  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) {
    return <div>{header}<Card><div className="flex flex-wrap items-center justify-between gap-3"><div className="text-muted text-[13px]">{tr("The components (keepalived, rsync) are not installed. Install them on both NAS.")}</div><Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button></div></Card></div>;
  }
  const c = data.config;
  const active = data.state === "MASTER";

  if (c.role) {
    return (
      <div>
        {header}
        <ErrorBox error={sync.error || back.error || unpair.error || maint.error || endMaint.error} />
        <div className="space-y-5">
          <Card title={tr("Status")} actions={<Badge tone={active ? "ok" : "neutral"} dot>{active ? tr("Active: serving address {0}", c.vip) : data.state === "FAULT" ? tr("Faulty") : tr("Pending")}</Badge>}>
            <div className="grid gap-4 text-[13px] sm:grid-cols-3">
              <div><div className="text-muted text-[12px]">{tr("Role of this NAS")}</div><b>{c.role === "primary" ? tr("Primary") : tr("Secondary")}</b></div>
              <div><div className="text-muted text-[12px]">{tr("Other NAS")}</div><div className="mono">{c.peerName} ({c.peerHost})</div></div>
              <div><div className="text-muted text-[12px]">{tr("Shared address")}</div><div className="mono">{c.vip} · {c.interface}</div></div>
              <div><div className="text-muted text-[12px]">{tr("Last replication")}</div><div>{c.lastSync && !c.lastSync.startsWith("0001") ? `${dateTime(c.lastSync)} (${ago(c.lastSync)})` : "—"}</div></div>
              <div className="sm:col-span-2"><div className="text-muted text-[12px]">{tr("Result")}</div><div className={c.lastResult && c.lastResult !== "OK" ? "text-danger" : ""}>{c.lastResult ? srv(c.lastResult) : "—"}</div></div>
              {c.role === "primary" && c.continuous && data.continuous && (
                <div className="sm:col-span-3">
                  <div className="text-muted text-[12px]">{tr("Continuous replication")}</div>
                  {!data.continuous.active ? <div className="text-muted">{tr("Paused (this NAS is not serving the shared address)")}</div>
                    : data.continuous.error ? <div className="text-danger">{srv(data.continuous.error)}</div>
                    : data.continuous.pending > 0 ? <div>{tr("{0} folder(s) being copied", data.continuous.pending)}</div>
                    : <div className="text-ok">{tr("Up to date")}{data.continuous.last && !data.continuous.last.startsWith("0001") ? ` · ${tr("last copy {0}", ago(data.continuous.last))}` : ""}</div>}
                  {data.continuous.watchLimit && <div className="text-warn text-[12px]">{tr("Too many folders to watch: some are only copied by the full replication.")}</div>}
                </div>
              )}
            </div>
            {c.role === "secondary" && active && !c.autoReturn && <div className="mt-4"><Notice tone="warn">{tr("This secondary NAS is currently serving the shares: the primary stopped responding. Once the primary is repaired and restarted, hand control back to it: changes made here will be copied to it first.")}</Notice></div>}
            {c.role === "primary" && !active && c.maintenance && <div className="mt-4"><Notice>{tr("Maintenance in progress: the secondary is serving the shares. You can restart or shut down this NAS; when it returns, control comes back to it automatically after changes are synced back.")}</Notice></div>}
            {c.role === "secondary" && active && c.autoReturn && <div className="mt-4"><Notice>{tr("Primary maintenance: this NAS is serving the shares and will hand control back automatically as soon as the primary returns.")}</Notice></div>}
            {c.role === "primary" && !active && !c.maintenance && <div className="mt-4"><Notice tone="warn">{tr("The secondary has been serving the shares since this NAS failed. Hand control back from the secondary so that this NAS resumes its role (its data will be updated).")}</Notice></div>}
            <div className="mt-5 flex flex-wrap gap-2">
              {c.role === "primary" && <Button icon={<RefreshCcw size={15} />} disabled={!active} loading={sync.isPending} onClick={() => sync.mutate()}>{tr("Replicate now")}</Button>}
              {c.role === "primary" && active && <Button icon={<Wrench size={15} />} loading={maint.isPending} onClick={async () => { if (await confirm({ title: tr("Enter maintenance mode?"), text: tr("A final replication is performed, then the secondary takes over. You can restart this NAS; when it returns, control comes back to it automatically."), confirm: tr("Enter maintenance") })) maint.mutate(); }}>{tr("Maintenance")}</Button>}
              {c.role === "primary" && !active && c.maintenance && <Button variant="primary" icon={<ArrowLeftRight size={15} />} loading={endMaint.isPending} onClick={() => endMaint.mutate()}>{tr("End maintenance")}</Button>}
              {c.role === "secondary" && <Button variant="primary" icon={<ArrowLeftRight size={15} />} disabled={!active} loading={back.isPending} onClick={async () => { if (await confirm({ title: tr("Hand control back to the primary?"), text: tr("Files modified here are copied to the primary, then the shared address returns to it. Clients may lose connection for a few seconds."), confirm: tr("Hand control back") })) back.mutate(); }}>{tr("Hand control back to the primary")}</Button>}
              <div className="flex-1" />
              <Button variant="danger" icon={<Unlink size={15} />} onClick={async () => { if (await confirm({ title: tr("Remove the pairing?"), text: tr("The two NAS become independent again; the shared address is no longer served. Data already replicated stays on the secondary."), danger: true })) unpair.mutate(); }}>{tr("Unpair")}</Button>
            </div>
          </Card>
          {c.role === "primary" && <Secondaries data={data} />}
          {c.role === "primary" && (
            <Card title={tr("Replication mode")}>
              <div className="space-y-4">
                <Switch checked={(mode ?? c).continuous ?? false} onChange={(v) => setMode({ continuous: v, interval: (mode ?? c).interval })} label={tr("Continuous replication")} hint={tr("Modified folders are copied to the secondary a few seconds after each change: in case of failure, almost nothing is lost.")} />
                <Field label={tr("Full replication every (minutes)")} hint={tr("Accounts, shares and verification of all files")}><Input type="number" min={1} className="max-w-[160px]" value={(mode ?? c).interval} onChange={(e) => setMode({ continuous: (mode ?? c).continuous ?? false, interval: Math.max(1, +e.target.value) })} /></Field>
                {mode && <div className="flex justify-end"><Button variant="primary" loading={saveMode.isPending} onClick={() => saveMode.mutate()}>{tr("Save")}</Button></div>}
                <ErrorBox error={saveMode.error} />
              </div>
            </Card>
          )}
          <Card title={tr("How it works")}>
            <ul className="text-muted list-disc space-y-1 pl-5 text-[13px]">
              <li>{tr("Clients connect to the shared address {0} (SMB, NFS, WebDAV, web interface).", c.vip.split("/")[0])}</li>
              <li>{c.continuous ? tr("Every change is copied to the secondary within seconds; every {0} minutes, a full replication also checks accounts and all files.", c.interval) : tr("Every {0} minutes, the primary copies accounts, shared folders and their files to the secondary.", c.interval)}</li>
              <li>{tr("If the primary does not respond for a few seconds, the secondary takes the address and publishes the shares, with data from the last replication.")}</li>
              <li>{tr("The primary never takes control back on its own: this avoids overwriting files modified on the secondary during the outage.")}</li>
            </ul>
          </Card>
        </div>
      </div>
    );
  }

  return (
    <div>
      {header}
      <ErrorBox error={pair.error || prepare.error} />
      <div className="mb-5"><Notice>{tr("At least two StrataOS NAS on the same local network are required. Prepare the secondary first to get a pairing code, then enter it on the primary; if its volumes have different names, specify the mapping. More secondaries can be added later.")}</Notice></div>
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Server size={16} className="text-accent" />{tr("This NAS will be the primary")}</span>}>
          <div className="space-y-4">
            <Field label={tr("Secondary NAS address")}><Input className="mono" value={f.peerHost} onChange={(e) => setF({ ...f, peerHost: e.target.value.trim() })} placeholder="192.168.1.21" /></Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Shared address")} hint={tr("Free on the network, with its prefix")}><Input className="mono" value={f.vip} onChange={(e) => setF({ ...f, vip: e.target.value.trim() })} placeholder="192.168.1.20/24" /></Field>
              <Field label={tr("Interface")}>
                <Select value={f.interface} onChange={(e) => setF({ ...f, interface: e.target.value })}>
                  {Object.entries(data.interfaces ?? {}).map(([n, ip]) => <option key={n} value={n}>{n} ({ip})</option>)}
                </Select>
              </Field>
              <Field label={tr("Replicate every (minutes)")}><Input type="number" min={1} value={f.interval} onChange={(e) => setF({ ...f, interval: Math.max(1, +e.target.value) })} /></Field>
              <Field label={tr("Pairing code")} hint={tr("Shown on the secondary")}><Input className="mono uppercase" value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} placeholder="ABCDE-FGHIJ" /></Field>
            </div>
            <VolumeMap value={f.pathMap} onChange={(m) => setF({ ...f, pathMap: m })} />
            <Switch checked={f.continuous} onChange={(v) => setF({ ...f, continuous: v })} label={tr("Continuous replication")} hint={tr("Copies every change within seconds, in addition to full replication.")} />
            <div className="flex justify-end"><Button variant="primary" icon={<KeyRound size={15} />} loading={pair.isPending} disabled={!f.peerHost || !f.vip || !f.code} onClick={() => pair.mutate()}>{tr("Pair")}</Button></div>
          </div>
        </Card>
        <Card title={<span className="flex items-center gap-2"><ServerCog size={16} className="text-accent" />{tr("This NAS will be the secondary")}</span>}>
          <div className="space-y-4 text-[13px]">
            <p className="text-muted">{tr("Its shared folders will be replaced by those of the primary. It only publishes them during a primary outage.")}</p>
            {data.pairingCode ? (
              <div className="space-y-2">
                <div className="text-muted text-[12px]">{tr("Code to enter on the primary (valid until {0})", new Date(data.pairingExpires!).toLocaleTimeString())}</div>
                <div className="flex items-center gap-2"><code className="mono rounded-lg bg-panel-2 px-3 py-2 text-[18px] tracking-widest">{data.pairingCode}</code><Button size="sm" icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(data.pairingCode!); toast(tr("Code copied")); }}>{tr("Copy")}</Button></div>
                <div className="text-muted text-[12px]">{tr("Addresses of this NAS: {0}", Object.values(data.interfaces ?? {}).map((x) => x.split("/")[0]).join(", "))}</div>
              </div>
            ) : (
              <Button icon={<KeyRound size={15} />} loading={prepare.isPending} onClick={() => prepare.mutate()}>{tr("Show a pairing code")}</Button>
            )}
          </div>
        </Card>
      </div>
    </div>
  );
}
