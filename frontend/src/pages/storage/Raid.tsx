import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { RaidArray } from "../../lib/types";
import { bytes, cx } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Progress, Spinner, useUI } from "../../components/ui";
import { DiskPicker, useCandidates } from "../../components/DiskPicker";
import { tr } from "../../lib/i18n";

const levels = [
  { id: "1", name: tr("RAID 1"), min: 2, desc: tr("Mirror: each disk holds a full copy. Tolerates n-1 failures."), cap: (_n: number, s: number) => s },
  { id: "5", name: tr("RAID 5"), min: 3, desc: tr("Distributed parity. Tolerates 1 disk failure."), cap: (n: number, s: number) => (n - 1) * s },
  { id: "6", name: tr("RAID 6"), min: 4, desc: tr("Double parity. Tolerates 2 simultaneous failures."), cap: (n: number, s: number) => (n - 2) * s },
  { id: "10", name: tr("RAID 10"), min: 2, desc: tr("Striped mirrors: performance and redundancy."), cap: (n: number, s: number) => (n * s) / 2 },
  { id: "0", name: tr("RAID 0"), min: 2, desc: tr("Aggregation without redundancy: fast, but one failure = total loss."), cap: (n: number, s: number) => n * s },
  { id: "linear", name: "JBOD", min: 1, desc: tr("Disk concatenation, no redundancy."), cap: (n: number, s: number) => n * s },
  { id: "4", name: tr("RAID 4"), min: 3, desc: tr("Parity on a dedicated disk."), cap: (n: number, s: number) => (n - 1) * s },
];

function CreateModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("data");
  const [level, setLevel] = useState("1");
  const [devices, setDevices] = useState<string[]>([]);
  const [spares, setSpares] = useState<string[]>([]);
  const { list } = useCandidates(false);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/raid", { name, level, devices, spares }), { onDone: onClose });
  const lv = levels.find((l) => l.id === level)!;
  const sizes = devices.map((d) => list.find((c) => c.path === d)?.size ?? 0);
  const minSize = sizes.length ? Math.min(...sizes) : 0;
  const capacity = devices.length >= lv.min ? lv.cap(devices.length, minSize) : 0;

  const submit = async () => {
    const ok = await confirm({ title: tr("Create the RAID array?"), text: <>{tr("The disks")}{" "}{devices.concat(spares).join(", ")} {" "}{tr("will be")}{" "}<b>{tr("completely erased")}</b>.</>, danger: true, confirm: tr("Create") });
    if (ok) create.mutate();
  };
  return (
    <Modal open={open} onClose={onClose} title={tr("New RAID array (mdadm)")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" onClick={submit} loading={create.isPending} disabled={devices.length < lv.min || !name}>{tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <Field label={tr("Array name")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("RAID level")}</div>
          <div className="grid gap-2 sm:grid-cols-2">
            {levels.map((l) => (
              <button key={l.id} type="button" onClick={() => setLevel(l.id)} className={cx("rounded-xl border px-3.5 py-2.5 text-left transition", level === l.id ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                <div className="flex items-center justify-between font-medium">{l.name} <span className="text-muted text-[11.5px] font-normal">min. {l.min} disque{l.min > 1 ? "s" : ""}</span></div>
                <div className="text-muted text-[12px]">{l.desc}</div>
              </button>
            ))}
          </div>
        </div>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Member disks (")}{devices.length})</div>
          <DiskPicker value={devices} onChange={setDevices} exclude={spares} />
        </div>
        {level !== "0" && level !== "linear" && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Spare disks (optional)")}</div>
            <DiskPicker value={spares} onChange={setSpares} exclude={devices} />
          </div>
        )}
        {capacity > 0 && <Notice>{tr("Estimated usable capacity:")}{" "}<b>{bytes(capacity)}</b>{tr(". Then create a volume on this array to store data.")}</Notice>}
        {(level === "0" || level === "linear") && <Notice tone="warn">{tr("This level offers no protection: the failure of a single disk results in the loss of all data.")}</Notice>}
      </div>
    </Modal>
  );
}

function ArrayCard({ a }: { a: RaidArray }) {
  const { confirm } = useUI();
  const [addOpen, setAddOpen] = useState<null | "add" | "add-spare">(null);
  const [dev, setDev] = useState<string[]>([]);
  const act = useAction((p: { action: string; device?: string; count?: number }) => api.post(`/api/storage/raid/${a.label || a.name}/${p.action}`, { device: p.device ?? "", count: p.count ?? 0 }), { success: tr("Operation completed") });
  const del = useAction(() => api.del(`/api/storage/raid/${a.label || a.name}`), { success: tr("Array deleted") });
  const spares = a.members.filter((m) => m.state.includes("spare")).length;

  const remove = async () => {
    if (await confirm({ title: tr("Delete {0}?", a.label || a.name), text: tr("The array will be stopped and the RAID metadata wiped from the disks. Data will be lost."), danger: true, typeToConfirm: a.label || a.name, confirm: tr("Delete") })) del.mutate();
  };

  return (
    <Card
      title={<span className="flex items-center gap-2.5">{a.label || a.name} <Badge>{a.level.toUpperCase()}</Badge> <Health value={a.health} /></span>}
      actions={<>
        <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => act.mutate({ action: a.syncAction ? "idle" : "check" })}>{a.syncAction?.startsWith("check") ? tr("Stop verification") : tr("Check")}</Button>
        <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setAddOpen("add")}>{tr("Add a disk")}</Button>
        <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={remove} />
      </>}
    >
      <div className="grid gap-4 sm:grid-cols-4">
        <div><div className="text-muted text-[12px]">{tr("Capacity")}</div><div className="font-semibold">{bytes(a.size)}</div></div>
        <div><div className="text-muted text-[12px]">{tr("Active disks")}</div><div className="font-semibold">{a.activeDevices}/{a.raidDevices}</div></div>
        <div><div className="text-muted text-[12px]">{tr("Spare / failed")}</div><div className="font-semibold">{a.spareDevices} / <span className={a.failedDevices ? "text-danger" : ""}>{a.failedDevices}</span></div></div>
        <div><div className="text-muted text-[12px]">{tr("Device")}</div><div className="mono">{a.device}</div></div>
      </div>
      {a.syncAction && (
        <div className="mt-4 rounded-xl bg-panel-2 p-3">
          <div className="mb-2 flex justify-between text-[12.5px]"><span className="capitalize">{a.syncAction}</span><span className="text-muted">{a.syncProgress?.toFixed(1)}{tr("% · remaining")}{" "}{a.syncFinish} · {a.syncSpeed}</span></div>
          <Progress value={a.syncProgress ?? 0} tone="accent" />
        </div>
      )}
      <div className="mt-4 overflow-x-auto rounded-xl border border-line">
        <table className="table">
          <thead><tr><th>#</th><th>{tr("Disk")}</th><th>{tr("Status")}</th><th /></tr></thead>
          <tbody>
            {a.members.map((m, i) => (
              <tr key={i}>
                <td className="mono">{m.raidDevice >= 0 ? m.raidDevice : "—"}</td>
                <td className="mono">{m.device || "—"}</td>
                <td><Badge tone={m.state.includes("faulty") || m.state.includes("removed") ? "danger" : m.state.includes("rebuild") ? "warn" : m.state.includes("spare") ? "accent" : "ok"}>{m.state}</Badge></td>
                <td className="text-right">
                  {m.device && (
                    <div className="flex justify-end gap-1">
                      {!m.state.includes("faulty") && <Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: tr("Mark as failed?"), text: tr("{0} will be removed from service; the array will become degraded.", m.device), danger: true })) act.mutate({ action: "fail", device: m.device }); }}>{tr("Mark as failed")}</Button>}
                      <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Remove the disk?"), text: tr("{0} will be removed from the array and its RAID metadata erased.", m.device), danger: true })) act.mutate({ action: "remove", device: m.device }); }}>{tr("Remove")}</Button>
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {spares > 0 && ["raid5", "raid6", "raid1", "raid10", "raid4", "linear"].includes(a.level) && (
        <div className="mt-3 flex items-center justify-between gap-3 rounded-xl border border-dashed border-line px-4 py-3 text-[13px]">
          <span>{spares} {" "}{tr("spare disk(s) available: you can extend the array.")}</span>
          <Button size="sm" onClick={async () => { if (await confirm({ title: tr("Expand the array?"), text: tr("The array will grow to {0} active disks. Reshaping may take several hours; then resize the volume.", a.raidDevices + spares) })) act.mutate({ action: "grow", count: a.raidDevices + spares }); }}>{tr("Expand")}</Button>
        </div>
      )}
      <Modal open={!!addOpen} onClose={() => setAddOpen(null)} title={tr("Add a disk")} footer={<>
        <Button onClick={() => setAddOpen(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!dev.length} loading={act.isPending} onClick={() => { act.mutate({ action: "add", device: dev[0] }); setAddOpen(null); setDev([]); }}>{tr("Add")}</Button>
      </>}>
        <div className="space-y-3">
          <Notice>{tr("The disk will be added as a spare. If the array is degraded, the rebuild will start automatically.")}</Notice>
          <DiskPicker value={dev} onChange={setDev} single />
        </div>
      </Modal>
      {act.error && <div className="mt-3"><ErrorBox error={act.error} /></div>}
    </Card>
  );
}

export default function Raid() {
  const [open, setOpen] = useState(false);
  const { data, isLoading, error } = useQuery({ queryKey: ["raid"], queryFn: () => api.get<RaidArray[]>("/api/storage/raid"), refetchInterval: 5000 });
  return (
    <div>
      <PageHeader title={tr("Software RAID")} subtitle={tr("mdadm arrays: RAID 0, 1, 4, 5, 6, 10 and JBOD")} icon={<Layers size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setOpen(true)}>{tr("Create an array")}</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Layers />} title={tr("No RAID arrays")} text={tr("Group several disks for redundancy or performance. For ZFS or Btrfs (built-in RAID), use the ZFS or Volumes pages.")} action={<Button variant="primary" onClick={() => setOpen(true)}>{tr("Create an array")}</Button>} /></Card>
      ) : <div className="space-y-5">{data.map((a) => <ArrayCard key={a.name} a={a} />)}</div>}
      {open && <CreateModal open={open} onClose={() => setOpen(false)} />}
    </div>
  );
}
