import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Box, Copy, History, Pencil, Play, Plus, Power, RotateCcw, Save, Square, Trash2 } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { bytes, dateTime, rate } from "../../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, Switch, Tabs, useUI } from "../../../components/ui";
import { AreaChart } from "../../../components/charts";
import { Terminal } from "../../../components/Terminal";
import { tr } from "../../../lib/i18n";
import { StorageSelect } from "../panels";
import type { Container, LXCArchive, LXCMount, LXCSettings, LXCSnapshot, LXCStats } from "./types";
import { settingsOf } from "./types";
import { backendLabel, lxcState, memLabel, useLXCAction } from "./ContainerCard";
import { FeatureFields, MountFields, NetworkFields } from "./CreateContainer";

export type DetailTab = "summary" | "console" | "resources" | "network" | "mounts" | "snapshots" | "backups" | "options";
const enc = encodeURIComponent;

/** Detail page of a container, inside the Virtual machines app. */
export function ContainerDetail({ name, bridges, tab, setTab, onBack }: { name: string; bridges: string[]; tab: DetailTab; setTab: (t: DetailTab) => void; onBack: () => void }) {
  const q = useQuery({ queryKey: ["lxc", name], queryFn: () => api.get<Container>(`/api/lxc/${enc(name)}`), refetchInterval: 3000 });
  const c = q.data;
  const tabs: { id: DetailTab; label: string }[] = [
    { id: "summary", label: tr("Summary") }, { id: "console", label: tr("Console") }, { id: "resources", label: tr("Resources") }, { id: "network", label: tr("Network") },
    { id: "mounts", label: tr("Shared folders") }, { id: "snapshots", label: tr("Snapshots") }, { id: "backups", label: tr("Backups") }, { id: "options", label: tr("Options") },
  ];
  const [label, tone] = c ? lxcState[c.state] ?? [c.state, "neutral"] : ["", "neutral"];
  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <IconButton title={tr("Back")} onClick={onBack}><ArrowLeft size={17} /></IconButton>
        <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-ok/10 text-ok"><Box size={20} /></div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-[17px] font-semibold">{name}<Badge tone="accent">{tr("Container")}</Badge>{c && <Badge tone={tone as "ok"} dot>{label}</Badge>}</div>
          {c && <div className="text-muted text-[12px]">{c.image.distro} {c.image.release} · {c.privileged ? tr("privileged") : tr("unprivileged")}{c.description && ` · ${c.description}`}</div>}
        </div>
        {c && <Actions c={c} onDeleted={onBack} />}
      </div>
      <ErrorBox error={q.error} />
      {!c ? <Spinner /> : (
        <>
          {c.missing && <div className="mb-4"><Notice tone="warn">{tr("The volume holding this container ({0}) is not available.", c.storage.location)}</Notice></div>}
          <Tabs tabs={tabs} value={tab} onChange={setTab} />
          {tab === "summary" && <Summary c={c} />}
          {tab === "console" && <ConsoleTab c={c} />}
          {tab === "resources" && <Resources c={c} />}
          {tab === "network" && <NetworkTab c={c} bridges={bridges} />}
          {tab === "mounts" && <MountsTab c={c} />}
          {tab === "snapshots" && <SnapshotsTab c={c} />}
          {tab === "backups" && <BackupsTab c={c} />}
          {tab === "options" && <OptionsTab c={c} />}
        </>
      )}
    </div>
  );
}

function Actions({ c, onDeleted }: { c: Container; onDeleted: () => void }) {
  const { confirm } = useUI();
  const act = useLXCAction(c.name);
  const del = useAction(() => api.del(`/api/lxc/${enc(c.name)}`), { success: tr("Container deleted"), onDone: onDeleted });
  const [clone, setClone] = useState(false);
  const [rename, setRename] = useState(false);
  const running = c.state === "running";
  const stopped = c.state === "stopped";
  return (
    <div className="flex flex-wrap items-center gap-2">
      {!running && <Button size="sm" variant="primary" icon={<Play size={14} />} disabled={c.missing} loading={act.isPending} onClick={() => act.mutate("start")}>{tr("Start")}</Button>}
      {running && <Button size="sm" icon={<Power size={14} />} onClick={() => act.mutate("stop")}>{tr("Stop")}</Button>}
      {running && <Button size="sm" icon={<RotateCcw size={14} />} onClick={() => act.mutate("restart")}>{tr("Restart")}</Button>}
      {!stopped && <IconButton title={tr("Force stop")} onClick={async () => (await confirm({ title: tr("Force stop?"), text: tr("The container's processes are killed at once: unsaved data is lost."), confirm: tr("Force"), danger: true })) && act.mutate("kill")}><Square size={15} /></IconButton>}
      <IconButton title={tr("Clone")} disabled={!stopped} onClick={() => setClone(true)}><Copy size={15} /></IconButton>
      <IconButton title={tr("Rename")} disabled={!stopped} onClick={() => setRename(true)}><Pencil size={15} /></IconButton>
      <IconButton title={tr("Delete")} disabled={!stopped} onClick={async () => (await confirm({ title: tr("Delete {0}?", c.name), text: tr("The container, its root file system and its snapshots are deleted. Backups and mounted shared folders are kept."), confirm: tr("Delete"), danger: true, typeToConfirm: c.name })) && del.mutate()}><Trash2 size={15} /></IconButton>
      {clone && <NameDialog title={tr("Clone {0}", c.name)} hint={tr("A copy with its own MAC address and host name, on the same volume.")} action={(n) => api.post(`/api/lxc/${enc(c.name)}/clone`, { name: n })} onClose={() => setClone(false)} />}
      {rename && <NameDialog title={tr("Rename {0}", c.name)} hint={tr("The host name inside changes too.")} initial={c.name} action={(n) => api.post(`/api/lxc/${enc(c.name)}/rename`, { name: n })} onClose={() => setRename(false)} onRenamed={onDeleted} />}
    </div>
  );
}

function NameDialog({ title, hint, initial = "", action, onClose, onRenamed }: { title: string; hint: string; initial?: string; action: (n: string) => Promise<unknown>; onClose: () => void; onRenamed?: () => void }) {
  const [n, setN] = useState(initial);
  const run = useAction(() => action(n), { success: tr("Done"), onDone: () => { onClose(); onRenamed?.(); } });
  return (
    <Modal open onClose={onClose} title={title} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(n) || n === initial} loading={run.isPending} onClick={() => run.mutate()}>{tr("OK")}</Button></>}>
      <ErrorBox error={run.error} />
      <Field label={tr("New name")} hint={hint}><Input value={n} onChange={(e) => setN(e.target.value)} autoFocus /></Field>
    </Modal>
  );
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return <div className="flex gap-3 py-1.5 text-[13px]"><div className="text-muted w-40 shrink-0">{k}</div><div className="min-w-0 break-words">{children}</div></div>;
}

function Summary({ c }: { c: Container }) {
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card title={tr("Container")}>
        <Row k={tr("Image")}>{c.image.distro} {c.image.release} ({c.image.arch}){c.image.build && <span className="text-muted"> · {tr("build {0}", c.image.build)}</span>}</Row>
        <Row k={tr("Created")}>{dateTime(c.created)}</Row>
        <Row k={tr("Isolation")}>{c.privileged ? <Badge tone="warn">{tr("Privileged")}</Badge> : tr("Unprivileged (ids {0}–{1} of the NAS)", c.idmap.base, c.idmap.base + c.idmap.count - 1)}</Row>
        <Row k={tr("Start with the NAS")}>{c.autostart ? tr("Yes, order {0}, then wait {1} s", c.autostartOrder ?? 0, c.autostartDelay ?? 0) : tr("No")}</Row>
        <Row k={tr("Features")}>{[c.features.nesting && tr("nesting"), c.features.keyctl && "keyctl", c.features.fuse && "FUSE", c.features.tun && "TUN"].filter(Boolean).join(", ") || "—"}</Row>
      </Card>
      <Card title={tr("Resources")}>
        <Row k={tr("CPU")}>{c.cpus ? tr("{0} core(s)", c.cpus) : tr("No limit")}</Row>
        <Row k={tr("Memory")}>{memLabel(c.memoryMb)}{c.memoryMb ? ` · ${tr("swap {0}", memLabel(c.swapMb) )}` : ""}</Row>
        <Row k={tr("Storage")}>{backendLabel[c.storage.backend]}{c.storage.sizeGb ? ` · ${tr("{0} GB", c.storage.sizeGb)}` : ""}<div className="text-muted mono text-[11.5px]">{c.storage.base}</div></Row>
        <Row k={tr("Network")}>{c.network.mode === "nat" ? tr("NAT") : c.network.mode === "bridge" ? tr("Bridge {0}", c.network.bridge ?? "") : tr("No network")}{c.network.mode !== "none" && <span className="mono text-muted"> · {c.network.mac}</span>}</Row>
        <Row k={tr("IP address")}><span className="mono">{c.ips.join(", ") || "—"}</span></Row>
        <Row k={tr("Shared folders")}>{(c.mounts ?? []).map((m) => `${m.share} → ${m.target}${m.readOnly ? ` (${tr("read-only")})` : ""}`).join(", ") || "—"}</Row>
      </Card>
    </div>
  );
}

function ConsoleTab({ c }: { c: Container }) {
  const [mode, setMode] = useState<"shell" | "console">("shell");
  const [key, setKey] = useState(0);
  if (c.state !== "running") return <Card><Empty icon={<Box />} title={tr("The container is not running")} text={tr("Start it to open its console.")} /></Card>;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <Select value={mode} onChange={(e) => setMode(e.target.value as "shell" | "console")} className="w-auto">
          <option value="shell">{tr("Shell (root, no password)")}</option>
          <option value="console">{tr("Console (login prompt)")}</option>
        </Select>
        <Button size="sm" icon={<RotateCcw size={14} />} onClick={() => setKey((k) => k + 1)}>{tr("Reconnect")}</Button>
        <span className="text-muted text-[12px]">{mode === "console" ? tr("Ctrl+a then q leaves the console.") : tr("exit or Ctrl+d closes the shell.")}</span>
      </div>
      <Terminal key={`${mode}-${key}`} path={`/api/lxc/${enc(c.name)}/console?mode=${mode}`} height="60vh" />
    </div>
  );
}

type Sample = { t: number; cpu: number; mem: number; rx: number; tx: number; rd: number; wr: number };

function Resources({ c }: { c: Container }) {
  const [hist, setHist] = useState<Sample[]>([]);
  const prev = useRef<LXCStats | null>(null);
  const stats = useQuery({
    queryKey: ["lxc-stats", c.name], queryFn: () => api.get<LXCStats>(`/api/lxc/${enc(c.name)}/stats?disk=1`), refetchInterval: 2000,
  });
  useEffect(() => {
    const s = stats.data;
    if (!s) return;
    const p = prev.current;
    prev.current = s;
    if (!p || s.state !== "running") return;
    const dt = (new Date(s.time).getTime() - new Date(p.time).getTime()) / 1000;
    if (dt <= 0) return;
    const d = (a: number, b: number) => Math.max(0, a - b) / dt;
    setHist((h) => [...h.slice(-89), { t: Date.now(), cpu: d(s.cpuUsec, p.cpuUsec) / 1e4, mem: s.memory, rx: d(s.netRx, p.netRx), tx: d(s.netTx, p.netTx), rd: d(s.diskRead, p.diskRead), wr: d(s.diskWrite, p.diskWrite) }]);
  }, [stats.data]);
  const s = stats.data;
  const cpuMax = (s?.cpuLimit || s?.cores || 1) * 100;
  return (
    <div className="space-y-4">
      {c.state !== "running" && <Notice>{tr("The graphs fill in while the container runs.")}</Notice>}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card title={tr("CPU")}><AreaChart series={[{ label: tr("CPU (% of one core)"), color: "var(--accent)", values: hist.map((x) => x.cpu) }]} max={cpuMax} format={(v) => `${v.toFixed(0)} %`} /></Card>
        <Card title={tr("Memory")}><AreaChart series={[{ label: tr("Used"), color: "var(--ok)", values: hist.map((x) => x.mem) }]} max={s?.memoryMax || undefined} format={(v) => bytes(v)} />
          {s && <div className="text-muted mt-2 text-[12px]">{tr("Limit {0} · swap {1} of {2} · {3} processes", s.memoryMax ? bytes(s.memoryMax) : tr("none"), bytes(s.swap), s.swapMax ? bytes(s.swapMax) : tr("no limit"), s.pids)}</div>}</Card>
        <Card title={tr("Network")}><AreaChart series={[{ label: tr("Received"), color: "var(--accent)", values: hist.map((x) => x.rx) }, { label: tr("Sent"), color: "var(--warn)", values: hist.map((x) => x.tx) }]} format={rate} /></Card>
        <Card title={tr("Disk")}><AreaChart series={[{ label: tr("Read"), color: "var(--accent)", values: hist.map((x) => x.rd) }, { label: tr("Written"), color: "var(--danger)", values: hist.map((x) => x.wr) }]} format={rate} />
          {s && <div className="text-muted mt-2 text-[12px]">{tr("Root file system: {0} used{1}", bytes(s.diskUsed), s.diskLimit ? ` / ${bytes(s.diskLimit)}` : "")}</div>}</Card>
      </div>
      <Limits c={c} />
    </div>
  );
}

function useSave(c: Container) {
  const { toast } = useUI();
  return useAction((set: Partial<LXCSettings>) => api.put<{ restart: boolean }>(`/api/lxc/${enc(c.name)}`, { ...settingsOf(c), ...set }), {
    onDone: (r) => toast(r?.restart ? tr("Saved: restart the container to apply everything") : tr("Saved")),
  });
}

function Limits({ c }: { c: Container }) {
  const [f, setF] = useState({ cpus: c.cpus ?? 0, memoryMb: c.memoryMb ?? 0, swapMb: c.swapMb ?? 0, sizeGb: c.storage.sizeGb ?? 0 });
  const save = useSave(c);
  return (
    <Card title={tr("Limits")}>
      <ErrorBox error={save.error} />
      <div className="grid gap-4 sm:grid-cols-4">
        <Field label={tr("CPU limit (cores)")} hint={tr("0: no limit")}><Input type="number" min={0} step={0.5} value={f.cpus} onChange={(e) => setF({ ...f, cpus: +e.target.value })} /></Field>
        <Field label={tr("Memory (MiB)")} hint={tr("0: no limit")}><Input type="number" min={0} step={256} value={f.memoryMb} onChange={(e) => setF({ ...f, memoryMb: +e.target.value })} /></Field>
        <Field label={tr("Swap (MiB)")}><Input type="number" min={0} step={256} disabled={!f.memoryMb} value={f.swapMb} onChange={(e) => setF({ ...f, swapMb: +e.target.value })} /></Field>
        <Field label={tr("Disk size (GB)")} hint={c.storage.backend === "image" ? tr("Can only grow") : c.storage.backend === "dir" ? tr("A folder has no size limit") : undefined}><Input type="number" min={1} disabled={c.storage.backend === "dir"} value={f.sizeGb || ""} onChange={(e) => setF({ ...f, sizeGb: +e.target.value })} /></Field>
      </div>
      <div className="mt-3 flex items-center gap-3"><Button variant="primary" icon={<Save size={14} />} loading={save.isPending} onClick={() => save.mutate(f)}>{tr("Apply")}</Button><span className="text-muted text-[12px]">{tr("Applied at once to a running container.")}</span></div>
    </Card>
  );
}

function NetworkTab({ c, bridges }: { c: Container; bridges: string[] }) {
  const [n, setN] = useState(c.network);
  const save = useSave(c);
  return (
    <Card>
      <ErrorBox error={save.error} />
      <NetworkFields n={n} set={setN} bridges={bridges} />
      <div className="mt-4 flex items-center gap-3"><Button variant="primary" icon={<Save size={14} />} loading={save.isPending} onClick={() => save.mutate({ network: n })}>{tr("Save")}</Button><span className="text-muted text-[12px]">{tr("Applied at the next start of the container.")}</span></div>
      {c.ips.length > 0 && <div className="text-muted mt-3 text-[12.5px]">{tr("Current addresses: {0}", c.ips.join(", "))}</div>}
    </Card>
  );
}

function MountsTab({ c }: { c: Container }) {
  const [mounts, setMounts] = useState<LXCMount[]>(settingsOf(c).mounts);
  const [mapUsers, setMapUsers] = useState(c.mapUsers);
  const [mapGroups, setMapGroups] = useState(c.mapGroups);
  const save = useSave(c);
  return (
    <Card>
      <ErrorBox error={save.error} />
      <MountFields mounts={mounts} set={setMounts} mapUsers={mapUsers} setMapUsers={setMapUsers} mapGroups={mapGroups} setMapGroups={setMapGroups} privileged={!!c.privileged} />
      <div className="mt-4 flex items-center gap-3"><Button variant="primary" icon={<Save size={14} />} loading={save.isPending} onClick={() => save.mutate({ mounts, mapUsers, mapGroups })}>{tr("Save")}</Button><span className="text-muted text-[12px]">{tr("Applied at the next start of the container.")}</span></div>
    </Card>
  );
}

function OptionsTab({ c }: { c: Container }) {
  const s = settingsOf(c);
  const [f, setF] = useState({ description: s.description, autostart: s.autostart, autostartOrder: s.autostartOrder, autostartDelay: s.autostartDelay, features: s.features });
  const save = useSave(c);
  return (
    <Card>
      <ErrorBox error={save.error} />
      <div className="space-y-5">
        <Field label={tr("Description")}><Input value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></Field>
        <div className="grid items-end gap-4 sm:grid-cols-3">
          <Switch checked={f.autostart} onChange={(v) => setF({ ...f, autostart: v })} label={tr("Start with the NAS")} />
          <Field label={tr("Start order")} hint={tr("Lower numbers start first")}><Input type="number" min={0} max={999} disabled={!f.autostart} value={f.autostartOrder} onChange={(e) => setF({ ...f, autostartOrder: +e.target.value })} /></Field>
          <Field label={tr("Delay after it (s)")}><Input type="number" min={0} max={3600} disabled={!f.autostart} value={f.autostartDelay} onChange={(e) => setF({ ...f, autostartDelay: +e.target.value })} /></Field>
        </div>
        <FeatureFields f={f.features} set={(x) => setF({ ...f, features: x })} privileged={!!c.privileged} />
        <Button variant="primary" icon={<Save size={14} />} loading={save.isPending} onClick={() => save.mutate(f)}>{tr("Save")}</Button>
      </div>
    </Card>
  );
}

function SnapshotsTab({ c }: { c: Container }) {
  const { confirm } = useUI();
  const base = `/api/lxc/${enc(c.name)}/snapshots`;
  const list = useQuery({ queryKey: ["lxc-snapshots", c.name], queryFn: () => api.get<LXCSnapshot[]>(base), refetchInterval: 5000 });
  const [f, setF] = useState({ name: "", description: "" });
  const create = useAction(() => api.post(base, f), { onDone: () => setF({ name: "", description: "" }) });
  const restore = useAction((s: string) => api.post(`${base}/${enc(s)}/restore`));
  const del = useAction((s: string) => api.del(`${base}/${enc(s)}`), { success: tr("Snapshot deleted") });
  const stopped = c.state === "stopped";
  return (
    <div className="space-y-4">
      <ErrorBox error={list.error || create.error || restore.error || del.error} />
      <div className="grid gap-3 rounded-xl border border-line p-4 sm:grid-cols-[1fr_2fr_auto] sm:items-end">
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="before-upgrade" /></Field>
        <Field label={tr("Description")}><Input value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></Field>
        <Button variant="primary" icon={<Plus size={14} />} disabled={!f.name} loading={create.isPending} onClick={() => create.mutate()}>{tr("Take")}</Button>
      </div>
      <Notice>{tr("A snapshot saves the root file system (not the memory, nor the mounted shared folders). Restoring needs the container stopped and keeps its current settings.")}{c.storage.backend === "zfs" && ` ${tr("On ZFS, restoring a snapshot deletes the snapshots taken after it.")}`}</Notice>
      {list.isLoading ? <Spinner /> : !list.data?.length ? <Empty icon={<History />} title={tr("No snapshots")} /> : (
        <div className="divide-y divide-line rounded-xl border border-line">
          {[...list.data].reverse().map((s) => (
            <div key={s.name} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 font-medium">{s.name}<Badge>{s.running ? tr("taken while running") : tr("taken stopped")}</Badge></div>
                <div className="text-muted text-[12px]">{dateTime(s.created)}{s.description && ` · ${s.description}`}</div>
              </div>
              <Button size="sm" icon={<RotateCcw size={14} />} disabled={!stopped} onClick={async () => (await confirm({ title: tr("Restore {0}?", s.name), text: tr("Changes made in the container since this snapshot are lost."), confirm: tr("Restore"), danger: true })) && restore.mutate(s.name)}>{tr("Restore")}</Button>
              <IconButton title={tr("Delete")} onClick={async () => (await confirm({ title: tr("Delete snapshot {0}?", s.name), text: tr("The container keeps its current state."), confirm: tr("Delete"), danger: true })) && del.mutate(s.name)}><Trash2 size={15} /></IconButton>
            </div>
          ))}
        </div>
      )}
      {!stopped && <div className="text-muted text-[12px]">{tr("Stop the container to restore a snapshot.")}</div>}
    </div>
  );
}

function BackupsTab({ c }: { c: Container }) {
  const [dir, setDir] = useState("");
  const [mode, setMode] = useState("");
  const [keep, setKeep] = useState(5);
  const list = useQuery({ queryKey: ["lxc-backups", c.name, dir], queryFn: () => api.get<LXCArchive[]>(`/api/lxc/${enc(c.name)}/backups?dir=${enc(dir)}`), enabled: !!dir, refetchInterval: 5000 });
  const run = useAction(() => api.post(`/api/lxc/${enc(c.name)}/backup`, { dir, mode, keep }));
  return (
    <div className="space-y-4">
      <Card title={tr("Back up now")}>
        <ErrorBox error={run.error} />
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={tr("Folder")} hint={tr("A .tar.zst archive is written there")}><StorageSelect value={dir} onChange={setDir} /></Field>
          <Field label={tr("Running container")}>
            <Select value={mode} onChange={(e) => setMode(e.target.value)}>
              <option value="">{c.storage.backend === "dir" ? tr("Copy it while it runs") : tr("From a snapshot (consistent, no downtime)")}</option>
              <option value="stop">{tr("Stop it during the copy")}</option>
              <option value="live">{tr("Copy it while it runs")}</option>
            </Select>
          </Field>
          <Field label={tr("Backups kept")} hint={tr("Older ones in this folder are removed")}><Input type="number" min={1} value={keep} onChange={(e) => setKeep(+e.target.value)} /></Field>
        </div>
        <div className="mt-3"><Button variant="primary" icon={<Save size={14} />} disabled={!dir} loading={run.isPending} onClick={() => run.mutate()}>{tr("Back up")}</Button></div>
        <div className="text-muted mt-3 text-[12px]">{tr("For scheduled backups, add the container to a backup task (Control Panel → Backups), next to the folders and VMs. Restore a backup with Create → Container (LXC) → Import a backup.")}</div>
      </Card>
      {dir && (list.data?.length ? (
        <div className="divide-y divide-line rounded-xl border border-line">
          {list.data.map((a) => <div key={a.path} className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]"><div className="min-w-0 flex-1 mono truncate">{a.path}</div><span className="text-muted">{dateTime(a.created)} · {bytes(a.size)}</span></div>)}
        </div>
      ) : <Empty icon={<History />} title={tr("No backups in this folder")} />)}
    </div>
  );
}
