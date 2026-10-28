import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, Boxes, Cable, Copy, Database, HardDrive, KeyRound, Network, Plug, Plus, Server, Trash2, Unlink, Wrench } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, cx, pct, rate } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Stat, Switch, Tabs, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

/* ---------- Types (backend: h_cluster.go, h_ceph.go, h_ceph_storage.go) ---------- */

type MemberInfo = { cephInstalled: boolean; cephVersion?: string; cephNode: boolean; memMb: number; daemons?: string[]; cephUse?: string[]; addr?: string };
type Member = { id: string; name: string; host: string; addr?: string; privAddr?: string; roles?: string[]; self: boolean; online: boolean; error?: string; info?: MemberInfo };
type ClusterInfo = { id: string; name: string; self: string; publicNet: string; privateNet?: string };
type ClusterData = { hostname: string; networks: string[]; cephInstalled: boolean; nameProblem?: string; pairingCode?: string; pairingExpires?: string; cluster: ClusterInfo | null; members: Member[] };
type Check = { code: string; severity: string; message: string; explain?: string; muted?: boolean };
type Status = {
  fsid: string; health: { status: string; checks: Check[] }; quorum: string[]; mons: number; osds: number; osdsUp: number; osdsIn: number; pgs: number;
  pgStates: { state: string; count: number }[]; pools: number; objects: number; used: number; avail: number; total: number;
  readBps: number; writeBps: number; readOps: number; writeOps: number; recoveringBps: number; degradedRatio: number; misplacedRatio: number;
  mgrAvailable: boolean; mds: { name: string; status: string }[]; mdsStandbys: number; progress: { message: string; progress: number }[];
};
type OSD = { id: number; name: string; host: string; class: string; status: string; in: boolean; size: number; used: number; utilization: number; pgs: number; device?: string };
type Daemon = { kind: string; name: string; addr?: string; status: string };
type Pool = { name: string; size: number; minSize: number; pgs: number; autoscale: string; apps: string[]; stored: number; used: number; maxAvail: number; objects: number; percentUsed: number };
type FS = { name: string; metadataPool: string; dataPools: string[] };
type Local = { fsid: string; mons: { name: string; addr: string }[]; test?: boolean; size: number; minSize: number; smallMemory?: boolean; publicNet: string };
type CephData = { installed: boolean; clientInstalled: boolean; version: string; cluster: ClusterInfo | null; local: Local | null; status?: Status; osds?: OSD[]; daemons?: Daemon[]; pools?: Pool[]; filesystems?: FS[]; noout?: boolean; clients?: { name: string; caps: Record<string, string> }[]; error?: string };
type Disk = { path: string; name: string; model: string; serial: string; size: number; kind: string; usage: string; usedBy?: string; eligible: boolean; erase?: boolean; reason?: string };
type DiskRow = { member: string; name: string; disks: Disk[]; error?: string };
type Source = { id: string; name: string; fsid: string; mons: string[]; user: string; local: boolean };
type CephVol = { volumeId: string; cluster: string; kind: string; fs?: string; path?: string; pool?: string; image?: string; name: string; mounted: boolean; usage: { total: number; used: number } };
type VMPool = { name: string; cluster: string; pool: string; user: string; active: boolean };
type Storage = { sources: Source[]; volumes: CephVol[]; vmPools: VMPool[]; vmsInstalled: boolean; clientInstalled: boolean };
type Probe = { fsid: string; health?: string; pools: Pool[]; filesystems: FS[]; poolsError?: string };

const healthTone = (h?: string) => (h === "HEALTH_OK" ? "ok" : h === "HEALTH_WARN" ? "warn" : "danger");
const healthLabel = (h?: string) => (h === "HEALTH_OK" ? tr("Healthy") : h === "HEALTH_WARN" ? tr("Warning") : h === "HEALTH_ERR" ? tr("Error") : tr("Unknown"));
const roleLabel: Record<string, string> = { mon: tr("monitor"), mgr: tr("manager"), mds: tr("metadata"), osd: tr("disks") };

/* ---------- Cluster of NAS and creation wizard ---------- */

function CreateWizard({ cl, onClose }: { cl: ClusterData; onClose: () => void }) {
  const { confirm } = useUI();
  const [step, setStep] = useState(cl.cluster ? 1 : 0);
  const [c, setC] = useState({ name: "Ceph", publicNet: cl.networks.find((n) => !n.startsWith("10.0.2.")) ?? cl.networks[0] ?? "", privateNet: "" });
  const [add, setAdd] = useState({ host: "", code: "" });
  const [test, setTest] = useState(false);
  const [sel, setSel] = useState<Record<string, string[]>>({});
  const [pools, setPools] = useState({ rbd: true, cephfs: true, size: 3, minSize: 2 });
  const create = useAction(() => api.post("/api/cluster", c), { onDone: () => setStep(1) });
  const join = useAction(() => api.post<{ warning?: string }>("/api/cluster/members", add), { success: (r) => r.warning ?? tr("NAS added to the cluster"), onDone: () => setAdd({ host: "", code: "" }) });
  const disks = useQuery({ queryKey: ["ceph-disks"], queryFn: () => api.get<DiskRow[]>("/api/ceph/disks"), enabled: step === 2 });
  const boot = useAction(() => api.post("/api/ceph/bootstrap", { test, disks: sel, erase: true, rbd: pools.rbd, cephfs: pools.cephfs, size: n < 3 ? 0 : pools.size, minSize: n < 3 ? 0 : pools.minSize }), { onDone: onClose });
  const n = cl.members.length;
  const chosen = Object.values(sel).reduce((a, l) => a + l.length, 0);
  const missing = cl.members.filter((m) => !m.online || !m.info?.cephInstalled);
  const toggle = (m: string, p: string) => setSel((s) => ({ ...s, [m]: (s[m] ?? []).includes(p) ? s[m].filter((x) => x !== p) : [...(s[m] ?? []), p] }));
  const start = async () => {
    const names = cl.members.flatMap((m) => (sel[m.id] ?? []).map((p) => `${m.name}:${p}`)).join(", ");
    if (await confirm({ title: tr("Create the Ceph cluster?"), text: <>{tr("Every selected disk is erased and given to Ceph: {0}.", names)}<br />{tr("Type the name of the cluster to confirm.")}</>, danger: true, typeToConfirm: cl.cluster?.name ?? "", confirm: tr("Erase the disks and create") })) boot.mutate();
  };
  const steps = [tr("Cluster"), tr("NAS"), tr("Disks and pools")];
  return (
    <Modal open onClose={onClose} wide title={tr("Create a Ceph cluster")} footer={<>
      <Button onClick={onClose}>{tr("Close")}</Button>
      {step === 0 && <Button variant="primary" loading={create.isPending} disabled={!c.name || !c.publicNet} onClick={() => create.mutate()}>{tr("Create the cluster of NAS")}</Button>}
      {step === 1 && <Button variant="primary" disabled={(n < 3 && !test) || missing.length > 0} onClick={() => setStep(2)}>{tr("Next")}</Button>}
      {step === 2 && <><Button onClick={() => setStep(1)}>{tr("Back")}</Button><Button variant="danger" loading={boot.isPending} disabled={!chosen || (!pools.rbd && !pools.cephfs)} onClick={start}>{tr("Create Ceph")}</Button></>}
    </>}>
      <div className="mb-5 flex gap-2 text-[12.5px]">
        {steps.map((s, i) => <span key={s} className={cx("rounded-full border px-3 py-1", i === step ? "border-accent bg-accent-soft text-accent" : "border-line text-muted")}>{i + 1}. {s}</span>)}
      </div>
      <ErrorBox error={create.error || join.error || boot.error} />
      {step === 0 && (
        <div className="space-y-4">
          <Notice>{tr("Ceph spreads the data over the disks of several NAS and keeps copies on different NAS: one NAS can stop without losing data or access. It needs 3 NAS at least, a fast network (10 Gb/s recommended) and about 4 GB of RAM per disk given to Ceph.")}</Notice>
          {cl.nameProblem && <Notice tone="warn">{srv(cl.nameProblem)}</Notice>}
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={tr("Name")}><Input value={c.name} onChange={(e) => setC({ ...c, name: e.target.value })} /></Field>
            <Field label={tr("Public network")} hint={tr("Where the NAS and the clients reach Ceph")}>
              <Select value={c.publicNet} onChange={(e) => setC({ ...c, publicNet: e.target.value })}>{cl.networks.map((x) => <option key={x}>{x}</option>)}</Select>
            </Field>
            <Field label={tr("Cluster network (optional)")} hint={tr("A separate network for the copies between NAS")}>
              <Select value={c.privateNet} onChange={(e) => setC({ ...c, privateNet: e.target.value })}><option value="">{tr("Same as the public network")}</option>{cl.networks.filter((x) => x !== c.publicNet).map((x) => <option key={x}>{x}</option>)}</Select>
            </Field>
          </div>
        </div>
      )}
      {step === 1 && (
        <div className="space-y-4">
          <Members cl={cl} compact />
          <Notice>{tr("On each other NAS: install the Ceph component, open Storage → Ceph and choose \"Join a cluster\" to show a code. Then enter its address and code here.")}</Notice>
          <div className="grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
            <Field label={tr("Address of the NAS")}><Input className="mono" value={add.host} onChange={(e) => setAdd({ ...add, host: e.target.value.trim() })} placeholder="192.168.10.12" /></Field>
            <Field label={tr("Pairing code")}><Input className="mono uppercase" value={add.code} onChange={(e) => setAdd({ ...add, code: e.target.value })} placeholder="ABCDE-FGHIJ" /></Field>
            <Button icon={<Plus size={15} />} loading={join.isPending} disabled={!add.host || !add.code} onClick={() => join.mutate()}>{tr("Add")}</Button>
          </div>
          {missing.length > 0 && <Notice tone="warn">{tr("Not ready: {0} (offline, or the Ceph component is not installed).", missing.map((m) => m.name).join(", "))}</Notice>}
          {n < 3 && (
            <Notice tone="warn">
              <div>{tr("With fewer than 3 NAS there is no fault tolerance: if one NAS stops, the data is unavailable or lost. Only use this to try Ceph.")}</div>
              <div className="mt-2"><Checkbox checked={test} onChange={setTest} label={tr("Test mode: create the cluster anyway")} /></div>
            </Notice>
          )}
        </div>
      )}
      {step === 2 && (
        <div className="space-y-5">
          {disks.isLoading && <Spinner />}
          <ErrorBox error={disks.error} />
          {disks.data?.map((row) => (
            <div key={row.member}>
              <div className="mb-1.5 font-medium text-[13px]">{row.name}</div>
              {row.error && <div className="text-danger text-[12.5px]">{srv(row.error)}</div>}
              <div className="space-y-1.5">
                {row.disks.map((d) => (
                  <label key={d.path} className={cx("flex items-center gap-3 rounded-xl border px-3 py-2 text-[13px]", d.eligible ? "border-line" : "border-line opacity-55")}>
                    <input type="checkbox" disabled={!d.eligible} checked={(sel[row.member] ?? []).includes(d.path)} onChange={() => toggle(row.member, d.path)} />
                    <HardDrive size={15} className="text-muted" />
                    <span className="min-w-0 flex-1"><b>{d.model || d.name}</b> <span className="mono text-muted">{d.path} · {d.serial}</span></span>
                    <span>{bytes(d.size)}</span>
                    {d.eligible ? (d.erase ? <Badge tone="warn">{tr("Has data: erased")}</Badge> : <Badge tone="ok">{tr("Blank")}</Badge>) : <span className="text-muted text-[12px]">{srv(d.reason)}</span>}
                  </label>
                ))}
              </div>
            </div>
          ))}
          <div className="grid gap-4 rounded-xl border border-line p-4 sm:grid-cols-2">
            <Switch checked={pools.rbd} onChange={(v) => setPools({ ...pools, rbd: v })} label={tr("RBD pool \"rbd\"")} hint={tr("Block devices: volumes for one NAS, VM disks")} />
            <Switch checked={pools.cephfs} onChange={(v) => setPools({ ...pools, cephfs: v })} label={tr("CephFS filesystem \"cephfs\"")} hint={tr("Shared filesystem, mounted by several NAS at once")} />
            {n >= 3 && <>
              <Field label={tr("Copies of the data")}><Input type="number" min={2} max={Math.min(n, 7)} value={pools.size} onChange={(e) => setPools({ ...pools, size: +e.target.value, minSize: Math.min(pools.minSize, +e.target.value) })} /></Field>
              <Field label={tr("Copies needed to keep writing")}><Input type="number" min={1} max={pools.size} value={pools.minSize} onChange={(e) => setPools({ ...pools, minSize: +e.target.value })} /></Field>
            </>}
          </div>
          <div className="text-muted text-[12.5px]">{tr("Monitors run on the first 3 NAS, a manager on each. The placement groups are sized automatically (autoscaler).")}</div>
        </div>
      )}
    </Modal>
  );
}

function Members({ cl, compact }: { cl: ClusterData; compact?: boolean }) {
  const { confirm } = useUI();
  const remove = useAction((id: string) => api.del<{ warning?: string } | null>(`/api/cluster/members/${id}`), { success: (r) => r?.warning ?? tr("NAS removed from the cluster") });
  return (
    <div className="space-y-2">
      <ErrorBox error={remove.error} />
      {cl.members.map((m) => (
        <div key={m.id} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
          <Server size={16} className="text-muted" />
          <div className="min-w-0 flex-1">
            <div><b>{m.name}</b> <span className="mono text-muted">{m.addr || m.host}</span>{m.self && <span className="text-muted"> · {tr("this NAS")}</span>}</div>
            {!compact && <div className="text-muted text-[12px]">{m.roles?.length ? m.roles.map((r) => roleLabel[r] ?? r).join(", ") : tr("Not a Ceph node")}{m.info?.memMb ? ` · ${bytes(m.info.memMb * 1048576, 0)} RAM` : ""}{m.info?.cephVersion ? ` · Ceph ${m.info.cephVersion}` : ""}</div>}
            {m.error && <div className="text-danger text-[12px]">{srv(m.error)}</div>}
          </div>
          {m.online ? (m.info?.cephInstalled ? <Badge tone="ok" dot>{tr("Online")}</Badge> : <Badge tone="warn">{tr("Ceph not installed")}</Badge>) : <Badge tone="danger" dot>{tr("Offline")}</Badge>}
          {!m.self && !m.roles?.length && <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove {0} from the cluster?", m.name), text: tr("It no longer takes part in the cluster. Nothing is erased on it."), danger: true })) remove.mutate(m.id); }} />}
        </div>
      ))}
    </div>
  );
}

function JoinCard({ cl }: { cl: ClusterData }) {
  const prepare = useAction(() => api.post("/api/cluster/prepare"));
  return (
    <Card title={tr("Join a cluster")}>
      <div className="space-y-3 text-[13px]">
        <div className="text-muted">{tr("Show a one-time code, then add this NAS from a NAS of the cluster (its address: {0}).", cl.networks.join(", "))}</div>
        {cl.pairingCode ? <div className="mono text-2xl font-semibold tracking-wider">{cl.pairingCode}</div> : <Button icon={<KeyRound size={15} />} loading={prepare.isPending} disabled={!!cl.nameProblem} onClick={() => prepare.mutate()}>{tr("Show a pairing code")}</Button>}
        {cl.pairingCode && <div className="text-muted text-[12px]">{tr("Valid for 15 minutes, once.")}</div>}
        {cl.nameProblem && <Notice tone="warn">{srv(cl.nameProblem)}</Notice>}
        <ErrorBox error={prepare.error} />
      </div>
    </Card>
  );
}

/* ---------- Dashboard ---------- */

function Overview({ d }: { d: CephData }) {
  const { confirm } = useUI();
  const maint = useAction((on: boolean) => api.post("/api/ceph/maintenance", { on }), { success: tr("Saved") });
  const st = d.status;
  if (!st) return <Card><ErrorBox error={d.error ?? tr("The Ceph cluster does not answer")} /></Card>;
  const mons = (d.daemons ?? []).filter((x) => x.kind === "mon");
  const mgrs = (d.daemons ?? []).filter((x) => x.kind === "mgr");
  const mds = (d.daemons ?? []).filter((x) => x.kind === "mds");
  const recovering = st.degradedRatio > 0 || st.misplacedRatio > 0 || st.progress.length > 0;
  return (
    <div className="space-y-5">
      <Card title={tr("Health")} actions={<Badge tone={healthTone(st.health.status)} dot>{healthLabel(st.health.status)}</Badge>}>
        {st.health.checks.length === 0 ? <div className="text-ok text-[13px]">{tr("Everything is fine: all the data has its copies.")}</div> : (
          <div className="space-y-2">
            {st.health.checks.map((c) => (
              <div key={c.code} className={cx("rounded-xl border px-4 py-2.5 text-[13px]", c.severity === "HEALTH_ERR" ? "border-danger/30 bg-danger/5" : "border-warn/30 bg-warn/5")}>
                <div className="flex items-center gap-2"><b className={c.severity === "HEALTH_ERR" ? "text-danger" : "text-warn"}>{c.message}</b><span className="mono text-muted text-[11px]">{c.code}</span>{c.muted && <Badge>{tr("muted")}</Badge>}</div>
                {c.explain && <div className="text-muted mt-0.5">{srv(c.explain)}</div>}
              </div>
            ))}
          </div>
        )}
        {d.local?.test && <div className="mt-3"><Notice tone="warn">{tr("Test cluster: fewer than 3 NAS, no fault tolerance.")}</Notice></div>}
        <div className="mt-4 flex flex-wrap items-center gap-3">
          <Switch checked={!!d.noout} onChange={async (v) => { if (!v || await confirm({ title: tr("Maintenance mode?"), text: tr("Ceph stops moving data when a disk or a NAS disappears, so you can restart NAS without starting a long copy. Turn it off when you are done.") })) maint.mutate(v); }} label={tr("Maintenance mode (noout)")} />
        </div>
      </Card>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label={tr("Capacity")} value={`${bytes(st.used)} / ${bytes(st.total)}`} sub={<Progress value={pct(st.used, st.total)} />} icon={<Database size={18} />} />
        <Stat label={tr("Disks (OSD)")} value={`${st.osdsUp} / ${st.osds}`} sub={tr("{0} in the cluster", st.osdsIn)} icon={<HardDrive size={18} />} tone={st.osdsUp < st.osds ? "warn" : "accent"} />
        <Stat label={tr("Monitors")} value={`${st.quorum.length} / ${st.mons}`} sub={tr("in quorum")} icon={<Server size={18} />} tone={st.quorum.length < st.mons ? "warn" : "accent"} />
        <Stat label={tr("Client traffic")} value={`↓ ${rate(st.readBps)}  ↑ ${rate(st.writeBps)}`} sub={tr("{0} reads/s · {1} writes/s", st.readOps, st.writeOps)} icon={<Activity size={18} />} />
      </div>
      {recovering && (
        <Card title={tr("Recovery")}>
          <div className="space-y-2 text-[13px]">
            {st.degradedRatio > 0 && <div>{tr("Data missing copies: {0}%", (st.degradedRatio * 100).toFixed(1))}</div>}
            {st.misplacedRatio > 0 && <div>{tr("Data being moved: {0}%", (st.misplacedRatio * 100).toFixed(1))}</div>}
            {st.recoveringBps > 0 && <div>{tr("Copying at {0}", rate(st.recoveringBps))}</div>}
            {st.progress.map((p) => <div key={p.message}><div className="text-muted text-[12px]">{p.message}</div><Progress value={p.progress * 100} /></div>)}
          </div>
        </Card>
      )}
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={tr("Placement groups ({0})", st.pgs)}>
          <div className="space-y-1 text-[13px]">
            {st.pgStates.map((p) => <div key={p.state} className="flex justify-between gap-3"><span className={cx("mono", p.state === "active+clean" ? "text-ok" : "text-warn")}>{p.state}</span><span>{p.count}</span></div>)}
          </div>
        </Card>
        <Card title={tr("Services")}>
          <div className="space-y-2 text-[13px]">
            {[[tr("Monitors"), mons], [tr("Managers"), mgrs], [tr("Metadata servers"), mds]].map(([label, list]) => (
              <div key={label as string} className="flex flex-wrap items-center gap-2"><span className="w-36 text-muted">{label as string}</span>
                {(list as Daemon[]).map((x) => <Badge key={x.name} tone={x.status === "up" ? "ok" : x.status === "standby" ? "neutral" : "danger"} dot>{x.name}{x.status === "standby" ? ` (${tr("standby")})` : ""}</Badge>)}
                {(list as Daemon[]).length === 0 && <span className="text-muted">—</span>}
              </div>
            ))}
          </div>
        </Card>
      </div>
    </div>
  );
}

/* ---------- Nodes and disks ---------- */

function Nodes({ d, cl }: { d: CephData; cl: ClusterData }) {
  const { confirm } = useUI();
  const [addDisk, setAddDisk] = useState(false);
  const [replace, setReplace] = useState<OSD | null>(null);
  const [addNode, setAddNode] = useState<Member | null>(null);
  const removeOSD = useAction((id: number) => api.del(`/api/ceph/osds/${id}`));
  const inout = useAction(({ id, a }: { id: number; a: string }) => api.post(`/api/ceph/osds/${id}/${a}`), { success: tr("Saved") });
  const removeNode = useAction((id: string) => api.del(`/api/ceph/nodes/${id}`));
  const osds = d.osds ?? [];
  return (
    <div className="space-y-5">
      <ErrorBox error={removeOSD.error || inout.error || removeNode.error} />
      <Card title={tr("NAS of the cluster")}>
        <div className="space-y-2">
          {cl.members.map((m) => {
            const node = !!m.roles?.length || (m.self && !!d.local);
            const mine = osds.filter((o) => o.host === m.name);
            return (
              <div key={m.id} className="rounded-xl border border-line px-4 py-2.5 text-[13px]">
                <div className="flex flex-wrap items-center gap-3">
                  <Server size={16} className="text-muted" />
                  <div className="min-w-0 flex-1"><b>{m.name}</b> <span className="mono text-muted">{m.addr}</span> <span className="text-muted">· {m.roles?.length ? m.roles.map((r) => roleLabel[r] ?? r).join(", ") : tr("Not a Ceph node")}</span></div>
                  <Badge tone={m.online ? "ok" : "danger"} dot>{m.online ? tr("Online") : tr("Offline")}</Badge>
                  {!node && <Button size="sm" icon={<Plus size={14} />} onClick={() => setAddNode(m)}>{tr("Add to Ceph")}</Button>}
                  {node && !m.self && mine.length === 0 && <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove {0} from Ceph?", m.name), text: tr("Its monitor, manager and metadata server are removed. It must not hold disks of Ceph any more."), danger: true })) removeNode.mutate(m.id); }}>{tr("Remove from Ceph")}</Button>}
                </div>
              </div>
            );
          })}
        </div>
      </Card>
      <Card title={tr("Disks (OSD)")} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => setAddDisk(true)}>{tr("Add a disk")}</Button>}>
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead className="text-muted text-left text-[12px]"><tr><th className="py-1.5">OSD</th><th>{tr("NAS")}</th><th>{tr("Disk")}</th><th>{tr("State")}</th><th>{tr("Used")}</th><th>PG</th><th /></tr></thead>
            <tbody>
              {osds.map((o) => (
                <tr key={o.id} className="border-t border-line">
                  <td className="py-2 mono">{o.name}</td><td>{o.host}</td><td className="mono">{o.device ?? "—"} <span className="text-muted">{o.class}</span></td>
                  <td><Badge tone={o.status === "up" ? (o.in ? "ok" : "warn") : "danger"} dot>{o.status === "up" ? (o.in ? tr("Up") : tr("Up, out")) : o.status === "destroyed" ? tr("Destroyed") : tr("Down")}</Badge></td>
                  <td className="min-w-[120px]">{o.size ? <><Progress value={o.utilization} /><span className="text-muted text-[11.5px]">{bytes(o.used)} / {bytes(o.size)}</span></> : "—"}</td>
                  <td>{o.pgs}</td>
                  <td className="whitespace-nowrap text-right">
                    {o.status === "up" && <Button size="sm" variant="ghost" onClick={() => inout.mutate({ id: o.id, a: o.in ? "out" : "in" })}>{o.in ? tr("Mark out") : tr("Mark in")}</Button>}
                    <Button size="sm" variant="ghost" icon={<Wrench size={14} />} onClick={() => setReplace(o)}>{tr("Replace")}</Button>
                    <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove {0}?", o.name), text: tr("Ceph first copies its data to the other disks (this can take hours), then removes it and erases the disk. The data stays available meanwhile."), danger: true })) removeOSD.mutate(o.id); }} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      {addDisk && <AddDisk cl={cl} onClose={() => setAddDisk(false)} />}
      {replace && <ReplaceDisk cl={cl} osd={replace} onClose={() => setReplace(null)} />}
      {addNode && <AddNode m={addNode} d={d} onClose={() => setAddNode(null)} />}
    </div>
  );
}

function DiskSelect({ member, value, onChange }: { member: string; value: string; onChange: (p: string, erase: boolean) => void }) {
  const disks = useQuery({ queryKey: ["ceph-disks"], queryFn: () => api.get<DiskRow[]>("/api/ceph/disks") });
  const row = disks.data?.find((r) => r.member === member);
  if (disks.isLoading) return <Spinner />;
  const ok = row?.disks.filter((x) => x.eligible) ?? [];
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value, !!ok.find((x) => x.path === e.target.value)?.erase)}>
      <option value="">{ok.length ? tr("Choose…") : tr("No disk can be used")}</option>
      {ok.map((x) => <option key={x.path} value={x.path}>{x.path} · {x.model || x.name} · {bytes(x.size)}{x.erase ? ` · ${tr("has data: erased")}` : ""}</option>)}
    </Select>
  );
}

function AddDisk({ cl, onClose }: { cl: ClusterData; onClose: () => void }) {
  const { confirm } = useUI();
  const nodes = cl.members.filter((m) => m.roles?.length || m.self);
  const [f, setF] = useState({ member: nodes[0]?.id ?? "", device: "", erase: false });
  const run = useAction(() => api.post("/api/ceph/osds", { ...f, erase: true }), { onDone: onClose });
  const go = async () => { if (await confirm({ title: tr("Erase {0}?", f.device), text: tr("The disk is erased and given to Ceph."), danger: true, typeToConfirm: f.device })) run.mutate(); };
  return (
    <Modal open onClose={onClose} title={tr("Add a disk")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="danger" disabled={!f.device} loading={run.isPending} onClick={go}>{tr("Erase and add")}</Button></>}>
      <div className="space-y-4">
        <Field label={tr("NAS")}><Select value={f.member} onChange={(e) => setF({ ...f, member: e.target.value, device: "" })}>{nodes.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</Select></Field>
        <Field label={tr("Disk")}><DiskSelect member={f.member} value={f.device} onChange={(device, erase) => setF({ ...f, device, erase })} /></Field>
        <ErrorBox error={run.error} />
      </div>
    </Modal>
  );
}

function ReplaceDisk({ cl, osd, onClose }: { cl: ClusterData; osd: OSD; onClose: () => void }) {
  const { confirm } = useUI();
  const m = cl.members.find((x) => x.name === osd.host);
  const [device, setDevice] = useState("");
  const run = useAction(() => api.post(`/api/ceph/osds/${osd.id}/replace`, { device, erase: true }), { onDone: onClose });
  const go = async () => { if (await confirm({ title: tr("Erase {0}?", device), text: tr("The new disk is erased and takes the place of {0}.", osd.name), danger: true, typeToConfirm: device })) run.mutate(); };
  return (
    <Modal open onClose={onClose} title={tr("Replace the disk of {0}", osd.name)} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="danger" disabled={!device} loading={run.isPending} onClick={go}>{tr("Replace")}</Button></>}>
      <div className="space-y-4">
        <Notice>{osd.status === "up" ? tr("This disk still works: Ceph first copies its data elsewhere, then the new disk takes its place.") : tr("Put the new disk in {0}, then choose it here. Ceph copies the missing data to it.", osd.host)}</Notice>
        {m ? <Field label={tr("New disk on {0}", osd.host)}><DiskSelect member={m.id} value={device} onChange={(p) => setDevice(p)} /></Field> : <ErrorBox error={tr("{0} is not a member of the cluster", osd.host)} />}
        <ErrorBox error={run.error} />
      </div>
    </Modal>
  );
}

function AddNode({ m, d, onClose }: { m: Member; d: CephData; onClose: () => void }) {
  const { confirm } = useUI();
  const [f, setF] = useState({ mon: (d.local?.mons.length ?? 3) < 3, mds: true, disks: [] as string[] });
  const run = useAction(() => api.post(`/api/ceph/nodes/${m.id}`, { ...f, erase: true }), { onDone: onClose });
  const [disk, setDisk] = useState("");
  const go = async () => { if (!f.disks.length || await confirm({ title: tr("Erase the disks?"), text: f.disks.join(", "), danger: true, typeToConfirm: m.name })) run.mutate(); };
  return (
    <Modal open onClose={onClose} title={tr("Add {0} to Ceph", m.name)} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={run.isPending} onClick={go}>{tr("Add")}</Button></>}>
      <div className="space-y-4">
        {!m.info?.cephInstalled && <Notice tone="warn">{tr("Install the Ceph component on {0} first.", m.name)}</Notice>}
        <Switch checked={f.mon} onChange={(v) => setF({ ...f, mon: v })} label={tr("Monitor")} hint={tr("Keep an odd number of monitors (3 or 5)")} />
        <Switch checked={f.mds} onChange={(v) => setF({ ...f, mds: v })} label={tr("Metadata server (CephFS)")} />
        <Field label={tr("Disks to give to Ceph")}>
          <div className="flex gap-2"><div className="flex-1"><DiskSelect member={m.id} value={disk} onChange={(p) => setDisk(p)} /></div><Button disabled={!disk} onClick={() => { setF({ ...f, disks: [...new Set([...f.disks, disk])] }); setDisk(""); }}>{tr("Add")}</Button></div>
        </Field>
        {f.disks.length > 0 && <div className="mono text-[12.5px]">{f.disks.join(", ")}</div>}
        <ErrorBox error={run.error} />
      </div>
    </Modal>
  );
}

/* ---------- Pools and client keys ---------- */

function Pools({ d, cl }: { d: CephData; cl: ClusterData }) {
  const { confirm } = useUI();
  const [f, setF] = useState<{ kind: string; name: string } | null>(null);
  const [key, setKey] = useState<{ name: string; pool: string; fs: string } | null>(null);
  const [shown, setShown] = useState<{ user: string; key: string; fsid: string; mons: string[] } | null>(null);
  const create = useAction(() => api.post("/api/ceph/pools", f), { onDone: () => setF(null) });
  const delPool = useAction((n: string) => api.del(`/api/ceph/pools/${encodeURIComponent(n)}${q({ confirm: n })}`), { success: tr("Pool deleted") });
  const delFS = useAction((n: string) => api.del(`/api/ceph/filesystems/${encodeURIComponent(n)}${q({ confirm: n })}`), { success: tr("Filesystem deleted") });
  const mkKey = useAction(() => api.post<{ user: string; key: string; fsid: string; mons: string[] }>("/api/ceph/clients", key), { onDone: (r) => { setKey(null); setShown(r); } });
  const delKey = useAction((n: string) => api.del(`/api/ceph/clients/${encodeURIComponent(n)}`), { success: tr("Key deleted") });
  const destroy = useAction(() => api.del(`/api/ceph${q({ confirm: cl.cluster?.name ?? "" })}`));
  const fsPools = new Set((d.filesystems ?? []).flatMap((x) => [x.metadataPool, ...x.dataPools]));
  return (
    <div className="space-y-5">
      <ErrorBox error={delPool.error || delFS.error || delKey.error || destroy.error} />
      <Card title={tr("Pools")} actions={<div className="flex gap-2"><Button size="sm" icon={<Plus size={14} />} onClick={() => setF({ kind: "rbd", name: "" })}>{tr("RBD pool")}</Button><Button size="sm" icon={<Plus size={14} />} onClick={() => setF({ kind: "cephfs", name: "" })}>{tr("CephFS filesystem")}</Button></div>}>
        <div className="space-y-2">
          {(d.filesystems ?? []).map((x) => (
            <div key={x.name} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
              <Boxes size={16} className="text-muted" /><div className="flex-1"><b>{x.name}</b> <Badge tone="accent">CephFS</Badge> <span className="mono text-muted text-[12px]">{x.metadataPool}, {x.dataPools.join(", ")}</span></div>
              <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete the filesystem {0}?", x.name), text: tr("All its files are lost."), danger: true, typeToConfirm: x.name })) delFS.mutate(x.name); }} />
            </div>
          ))}
          {(d.pools ?? []).filter((p) => !fsPools.has(p.name)).map((p) => (
            <div key={p.name} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
              <Database size={16} className="text-muted" />
              <div className="min-w-0 flex-1"><b>{p.name}</b> {p.apps.map((a) => <Badge key={a}>{a}</Badge>)} <span className="text-muted text-[12px]">· {tr("{0} copies (min. {1})", p.size, p.minSize)} · {p.pgs} PG · {bytes(p.stored)} · {tr("{0} free", bytes(p.maxAvail))}</span></div>
              {!p.name.startsWith(".") && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete the pool {0}?", p.name), text: tr("All its data is lost."), danger: true, typeToConfirm: p.name })) delPool.mutate(p.name); }} />}
            </div>
          ))}
        </div>
      </Card>
      <Card title={tr("Client keys")} actions={<Button size="sm" icon={<KeyRound size={14} />} onClick={() => setKey({ name: "", pool: "rbd", fs: "cephfs" })}>{tr("New key")}</Button>}>
        <div className="space-y-2 text-[13px]">
          <div className="text-muted">{tr("For another NAS or computer that uses this cluster as external storage, with rights limited to a pool and a filesystem.")}</div>
          {(d.clients ?? []).map((c) => (
            <div key={c.name} className="flex items-center gap-3 rounded-xl border border-line px-4 py-2"><KeyRound size={15} className="text-muted" /><span className="mono flex-1">client.{c.name}</span>
              {c.name !== "strata-vms" && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete the key client.{0}?", c.name), text: tr("What uses it loses access."), danger: true })) delKey.mutate(c.name); }} />}
            </div>
          ))}
        </div>
      </Card>
      <Card title={tr("Danger zone")}>
        <div className="flex flex-wrap items-center justify-between gap-3 text-[13px]">
          <div className="text-muted">{tr("Destroying the Ceph cluster erases the Ceph disks of every NAS. Remove the Ceph volumes and VM storage first.")}</div>
          <Button variant="danger" icon={<Trash2 size={15} />} loading={destroy.isPending} onClick={async () => { if (await confirm({ title: tr("Destroy the Ceph cluster?"), text: tr("All the data stored in Ceph is lost. Type the name of the cluster to confirm."), danger: true, typeToConfirm: cl.cluster?.name ?? "" })) destroy.mutate(); }}>{tr("Destroy")}</Button>
        </div>
      </Card>
      <Modal open={!!f} onClose={() => setF(null)} title={f?.kind === "rbd" ? tr("New RBD pool") : tr("New CephFS filesystem")} footer={<><Button onClick={() => setF(null)}>{tr("Cancel")}</Button><Button variant="primary" disabled={!f?.name} loading={create.isPending} onClick={() => create.mutate()}>{tr("Create")}</Button></>}>
        {f && <div className="space-y-4"><Field label={tr("Name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value.trim() })} /></Field><ErrorBox error={create.error} /></div>}
      </Modal>
      <Modal open={!!key} onClose={() => setKey(null)} title={tr("New client key")} footer={<><Button onClick={() => setKey(null)}>{tr("Cancel")}</Button><Button variant="primary" disabled={!key?.name || (!key.pool && !key.fs)} loading={mkKey.isPending} onClick={() => mkKey.mutate()}>{tr("Create")}</Button></>}>
        {key && <div className="space-y-4">
          <Field label={tr("User")} hint={tr("client.{0}", key.name || "…")}><Input className="mono" value={key.name} onChange={(e) => setKey({ ...key, name: e.target.value.trim() })} placeholder="nas-office" /></Field>
          <Field label={tr("RBD pool")}><Select value={key.pool} onChange={(e) => setKey({ ...key, pool: e.target.value })}><option value="">{tr("None")}</option>{(d.pools ?? []).filter((p) => p.apps.includes("rbd")).map((p) => <option key={p.name}>{p.name}</option>)}</Select></Field>
          <Field label={tr("CephFS filesystem")}><Select value={key.fs} onChange={(e) => setKey({ ...key, fs: e.target.value })}><option value="">{tr("None")}</option>{(d.filesystems ?? []).map((x) => <option key={x.name}>{x.name}</option>)}</Select></Field>
          <ErrorBox error={mkKey.error} />
        </div>}
      </Modal>
      <Modal open={!!shown} onClose={() => setShown(null)} title={tr("Key created")} footer={<Button variant="primary" onClick={() => setShown(null)}>{tr("Done")}</Button>}>
        {shown && <div className="space-y-3 text-[13px]">
          <Notice tone="warn">{tr("Copy the key now: it is not shown again.")}</Notice>
          <Field label={tr("Monitors")}><Input readOnly className="mono" value={shown.mons.join(", ")} /></Field>
          <Field label={tr("User")}><Input readOnly className="mono" value={`client.${shown.user}`} /></Field>
          <Field label={tr("Key")}><div className="flex gap-2"><Input readOnly className="mono" value={shown.key} /><Button icon={<Copy size={14} />} onClick={() => navigator.clipboard?.writeText(shown.key)} /></div></Field>
          <div className="text-muted mono text-[11.5px]">fsid {shown.fsid}</div>
        </div>}
      </Modal>
    </div>
  );
}

/* ---------- Ceph storage on this NAS ---------- */

function ExternalModal({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState({ name: "", mons: "", user: "", key: "" });
  const [probe, setProbe] = useState<Probe | null>(null);
  const test = useAction(() => api.post<Probe>("/api/ceph/external/test", f), { onDone: setProbe });
  const save = useAction(() => api.post("/api/ceph/external", f), { success: tr("Ceph cluster added"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Use an external Ceph cluster")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button loading={test.isPending} disabled={!f.mons || !f.user || !f.key} onClick={() => test.mutate()}>{tr("Test the connection")}</Button><Button variant="primary" loading={save.isPending} disabled={!f.name || !f.mons || !f.user || !f.key} onClick={() => save.mutate()}>{tr("Add")}</Button></>}>
      <div className="space-y-4">
        <Notice>{tr("On the Ceph cluster, create a user with rights on the pools and filesystems this NAS may use (Ceph → Pools → Client keys on a StrataOS cluster), then enter it here. The key stays on this NAS, readable by the system only.")}</Notice>
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder={tr("Office cluster")} /></Field>
        <Field label={tr("Monitor addresses")} hint={tr("Separated by commas, e.g. 192.168.10.11, 192.168.10.12")}><Input className="mono" value={f.mons} onChange={(e) => setF({ ...f, mons: e.target.value })} /></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("User")}><Input className="mono" value={f.user} onChange={(e) => setF({ ...f, user: e.target.value.trim() })} placeholder="client.nas-office" /></Field>
          <Field label={tr("Key")}><Input className="mono" type="password" autoComplete="off" value={f.key} onChange={(e) => setF({ ...f, key: e.target.value.trim() })} placeholder="AQ…==" /></Field>
        </div>
        {probe && <Notice>{tr("Connected: cluster {0}", probe.fsid)}{probe.health ? ` · ${healthLabel(probe.health)}` : ""} · {tr("pools: {0}", probe.pools.map((p) => p.name).join(", ") || "—")} · {tr("filesystems: {0}", probe.filesystems.map((x) => x.name).join(", ") || "—")}</Notice>}
        <ErrorBox error={test.error || save.error} />
      </div>
    </Modal>
  );
}

function AddCephVolume({ sources, onClose }: { sources: Source[]; onClose: () => void }) {
  const [f, setF] = useState({ name: "", source: sources[0]?.id ?? "", kind: "cephfs", fs: "", path: "/", pool: "", image: "", create: true, sizeGb: 100, fsType: "ext4", confirm: false });
  const set = (k: Partial<typeof f>) => setF({ ...f, ...k });
  const probe = useQuery({ queryKey: ["ceph-source", f.source], queryFn: () => api.get<Probe>(`/api/ceph/sources/${f.source}`), enabled: !!f.source });
  const images = useQuery({ queryKey: ["ceph-images", f.source, f.pool], queryFn: () => api.get<{ name: string; size: number }[]>(`/api/ceph/sources/${f.source}${q({ images: f.pool })}`), enabled: !!f.source && !!f.pool && f.kind === "rbd" });
  const run = useAction(() => api.post("/api/ceph/volumes", f), { onDone: onClose });
  const rbdPools = (probe.data?.pools ?? []).filter((p) => p.apps.includes("rbd") || p.apps.length === 0);
  useEffect(() => {
    if (!probe.data) return;
    setF((x) => ({ ...x, fs: x.fs || probe.data.filesystems[0]?.name || "", pool: x.pool || probe.data.pools.find((p) => p.apps.includes("rbd"))?.name || "" }));
  }, [probe.data]);
  const ok = !!f.name && (f.kind === "cephfs" ? !!f.fs : !!f.pool && !!f.image && f.confirm);
  return (
    <Modal open onClose={onClose} wide title={tr("Add Ceph storage")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!ok} loading={run.isPending} onClick={() => run.mutate()}>{tr("Create the volume")}</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={tr("Volume name")} hint={tr("Mounted at /srv/strata/{0}", f.name || "…")}><Input value={f.name} onChange={(e) => set({ name: e.target.value.trim() })} /></Field>
          <Field label={tr("Ceph cluster")}><Select value={f.source} onChange={(e) => set({ source: e.target.value, fs: "", pool: "", image: "" })}>{sources.map((s) => <option key={s.id} value={s.id}>{s.local ? tr("{0} (this cluster)", s.name) : s.name}</option>)}</Select></Field>
          <Field label={tr("Kind")}><Select value={f.kind} onChange={(e) => set({ kind: e.target.value })}><option value="cephfs">{tr("CephFS: shared filesystem")}</option><option value="rbd">{tr("RBD: block device for this NAS only")}</option></Select></Field>
        </div>
        {probe.isLoading && <Spinner />}
        <ErrorBox error={probe.error} />
        {probe.data?.poolsError && <Notice tone="warn">{tr("This user cannot list the pools: type their names.")}</Notice>}
        {f.kind === "cephfs" ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Filesystem")}>{probe.data?.filesystems.length ? <Select value={f.fs} onChange={(e) => set({ fs: e.target.value })}>{probe.data.filesystems.map((x) => <option key={x.name}>{x.name}</option>)}</Select> : <Input value={f.fs} onChange={(e) => set({ fs: e.target.value.trim() })} />}</Field>
            <Field label={tr("Folder in the filesystem")} hint={tr("Several NAS can each use their own folder, or share one")}><Input className="mono" value={f.path} onChange={(e) => set({ path: e.target.value.trim() })} /></Field>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Pool")}>{rbdPools.length ? <Select value={f.pool} onChange={(e) => set({ pool: e.target.value, image: "" })}>{rbdPools.map((p) => <option key={p.name}>{p.name}</option>)}</Select> : <Input value={f.pool} onChange={(e) => set({ pool: e.target.value.trim() })} />}</Field>
              <Field label={tr("Image")}>
                <Select value={f.create ? "" : f.image} onChange={(e) => set({ create: !e.target.value, image: e.target.value })}>
                  <option value="">{tr("New image")}</option>
                  {images.data?.map((i) => <option key={i.name} value={i.name}>{i.name} ({bytes(i.size)})</option>)}
                </Select>
              </Field>
            </div>
            {f.create && <div className="grid gap-4 sm:grid-cols-3">
              <Field label={tr("Image name")}><Input className="mono" value={f.image} onChange={(e) => set({ image: e.target.value.trim() })} placeholder={f.name} /></Field>
              <Field label={tr("Size (GB)")} hint={tr("Thin: space is used as data is written")}><Input type="number" min={1} value={f.sizeGb} onChange={(e) => set({ sizeGb: +e.target.value })} /></Field>
              <Field label={tr("File system")}><Select value={f.fsType} onChange={(e) => set({ fsType: e.target.value })}><option value="ext4">ext4</option><option value="xfs">XFS</option><option value="btrfs">Btrfs</option></Select></Field>
            </div>}
            <Notice tone="warn">
              <div>{tr("A filesystem on an RBD image must be used by a single NAS at a time: mounting it on two NAS at once corrupts it. Use CephFS to share files between NAS.")}</div>
              <div className="mt-2"><Checkbox checked={f.confirm} onChange={(v) => set({ confirm: v })} label={tr("Only this NAS will use this image")} /></div>
            </Notice>
          </div>
        )}
        <ErrorBox error={run.error} />
      </div>
    </Modal>
  );
}

function StorageTab({ d }: { d: CephData }) {
  const { confirm } = useUI();
  const s = useQuery({ queryKey: ["ceph-storage"], queryFn: () => api.get<Storage>("/api/ceph/storage"), refetchInterval: 10000 });
  const [ext, setExt] = useState(false);
  const [vol, setVol] = useState(false);
  const [vmp, setVmp] = useState<{ source: string; pool: string } | null>(null);
  const delExt = useAction((id: string) => api.del(`/api/ceph/external/${id}`), { success: tr("Ceph cluster removed") });
  const addVM = useAction(() => api.post("/api/ceph/vm-pools", vmp), { success: tr("VM storage added"), onDone: () => setVmp(null) });
  const delVM = useAction((n: string) => api.del(`/api/ceph/vm-pools/${encodeURIComponent(n)}`), { success: tr("VM storage removed") });
  const delVol = useAction((id: string) => api.del(`/api/storage/volumes/${id}`), { success: tr("Volume removed") });
  const mount = useAction((id: string) => api.post(`/api/storage/volumes/${id}/mount`), { success: tr("Mounted") });
  if (!s.data) return <><ErrorBox error={s.error} /><Spinner /></>;
  const st = s.data;
  const name = (id: string) => st.sources.find((x) => x.id === id)?.name ?? id;
  return (
    <div className="space-y-5">
      <ErrorBox error={delExt.error || delVM.error || delVol.error || mount.error} />
      {!d.clientInstalled && <Notice tone="warn">{tr("Install the Ceph component to use Ceph storage.")}</Notice>}
      <Card title={tr("Ceph clusters")} actions={<Button size="sm" icon={<Cable size={14} />} disabled={!d.clientInstalled} onClick={() => setExt(true)}>{tr("Use an external cluster")}</Button>}>
        <div className="space-y-2">
          {st.sources.length === 0 && <div className="text-muted text-[13px]">{tr("No Ceph cluster yet: create one with other NAS, or connect an external cluster.")}</div>}
          {st.sources.map((x) => (
            <div key={x.id} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
              <Network size={16} className="text-muted" />
              <div className="min-w-0 flex-1"><b>{x.name}</b> {x.local ? <Badge tone="accent">{tr("this cluster")}</Badge> : <Badge>{tr("external")}</Badge>} <span className="mono text-muted text-[12px]">client.{x.user} · {x.mons.join(", ")}</span></div>
              {!x.local && <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove {0}?", x.name), text: tr("Its key is deleted from this NAS. Nothing is changed on the cluster."), danger: true })) delExt.mutate(x.id); }} />}
            </div>
          ))}
        </div>
      </Card>
      <Card title={tr("Ceph volumes")} actions={<Button size="sm" icon={<Plus size={14} />} disabled={!st.sources.length} onClick={() => setVol(true)}>{tr("Add Ceph storage")}</Button>}>
        <div className="space-y-2">
          {st.volumes.length === 0 && <div className="text-muted text-[13px]">{tr("A CephFS folder or an RBD image becomes a volume of this NAS: create shared folders on it like on any volume.")}</div>}
          {st.volumes.map((v) => (
            <div key={v.volumeId} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
              <Database size={16} className="text-muted" />
              <div className="min-w-0 flex-1">
                <div><b>{v.name}</b> <Badge tone="accent">{v.kind === "cephfs" ? "CephFS" : "RBD"}</Badge> <span className="mono text-muted text-[12px]">{name(v.cluster)} · {v.kind === "cephfs" ? `${v.fs}:${v.path}` : `${v.pool}/${v.image}`}</span></div>
                {v.mounted && <div className="mt-1 max-w-sm"><Progress value={pct(v.usage.used, v.usage.total)} /><span className="text-muted text-[11.5px]">{bytes(v.usage.used)} / {bytes(v.usage.total)}</span></div>}
              </div>
              {v.mounted ? <Badge tone="ok" dot>{tr("Mounted")}</Badge> : <Button size="sm" onClick={() => mount.mutate(v.volumeId)}>{tr("Mount")}</Button>}
              <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove the volume {0}?", v.name), text: v.kind === "rbd" ? tr("It is unmounted from this NAS. The RBD image stays in Ceph (delete it from the Volumes page with its data if needed).") : tr("It is unmounted from this NAS. The files stay in CephFS."), danger: true })) delVol.mutate(v.volumeId); }} />
            </div>
          ))}
        </div>
      </Card>
      <Card title={tr("VM storage")} actions={<Button size="sm" icon={<Plus size={14} />} disabled={!st.vmsInstalled || !st.sources.length} onClick={() => setVmp({ source: st.sources[0]?.id ?? "", pool: "rbd" })}>{tr("Add VM storage")}</Button>}>
        <div className="space-y-2 text-[13px]">
          {!st.vmsInstalled && <div className="text-muted">{tr("Install the Virtual machines component to store VM disks on Ceph.")}</div>}
          {st.vmPools.map((p) => (
            <div key={p.name} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5">
              <Server size={16} className="text-muted" /><div className="flex-1"><b className="mono">{p.name}</b> <span className="text-muted">· {name(p.cluster)} · {tr("pool {0}", p.pool)}</span></div>
              <Badge tone={p.active ? "ok" : "danger"} dot>{p.active ? tr("Active") : tr("Inactive")}</Badge>
              <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove {0}?", p.name), text: tr("Only possible when no VM disk is left in it."), danger: true })) delVM.mutate(p.name); }} />
            </div>
          ))}
          {st.vmsInstalled && <div className="text-muted text-[12px]">{tr("VM storage on Ceph appears in \"Disk locations\" when you create a VM from an ISO.")}</div>}
        </div>
      </Card>
      {ext && <ExternalModal onClose={() => setExt(false)} />}
      {vol && <AddCephVolume sources={st.sources} onClose={() => setVol(false)} />}
      <Modal open={!!vmp} onClose={() => setVmp(null)} title={tr("Add VM storage")} footer={<><Button onClick={() => setVmp(null)}>{tr("Cancel")}</Button><Button variant="primary" disabled={!vmp?.pool} loading={addVM.isPending} onClick={() => addVM.mutate()}>{tr("Add")}</Button></>}>
        {vmp && <div className="space-y-4">
          <Field label={tr("Ceph cluster")}><Select value={vmp.source} onChange={(e) => setVmp({ ...vmp, source: e.target.value })}>{st.sources.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</Select></Field>
          <Field label={tr("RBD pool")}><Input value={vmp.pool} onChange={(e) => setVmp({ ...vmp, pool: e.target.value.trim() })} /></Field>
          <ErrorBox error={addVM.error} />
        </div>}
      </Modal>
    </div>
  );
}

/* ---------- Page ---------- */

type Tab = "overview" | "nodes" | "pools" | "storage";

export default function Ceph() {
  const d = useQuery({ queryKey: ["ceph"], queryFn: () => api.get<CephData>("/api/ceph"), refetchInterval: 5000 });
  const cl = useQuery({ queryKey: ["cluster"], queryFn: () => api.get<ClusterData>("/api/cluster"), refetchInterval: 10000 });
  const install = useAction(() => api.post("/api/system/components/ceph/install"));
  const leave = useAction(() => api.post("/api/cluster/leave"), { success: tr("This NAS left the cluster") });
  const { confirm } = useUI();
  const [wizard, setWizard] = useState(false);
  const [tab, setTab] = useState<Tab>("overview");
  const header = <PageHeader title="Ceph" subtitle={tr("Storage spread over several NAS, and Ceph clusters used as storage")} icon={<Boxes size={20} />} />;
  if (!d.data || !cl.data) return <div>{header}<ErrorBox error={d.error || cl.error} /><Spinner /></div>;
  const data = d.data, c = cl.data;
  if (!data.installed && !data.clientInstalled) {
    return <div>{header}<Card><Empty icon={<Boxes size={22} />} title={tr("Ceph is not installed")} text={tr("Install the Ceph component on each NAS that takes part in a cluster, or that uses an external Ceph cluster.")} action={<Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card></div>;
  }
  if (!data.local) {
    return (
      <div>
        {header}
        <div className="space-y-5">
          <div className="grid gap-5 lg:grid-cols-2">
            <Card title={c.cluster ? tr("Cluster {0}", c.cluster.name) : tr("Create a Ceph cluster")} actions={c.cluster && <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Leave the cluster?"), text: tr("This NAS no longer takes part in the cluster."), danger: true })) leave.mutate(); }}>{tr("Leave")}</Button>}>
              <div className="space-y-3 text-[13px]">
                {c.cluster ? <Members cl={c} /> : <div className="text-muted">{tr("Group 3 NAS or more: Ceph keeps copies of the data on different NAS, so that one can stop without interruption.")}</div>}
                <Button variant="primary" icon={<Plus size={15} />} onClick={() => setWizard(true)}>{c.cluster ? tr("Continue the creation") : tr("Create a Ceph cluster")}</Button>
                <ErrorBox error={leave.error} />
              </div>
            </Card>
            {!c.cluster && <JoinCard cl={c} />}
          </div>
          <StorageTab d={data} />
        </div>
        {wizard && <CreateWizard cl={c} onClose={() => setWizard(false)} />}
      </div>
    );
  }
  return (
    <div>
      {header}
      <Tabs<Tab> value={tab} onChange={setTab} tabs={[{ id: "overview", label: tr("Overview") }, { id: "nodes", label: tr("NAS and disks") }, { id: "pools", label: tr("Pools") }, { id: "storage", label: tr("Storage on this NAS") }]} />
      {tab === "overview" && <Overview d={data} />}
      {tab === "nodes" && <Nodes d={data} cl={c} />}
      {tab === "pools" && <Pools d={data} cl={c} />}
      {tab === "storage" && <StorageTab d={data} />}
    </div>
  );
}
