import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRightLeft, Box, Cpu, Crown, HeartPulse, Monitor, MonitorPlay, Network, Play, Power, Server, Square, Wrench } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { bytes, cx } from "../../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Modal, Notice, Select, Spinner, Switch, useUI } from "../../../components/ui";
import { tr } from "../../../lib/i18n";
import { Console } from "../VirtualMachines";

export type ClusterVM = {
  name: string; state: string; vcpus: number; memoryMb: number; memUsedKb?: number; storage: "ceph" | "local" | "mixed" | "none";
  cpu: string; template: boolean; autostart: boolean; ips?: string[]; os?: string; bridges?: string[];
};
export type ClusterCT = { name: string; state: string; cpus: number; memoryMb: number; memoryBytes?: number; ips?: string[]; location: string; autostart: boolean };
export type Inventory = {
  id: string; name: string; libvirt: boolean; lxc: boolean; kvm: boolean; memMb: number; freeMb: number; cpus: number;
  networks: string[]; bridges: string[]; volumes: string[]; vms: ClusterVM[]; containers: ClusterCT[]; models?: string[];
};
export type Member = { id: string; name: string; host: string; self: boolean; online: boolean; maintenance: boolean; leader: boolean; inventory?: Inventory };
export type HAEntry = { name: string; owner: string; preferred?: string; running: boolean; recover?: boolean; migrating?: string };
export type ClusterData = { cluster: { id: string; name: string; self: string } | null; members: Member[]; ha: HAEntry[]; cpuModel: string; quorum: boolean; haPossible: boolean };

export function useClusterData() {
  return useQuery({ queryKey: ["vmcluster"], queryFn: () => api.get<ClusterData>("/api/vmcluster"), refetchInterval: 5000 });
}

const stateTone = (s: string): "ok" | "warn" | "neutral" | "danger" => (s === "running" ? "ok" : s === "paused" || s === "in shutdown" || s === "starting" || s === "stopping" ? "warn" : s === "crashed" ? "danger" : "neutral");
const stateText = (s: string) => ({ running: tr("Running"), paused: tr("Paused"), "shut off": tr("Stopped"), stopped: tr("Stopped"), crashed: tr("Crashed"), "in shutdown": tr("Shutting down") } as Record<string, string>)[s] ?? s;
const mem = (mb: number) => (mb >= 1024 ? tr("{0} GiB", (mb / 1024).toFixed(1)) : tr("{0} MiB", mb));

export function ClusterPanel({ data }: { data: ClusterData }) {
  const { confirm } = useUI();
  const [console, setConsole] = useState<{ node: string; name: string } | null>(null);
  const [migrating, setMigrating] = useState<{ node: Member; vm: ClusterVM } | null>(null);
  const [moving, setMoving] = useState<{ node: Member; ct: ClusterCT } | null>(null);
  const [haFor, setHAFor] = useState<{ node: Member; vm: ClusterVM } | null>(null);
  const maint = useAction(({ id, on }: { id: string; on: boolean }) => api.post(`/api/vmcluster/nodes/${id}/maintenance`, { on }));
  const cpu = useAction((model: string) => api.put("/api/vmcluster/settings", { cpuModel: model }), { success: tr("Saved") });
  const vmAct = useAction(({ node, name, action, body }: { node: string; name: string; action: string; body?: unknown }) => api.post(`/api/vmcluster/nodes/${node}/vms/${encodeURIComponent(name)}/${action}`, body ?? {}), { success: tr("Command sent") });
  const ctAct = useAction(({ node, name, action }: { node: string; name: string; action: string }) => api.post(`/api/vmcluster/nodes/${node}/lxc/${encodeURIComponent(name)}/${action}`), { success: tr("Command sent") });
  const online = data.members.filter((m) => m.online).length;
  const haOf = (name: string) => data.ha.find((h) => h.name === name);
  const nameOf = (id?: string) => data.members.find((m) => m.id === id)?.name ?? id ?? "";

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <div className={cx("flex h-11 w-11 items-center justify-center rounded-xl", data.quorum ? "bg-ok/10 text-ok" : "bg-danger/10 text-danger")}><Server size={22} /></div>
            <div>
              <div className="font-semibold">{tr("Cluster {0}", data.cluster?.name ?? "")}</div>
              <div className="text-muted text-[12px]">{tr("{0} of {1} NAS online", online, data.members.length)} · {data.quorum ? tr("quorum reached") : tr("no quorum: nothing is restarted or changed")}</div>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={data.haPossible ? "ok" : "neutral"}><HeartPulse size={11} className="mr-1 inline" />{data.haPossible ? tr("High availability possible") : tr("High availability needs 3 NAS")}</Badge>
            <Field label={tr("CPU of new VMs")} className="min-w-[220px]">
              <Select value={data.cpuModel ? "model" : ""} onChange={async (e) => {
                const v = e.target.value === "model" ? "auto" : "";
                if (v === "auto" || await confirm({ title: tr("Use the host processor?"), text: tr("New VMs then see the processor of their NAS: fastest, but they can only move live between NAS with the very same processor."), confirm: tr("Use the host processor") })) cpu.mutate(v);
              }}>
                <option value="">{tr("Host processor (fastest)")}</option>
                <option value="model">{data.cpuModel ? tr("Cluster-safe: {0}", data.cpuModel) : tr("Cluster-safe (common model)")}</option>
              </Select>
            </Field>
          </div>
        </div>
        {!data.quorum && <div className="mt-3"><Notice tone="warn">{tr("This NAS does not reach most of the cluster. VMs under high availability that run here are stopped after 30 seconds, so that the others can restart them safely.")}</Notice></div>}
      </Card>

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {data.members.map((m) => {
          const inv = m.inventory;
          const running = (inv?.vms ?? []).filter((v) => v.state === "running").length;
          return (
            <Card key={m.id}>
              <div className="flex items-start justify-between gap-2">
                <div>
                  <div className="flex items-center gap-1.5 font-semibold">{m.name}{m.leader && <Crown size={13} className="text-warn" aria-label={tr("Leader")} />}{m.self && <span className="text-muted text-[11.5px] font-normal">({tr("this NAS")})</span>}</div>
                  <div className="text-muted mono text-[11.5px]">{m.host}</div>
                </div>
                <Badge tone={!m.online ? "danger" : m.maintenance ? "warn" : "ok"} dot>{!m.online ? tr("Offline") : m.maintenance ? tr("Maintenance") : tr("Online")}</Badge>
              </div>
              {inv && (
                <div className="mt-3 grid grid-cols-3 gap-2 text-[12px]">
                  <div><div className="text-muted">{tr("Memory free")}</div><b>{mem(inv.freeMb)}</b><span className="text-muted"> / {mem(inv.memMb)}</span></div>
                  <div><div className="text-muted">{tr("VMs running")}</div><b>{running}</b><span className="text-muted"> / {inv.vms.filter((v) => !v.template).length}</span></div>
                  <div><div className="text-muted">{tr("Containers")}</div><b>{inv.containers.length}</b></div>
                </div>
              )}
              {m.online && (
                <div className="mt-3 border-t border-line pt-3">
                  <Button size="sm" icon={<Wrench size={14} />} loading={maint.isPending} onClick={async () => {
                    if (m.maintenance) return maint.mutate({ id: m.id, on: false });
                    if (await confirm({ title: tr("Put {0} in maintenance?", m.name), text: tr("Its running VMs move live to the other NAS (those on Ceph storage), and no VM is placed on it until the maintenance ends. Use it before an update or a restart."), confirm: tr("Start maintenance") })) maint.mutate({ id: m.id, on: true });
                  }}>{m.maintenance ? tr("End maintenance") : tr("Maintenance")}</Button>
                </div>
              )}
            </Card>
          );
        })}
      </div>

      <Card>
        <div className="mb-3 font-semibold">{tr("All virtual machines and containers")}</div>
        <div className="overflow-x-auto">
          <table className="w-full text-[12.5px]">
            <thead className="text-muted text-left">
              <tr><th className="py-1.5 pr-3 font-medium">{tr("Name")}</th><th className="pr-3 font-medium">{tr("NAS")}</th><th className="pr-3 font-medium">{tr("State")}</th><th className="pr-3 font-medium">{tr("Resources")}</th><th className="pr-3 font-medium">{tr("Storage")}</th><th className="font-medium" /></tr>
            </thead>
            <tbody>
              {data.members.flatMap((m) => (m.inventory?.vms ?? []).filter((v) => !v.template).map((v) => {
                const ha = haOf(v.name);
                const run = v.state === "running";
                return (
                  <tr key={`vm-${m.id}-${v.name}`} className="border-t border-line">
                    <td className="py-2 pr-3"><span className="flex items-center gap-1.5"><Monitor size={14} className="text-accent" /><b>{v.name}</b>{ha && <Badge tone="accent"><HeartPulse size={10} className="mr-1 inline" />HA{ha.preferred ? ` → ${nameOf(ha.preferred)}` : ""}</Badge>}</span></td>
                    <td className="pr-3">{m.name}</td>
                    <td className="pr-3"><Badge tone={stateTone(v.state)} dot>{stateText(v.state)}</Badge></td>
                    <td className="pr-3">{v.vcpus} vCPU · {mem(v.memoryMb)}{run && v.memUsedKb ? <span className="text-muted"> ({bytes(v.memUsedKb * 1024)})</span> : null}<div className="text-muted text-[11px]"><Cpu size={10} className="mr-0.5 inline" />{v.cpu === "host" ? tr("host processor") : v.cpu}</div></td>
                    <td className="pr-3">{v.storage === "ceph" ? <Badge tone="ok">Ceph</Badge> : v.storage === "local" ? <Badge>{tr("This NAS")}</Badge> : v.storage === "mixed" ? <Badge tone="warn">{tr("Mixed")}</Badge> : "—"}</td>
                    <td className="whitespace-nowrap py-1 text-right">
                      {!run && v.state !== "paused" && <IconButton title={tr("Start")} onClick={() => vmAct.mutate({ node: m.id, name: v.name, action: "start" })}><Play size={15} /></IconButton>}
                      {run && <IconButton title={tr("Console")} onClick={() => setConsole({ node: m.id, name: v.name })}><MonitorPlay size={15} /></IconButton>}
                      {run && <IconButton title={tr("Stop")} onClick={() => vmAct.mutate({ node: m.id, name: v.name, action: "shutdown" })}><Power size={15} /></IconButton>}
                      {(run || v.state === "paused") && <IconButton title={tr("Force stop")} onClick={async () => (await confirm({ title: tr("Force stop?"), text: tr("Equivalent to pulling the plug: unsaved data in the VM is lost."), confirm: tr("Force"), danger: true })) && vmAct.mutate({ node: m.id, name: v.name, action: "destroy" })}><Square size={15} /></IconButton>}
                      <IconButton title={tr("Migrate")} disabled={!m.online || data.members.filter((x) => x.online).length < 2} onClick={() => setMigrating({ node: m, vm: v })}><ArrowRightLeft size={15} /></IconButton>
                      <IconButton title={tr("High availability")} onClick={() => setHAFor({ node: m, vm: v })}><HeartPulse size={15} /></IconButton>
                      {!run && v.state === "shut off" && data.cpuModel && v.cpu !== data.cpuModel && <IconButton title={tr("Use the cluster-safe CPU ({0})", data.cpuModel)} onClick={() => vmAct.mutate({ node: m.id, name: v.name, action: "cpu", body: { model: "cluster" } })}><Cpu size={15} /></IconButton>}
                    </td>
                  </tr>
                );
              }))}
              {data.members.flatMap((m) => (m.inventory?.containers ?? []).map((c) => (
                <tr key={`ct-${m.id}-${c.name}`} className="border-t border-line">
                  <td className="py-2 pr-3"><span className="flex items-center gap-1.5"><Box size={14} className="text-ok" /><b>{c.name}</b></span></td>
                  <td className="pr-3">{m.name}</td>
                  <td className="pr-3"><Badge tone={stateTone(c.state)} dot>{stateText(c.state)}</Badge></td>
                  <td className="pr-3">{c.cpus ? `${c.cpus} CPU · ` : ""}{c.memoryMb ? mem(c.memoryMb) : tr("no limit")}{c.memoryBytes ? <span className="text-muted"> ({bytes(c.memoryBytes)})</span> : null}</td>
                  <td className="pr-3 text-muted mono text-[11px]">{c.location}</td>
                  <td className="whitespace-nowrap py-1 text-right">
                    {c.state !== "running" && <IconButton title={tr("Start")} onClick={() => ctAct.mutate({ node: m.id, name: c.name, action: "start" })}><Play size={15} /></IconButton>}
                    {c.state === "running" && <IconButton title={tr("Stop")} onClick={() => ctAct.mutate({ node: m.id, name: c.name, action: "stop" })}><Power size={15} /></IconButton>}
                    <IconButton title={tr("Move to another NAS")} disabled={!m.online} onClick={() => setMoving({ node: m, ct: c })}><ArrowRightLeft size={15} /></IconButton>
                  </td>
                </tr>
              )))}
            </tbody>
          </table>
        </div>
        {data.members.every((m) => !m.inventory?.vms.length && !m.inventory?.containers.length) && <Empty icon={<Monitor />} title={tr("No virtual machines")} text={tr("Create VMs on any NAS of the cluster: they all show here.")} />}
      </Card>

      {console && <Console name={console.name} url={`/api/vmcluster/nodes/${console.node}/vms/${encodeURIComponent(console.name)}/vnc`} onClose={() => setConsole(null)} />}
      {migrating && <MigrateDialog data={data} node={migrating.node} vm={migrating.vm} onClose={() => setMigrating(null)} />}
      {moving && <MoveContainer data={data} node={moving.node} ct={moving.ct} onClose={() => setMoving(null)} />}
      {haFor && <HADialog data={data} node={haFor.node} vm={haFor.vm} onClose={() => setHAFor(null)} />}
    </div>
  );
}

type Plan = { live: boolean; copyStorage: boolean; copied?: string[]; problems?: string[]; warnings?: string[] };

function MigrateDialog({ data, node, vm, onClose }: { data: ClusterData; node: Member; vm: ClusterVM; onClose: () => void }) {
  const targets = data.members.filter((m) => m.online && m.id !== node.id);
  const [target, setTarget] = useState(targets.find((m) => !m.maintenance)?.id ?? targets[0]?.id ?? "");
  const [copy, setCopy] = useState(false);
  const [bridges, setBridges] = useState<Record<string, string>>({});
  const [storage, setStorage] = useState("");
  const [plan, setPlan] = useState<Plan | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const tgt = data.members.find((m) => m.id === target);
  const body = { node: node.id, name: vm.name, target, copyStorage: copy, bridges, storage };
  useEffect(() => {
    if (!target) return;
    let live = true;
    setPlan(null);
    setErr(null);
    api.post<Plan>("/api/vmcluster/migrate/check", body).then((p) => live && setPlan(p), (e) => live && setErr(e));
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target, copy, JSON.stringify(bridges), storage]);
  const go = useAction(() => api.post("/api/vmcluster/migrate", body), { onDone: onClose });
  const missing = (vm.bridges ?? []).filter((b) => tgt?.inventory && !tgt.inventory.bridges.includes(b));
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><ArrowRightLeft size={17} />{tr("Migrate {0}", vm.name)}</span>}
      footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!plan || (plan.problems?.length ?? 0) > 0} loading={go.isPending} onClick={() => go.mutate()}>{plan?.live ? tr("Migrate live") : tr("Move")}</Button></>}>
      <div className="space-y-4">
        <Field label={tr("From {0} to", node.name)}>
          <Select value={target} onChange={(e) => setTarget(e.target.value)}>
            {targets.map((m) => <option key={m.id} value={m.id}>{m.name}{m.maintenance ? ` (${tr("maintenance")})` : ""}{m.inventory ? ` — ${tr("{0} free", mem(m.inventory.freeMb))}` : ""}</option>)}
          </Select>
        </Field>
        {(vm.bridges ?? []).map((b) => (missing.includes(b) || bridges[b]) && tgt?.inventory && (
          <Field key={b} label={tr("Network bridge {0} on {1}", b, tgt.name)}>
            <Select value={bridges[b] ?? ""} onChange={(e) => setBridges({ ...bridges, [b]: e.target.value })}>
              <option value="">{tr("Choose a bridge")}</option>
              {tgt.inventory.bridges.map((x) => <option key={x} value={x}>{x}</option>)}
            </Select>
          </Field>
        ))}
        {vm.storage !== "ceph" && (
          <div className="space-y-3">
            <Checkbox checked={copy} onChange={setCopy} label={tr("Copy the disks of this NAS to the target (slower)")} />
            {copy && tgt?.inventory && (
              <Field label={tr("Folder for the disks on {0}", tgt.name)} hint={tr("Empty: the same folder as here")}>
                <Select value={storage} onChange={(e) => setStorage(e.target.value)}>
                  <option value="">{tr("Same folder")}</option>
                  {tgt.inventory.volumes.map((v) => <option key={v} value={`${v}/VMs`}>{v}/VMs</option>)}
                </Select>
              </Field>
            )}
          </div>
        )}
        <ErrorBox error={err} />
        {!plan && !err && <Spinner />}
        {plan && (plan.problems?.length ? (
          <Notice tone="warn"><ul className="list-disc pl-4">{plan.problems.map((p) => <li key={p}>{p}</li>)}</ul></Notice>
        ) : (
          <Notice>{plan.live ? (plan.copyStorage ? tr("The VM keeps running while its memory and disks are copied, then continues on the target.") : tr("The VM keeps running while its memory is copied (its disks are on Ceph), then continues on the target within a second.")) : tr("The VM is stopped: its definition moves to the target.")}{plan.warnings?.map((w) => <div key={w} className="mt-1 text-warn">{w}</div>)}</Notice>
        ))}
      </div>
    </Modal>
  );
}

function MoveContainer({ data, node, ct, onClose }: { data: ClusterData; node: Member; ct: ClusterCT; onClose: () => void }) {
  const targets = data.members.filter((m) => m.online && m.id !== node.id && m.inventory?.lxc);
  const [target, setTarget] = useState(targets[0]?.id ?? "");
  const tgt = data.members.find((m) => m.id === target);
  const vols = tgt?.inventory?.volumes ?? [];
  const [location, setLocation] = useState("");
  const loc = location || (vols.includes(ct.location) ? ct.location : vols[0] ?? "");
  const go = useAction(() => api.post(`/api/vmcluster/nodes/${node.id}/lxc/${encodeURIComponent(ct.name)}/migrate`, { target, location: loc }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><ArrowRightLeft size={17} />{tr("Move {0}", ct.name)}</span>}
      footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!target || !loc} loading={go.isPending} onClick={() => go.mutate()}>{tr("Move")}</Button></>}>
      <div className="space-y-4">
        {targets.length === 0 ? <Notice tone="warn">{tr("No other NAS of the cluster has containers (LXC) installed.")}</Notice> : (
          <>
            <Field label={tr("From {0} to", node.name)}>
              <Select value={target} onChange={(e) => { setTarget(e.target.value); setLocation(""); }}>
                {targets.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
              </Select>
            </Field>
            <Field label={tr("Volume on the target")}>
              <Select value={loc} onChange={(e) => setLocation(e.target.value)}>
                {vols.map((v) => <option key={v} value={v}>{v}</option>)}
              </Select>
            </Field>
            <Notice>{tr("The container is stopped, copied to the target in the backup format, started there if it was running, then deleted here.")}</Notice>
          </>
        )}
      </div>
    </Modal>
  );
}

function HADialog({ data, node, vm, onClose }: { data: ClusterData; node: Member; vm: ClusterVM; onClose: () => void }) {
  const cur = data.ha.find((h) => h.name === vm.name);
  const [on, setOn] = useState(!!cur);
  const [pref, setPref] = useState(cur?.preferred ?? "");
  const save = useAction(() => api.put(`/api/vmcluster/ha/${encodeURIComponent(vm.name)}`, { node: node.id, on, preferred: pref }), { success: tr("Saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><HeartPulse size={17} />{tr("High availability of {0}", vm.name)}</span>}
      footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={save.isPending} disabled={on && !data.haPossible} onClick={() => save.mutate()}>{tr("Save")}</Button></>}>
      <div className="space-y-4">
        <Switch checked={on} onChange={setOn} label={tr("Restart on another NAS if {0} fails", node.name)} hint={tr("Its disks must all be on the cluster's Ceph storage. The failed NAS is cut from the disks (fencing) before the VM starts elsewhere, so it never runs twice.")} />
        {on && (
          <Field label={tr("Preferred NAS")} hint={tr("Chosen first when it has enough free memory; otherwise the NAS with the most free memory.")}>
            <Select value={pref} onChange={(e) => setPref(e.target.value)}>
              <option value="">{tr("Any")}</option>
              {data.members.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}
            </Select>
          </Field>
        )}
        {!data.haPossible && <Notice tone="warn">{tr("High availability needs 3 NAS in the cluster at least: with two, neither can tell the failure of the other from its own isolation.")}</Notice>}
        {vm.storage !== "ceph" && on && <Notice tone="warn">{tr("This VM has disks outside Ceph: high availability will be refused.")}</Notice>}
        <div className="text-muted flex items-center gap-1.5 text-[12px]"><Network size={13} />{tr("Use the same bridge names on every NAS so the VM finds its network.")}</div>
      </div>
    </Modal>
  );
}
