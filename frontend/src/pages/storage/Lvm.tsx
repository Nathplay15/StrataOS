import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Expand, Plus, Server, Trash2, Zap } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { VG } from "../../lib/types";
import { bytes, parseSize, pct } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";
import { tr } from "../../lib/i18n";

function CreateVG({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("vg0");
  const [devices, setDevices] = useState<string[]>([]);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/lvm", { name, devices }), { success: tr("Volume group created"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("New volume group")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" disabled={!devices.length || !name} loading={create.isPending} onClick={async () => { if (await confirm({ title: tr("Create the group?"), text: tr("{0} will be erased.", devices.join(", ")), danger: true })) create.mutate(); }}>{tr("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Name")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Notice>{tr("The selected disks (or RAID arrays) become pooled physical volumes. You can then carve resizable logical volumes out of them.")}</Notice>
        <DiskPicker value={devices} onChange={setDevices} includeVirtual />
      </div>
    </Modal>
  );
}

function CreateLV({ vg, onClose }: { vg: VG; onClose: () => void }) {
  const [name, setName] = useState("lv0");
  const [size, setSize] = useState("");
  const [type, setType] = useState("linear");
  const [stripes, setStripes] = useState(2);
  const create = useAction(() => api.post(`/api/storage/lvm/${vg.name}/lv`, { name, size: size ? parseSize(size) : 0, type, stripes }), { success: tr("Logical volume created"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("New logical volume in {0}", vg.name)} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" disabled={!name || (!!size && isNaN(parseSize(size)))} loading={create.isPending} onClick={() => create.mutate()}>{tr("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Name")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label={tr("Size")} hint={tr("e.g. 100G — empty = all free space ({0})", bytes(vg.free))}><Input value={size} onChange={(e) => setSize(e.target.value)} /></Field>
        <Field label={tr("Type")}>
          <Select value={type} onChange={(e) => setType(e.target.value)}>
            <option value="linear">{tr("Linear")}</option>
            <option value="striped">{tr("Striped")}</option>
            <option value="raid1">{tr("RAID 1 LVM")}</option>
            <option value="raid5">{tr("RAID 5 LVM")}</option>
            <option value="raid6">{tr("RAID 6 LVM")}</option>
            <option value="raid10">{tr("RAID 10 LVM")}</option>
          </Select>
        </Field>
        {type !== "linear" && type !== "raid1" && <Field label={tr("Number of stripes")}><Input type="number" min={2} value={stripes} onChange={(e) => setStripes(+e.target.value)} /></Field>}
        <Notice>{tr("Then create a volume (ext4, XFS or Btrfs) on this logical volume from the Volumes page.")}</Notice>
      </div>
    </Modal>
  );
}

export default function Lvm() {
  const { confirm } = useUI();
  const [create, setCreate] = useState(false);
  const [newLV, setNewLV] = useState<VG | null>(null);
  const [extend, setExtend] = useState<VG | null>(null);
  const [extDev, setExtDev] = useState<string[]>([]);
  const [resize, setResize] = useState<{ vg: string; lv: string; size: string } | null>(null);
  const [cache, setCache] = useState<{ vg: string; lv: string; dev: string[]; mode: string } | null>(null);
  const addCache = useAction((c: { vg: string; lv: string; device: string; mode: string }) => api.post(`/api/storage/lvm/${c.vg}/lv/${c.lv}/cache`, { device: c.device, mode: c.mode }));
  const delCache = useAction((c: { vg: string; lv: string }) => api.del(`/api/storage/lvm/${c.vg}/lv/${c.lv}/cache`));
  const { data, isLoading, error } = useQuery({ queryKey: ["lvm"], queryFn: () => api.get<VG[]>("/api/storage/lvm") });
  const delVG = useAction((n: string) => api.del(`/api/storage/lvm/${n}`), { success: tr("Group deleted") });
  const delLV = useAction((p: { vg: string; lv: string }) => api.del(`/api/storage/lvm/${p.vg}/lv/${p.lv}`), { success: tr("Logical volume deleted") });
  const ext = useAction((p: { vg: string; devices: string[] }) => api.post(`/api/storage/lvm/${p.vg}/extend`, { devices: p.devices }), { success: tr("Volume group extended") });
  const rs = useAction((p: { vg: string; lv: string; size: number }) => api.post(`/api/storage/lvm/${p.vg}/lv/${p.lv}/resize`, { size: p.size }), { success: tr("Volume extended (file system included)") });
  const snap = useAction((p: { vg: string; lv: string }) => api.post(`/api/storage/lvm/${p.vg}/lv/${p.lv}/snapshot`, { name: `${p.lv}-snap-${Date.now().toString(36)}`, size: 0 }), { success: tr("Snapshot created") });

  return (
    <div>
      <PageHeader title="LVM" subtitle={tr("Volume groups and logical volumes")} icon={<Server size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Create a group")}</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Server />} title={tr("No volume groups")} text={tr("LVM lets you pool disks and carve out volumes that can be resized online.")} action={<Button variant="primary" onClick={() => setCreate(true)}>{tr("Create a group")}</Button>} /></Card>
      ) : (
        <div className="space-y-5">
          {data.map((vg) => (
            <Card key={vg.name} title={<span className="flex items-center gap-2">{vg.name} <Badge>{vg.pvCount} PV</Badge><Badge>{vg.lvCount} LV</Badge></span>} actions={<>
              <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setNewLV(vg)}>{tr("Logical volume")}</Button>
              <Button size="sm" variant="ghost" icon={<Expand size={14} />} onClick={() => setExtend(vg)}>{tr("Extend")}</Button>
              <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete {0}?", vg.name), text: tr("The group must be empty."), danger: true })) delVG.mutate(vg.name); }} />
            </>}>
              <Progress value={pct(vg.size - vg.free, vg.size)} />
              <div className="mt-1.5 mb-4 flex justify-between text-[12px] text-muted"><span>{bytes(vg.size - vg.free)} {" "}{tr("allocated")}</span><span>{bytes(vg.free)} {" "}{tr("free on")}{" "}{bytes(vg.size)}</span></div>
              <div className="mb-3 flex flex-wrap gap-1.5">{vg.pvs.map((p) => <Badge key={p.name}>{p.name} · {bytes(p.size)}</Badge>)}</div>
              {vg.lvs.length > 0 && (
                <div className="overflow-x-auto rounded-xl border border-line">
                  <table className="table">
                    <thead><tr><th>{tr("Logical volume")}</th><th>{tr("Size")}</th><th>{tr("Type")}</th><th>{tr("Content")}</th><th /></tr></thead>
                    <tbody>
                      {vg.lvs.map((lv) => (
                        <tr key={lv.name}>
                          <td>
                            <span className="mono">{lv.name}</span>{lv.snapshot && <span className="ml-2"><Badge tone="accent">{tr("snapshot of")}{" "}{lv.origin}</Badge></span>}
                            {lv.cache && (
                              <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted">
                                <Badge tone="ok"><Zap size={11} className="mr-1 inline" />{tr("SSD cache")} · {lv.cache.mode === "writeback" ? tr("write-back") : tr("write-through")}</Badge>
                                <span>{lv.cache.device}</span>
                                <span>{tr("{0} % full", lv.cache.usedPercent.toFixed(0))}</span>
                                {lv.cache.readHits + lv.cache.readMisses > 0 && <span>{tr("{0} % of reads served by the SSD", Math.round((lv.cache.readHits * 100) / (lv.cache.readHits + lv.cache.readMisses)))}</span>}
                              </div>
                            )}
                          </td>
                          <td>{bytes(lv.size)}</td><td>{lv.type}</td><td>{lv.fsType || <span className="text-muted">vide</span>}</td>
                          <td className="text-right whitespace-nowrap">
                            <Button size="sm" variant="ghost" onClick={() => setResize({ vg: vg.name, lv: lv.name, size: "" })}>{tr("Expand")}</Button>
                            {!lv.snapshot && <Button size="sm" variant="ghost" icon={<Camera size={13} />} onClick={() => snap.mutate({ vg: vg.name, lv: lv.name })} />}
                            {!lv.snapshot && (lv.cache
                              ? <Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: tr("Remove the SSD cache?"), text: tr("Pending data is first copied to the disks; the volume remains usable during the operation.") })) delCache.mutate({ vg: vg.name, lv: lv.name }); }}>{tr("Remove cache")}</Button>
                              : <Button size="sm" variant="ghost" icon={<Zap size={13} />} onClick={() => setCache({ vg: vg.name, lv: lv.name, dev: [], mode: "writethrough" })}>{tr("SSD cache")}</Button>)}
                            <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={async () => { if (await confirm({ title: tr("Delete {0}?", lv.name), text: tr("Data on the logical volume will be lost."), danger: true, typeToConfirm: lv.name })) delLV.mutate({ vg: vg.name, lv: lv.name }); }} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          ))}
        </div>
      )}
      {create && <CreateVG onClose={() => setCreate(false)} />}
      <Modal open={!!cache} onClose={() => setCache(null)} title={tr("SSD cache for {0}", cache?.lv)} wide footer={<>
        <Button onClick={() => setCache(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={cache?.dev.length !== 1} onClick={() => { addCache.mutate({ vg: cache!.vg, lv: cache!.lv, device: cache!.dev[0], mode: cache!.mode }); setCache(null); }}>{tr("Add cache")}</Button>
      </>}>
        {cache && (
          <div className="space-y-4">
            <p className="text-muted text-[13px]">{tr("A free SSD speeds up the volume: frequently read data is kept on it. It can be added and removed without stopping the volume. For a volume on a RAID array, first create an LVM group on the array.")}</p>
            <DiskPicker value={cache.dev} onChange={(v) => setCache({ ...cache, dev: v.slice(-1) })} includeVirtual single />
            <Field label={tr("Mode")}>
              <Select value={cache.mode} onChange={(e) => setCache({ ...cache, mode: e.target.value })}>
                <option value="writethrough">{tr("Write-through (recommended): speeds up reads, risk-free")}</option>
                <option value="writeback">{tr("Write-back: also speeds up writes")}</option>
              </Select>
            </Field>
            {cache.mode === "writeback" && <Notice tone="warn">{tr("With write-back, data not yet copied to the disks is lost if the SSD fails. Use a reliable SSD, ideally mirrored.")}</Notice>}
          </div>
        )}
      </Modal>
      {newLV && <CreateLV vg={newLV} onClose={() => setNewLV(null)} />}
      <Modal open={!!extend} onClose={() => setExtend(null)} title={tr("Extend {0}", extend?.name)} footer={<>
        <Button onClick={() => setExtend(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!extDev.length} onClick={() => { ext.mutate({ vg: extend!.name, devices: extDev }); setExtend(null); setExtDev([]); }}>{tr("Add")}</Button>
      </>}>
        <DiskPicker value={extDev} onChange={setExtDev} includeVirtual />
      </Modal>
      <Modal open={!!resize} onClose={() => setResize(null)} title={tr("Expand {0}", resize?.lv)} footer={<>
        <Button onClick={() => setResize(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" onClick={() => { rs.mutate({ vg: resize!.vg, lv: resize!.lv, size: resize!.size ? parseSize(resize!.size) : 0 }); setResize(null); }}>{tr("Expand")}</Button>
      </>}>
        {resize && <Field label={tr("New total size")} hint={tr("e.g. 200G — empty = use all free space in the group. The file system is extended automatically.")}><Input value={resize.size} onChange={(e) => setResize({ ...resize, size: e.target.value })} /></Field>}
      </Modal>
    </div>
  );
}
