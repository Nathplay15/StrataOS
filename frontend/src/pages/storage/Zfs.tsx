import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Boxes, Camera, Download, FolderPlus, Plus, RotateCcw, ShieldCheck, Trash2, X, Lock } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { ZDataset, ZPool } from "../../lib/types";
import { bytes, cx, dateTime, parseSize, pct } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Switch, Tabs, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";
import { tr } from "../../lib/i18n";

type VDev = { type: string; disks: string[] };
const vdevTypes = [
  { id: "mirror", label: tr("Mirror"), min: 2, hint: tr("n-1 failures tolerated per vdev") },
  { id: "raidz1", label: "RAIDZ1", min: 3, hint: tr("1 failure tolerated") },
  { id: "raidz2", label: "RAIDZ2", min: 4, hint: tr("2 failures tolerated") },
  { id: "raidz3", label: "RAIDZ3", min: 5, hint: tr("3 failures tolerated") },
  { id: "stripe", label: tr("Stripe"), min: 1, hint: tr("no redundancy") },
];

function CreatePool({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("tank");
  const [vdevs, setVdevs] = useState<VDev[]>([{ type: "mirror", disks: [] }]);
  const [cache, setCache] = useState<string[]>([]);
  const [log, setLog] = useState<string[]>([]);
  const [spares, setSpares] = useState<string[]>([]);
  const [compression, setCompression] = useState("lz4");
  const [encryption, setEncryption] = useState(false);
  const [passphrase, setPassphrase] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/zfs", { name, vdevs, cache, log, spares, compression, encryption, passphrase }), { onDone: onClose });
  const used = (except: string[]) => [...vdevs.flatMap((v) => v.disks), ...cache, ...log, ...spares].filter((d) => !except.includes(d));
  const valid = name && vdevs.every((v) => v.disks.length >= (vdevTypes.find((t) => t.id === v.type)?.min ?? 1)) && (!encryption || passphrase.length >= 8);
  const submit = async () => {
    if (await confirm({ title: tr("Create pool {0}?", name), text: <>{tr("All selected disks will be")}{" "}<b>{tr("erased")}</b>.</>, danger: true, confirm: tr("Create pool") })) create.mutate();
  };
  return (
    <Modal open onClose={onClose} title={tr("New ZFS pool")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" onClick={submit} loading={create.isPending} disabled={!valid}>{tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Pool name")} hint={tr("Mounted at /srv/strata/{0}", name)}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label={tr("Compression")}>
            <Select value={compression} onChange={(e) => setCompression(e.target.value)}>
              <option value="lz4">{tr("lz4 (recommended)")}</option><option value="zstd">zstd</option><option value="gzip">gzip</option><option value="off">{tr("disabled")}</option>
            </Select>
          </Field>
        </div>
        {vdevs.map((v, i) => {
          const t = vdevTypes.find((x) => x.id === v.type)!;
          return (
            <div key={i} className="rounded-xl border border-line p-4">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <span className="font-medium">vdev {i + 1}</span>
                <div className="flex flex-wrap gap-1">
                  {vdevTypes.map((x) => (
                    <button key={x.id} type="button" onClick={() => setVdevs(vdevs.map((y, k) => (k === i ? { ...y, type: x.id } : y)))} className={cx("rounded-lg px-2.5 py-1 text-[12px]", v.type === x.id ? "bg-accent text-white" : "bg-panel-2 text-muted hover:text-fg")}>{x.label}</button>
                  ))}
                </div>
                <span className="text-muted text-[12px]">min. {t.min} {" "}{tr("disks ·")}{" "}{t.hint}</span>
                <div className="flex-1" />
                {vdevs.length > 1 && <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => setVdevs(vdevs.filter((_, k) => k !== i))} />}
              </div>
              <DiskPicker value={v.disks} onChange={(d) => setVdevs(vdevs.map((y, k) => (k === i ? { ...y, disks: d } : y)))} exclude={used(v.disks)} />
            </div>
          );
        })}
        <Button size="sm" icon={<Plus size={14} />} onClick={() => setVdevs([...vdevs, { type: vdevs[0].type, disks: [] }])}>{tr("Add a vdev (striping)")}</Button>
        <Switch checked={encryption} onChange={setEncryption} label={tr("Native AES-256-GCM encryption")} hint={tr("The passphrase will be required after each reboot to unlock the pool.")} />
        {encryption && <Field label={tr("Passphrase")}><Input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} /></Field>}
        <Switch checked={advanced} onChange={setAdvanced} label={tr("Special devices (L2ARC cache, SLOG log, spare)")} />
        {advanced && (
          <div className="space-y-4">
            <div><div className="mb-2 text-[12.5px] font-medium text-muted">{tr("L2ARC cache (SSD)")}</div><DiskPicker value={cache} onChange={setCache} exclude={used(cache)} /></div>
            <div><div className="mb-2 text-[12.5px] font-medium text-muted">{tr("SLOG log (fast SSD)")}</div><DiskPicker value={log} onChange={setLog} exclude={used(log)} /></div>
            <div><div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Spare disks")}</div><DiskPicker value={spares} onChange={setSpares} exclude={used(spares)} /></div>
          </div>
        )}
      </div>
    </Modal>
  );
}

function Datasets({ pool }: { pool: ZPool }) {
  const { confirm } = useUI();
  const [form, setForm] = useState<{ name: string; compression: string; quota: string } | null>(null);
  const create = useAction(() => api.post("/api/storage/zfs-datasets", { name: `${pool.name}/${form!.name}`, compression: form!.compression, quota: form!.quota ? parseSize(form!.quota) : 0 }), { success: tr("Dataset created"), onDone: () => setForm(null) });
  const destroy = useAction((n: string) => api.del(`/api/storage/zfs-datasets${q({ name: n, recursive: 1 })}`), { success: tr("Dataset deleted") });
  const snap = useAction((n: string) => api.post("/api/storage/zfs-snapshots", { dataset: n }), { success: tr("Snapshot created") });
  return (
    <div>
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[12.5px] font-medium text-muted">{tr("Datasets")}</div>
        <Button size="sm" icon={<FolderPlus size={14} />} onClick={() => setForm({ name: "", compression: "", quota: "" })}>{tr("New dataset")}</Button>
      </div>
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="table">
          <thead><tr><th>{tr("Name")}</th><th>{tr("Used")}</th><th>{tr("Available")}</th><th>{tr("Compression")}</th><th>{tr("Quota")}</th><th>{tr("Mount point")}</th><th /></tr></thead>
          <tbody>
            {pool.datasets?.map((d) => (
              <tr key={d.name}>
                <td className="mono">{d.name}{d.type === "volume" && <Badge>zvol</Badge>}</td>
                <td>{bytes(d.used)}</td><td>{bytes(d.avail)}</td><td>{d.compression}</td><td>{d.quota ? bytes(d.quota) : "—"}</td>
                <td className="mono text-muted">{d.mountpoint}</td>
                <td className="text-right whitespace-nowrap">
                  <Button size="sm" variant="ghost" icon={<Camera size={13} />} onClick={() => snap.mutate(d.name)}>{tr("Snapshot")}</Button>
                  {d.name.includes("/") && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={async () => { if (await confirm({ title: tr("Destroy {0}?", d.name), text: tr("The dataset, its children and its snapshots will be destroyed."), danger: true, typeToConfirm: d.name })) destroy.mutate(d.name); }} />}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Modal open={!!form} onClose={() => setForm(null)} title={tr("New dataset")} footer={<>
        <Button onClick={() => setForm(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!form?.name} loading={create.isPending} onClick={() => create.mutate()}>{tr("Create")}</Button>
      </>}>
        {form && (
          <div className="space-y-4">
            <Field label={tr("Name")} hint={`${pool.name}/${form.name || "…"}`}><Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Field>
            <Field label={tr("Compression")}><Select value={form.compression} onChange={(e) => setForm({ ...form, compression: e.target.value })}><option value="">{tr("Inherited")}</option><option value="lz4">lz4</option><option value="zstd">zstd</option><option value="off">{tr("disabled")}</option></Select></Field>
            <Field label={tr("Quota")} hint={tr("e.g. 500G, 2T — empty for none")}><Input value={form.quota} onChange={(e) => setForm({ ...form, quota: e.target.value })} /></Field>
          </div>
        )}
      </Modal>
    </div>
  );
}

function PoolCard({ p }: { p: ZPool }) {
  const { confirm } = useUI();
  const [showStatus, setShowStatus] = useState(false);
  const [add, setAdd] = useState<string | null>(null);
  const [dev, setDev] = useState<string[]>([]);
  const [replace, setReplace] = useState<string | null>(null);
  const act = useAction((a: { action: string; device?: string; newDevice?: string }) => api.post(`/api/storage/zfs/${p.name}/${a.action}`, { device: a.device ?? "", newDevice: a.newDevice ?? "" }), { success: tr("Operation completed") });
  const destroy = useAction(() => api.del(`/api/storage/zfs/${p.name}`), { success: tr("Pool destroyed") });
  return (
    <Card title={<span className="flex items-center gap-2.5"><Boxes size={17} className="text-accent" />{p.name} <Health value={p.health === "ONLINE" ? "healthy" : p.health.toLowerCase()} /></span>} actions={<>
      <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => act.mutate({ action: "scrub" })}>{tr("Scrub")}</Button>
      <Select className="!w-auto !py-1 !text-[12.5px]" value="" onChange={(e) => { const v = e.target.value; if (v.startsWith("add-")) setAdd(v); else if (v) act.mutate({ action: v }); }}>
        <option value="">{tr("More…")}</option>
        <option value="add-spare">{tr("Add a spare")}</option><option value="add-cache">{tr("Add a cache")}</option><option value="add-log">{tr("Add a log device")}</option>
        <option value="clear">{tr("Clear errors")}</option><option value="trim">TRIM</option><option value="scrub-stop">{tr("Stop scrub")}</option><option value="export">{tr("Export")}</option>
      </Select>
      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Destroy pool {0}?", p.name), text: tr("All data in the pool will be permanently lost."), danger: true, typeToConfirm: p.name })) destroy.mutate(); }} />
    </>}>
      <div className="grid gap-4 sm:grid-cols-4">
        <div><div className="text-muted text-[12px]">{tr("Size")}</div><div className="font-semibold">{bytes(p.size)}</div></div>
        <div><div className="text-muted text-[12px]">{tr("Allocated")}</div><div className="font-semibold">{bytes(p.alloc)}</div></div>
        <div><div className="text-muted text-[12px]">{tr("Fragmentation")}</div><div className="font-semibold">{p.fragmentation}%</div></div>
        <div><div className="text-muted text-[12px]">{tr("Deduplication")}</div><div className="font-semibold">{p.dedup}</div></div>
      </div>
      <Progress className="mt-4" value={pct(p.alloc, p.size)} />
      {!!p.problems?.length && (
        <div className="mt-4 space-y-2">
          {p.problems.map((d) => (
            <div key={d.device} className="flex flex-wrap items-center gap-3 rounded-xl border border-danger/30 bg-danger/10 px-4 py-3 text-[13px]">
              <Badge tone="danger">{d.state}</Badge>
              <span className="mono min-w-0 flex-1 truncate">{d.was || d.device}</span>
              <Button size="sm" variant="primary" onClick={() => { setDev([]); setReplace(d.device); }}>{tr("Replace")}</Button>
            </div>
          ))}
        </div>
      )}
      <button className="mt-4 text-accent text-[12.5px]" onClick={() => setShowStatus(!showStatus)}>{showStatus ? tr("Hide") : tr("Show")} {" "}{tr("the topology and detailed status")}</button>
      {showStatus && <pre className="mono mt-2 overflow-x-auto rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{p.status}</pre>}
      <div className="mt-5"><Datasets pool={p} /></div>
      <Modal open={!!replace} onClose={() => setReplace(null)} title={tr("Replace a disk")} footer={<>
        <Button onClick={() => setReplace(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!dev.length} loading={act.isPending} onClick={() => { act.mutate({ action: "replace", device: replace!, newDevice: dev[0] }); setReplace(null); setDev([]); }}>{tr("Replace and rebuild")}</Button>
      </>}>
        <div className="space-y-3">
          <Notice>{tr("The new disk will be wiped, then ZFS will copy the data over (resilver). The pool remains usable during the operation.")}</Notice>
          <DiskPicker value={dev} onChange={setDev} single />
        </div>
      </Modal>
      <Modal open={!!add} onClose={() => setAdd(null)} title={tr("Add a device to the pool")} footer={<>
        <Button onClick={() => setAdd(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!dev.length} onClick={() => { act.mutate({ action: add!, device: dev[0] }); setAdd(null); setDev([]); }}>{tr("Add")}</Button>
      </>}>
        <DiskPicker value={dev} onChange={setDev} single />
      </Modal>
    </Card>
  );
}

function Snapshots() {
  const { confirm } = useUI();
  const { data, isLoading } = useQuery({ queryKey: ["zfs-snapshots"], queryFn: () => api.get<ZDataset[]>("/api/storage/zfs-snapshots") });
  const destroy = useAction((n: string) => api.del(`/api/storage/zfs-datasets${q({ name: n })}`), { success: tr("Snapshot deleted") });
  const rollback = useAction((n: string) => api.post("/api/storage/zfs-snapshots/rollback", { snapshot: n }), { success: tr("Restore completed") });
  if (isLoading) return <Spinner />;
  if (!data?.length) return <Card><Empty icon={<Camera />} title={tr("No snapshots")} text={tr("Create snapshots from the dataset list. They are instant and only consume the differences.")} /></Card>;
  return (
    <Card pad={false}>
      <div className="overflow-x-auto">
        <table className="table">
          <thead><tr><th>{tr("Snapshot")}</th><th>{tr("Created on")}</th><th>{tr("Used")}</th><th>{tr("Referenced")}</th><th /></tr></thead>
          <tbody>
            {data.map((s) => (
              <tr key={s.name}>
                <td className="mono">{s.name}</td><td>{dateTime((s.creation ?? 0) * 1000)}</td><td>{bytes(s.used)}</td><td>{bytes(s.refer)}</td>
                <td className="text-right whitespace-nowrap">
                  <Button size="sm" variant="ghost" icon={<RotateCcw size={13} />} onClick={async () => { if (await confirm({ title: tr("Restore this snapshot?"), text: tr("The dataset will revert to this state. Newer changes and snapshots will be lost."), danger: true, confirm: tr("Restore") })) rollback.mutate(s.name); }}>{tr("Restore")}</Button>
                  {s.lockedUntil ? <span className="inline-flex items-center gap-1 px-2 text-[12px] text-muted" title={tr("Locked: cannot be deleted before {0}", dateTime(s.lockedUntil * 1000))}><Lock size={13} />{dateTime(s.lockedUntil * 1000)}</span> : <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={() => destroy.mutate(s.name)} />}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

export default function Zfs() {
  const [tab, setTab] = useState<"pools" | "snapshots">("pools");
  const [create, setCreate] = useState(false);
  const [imp, setImp] = useState(false);
  const { data, isLoading, error } = useQuery({ queryKey: ["zfs"], queryFn: () => api.get<{ available: boolean; broken?: boolean; pools: ZPool[] }>("/api/storage/zfs"), refetchInterval: 10000 });
  const importable = useQuery({ queryKey: ["zfs-importable"], queryFn: () => api.get<{ name: string; id: string; state: string }[]>("/api/storage/zfs/importable"), enabled: imp });
  const doImport = useAction((n: string) => api.post("/api/storage/zfs/import", { name: n }), { success: tr("Pool imported"), onDone: () => setImp(false) });
  const install = useAction(() => api.post("/api/system/components/zfs/install"));

  if (isLoading) return <Spinner />;
  return (
    <div>
      <PageHeader title="ZFS" subtitle={tr("OpenZFS pools, datasets and snapshots")} icon={<Boxes size={20} />} actions={data?.available && <>
        <Button icon={<Download size={15} />} onClick={() => setImp(true)}>{tr("Import")}</Button>
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Create a pool")}</Button>
      </>} />
      <ErrorBox error={error} />
      {!data?.available ? (
        <Card>
          {data?.broken ? (
            <Empty icon={<Boxes />} title={tr("The ZFS module fails to load")} text={tr("The ZFS tools are installed but the kernel module is missing, usually after a kernel update. Repair rebuilds the module; your pools will then be re-imported.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Repair ZFS")}</Button>} />
          ) : (
            <Empty icon={<Boxes />} title={tr("ZFS is not installed")} text={tr("OpenZFS is available in Debian's contrib section. Installation compiles the kernel module (DKMS) and may take several minutes.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install ZFS")}</Button>} />
          )}
        </Card>
      ) : (
        <>
          <Tabs value={tab} onChange={setTab} tabs={[{ id: "pools", label: tr("Pools ({0})", data.pools.length) }, { id: "snapshots", label: tr("Snapshots") }]} />
          {tab === "pools" ? (
            !data.pools.length ? <Card><Empty icon={<Boxes />} title={tr("No ZFS pools")} text={tr("A ZFS pool combines disks, redundancy (mirror, RAIDZ), data integrity, compression and snapshots.")} action={<Button variant="primary" onClick={() => setCreate(true)}>{tr("Create a pool")}</Button>} /></Card>
              : <div className="space-y-5">{data.pools.map((p) => <PoolCard key={p.name} p={p} />)}</div>
          ) : <Snapshots />}
        </>
      )}
      {create && <CreatePool onClose={() => setCreate(false)} />}
      <Modal open={imp} onClose={() => setImp(false)} title={tr("Import an existing pool")}>
        {importable.isLoading ? <Spinner /> : !importable.data?.length ? <Notice>{tr("No importable pools detected on the disks.")}</Notice> : (
          <div className="space-y-2">
            {importable.data.map((p) => (
              <div key={p.id} className="flex items-center justify-between rounded-xl bg-panel-2 px-4 py-3">
                <div><b>{p.name}</b> <span className="text-muted text-[12px]">{p.id} · {p.state}</span></div>
                <Button size="sm" variant="primary" loading={doImport.isPending} onClick={() => doImport.mutate(p.name)}>{tr("Import")}</Button>
              </div>
            ))}
          </div>
        )}
      </Modal>
    </div>
  );
}
