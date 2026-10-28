import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArchiveRestore, Camera, Cpu, History, Plus, RotateCcw, Save, Trash2, Unplug, Usb } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, dateTime } from "../../lib/format";
import type { Share } from "../../lib/types";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, useUI } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";
import type { AutostartEntry, IOMMU, PCIDevice, Snapshot, USBDevice, VM, VMBackup } from "./types";

const enc = encodeURIComponent;

function useShares() {
  return useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
}

export function StorageSelect({ value, onChange, allowSame, ceph }: { value: string; onChange: (v: string) => void; allowSame?: boolean; ceph?: boolean }) {
  const shares = useShares();
  // VM storage on Ceph (libvirt RBD pools), for VMs installed from an ISO.
  const rbd = useQuery({ queryKey: ["ceph-vm-pools"], queryFn: () => api.get<{ name: string; label: string; active: boolean }[]>("/api/ceph/vm-pools"), enabled: !!ceph });
  useEffect(() => {
    if (!ceph && value.startsWith("rbd:")) onChange(shares.data?.[0]?.path ?? "");
  }, [ceph]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!value && !allowSame && shares.data?.length) onChange(shares.data[0].path);
  }, [shares.data]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)}>
      {allowSame && <option value="">{tr("Next to the original VM")}</option>}
      {!allowSame && shares.data?.length === 0 && <option value="">{tr("No shared folders")}</option>}
      {shares.data?.map((s) => <option key={s.name} value={s.path}>{s.name} ({s.path})</option>)}
      {ceph && rbd.data?.filter((p) => p.active).map((p) => <option key={p.name} value={`rbd:${p.name}`}>{p.label}</option>)}
    </Select>
  );
}

/* ---------- Snapshots ---------- */

export function Snapshots({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const { confirm } = useUI();
  const base = `/api/vms/${enc(vm.name)}/snapshots`;
  const list = useQuery({ queryKey: ["vm-snapshots", vm.name], queryFn: () => api.get<Snapshot[]>(base) });
  const [f, setF] = useState({ name: "", description: "" });
  const create = useAction(() => api.post<{ kind: string }>(base, f), {
    success: (r) => (r.kind === "external" ? tr("Snapshot taken (external: overlay files next to the disks)") : tr("Snapshot taken")),
    onDone: () => { setF({ name: "", description: "" }); list.refetch(); },
  });
  const revert = useAction((s: string) => api.post(`${base}/${enc(s)}/revert`), { success: tr("VM reverted to the snapshot"), onDone: () => list.refetch() });
  const del = useAction((s: string) => api.del(`${base}/${enc(s)}`), { success: tr("Snapshot deleted"), onDone: () => list.refetch() });
  const running = vm.state === "running" || vm.state === "paused";

  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-2"><Camera size={17} />{tr("Snapshots of {0}", vm.name)}</span>}>
      <div className="space-y-4">
        <ErrorBox error={list.error || create.error || revert.error || del.error} />
        <div className="grid gap-3 rounded-xl border border-line p-4 sm:grid-cols-[1fr_2fr_auto] sm:items-end">
          <Field label={tr("Name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="before-upgrade" /></Field>
          <Field label={tr("Description")}><Input value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></Field>
          <Button variant="primary" icon={<Plus size={14} />} disabled={!f.name} loading={create.isPending} onClick={() => create.mutate()}>{tr("Take")}</Button>
        </div>
        <Notice>
          {running
            ? tr("The VM is running: its memory is saved too, so reverting brings it back running exactly where it was.")
            : tr("The VM is stopped: only its disks are saved.")}{" "}
          {vm.firmware === "uefi" && tr("UEFI VMs get external snapshots (libvirt cannot store internal snapshots with the UEFI variables): an overlay file is added next to each disk.")}
        </Notice>
        {list.isLoading ? <Spinner /> : !list.data?.length ? <Empty icon={<History />} title={tr("No snapshots")} /> : (
          <div className="divide-y divide-line rounded-xl border border-line">
            {[...list.data].reverse().map((s) => (
              <div key={s.name} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 font-medium">{s.name}{s.current && <Badge tone="accent">{tr("current")}</Badge>}<Badge>{s.memory ? tr("disk + memory") : tr("disk only")}</Badge><Badge>{s.kind === "external" ? tr("external") : tr("internal")}</Badge></div>
                  <div className="text-muted text-[12px]">{dateTime(s.created)}{s.description && ` · ${s.description}`}</div>
                </div>
                <Button size="sm" icon={<RotateCcw size={14} />} onClick={async () => (await confirm({ title: tr("Revert to {0}?", s.name), text: tr("Changes made since this snapshot are lost."), confirm: tr("Revert"), danger: true })) && revert.mutate(s.name)}>{tr("Revert")}</Button>
                <IconButton title={tr("Delete")} onClick={async () => (await confirm({ title: tr("Delete snapshot {0}?", s.name), text: tr("The VM keeps its current state."), confirm: tr("Delete"), danger: true })) && del.mutate(s.name)}><Trash2 size={15} /></IconButton>
              </div>
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
}

/* ---------- Clone / create from template ---------- */

export function CloneVM({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const [f, setF] = useState({ name: vm.template ? "" : `${vm.name}-clone`, storage: "", start: vm.template });
  const clone = useAction(() => api.post(`/api/vms/${enc(vm.name)}/clone`, f), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={vm.template ? tr("New VM from template {0}", vm.name) : tr("Clone {0}", vm.name)}
      footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!f.name} loading={clone.isPending} onClick={() => clone.mutate()}>{vm.template ? tr("Create") : tr("Clone")}</Button></>}>
      <div className="space-y-4">
        <ErrorBox error={clone.error} />
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} autoFocus /></Field>
        <Field label={tr("Disk locations")}><StorageSelect allowSame value={f.storage} onChange={(v) => setF({ ...f, storage: v })} /></Field>
        <Checkbox checked={f.start} onChange={(v) => setF({ ...f, start: v })} label={tr("Start it once created")} />
        <Notice>{tr("A full copy: the disks are copied (snapshots are not), the copy gets new MAC addresses and a new identity. VMs made from a cloud image get a new host name and SSH host keys at first boot.")}</Notice>
      </div>
    </Modal>
  );
}

/* ---------- Console log (cloud images, imports) ---------- */

export function ConsoleLog({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const q = useQuery({ queryKey: ["vm-console-log", vm.name], queryFn: () => api.get<{ log: string }>(`/api/vms/${enc(vm.name)}/console-log`), refetchInterval: 3000 });
  return (
    <Modal open onClose={onClose} wide title={tr("Boot log of {0}", vm.name)}>
      <ErrorBox error={q.error} />
      <pre className="mono max-h-[60vh] overflow-auto whitespace-pre-wrap rounded-lg bg-black p-3 text-[11.5px] text-green-200">{q.data?.log || tr("Nothing logged yet.")}</pre>
    </Modal>
  );
}

/* ---------- USB devices ---------- */

export function USBPanel({ vms }: { vms: VM[] }) {
  const q = useQuery({ queryKey: ["vm-usb"], queryFn: () => api.get<USBDevice[]>("/api/vms/usb"), refetchInterval: 5000 });
  const [target, setTarget] = useState<Record<string, string>>({});
  const attach = useAction((u: USBDevice & { vm: string }) => api.post(`/api/vms/${enc(u.vm)}/usb`, { vendor: u.vendor, product: u.product, serial: u.serial ?? "" }), { success: tr("Device given to the VM"), onDone: () => q.refetch() });
  const detach = useAction((u: { vm: string; vendor: string; product: string }) => api.del(`/api/vms/${enc(u.vm)}/usb/${u.vendor}/${u.product}`), { success: tr("Device taken back"), onDone: () => q.refetch() });
  const usable = vms.filter((v) => !v.template);
  const host = q.data ?? [];
  // Remembered devices that are unplugged right now.
  const missing = vms.flatMap((v) => v.usb.filter((u) => !host.some((h) => h.vendor === u.vendor && h.product === u.product)).map((u) => ({ ...u, vm: v.name })));

  return (
    <Card title={<span className="flex items-center gap-2"><Usb size={16} />{tr("USB devices of the NAS")}</span>}>
      <ErrorBox error={q.error || attach.error || detach.error} />
      <p className="text-muted mb-3 text-[12.5px]">{tr("Give a USB device (Zigbee or Z-Wave dongle, UPS, licence key…) to a VM. It is remembered by vendor and product (and serial number), so it comes back after being unplugged; it is plugged in live when the VM runs.")}</p>
      {q.isLoading ? <Spinner /> : host.length === 0 && missing.length === 0 ? <Empty icon={<Usb />} title={tr("No USB devices")} /> : (
        <div className="divide-y divide-line rounded-xl border border-line">
          {host.map((u) => {
            const key = `${u.vendor}:${u.product}:${u.bus}:${u.device}`;
            return (
              <div key={key} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{u.name}</div>
                  <div className="text-muted mono text-[11.5px]">{u.vendor}:{u.product} · {tr("bus {0} device {1}", u.bus ?? 0, u.device ?? 0)}{u.serial && ` · S/N ${u.serial}`}</div>
                </div>
                {u.internal && <Badge tone="warn">{tr("storage")}</Badge>}
                {u.usedBy ? (
                  <>
                    <Badge tone="accent">{u.usedBy}</Badge>
                    <Button size="sm" icon={<Unplug size={14} />} onClick={() => detach.mutate({ vm: u.usedBy!, vendor: u.vendor, product: u.product })}>{tr("Take back")}</Button>
                  </>
                ) : (
                  <>
                    <Select className="w-44" value={target[key] ?? ""} onChange={(e) => setTarget({ ...target, [key]: e.target.value })}>
                      <option value="">{tr("Choose a VM…")}</option>
                      {usable.map((v) => <option key={v.name} value={v.name}>{v.name}</option>)}
                    </Select>
                    <Button size="sm" variant="primary" disabled={!target[key]} onClick={() => attach.mutate({ ...u, vm: target[key] })}>{tr("Give")}</Button>
                  </>
                )}
              </div>
            );
          })}
          {missing.map((u) => (
            <div key={`${u.vm}-${u.vendor}:${u.product}`} className="flex flex-wrap items-center gap-3 px-4 py-3 opacity-70">
              <div className="min-w-0 flex-1"><div className="font-medium">{tr("Unplugged device")}</div><div className="text-muted mono text-[11.5px]">{u.vendor}:{u.product}{u.serial && ` · S/N ${u.serial}`}</div></div>
              <Badge tone="accent">{u.vm}</Badge>
              <Button size="sm" icon={<Unplug size={14} />} onClick={() => detach.mutate({ vm: u.vm, vendor: u.vendor, product: u.product })}>{tr("Forget")}</Button>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

/* ---------- Start order ---------- */

export function AutostartPanel({ vms }: { vms: VM[] }) {
  const q = useQuery({ queryKey: ["vm-autostart"], queryFn: () => api.get<AutostartEntry[]>("/api/vms/autostart") });
  const [list, setList] = useState<AutostartEntry[] | null>(null);
  const [add, setAdd] = useState("");
  useEffect(() => { if (q.data) setList(q.data); }, [q.data]);
  const save = useAction(() => api.put("/api/vms/autostart", list), { success: tr("Start order saved") });
  if (!list) return <Card><Spinner /></Card>;
  const others = vms.filter((v) => !v.template && !list.some((e) => e.name === v.name));
  const upd = (i: number, k: Partial<AutostartEntry>) => setList(list.map((e, j) => (j === i ? { ...e, ...k } : e)));

  return (
    <Card title={tr("Start with the NAS")} actions={<Button variant="primary" size="sm" icon={<Save size={14} />} loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
      <ErrorBox error={q.error || save.error} />
      <p className="text-muted mb-3 text-[12.5px]">{tr("StrataOS starts these VMs when the NAS starts, by increasing order, waiting the given delay after each one (e.g. the database before the application). They are shut down in the reverse order when the NAS shuts down.")}</p>
      {list.length === 0 ? <Empty title={tr("No VM starts with the NAS")} /> : (
        <div className="divide-y divide-line rounded-xl border border-line">
          {[...list].map((e, i) => (
            <div key={e.name} className="flex flex-wrap items-end gap-3 px-4 py-3">
              <div className="min-w-0 flex-1 pb-2 font-medium">{e.name}</div>
              <Field label={tr("Order")}><Input className="w-24" type="number" min={0} max={999} value={e.order} onChange={(ev) => upd(i, { order: +ev.target.value })} /></Field>
              <Field label={tr("Delay after (s)")}><Input className="w-28" type="number" min={0} max={3600} value={e.delay} onChange={(ev) => upd(i, { delay: +ev.target.value })} /></Field>
              <IconButton title={tr("Remove")} onClick={() => setList(list.filter((_, j) => j !== i))}><Trash2 size={15} /></IconButton>
            </div>
          ))}
        </div>
      )}
      {others.length > 0 && (
        <div className="mt-3 flex items-center gap-2">
          <Select className="w-56" value={add} onChange={(e) => setAdd(e.target.value)}>
            <option value="">{tr("Add a VM…")}</option>
            {others.map((v) => <option key={v.name} value={v.name}>{v.name}</option>)}
          </Select>
          <Button size="sm" icon={<Plus size={14} />} disabled={!add} onClick={() => { setList([...list, { name: add, order: Math.max(0, ...list.map((e) => e.order)) + 1, delay: 0 }]); setAdd(""); }}>{tr("Add")}</Button>
        </div>
      )}
    </Card>
  );
}

/* ---------- Backups ---------- */

export function BackupsPanel() {
  const q = useQuery({ queryKey: ["vm-backups"], queryFn: () => api.get<VMBackup[]>("/api/vms/backups") });
  const [restore, setRestore] = useState<VMBackup | null>(null);
  return (
    <Card title={<span className="flex items-center gap-2"><ArchiveRestore size={16} />{tr("VM backups")}</span>}>
      <ErrorBox error={q.error} />
      <p className="text-muted mb-3 text-[12.5px]">{tr("Add virtual machines to a backup task in Backup & restore: their definition and disks are copied consistently. Restore one here as a new VM.")}</p>
      {q.isLoading ? <Spinner /> : !q.data?.length ? <Empty icon={<ArchiveRestore />} title={tr("No VM backups yet")} /> : (
        <div className="divide-y divide-line rounded-xl border border-line">
          {q.data.map((b) => (
            <div key={`${b.task}-${b.vm}-${b.snapshot ?? ""}`} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="font-medium">{b.vm}</div>
                <div className="text-muted text-[12px]">{srv(b.taskName)} · {dateTime(b.created)}{b.size ? ` · ${bytes(b.size)}` : ""}{b.snapshot && ` · ${b.snapshot.slice(0, 8)}`}</div>
              </div>
              {b.consistency && <Badge>{({ offline: tr("VM stopped"), frozen: tr("file systems frozen"), crash: tr("crash-consistent"), suspended: tr("VM paused") } as Record<string, string>)[b.consistency] ?? b.consistency}</Badge>}
              <Button size="sm" icon={<RotateCcw size={14} />} onClick={() => setRestore(b)}>{tr("Restore")}</Button>
            </div>
          ))}
        </div>
      )}
      {restore && <RestoreVM b={restore} onClose={() => setRestore(null)} />}
    </Card>
  );
}

function RestoreVM({ b, onClose }: { b: VMBackup; onClose: () => void }) {
  const [f, setF] = useState({ name: `${b.vm}-restored`, storage: "", start: false });
  const run = useAction(() => api.post("/api/vms/backups/restore", { task: b.task, vm: b.vm, snapshot: b.snapshot ?? "", ...f }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Restore {0}", b.vm)} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!f.name || !f.storage} loading={run.isPending} onClick={() => run.mutate()}>{tr("Restore")}</Button></>}>
      <div className="space-y-4">
        <ErrorBox error={run.error} />
        <Field label={tr("Name of the restored VM")} hint={tr("Keep the original name (once the original VM is deleted) to keep its identity and MAC address.")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label={tr("Disk locations")}><StorageSelect value={f.storage} onChange={(v) => setF({ ...f, storage: v })} /></Field>
        <Checkbox checked={f.start} onChange={(v) => setF({ ...f, start: v })} label={tr("Start it once restored")} />
      </div>
    </Modal>
  );
}

/* ---------- PCI passthrough (advanced) ---------- */

export function PCIPanel({ vms }: { vms: VM[] }) {
  const { confirm } = useUI();
  const q = useQuery({ queryKey: ["vm-pci"], queryFn: () => api.get<{ iommu: IOMMU; devices: PCIDevice[] }>("/api/vms/pci") });
  const [target, setTarget] = useState<Record<string, string>>({});
  const enable = useAction(() => api.post<{ message: string }>("/api/vms/iommu/enable"), { success: (r) => srv(r.message), onDone: () => q.refetch() });
  const attach = useAction((a: { vm: string; address: string }) => api.post(`/api/vms/${enc(a.vm)}/pci`, { address: a.address }), { success: tr("Device given to the VM"), onDone: () => q.refetch() });
  const detach = useAction((a: { vm: string; address: string }) => api.del(`/api/vms/${enc(a.vm)}/pci/${a.address}`), { success: tr("Device given back to the NAS"), onDone: () => q.refetch() });
  if (q.isLoading || !q.data) return <Card><ErrorBox error={q.error} /><Spinner /></Card>;
  const { iommu, devices } = q.data;
  const stopped = vms.filter((v) => !v.template && v.state === "shut off");
  const groups = new Map<number, PCIDevice[]>();
  for (const d of devices) groups.set(d.group, [...(groups.get(d.group) ?? []), d]);

  const turnOn = async () => {
    if (!(await confirm({ title: tr("Enable the IOMMU?"), danger: true, confirm: tr("Change the boot parameters"),
      text: tr("StrataOS adds {0} to the kernel command line in /etc/default/grub (a backup is kept as /etc/default/grub.strata-bak) and runs update-grub. It takes effect after a reboot; VT-d / AMD-Vi must also be enabled in the firmware. If the NAS does not boot correctly afterwards, choose the previous entry in the GRUB menu.", iommu.params) }))) return;
    enable.mutate();
  };
  const give = async (d: PCIDevice, vm: string) => {
    const group = groups.get(d.group) ?? [];
    if (!(await confirm({ title: tr("Give {0} to {1}?", d.name || d.address, vm), danger: true, confirm: tr("Give the device"),
      text: tr("Every device of IOMMU group {0} ({1} device(s)) is taken from the NAS and bound to vfio-pci when the VM starts: the NAS can no longer use them until you give them back. Passing through the wrong device can freeze the NAS.", d.group, group.length) }))) return;
    attach.mutate({ vm, address: d.address });
  };

  return (
    <div className="space-y-4">
      <ErrorBox error={enable.error || attach.error || detach.error} />
      <Notice tone="warn"><span className="flex items-start gap-2"><AlertTriangle size={16} className="mt-0.5 shrink-0" />{tr("Advanced: PCI passthrough gives a whole device (graphics card, disk controller, network card) to a VM. StrataOS refuses the devices the NAS needs (its console, network cards in use, controllers of disks holding data), but a mistake can still make the NAS unreachable.")}</span></Notice>
      <Card title={<span className="flex items-center gap-2"><Cpu size={16} />IOMMU</span>}>
        <div className="flex flex-wrap items-center gap-3">
          {iommu.enabled ? <Badge tone="ok" dot>{tr("Enabled ({0} groups)", iommu.groups)}</Badge> : <Badge tone="warn" dot>{tr("Disabled")}</Badge>}
          {!iommu.enabled && iommu.pending && <Badge tone="accent">{tr("Reboot needed")}</Badge>}
          <div className="flex-1" />
          {!iommu.enabled && !iommu.pending && iommu.grub && <Button variant="danger" loading={enable.isPending} onClick={turnOn}>{tr("Enable at next boot")}</Button>}
        </div>
        {!iommu.enabled && (
          <p className="text-muted mt-3 text-[12.5px]">
            {iommu.params ? tr("Enable VT-d (Intel) or AMD-Vi / IOMMU (AMD) in the firmware settings, and add {0} to the kernel parameters.", iommu.params) : tr("This processor or platform is not supported for passthrough here.")}
          </p>
        )}
      </Card>
      {iommu.enabled && (
        <Card title={tr("PCI devices by IOMMU group")} pad={false}>
          <div className="divide-y divide-line">
            {[...groups.entries()].sort((a, b) => a[0] - b[0]).map(([g, list]) => {
              const blocked = list.find((d) => d.protected && d.protected !== "PCI bridge");
              return (
                <div key={g} className="px-4 py-3">
                  <div className="mb-1.5 flex items-center gap-2 text-[12px] font-medium text-muted">{tr("Group {0}", g)}{blocked && <Badge tone="warn">{tr("kept by the NAS")}</Badge>}</div>
                  {list.map((d) => (
                    <div key={d.address} className="flex flex-wrap items-center gap-3 py-1">
                      <span className="mono text-[11.5px] text-muted">{d.address}</span>
                      <span className="min-w-0 flex-1 text-[13px]">{d.name || `${d.vendor}:${d.device}`} <span className="text-muted text-[11.5px]">{d.className}{d.driver && ` · ${d.driver}`}</span></span>
                      {d.protected && <span className="text-[11.5px] text-muted">{srv(d.protected)}</span>}
                      {d.usedBy ? (
                        <>
                          <Badge tone="accent">{d.usedBy}</Badge>
                          <Button size="sm" onClick={() => detach.mutate({ vm: d.usedBy!, address: d.address })}>{tr("Give back")}</Button>
                        </>
                      ) : !blocked && !d.protected && (
                        <>
                          <Select className="w-40" value={target[d.address] ?? ""} onChange={(e) => setTarget({ ...target, [d.address]: e.target.value })}>
                            <option value="">{tr("Stopped VM…")}</option>
                            {stopped.map((v) => <option key={v.name} value={v.name}>{v.name}</option>)}
                          </Select>
                          <Button size="sm" variant="danger" disabled={!target[d.address]} onClick={() => give(d, target[d.address])}>{tr("Give")}</Button>
                        </>
                      )}
                    </div>
                  ))}
                </div>
              );
            })}
          </div>
        </Card>
      )}
    </div>
  );
}
