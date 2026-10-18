import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Database, FolderPlus, Import, Plus, Plug, RefreshCw, ShieldCheck, Trash2, Lock } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { BlockDevice, BtrfsInfo, QuotaTarget, Volume } from "../../lib/types";
import { bytes, cx, pct, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Switch, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";
import { tr } from "../../lib/i18n";

const btrfsProfiles = [
  { id: "single", label: tr("Single (no redundancy)"), min: 1 },
  { id: "raid0", label: tr("RAID 0 (striping)"), min: 2 },
  { id: "raid1", label: tr("RAID 1 (2 copies)"), min: 2 },
  { id: "raid1c3", label: tr("RAID 1C3 (3 copies)"), min: 3 },
  { id: "raid1c4", label: tr("RAID 1C4 (4 copies)"), min: 4 },
  { id: "raid10", label: tr("RAID 10"), min: 4 },
  { id: "raid5", label: tr("RAID 5 (experimental)"), min: 2 },
  { id: "raid6", label: tr("RAID 6 (experimental)"), min: 3 },
];

function CreateModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("volume1");
  const [fsType, setFsType] = useState("ext4");
  const [devices, setDevices] = useState<string[]>([]);
  const [dataProfile, setDataProfile] = useState("raid1");
  const [compression, setCompression] = useState(true);
  const [cache, setCache] = useState<{ on: boolean; dev: string[]; mode: string }>({ on: false, dev: [], mode: "writethrough" });
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/volumes", { name, fsType, devices, dataProfile: fsType === "btrfs" ? (devices.length > 1 ? dataProfile : "single") : "", compression, cacheDevice: cache.on ? cache.dev[0] : undefined, cacheMode: cache.on ? cache.mode : undefined }), { onDone: onClose });
  const multi = fsType === "btrfs";
  const submit = async () => {
    if (await confirm({ title: tr("Create the volume?"), text: <>{tr("The contents of")}{" "}{devices.join(", ")} sera <b>{tr("erased")}</b> {" "}{tr("and formatted as")}{" "}{fsType}.</>, danger: true, confirm: tr("Format and create") })) create.mutate();
  };
  return (
    <Modal open onClose={onClose} title={tr("New volume")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" onClick={submit} loading={create.isPending} disabled={!devices.length || !name || (cache.on && cache.dev.length !== 1)}>{tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Name")} hint={tr("Mounted at /srv/strata/{0}", name || "…")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label={tr("File system")}>
            <Select value={fsType} onChange={(e) => { setFsType(e.target.value); setDevices([]); }}>
              <option value="ext4">{tr("ext4 — robust and universal")}</option>
              <option value="xfs">{tr("XFS — very large files, high performance")}</option>
              <option value="btrfs">{tr("Btrfs — snapshots, compression, built-in RAID")}</option>
            </Select>
          </Field>
        </div>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{multi ? tr("Disks or devices (Btrfs manages RAID itself)") : tr("Device (disk, RAID array or LVM logical volume)")}</div>
          <DiskPicker value={devices} onChange={setDevices} includeVirtual single={!multi} />
        </div>
        {multi && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Data profile")} hint={tr("Metadata automatically uses a redundant profile.")}>
              <Select value={devices.length > 1 ? dataProfile : "single"} disabled={devices.length < 2} onChange={(e) => setDataProfile(e.target.value)}>
                {btrfsProfiles.filter((p) => p.min <= Math.max(devices.length, 1)).map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
              </Select>
            </Field>
            <div className="pt-6"><Switch checked={compression} onChange={setCompression} label={tr("zstd compression")} hint={tr("Saves space, minimal CPU cost")} /></div>
          </div>
        )}
        {devices.length === 1 && (
          <div className="space-y-3 rounded-xl border border-line p-4">
            <Switch checked={cache.on} onChange={(v) => setCache({ ...cache, on: v })} label={tr("SSD cache")} hint={tr("A free SSD speeds up the volume (frequently read data is kept on it). StrataOS creates the required LVM layer itself.")} />
            {cache.on && <>
              <DiskPicker value={cache.dev} onChange={(v) => setCache({ ...cache, dev: v.slice(-1) })} includeVirtual single exclude={devices} />
              <Select value={cache.mode} onChange={(e) => setCache({ ...cache, mode: e.target.value })}>
                <option value="writethrough">{tr("Write-through (recommended): speeds up reads, risk-free")}</option>
                <option value="writeback">{tr("Write-back: also speeds up writes")}</option>
              </Select>
              {cache.mode === "writeback" && <Notice tone="warn">{tr("With write-back, data not yet copied to the disks is lost if the SSD fails. Use a reliable SSD, ideally mirrored.")}</Notice>}
            </>}
          </div>
        )}
        {multi && (dataProfile === "raid5" || dataProfile === "raid6") && devices.length > 1 && <Notice tone="warn">{tr("Btrfs RAID 5/6 is still not recommended in production. Prefer Btrfs RAID 1/10, mdadm or ZFS.")}</Notice>}
      </div>
    </Modal>
  );
}

function ImportModal({ onClose }: { onClose: () => void }) {
  const { data } = useQuery({ queryKey: ["block"], queryFn: () => api.get<BlockDevice[]>("/api/storage/block") });
  const [sel, setSel] = useState("");
  const [name, setName] = useState("");
  const imp = useAction(() => api.post("/api/storage/volumes/import", { name, device: sel }), { success: tr("Volume imported"), onDone: onClose });
  const found: BlockDevice[] = [];
  const walk = (b: BlockDevice, sys: boolean) => {
    const isSys = sys || b.mountpoints.some((m) => ["/", "/boot", "/boot/efi", "[SWAP]"].includes(m));
    if (["ext4", "ext3", "ext2", "xfs", "btrfs", "exfat", "vfat", "ntfs"].includes(b.fsType) && b.mountpoints.length === 0 && !isSys) found.push(b);
    b.children?.forEach((c) => walk(c, isSys));
  };
  data?.forEach((d) => walk(d, d.children?.some((c) => c.mountpoints.includes("/")) ?? false));
  return (
    <Modal open onClose={onClose} title={tr("Import an existing filesystem")} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" disabled={!sel || !name} loading={imp.isPending} onClick={() => imp.mutate()}>{tr("Import")}</Button>
    </>}>
      <div className="space-y-4">
        <Notice>{tr("Mount a disk that already contains data (USB drive, old NAS…) without formatting it.")}</Notice>
        {found.length === 0 ? <div className="text-muted text-[13px]">{tr("No unmounted file systems detected.")}</div> : (
          <div className="space-y-2">
            {found.map((b) => (
              <button key={b.path} type="button" onClick={() => { setSel(b.path); setName((b.label || b.name).replace(/[^A-Za-z0-9_-]/g, "_")); }} className={cx("flex w-full justify-between rounded-xl border px-3 py-2.5 text-left", sel === b.path ? "border-accent bg-accent-soft" : "border-line bg-panel-2")}>
                <span><b className="mono">{b.path}</b> <span className="text-muted">{b.label}</span></span>
                <span className="text-muted">{b.fsType} · {bytes(b.size)}</span>
              </button>
            ))}
          </div>
        )}
        <Field label={tr("Volume name")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      </div>
    </Modal>
  );
}

function BtrfsPanel({ v }: { v: Volume }) {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["btrfs", v.id], queryFn: () => api.get<BtrfsInfo>(`/api/storage/volumes/${v.id}/btrfs`), refetchInterval: 10000 });
  const act = useAction((p: { action: string; body?: object }) => api.post(`/api/storage/volumes/${v.id}/btrfs/${p.action}`, p.body ?? {}), { success: tr("Operation completed") });
  const [sub, setSub] = useState("");
  const [addDev, setAddDev] = useState<string[] | null>(null);
  const [convert, setConvert] = useState<string | null>(null);
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data) return null;
  const subvols = data.subvolumes.filter((s) => !s.snapshot);
  const snaps = data.subvolumes.filter((s) => s.snapshot);
  return (
    <div className="mt-4 space-y-4 border-t border-line pt-4">
      <div className="flex flex-wrap gap-2">
        <Badge tone="accent">{tr("Data:")}{" "}{data.dataProfile}</Badge>
        <Badge tone="accent">{tr("Metadata:")}{" "}{data.metaProfile}</Badge>
        <Badge>{tr("Scrub:")}{" "}{data.scrub || "jamais"}</Badge>
        {data.balance && !data.balance.startsWith("No balance") && <Badge tone="warn">{data.balance}</Badge>}
        <div className="flex-1" />
        <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => act.mutate({ action: "scrub" })}>{tr("Scrub")}</Button>
        <Button size="sm" variant="ghost" icon={<Camera size={14} />} onClick={() => act.mutate({ action: "snapshot" })}>{tr("Snapshot")}</Button>
        <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setAddDev([])}>{tr("Add a disk")}</Button>
        <Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} onClick={() => setConvert(data.dataProfile)}>{tr("Convert RAID")}</Button>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Disks (")}{data.devices.length})</div>
          <div className="space-y-1.5">
            {data.devices.map((d) => (
              <div key={d.id} className="flex items-center justify-between gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[12.5px]">
                <span className="mono">{d.path}</span>
                <span className="text-muted">{bytes(d.used)} / {bytes(d.size)}</span>
                {data.devices.length > 1 && <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Remove this disk?"), text: tr("Data will first be moved to the other disks (this may take a while)."), danger: true })) act.mutate({ action: "remove-device", body: { device: d.path } }); }}>{tr("Remove")}</Button>}
              </div>
            ))}
          </div>
        </div>
        <div>
          <div className="mb-2 flex items-center justify-between text-[12.5px] font-medium text-muted">
            Sous-volumes
            <span className="flex gap-1.5">
              <Input className="!py-1 !text-[12px] w-32" placeholder="nouveau" value={sub} onChange={(e) => setSub(e.target.value)} />
              <Button size="sm" icon={<FolderPlus size={13} />} disabled={!sub} onClick={() => { act.mutate({ action: "subvolume", body: { name: sub } }); setSub(""); }}>{tr("Create")}</Button>
            </span>
          </div>
          <div className="space-y-1.5">
            {subvols.length === 0 && <div className="text-muted text-[12.5px]">{tr("No subvolumes")}</div>}
            {subvols.map((s) => (
              <div key={s.id} className="flex items-center justify-between rounded-lg bg-panel-2 px-3 py-1.5 text-[12.5px]">
                <span className="mono">{s.path}</span>
                <span className="flex gap-1">
                  <Button size="sm" variant="ghost" onClick={() => act.mutate({ action: "snapshot", body: { path: s.path } })}>{tr("Snapshot")}</Button>
                  <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Delete {0}?", s.path), text: tr("The subvolume and its contents will be deleted."), danger: true, typeToConfirm: s.path })) act.mutate({ action: "delete-subvolume", body: { path: s.path } }); }}>{tr("Delete")}</Button>
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
      {snaps.length > 0 && (
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Snapshots (")}{snaps.length}{tr(") — accessible read-only in .snapshots/")}</div>
          <div className="grid gap-1.5 md:grid-cols-2">
            {snaps.map((s) => (
              <div key={s.id} className="flex items-center justify-between rounded-lg bg-panel-2 px-3 py-1.5 text-[12.5px]">
                <span className="mono truncate">{s.path.replace(".snapshots/", "")}</span>
                {s.lockedUntil ? <span className="inline-flex items-center gap-1 text-[12px] text-muted" title={tr("Locked: cannot be deleted before {0}", dateTime(s.lockedUntil * 1000))}><Lock size={13} />{dateTime(s.lockedUntil * 1000)}</span> : <Button size="sm" variant="ghost" className="text-danger" onClick={() => act.mutate({ action: "delete-subvolume", body: { path: s.path } })}>{tr("Delete")}</Button>}
              </div>
            ))}
          </div>
        </div>
      )}
      <Modal open={addDev !== null} onClose={() => setAddDev(null)} title={tr("Add a disk to the Btrfs volume")} footer={<>
        <Button onClick={() => setAddDev(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!addDev?.length} onClick={() => { act.mutate({ action: "add-device", body: { device: addDev![0] } }); setAddDev(null); }}>{tr("Add")}</Button>
      </>}>
        <div className="space-y-3">
          <Notice>{tr("After adding, run a conversion/balance to spread the data.")}</Notice>
          <DiskPicker value={addDev ?? []} onChange={setAddDev} single />
        </div>
      </Modal>
      <Modal open={convert !== null} onClose={() => setConvert(null)} title={tr("Convert RAID profile")} footer={<>
        <Button onClick={() => setConvert(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" onClick={() => { act.mutate({ action: "balance", body: { dataProfile: convert, metaProfile: convert === "single" ? "dup" : ["raid5", "raid6", "raid0"].includes(convert!) ? "raid1" : convert } }); setConvert(null); }}>{tr("Start conversion")}</Button>
      </>}>
        <div className="space-y-3">
          <Field label={tr("New data profile")}>
            <Select value={convert ?? ""} onChange={(e) => setConvert(e.target.value)}>
              {btrfsProfiles.filter((p) => p.min <= data.devices.length).map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
            </Select>
          </Field>
          <Notice>{tr("The conversion is done online via a full balance; it may take several hours.")}</Notice>
        </div>
      </Modal>
    </div>
  );
}

export default function Volumes() {
  const { confirm } = useUI();
  const [create, setCreate] = useState(false);
  const [imp, setImp] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["volumes"], queryFn: () => api.get<Volume[]>("/api/storage/volumes"), refetchInterval: 10000 });
  const del = useAction((p: { id: string; wipe: boolean }) => api.del(`/api/storage/volumes/${p.id}${p.wipe ? "?wipe=1" : ""}`), { success: tr("Volume deleted") });
  const mount = useAction((id: string) => api.post(`/api/storage/volumes/${id}/mount`), { success: tr("Volume mounted") });
  const quotas = useQuery({ queryKey: ["quota-targets"], queryFn: () => api.get<QuotaTarget[]>("/api/quotas/targets") });
  const enableQuota = useAction((id: string) => api.post(`/api/storage/volumes/${id}/quota`));
  const quotaOf = (v: Volume) => quotas.data?.find((q) => q.target === v.id && q.kind === v.fsType);

  const remove = async (v: Volume) => {
    wipeRef.current = false;
    const ok = await confirm({
      title: tr("Delete volume {0}?", v.name),
      text: (
        <div className="space-y-3">
          <p>{tr("The volume will be unmounted and removed from StrataOS.")}</p>
          <WipeToggle />
        </div>
      ),
      danger: true, typeToConfirm: v.name, confirm: tr("Delete"),
    });
    if (ok) del.mutate({ id: v.id, wipe: wipeRef.current });
  };

  return (
    <div>
      <PageHeader title={tr("Volumes")} subtitle={tr("File systems mounted in /srv/strata")} icon={<Database size={20} />} actions={<>
        <Button icon={<Import size={15} />} onClick={() => setImp(true)}>{tr("Import")}</Button>
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Create a volume")}</Button>
      </>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Database />} title={tr("No volumes")} text={tr("A volume is a file system (ext4, XFS or Btrfs) created on a disk, a RAID array or an LVM volume. ZFS pools are managed on the ZFS page.")} action={<Button variant="primary" onClick={() => setCreate(true)}>{tr("Create a volume")}</Button>} /></Card>
      ) : (
        <div className="space-y-4">
          {data.map((v) => (
            <Card key={v.id}>
              <div className="flex flex-wrap items-center gap-4">
                <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-accent-soft text-accent"><Database size={20} /></div>
                <div className="min-w-[180px] flex-1">
                  <div className="flex items-center gap-2 font-semibold">{v.name} <Badge>{v.fsType}</Badge>{v.profile && <Badge tone="accent">{v.profile}</Badge>}{v.managedVg && <Badge tone="ok">{tr("SSD cache")}</Badge>} <Health value={v.health} /></div>
                  <div className="text-muted text-[12px]">{v.mountPoint} · {v.devices.join(", ")}</div>
                </div>
                <div className="w-full max-w-sm sm:w-72">
                  <Progress value={pct(v.usage.used, v.usage.total)} />
                  <div className="mt-1.5 flex justify-between text-[12px] text-muted"><span>{bytes(v.usage.used)} {" "}{tr("used")}</span><span>{bytes(v.usage.total)}</span></div>
                </div>
                <div className="flex gap-1">
                  {quotaOf(v)?.supported && (quotaOf(v)!.enabled ? <span className="self-center"><Badge tone="ok">{tr("Quotas")}</Badge></span> : v.mounted && (
                    <Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: tr("Enable quotas on {0}?", v.name), text: tr("The volume is unmounted for a few seconds: close open files and stop the applications using it. Shares resume automatically."), confirm: tr("Enable") })) enableQuota.mutate(v.id); }}>{tr("Enable quotas")}</Button>
                  ))}
                  {!v.mounted && <Button size="sm" icon={<Plug size={14} />} onClick={() => mount.mutate(v.id)}>{tr("Mount")}</Button>}
                  {v.fsType === "btrfs" && v.mounted && <Button size="sm" variant="ghost" onClick={() => setOpen(open === v.id ? null : v.id)}>{open === v.id ? tr("Hide") : tr("Btrfs…")}</Button>}
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={() => remove(v)} />
                </div>
              </div>
              {v.shares.length > 0 && <div className="mt-3 text-[12px] text-muted">{tr("Shares:")}{" "}{v.shares.join(", ")}</div>}
              {open === v.id && <BtrfsPanel v={v} />}
            </Card>
          ))}
        </div>
      )}
      {create && <CreateModal onClose={() => setCreate(false)} />}
      {imp && <ImportModal onClose={() => setImp(false)} />}
      {del.error && <ErrorBox error={del.error} />}
    </div>
  );
}

// The confirm dialog content is rendered outside this component's state, so keep the wipe choice in a ref.
const wipeRef = { current: false };
function WipeToggle() {
  const [v, setV] = useState(false);
  return <Checkbox checked={v} onChange={(x) => { setV(x); wipeRef.current = x; }} label={tr("Also wipe filesystem signatures (irreversible)")} />;
}
