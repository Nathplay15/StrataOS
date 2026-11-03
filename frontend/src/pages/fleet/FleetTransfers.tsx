import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRightLeft, Box, Monitor, Play, RotateCcw, Trash2, X } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, Progress, Select, Spinner, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";
import type { HealthRow } from "./FleetHealth";

type Obj = { name: string; state: string; bridges: string[]; ceph?: boolean; template?: boolean; location?: string; size?: number };
type Volume = { path: string; free: number };
type Inventory = { name: string; libvirt: boolean; lxc: boolean; vms: Obj[]; containers: Obj[]; bridges: string[]; volumes: Volume[] };
type Issue = { text: string; bridge?: string };
type Plan = { issues: Issue[]; object?: Obj; bridges: string[]; volumes: Volume[] };
type Transfer = {
  id: string; kind: "vm" | "lxc"; name: string; newName: string; from: string; to: string; fromName: string; toName: string; mode: string; move?: boolean;
  storage: string; bridges?: Record<string, string>; start?: boolean; user: string; status: string; stage: string; progress: number; bytes: number; total: number;
  error?: string; started: string; finished?: string;
};
type Form = { kind: "vm" | "lxc"; from: string; to: string; name: string; newName: string; mode: "stop" | "snapshot"; move: boolean; volume: string; folder: string; bridges: Record<string, string>; start: boolean };

const stageLabel: Record<string, string> = {
  export: tr("Preparing the copy"), transfer: tr("Sending"), restore: tr("Verifying and restoring"), finish: tr("Finishing"),
};

function TransferRow({ t, onResume, onCancel, onRemove }: { t: Transfer; onResume: () => void; onCancel: () => void; onRemove: () => void }) {
  const running = t.status === "running";
  return (
    <div className="rounded-xl border border-line p-4">
      <div className="flex flex-wrap items-center gap-2">
        {t.kind === "vm" ? <Monitor size={15} /> : <Box size={15} />}
        <span className="font-medium">{t.name}{t.newName !== t.name && <span className="text-muted"> → {t.newName}</span>}</span>
        <span className="text-muted text-[12.5px]">{t.fromName} → {t.toName}</span>
        <Badge>{t.move ? tr("Move") : tr("Copy")}</Badge>
        {running ? <Badge tone="accent" dot>{stageLabel[t.stage] ?? t.stage}</Badge>
          : t.status === "success" ? <Badge tone="ok" dot>{tr("Done")}</Badge>
          : t.status === "cancelled" ? <Badge tone="neutral" dot>{tr("Cancelled")}</Badge>
          : <Badge tone="danger" dot>{tr("Failed")}</Badge>}
        <span className="text-muted text-[12px]">{dateTime(t.started)}{t.user ? ` · ${t.user}` : ""}</span>
        <span className="flex-1" />
        {t.status === "failed" && <Button size="sm" icon={<RotateCcw size={14} />} onClick={onResume}>{tr("Resume")}</Button>}
        {(running || t.status === "failed") && <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={onCancel}>{tr("Cancel")}</Button>}
        {!running && t.status !== "failed" && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={onRemove} />}
      </div>
      {(running || t.status === "failed") && <div className="mt-3">
        <Progress value={t.progress} tone={t.status === "failed" ? "danger" : "accent"} />
        <div className="mt-1 flex justify-between text-muted text-[12px]">
          <span>{t.total > 0 && `${bytes(t.bytes)} / ${bytes(t.total)}`}</span>
          <span>{Math.round(t.progress)} %</span>
        </div>
      </div>}
      {t.error && <div className="mt-2 break-words text-danger text-[12.5px]">{srv(t.error)}{t.status === "failed" && <span className="ml-1 text-muted">{tr("The data already sent is kept: Resume continues from there. Cancel puts everything back (a moved VM starts again on its NAS).")}</span>}</div>}
      {t.finished && t.status === "success" && <div className="mt-1 text-muted text-[12px]">{tr("Finished {0}", ago(t.finished))}</div>}
    </div>
  );
}

function CopyDialog({ nas, onClose }: { nas: HealthRow[]; onClose: () => void }) {
  const [f, setF] = useState<Form>({ kind: "vm", from: "local", to: nas.find((n) => n.id !== "local")?.id ?? "", name: "", newName: "", mode: "stop", move: false, volume: "", folder: "vms", bridges: {}, start: false });
  const src = useQuery({ queryKey: ["fleet-xfer-inv", f.from], queryFn: () => api.get<Inventory>(`/api/fleet/xfer/inventory?nas=${encodeURIComponent(f.from)}`), enabled: !!f.from });
  const list = (f.kind === "vm" ? src.data?.vms.filter((v) => !v.ceph) : src.data?.containers) ?? [];
  const storage = f.kind === "vm" ? (f.volume ? `${f.volume}/${f.folder.replace(/^\/+|\/+$/g, "")}`.replace(/\/$/, "") : "") : f.volume;
  const body = { from: f.from, to: f.to, kind: f.kind, name: f.name, newName: f.newName || f.name, mode: f.move ? "stop" : f.mode, move: f.move, storage, bridges: f.bridges, start: f.start };
  const plan = useQuery({
    queryKey: ["fleet-xfer-plan", body], queryFn: () => api.post<Plan>("/api/fleet/transfers/plan", body),
    enabled: !!f.name && !!f.to && f.from !== f.to, retry: false,
  });
  const start = useAction(() => api.post<Transfer>("/api/fleet/transfers", body), { success: tr("Transfer started"), onDone: onClose });
  useEffect(() => {
    if (!f.volume && plan.data?.volumes.length) setF((x) => ({ ...x, volume: plan.data!.volumes[0].path }));
  }, [plan.data]); // eslint-disable-line react-hooks/exhaustive-deps
  const obj = list.find((o) => o.name === f.name);
  const running = obj && (obj.state === "running" || obj.state === "paused");
  const issues = plan.data?.issues ?? [];
  const vol = plan.data?.volumes.find((v) => v.path === f.volume);
  const set = (p: Partial<Form>) => setF({ ...f, ...p });

  return (
    <Modal open onClose={onClose} wide title={tr("Copy a VM or container to another NAS")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={start.isPending} disabled={!f.name || !storage || !plan.data || issues.length > 0} onClick={() => start.mutate()}>{f.move ? tr("Move") : tr("Copy")}</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={tr("What")}>
            <Select value={f.kind} onChange={(e) => set({ kind: e.target.value as Form["kind"], name: "", newName: "", bridges: {} })}>
              <option value="vm">{tr("Virtual machine")}</option>
              <option value="lxc">{tr("Container (LXC)")}</option>
            </Select>
          </Field>
          <Field label={tr("From")}>
            <Select value={f.from} onChange={(e) => set({ from: e.target.value, name: "", newName: "", bridges: {} })}>
              {nas.map((n) => <option key={n.id} value={n.id}>{n.health?.hostname || n.name}{n.id === "local" ? ` ${tr("(this NAS)")}` : ""}</option>)}
            </Select>
          </Field>
          <Field label={tr("To")}>
            <Select value={f.to} onChange={(e) => set({ to: e.target.value, volume: "", bridges: {} })}>
              <option value="">—</option>
              {nas.filter((n) => n.id !== f.from).map((n) => <option key={n.id} value={n.id}>{n.health?.hostname || n.name}{n.id === "local" ? ` ${tr("(this NAS)")}` : ""}</option>)}
            </Select>
          </Field>
        </div>
        <ErrorBox error={src.error} />
        {src.isLoading ? <Spinner /> : (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={f.kind === "vm" ? tr("Virtual machine") : tr("Container")}>
              <Select value={f.name} onChange={(e) => set({ name: e.target.value, newName: e.target.value, bridges: {} })}>
                <option value="">{list.length ? tr("Choose…") : tr("None on this NAS")}</option>
                {list.map((o) => <option key={o.name} value={o.name}>{o.name} ({srv(o.state)}){o.size ? ` · ${bytes(o.size)}` : ""}</option>)}
              </Select>
            </Field>
            <Field label={tr("Name on the destination")}><Input value={f.newName} onChange={(e) => set({ newName: e.target.value.trim() })} placeholder={f.name} /></Field>
          </div>
        )}
        {f.name && <>
          <Checkbox checked={f.move} onChange={(v) => set({ move: v, mode: v ? "stop" : f.mode })} label={tr("Move: delete it from {0} once the copy runs on the destination", src.data?.name ?? "")} />
          {running && !f.move && (
            <Field label={tr("While it runs")} hint={f.mode === "stop" ? tr("It is shut down for the copy, then started again; the copy is exactly the stopped state.") : tr("It keeps running: a temporary snapshot is copied (file systems frozen by the guest agent when it answers, otherwise like a power cut).")}>
              <Select value={f.mode} onChange={(e) => set({ mode: e.target.value as Form["mode"] })}>
                <option value="stop">{tr("Stop it during the copy")}</option>
                <option value="snapshot">{tr("Copy it live from a snapshot")}</option>
              </Select>
            </Field>
          )}
          {running && f.move && <Notice tone="warn">{tr("It is shut down for the move and stays stopped here. If the transfer is cancelled, it starts again here.")}</Notice>}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Volume on the destination")} hint={vol ? tr("{0} free", bytes(vol.free)) : undefined}>
              <Select value={f.volume} onChange={(e) => set({ volume: e.target.value })}>
                {!plan.data?.volumes.length && <option value="">—</option>}
                {plan.data?.volumes.map((v) => <option key={v.path} value={v.path}>{v.path}</option>)}
              </Select>
            </Field>
            {f.kind === "vm" && <Field label={tr("Folder in this volume")} hint={storage ? tr("Disks in {0}", `${storage}/${f.newName || f.name}`) : undefined}><Input value={f.folder} onChange={(e) => set({ folder: e.target.value })} /></Field>}
          </div>
          {[...new Set([...issues.filter((i) => i.bridge).map((i) => i.bridge!), ...Object.keys(f.bridges)])].map((b) => (
            <Field key={b} label={tr("Network: bridge {0}", b)} hint={srv(issues.find((i) => i.bridge === b)?.text ?? "")}>
              <Select value={f.bridges[b] ?? ""} onChange={(e) => set({ bridges: { ...f.bridges, [b]: e.target.value } })}>
                <option value="">{tr("Choose a bridge of the destination…")}</option>
                {plan.data?.bridges.map((x) => <option key={x} value={x}>{x}</option>)}
                {f.kind === "lxc" && <option value="nat">{tr("NAT (private network of the NAS)")}</option>}
              </Select>
            </Field>
          ))}
          <Checkbox checked={f.start} onChange={(v) => set({ start: v })} label={tr("Start it on the destination")} />
          {!f.move && <p className="text-muted text-[12px]">{f.kind === "vm" ? tr("The copy gets new MAC addresses and a new identity, so both can run on the same network.") : tr("The copy gets a new MAC address, so both can run on the same network.")}</p>}
          {plan.isFetching && <Spinner />}
          <ErrorBox error={plan.error} />
          {issues.filter((i) => !i.bridge).map((i, n) => <Notice key={n} tone="warn">{srv(i.text)}</Notice>)}
          <p className="text-muted text-[12px]">{tr("The data goes compressed over the link of the console and is checked with SHA-256 on arrival; an interrupted transfer resumes where it stopped. The source and the destination each need room for one copy.")}</p>
        </>}
        <ErrorBox error={start.error} />
      </div>
    </Modal>
  );
}

/** VM and container transfers between the NAS of the console. */
export function FleetTransfers({ nas }: { nas: HealthRow[] }) {
  const { confirm } = useUI();
  const q = useQuery({ queryKey: ["fleet-transfers"], queryFn: () => api.get<Transfer[]>("/api/fleet/transfers"), refetchInterval: (x) => (x.state.data?.some((t) => t.status === "running") ? 2000 : 20000) });
  const [copy, setCopy] = useState(false);
  const resume = useAction((id: string) => api.post(`/api/fleet/transfers/${id}/resume`), { success: tr("Transfer resumed") });
  const cancel = useAction((id: string) => api.post(`/api/fleet/transfers/${id}/cancel`));
  const remove = useAction((id: string) => api.del(`/api/fleet/transfers/${id}`));
  // NAS that allow it (this one always).
  const able = nas.filter((n) => n.id === "local" || (n.online && n.health?.access?.vms));
  const others = nas.filter((n) => n.id !== "local" && n.online && !n.health?.access?.vms);

  return (
    <Card title={<span className="flex items-center gap-2"><ArrowRightLeft size={16} />{tr("VM and container transfers")}</span>}
      actions={<Button size="sm" variant="primary" icon={<Play size={14} />} disabled={able.length < 2} onClick={() => setCopy(true)}>{tr("Copy or move…")}</Button>}>
      <ErrorBox error={q.error || resume.error || cancel.error || remove.error} />
      {others.length > 0 && <p className="mb-3 text-muted text-[12.5px]">{tr("Not allowed by: {0}. On those NAS, open Multi-NAS Console → Authorized access and allow this console to copy virtual machines and containers.", others.map((n) => n.health?.hostname || n.name).join(", "))}</p>}
      {q.isLoading ? <Spinner /> : !q.data?.length ? <Empty icon={<ArrowRightLeft />} title={tr("No transfer yet")} text={tr("Copy or move a virtual machine or a container to another NAS of the console, without a cluster.")} /> : (
        <div className="space-y-3">{q.data.map((t) => <TransferRow key={t.id} t={t} onResume={() => resume.mutate(t.id)} onRemove={() => remove.mutate(t.id)}
          onCancel={async () => { if (await confirm({ title: tr("Cancel this transfer?"), text: tr("The copy is removed from both NAS. A moved VM or container starts again on its NAS."), danger: true })) cancel.mutate(t.id); }} />)}</div>
      )}
      {copy && <CopyDialog nas={able} onClose={() => setCopy(false)} />}
    </Card>
  );
}
