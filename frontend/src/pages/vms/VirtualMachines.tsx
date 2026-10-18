import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cpu, Disc3, HardDrive, Monitor, MonitorPlay, Pause, Play, Plug, Plus, Power, RotateCcw, Settings2, Square, Trash2 } from "lucide-react";
import { api, wsURL } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes } from "../../lib/format";
import type { Share } from "../../lib/types";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Disk = { device: string; target: string; bus: string; source: string; size?: number };
type NIC = { type: string; source: string; mac: string; model: string; ip?: string };
type VM = { name: string; state: string; vcpus: number; memoryMb: number; autostart: boolean; firmware: string; os?: string; disks: Disk[]; nics: NIC[]; vncPort: number };
type VMData = { installed: boolean; kvm: boolean; vms: VM[]; bridges?: string[]; stats?: Record<string, [number, number]> };

const stateLabel: Record<string, [string, "ok" | "warn" | "neutral" | "danger"]> = {
  running: [tr("Running"), "ok"],
  paused: [tr("Paused"), "warn"],
  "shut off": [tr("Stopped"), "neutral"],
  crashed: [tr("Crashed"), "danger"],
  "in shutdown": [tr("Shutting down"), "warn"],
};

export default function VirtualMachines() {
  const { data, isLoading, error } = useQuery({ queryKey: ["vms"], queryFn: () => api.get<VMData>("/api/vms"), refetchInterval: 4000 });
  const install = useAction(() => api.post("/api/system/components/vms/install"));
  const [creating, setCreating] = useState(false);
  const [console, setConsole] = useState<string | null>(null);
  const [editing, setEditing] = useState<VM | null>(null);

  const header = (actions?: React.ReactNode) => <PageHeader title={tr("Virtual machines")} subtitle={tr("KVM machines with in-browser console")} icon={<Monitor size={20} />} actions={actions} />;
  if (isLoading || !data) return <div>{header()}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) {
    return (
      <div>
        {header()}
        <Card><Empty icon={<Plug />} title={tr("Virtualization is not installed")} text={tr("Installs QEMU/KVM, libvirt and the UEFI firmware (OVMF).")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card>
      </div>
    );
  }

  return (
    <div>
      {header(<Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreating(true)}>{tr("Create a VM")}</Button>)}
      <ErrorBox error={error} />
      {!data.kvm && <div className="mb-4"><Notice tone="warn">{tr("Hardware acceleration (/dev/kvm) is unavailable: enable virtualization (VT-x / AMD-V) in the BIOS. VMs will run very slowly.")}</Notice></div>}
      {data.vms.length === 0 ? (
        <Card><Empty icon={<Monitor />} title={tr("No virtual machines")} text={tr("Drop an ISO image into a shared folder, then create your first VM.")} action={<Button variant="primary" onClick={() => setCreating(true)}>{tr("Create a VM")}</Button>} /></Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {data.vms.map((vm) => <VMCard key={vm.name} vm={vm} mem={data.stats?.[vm.name]?.[1]} onConsole={() => setConsole(vm.name)} onEdit={() => setEditing(vm)} />)}
        </div>
      )}
      {creating && <CreateVM bridges={data.bridges ?? []} onClose={() => setCreating(false)} />}
      {console && <Console name={console} onClose={() => setConsole(null)} />}
      {editing && <EditVM vm={editing} onClose={() => setEditing(null)} />}
    </div>
  );
}

function VMCard({ vm, mem, onConsole, onEdit }: { vm: VM; mem?: number; onConsole: () => void; onEdit: () => void }) {
  const { confirm } = useUI();
  const act = useAction((a: string) => api.post(`/api/vms/${encodeURIComponent(vm.name)}/${a}`), { success: tr("Command sent") });
  const del = useAction((disks: boolean) => api.del(`/api/vms/${encodeURIComponent(vm.name)}${disks ? "?disks=1" : ""}`), { success: tr("VM deleted") });
  const [label, tone] = stateLabel[vm.state] ?? [vm.state, "neutral"];
  const running = vm.state === "running";
  const disk = vm.disks.find((d) => d.device === "disk");
  const cd = vm.disks.find((d) => d.device === "cdrom" && d.source);
  const ip = vm.nics.find((n) => n.ip)?.ip;

  const remove = async () => {
    if (!(await confirm({ title: tr("Delete {0}?", vm.name), text: tr("The VM will be removed. Its virtual disks are also deleted."), confirm: tr("Delete"), danger: true, typeToConfirm: vm.name }))) return;
    del.mutate(true);
  };

  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-center gap-3">
          <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-accent-soft text-accent"><Monitor size={22} /></div>
          <div>
            <div className="font-semibold">{vm.name}</div>
            <div className="text-muted text-[12px]">{vm.os === "windows" ? tr("Windows") : vm.os === "linux" ? tr("Linux") : tr("Other")} · {vm.firmware.toUpperCase()}{vm.autostart && ` · ${tr("auto-start")}`}</div>
          </div>
        </div>
        <Badge tone={tone} dot>{label}</Badge>
      </div>
      <div className="mt-4 grid grid-cols-2 gap-3 text-[12.5px] sm:grid-cols-4">
        <div><div className="text-muted flex items-center gap-1"><Cpu size={12} />vCPU</div><b>{vm.vcpus}</b></div>
        <div><div className="text-muted">{tr("Memory")}</div><b>{vm.memoryMb >= 1024 ? tr("{0} GiB", (vm.memoryMb / 1024).toFixed(1)) : tr("{0} MiB", vm.memoryMb)}</b>{running && mem ? <span className="text-muted"> ({bytes(mem * 1024)})</span> : null}</div>
        <div><div className="text-muted flex items-center gap-1"><HardDrive size={12} />{tr("Disk")}</div><b>{disk?.size ? bytes(disk.size) : "—"}</b></div>
        <div><div className="text-muted">{tr("IP address")}</div><b className="mono">{ip ?? (running ? "…" : "—")}</b></div>
      </div>
      {cd && <div className="text-muted mt-3 flex items-center gap-1.5 truncate text-[12px]"><Disc3 size={13} />{cd.source.split("/").pop()}</div>}
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-3">
        {!running && vm.state !== "paused" && <Button size="sm" variant="primary" icon={<Play size={14} />} onClick={() => act.mutate("start")}>{tr("Start")}</Button>}
        {running && <Button size="sm" variant="primary" icon={<MonitorPlay size={14} />} onClick={onConsole}>{tr("Console")}</Button>}
        {running && <Button size="sm" icon={<Power size={14} />} onClick={() => act.mutate("shutdown")}>{tr("Stop")}</Button>}
        {running && <Button size="sm" icon={<RotateCcw size={14} />} onClick={() => act.mutate("reboot")}>{tr("Restart")}</Button>}
        {running && <IconButton title={tr("Pause")} onClick={() => act.mutate("suspend")}><Pause size={15} /></IconButton>}
        {vm.state === "paused" && <Button size="sm" variant="primary" icon={<Play size={14} />} onClick={() => act.mutate("resume")}>{tr("Resume")}</Button>}
        {(running || vm.state === "paused" || vm.state === "in shutdown") && <IconButton title={tr("Force stop")} onClick={async () => (await confirm({ title: tr("Force stop?"), text: tr("Equivalent to pulling the plug: unsaved data in the VM is lost."), confirm: tr("Force"), danger: true })) && act.mutate("destroy")}><Square size={15} /></IconButton>}
        <div className="flex-1" />
        <IconButton title={tr("Settings")} onClick={onEdit}><Settings2 size={15} /></IconButton>
        <IconButton title={tr("Delete")} disabled={running} onClick={remove}><Trash2 size={15} /></IconButton>
      </div>
    </Card>
  );
}

function CreateVM({ bridges, onClose }: { bridges: string[]; onClose: () => void }) {
  const isos = useQuery({ queryKey: ["vm-isos"], queryFn: () => api.get<string[]>("/api/vms/isos") });
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const [f, setF] = useState({ name: "", os: "linux", vcpus: 2, memoryMb: 2048, diskGb: 32, iso: "", storage: "", network: "nat", firmware: "uefi", autostart: false });
  const set = (k: Partial<typeof f>) => setF({ ...f, ...k });
  const create = useAction(() => api.post("/api/vms", f), { onDone: onClose });
  useEffect(() => {
    if (!f.storage && shares.data?.length) set({ storage: shares.data[0].path });
  }, [shares.data]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Modal open onClose={onClose} title={tr("New virtual machine")} wide footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={create.isPending} disabled={!f.name || !f.storage} onClick={() => create.mutate()}>{tr("Create and start")}</Button></>}>
      <ErrorBox error={create.error} />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => set({ name: e.target.value })} placeholder="debian-serveur" autoFocus /></Field>
        <Field label={tr("Guest OS")}>
          <Select value={f.os} onChange={(e) => set({ os: e.target.value, ...(e.target.value === "windows" ? { memoryMb: Math.max(f.memoryMb, 4096), diskGb: Math.max(f.diskGb, 64), firmware: "uefi" } : {}) })}>
            <option value="linux">{tr("Linux")}</option>
            <option value="windows">{tr("Windows 10 / 11")}</option>
            <option value="other">{tr("Other")}</option>
          </Select>
        </Field>
        <Field label={tr("Virtual CPUs")}><Input type="number" min={1} max={64} value={f.vcpus} onChange={(e) => set({ vcpus: +e.target.value })} /></Field>
        <Field label={tr("Memory (MiB)")}><Input type="number" min={256} step={256} value={f.memoryMb} onChange={(e) => set({ memoryMb: +e.target.value })} /></Field>
        <Field label={tr("Disk size (GiB)")} hint={tr("Thin-provisioned (qcow2)")}><Input type="number" min={1} value={f.diskGb} onChange={(e) => set({ diskGb: +e.target.value })} /></Field>
        <Field label={tr("Disk locations")} hint={tr("Shared folder that will hold the VM")}>
          <Select value={f.storage} onChange={(e) => set({ storage: e.target.value })}>
            {shares.data?.length === 0 && <option value="">{tr("No shared folders")}</option>}
            {shares.data?.map((s) => <option key={s.name} value={s.path}>{s.name} ({s.path})</option>)}
          </Select>
        </Field>
        <Field label={tr("Installation image (ISO)")} hint={tr(".iso files found in volumes")} className="sm:col-span-2">
          <Select value={f.iso} onChange={(e) => set({ iso: e.target.value })}>
            <option value="">{tr("None")}</option>
            {isos.data?.map((p) => <option key={p} value={p}>{p}</option>)}
          </Select>
        </Field>
        <Field label={tr("Network")}>
          <Select value={f.network} onChange={(e) => set({ network: e.target.value })}>
            <option value="nat">{tr("NAT (private network behind the NAS)")}</option>
            {bridges.map((b) => <option key={b} value={b}>{tr("Bridge")}{" "}{b} {" "}{tr("(on the local network)")}</option>)}
          </Select>
        </Field>
        <Field label={tr("Firmware")}>
          <Select value={f.firmware} onChange={(e) => set({ firmware: e.target.value })}>
            <option value="uefi">UEFI</option>
            <option value="bios">{tr("BIOS (legacy)")}</option>
          </Select>
        </Field>
      </div>
      {bridges.length === 0 && <div className="mt-4"><Notice>{tr("For the VM to appear directly on your local network, create a bridge in Control Panel → Network.")}</Notice></div>}
      {f.os === "windows" && <div className="mt-4"><Notice>{tr("Windows 11: a virtual TPM 2.0 chip and Secure Boot are provided. The disk is SATA so the installer can see it without an extra driver.")}</Notice></div>}
      <div className="mt-4"><Checkbox checked={f.autostart} onChange={(v) => set({ autostart: v })} label={tr("Start the VM with the NAS")} /></div>
    </Modal>
  );
}

function EditVM({ vm, onClose }: { vm: VM; onClose: () => void }) {
  const isos = useQuery({ queryKey: ["vm-isos"], queryFn: () => api.get<string[]>("/api/vms/isos") });
  const cd = vm.disks.find((d) => d.device === "cdrom");
  const [iso, setIso] = useState(cd?.source ?? "");
  const [res, setRes] = useState({ vcpus: vm.vcpus, memoryMb: vm.memoryMb });
  const base = `/api/vms/${encodeURIComponent(vm.name)}`;
  const media = useAction(() => api.post(`${base}/media`, { iso }), { success: tr("CD drive updated") });
  const auto = useAction((v: boolean) => api.post(`${base}/autostart`, { autostart: v }), { success: tr("Auto-start changed") });
  const resources = useAction(() => api.post(`${base}/resources`, res), { success: tr("Resources modified"), onDone: onClose });
  const off = vm.state === "shut off";

  return (
    <Modal open onClose={onClose} title={tr("{0} settings", vm.name)}>
      <div className="space-y-5">
        <ErrorBox error={media.error || auto.error || resources.error} />
        <Switch checked={vm.autostart} onChange={(v) => auto.mutate(v)} label={tr("Start with the NAS")} />
        {cd && (
          <div className="flex items-end gap-2">
            <Field label={tr("CD drive")} className="flex-1">
              <Select value={iso} onChange={(e) => setIso(e.target.value)}>
                <option value="">{tr("Ejected")}</option>
                {cd.source && !isos.data?.includes(cd.source) && <option value={cd.source}>{cd.source}</option>}
                {isos.data?.map((p) => <option key={p} value={p}>{p}</option>)}
              </Select>
            </Field>
            <Button loading={media.isPending} onClick={() => media.mutate()}>{tr("Apply")}</Button>
          </div>
        )}
        <div className="space-y-3 rounded-xl border border-line p-4">
          <div className="font-medium text-[13px]">{tr("Resources")}</div>
          {!off && <Notice>{tr("Stop the VM to change its processors and memory.")}</Notice>}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="vCPU"><Input type="number" min={1} disabled={!off} value={res.vcpus} onChange={(e) => setRes({ ...res, vcpus: +e.target.value })} /></Field>
            <Field label={tr("Memory (MiB)")}><Input type="number" min={256} step={256} disabled={!off} value={res.memoryMb} onChange={(e) => setRes({ ...res, memoryMb: +e.target.value })} /></Field>
          </div>
          <Button variant="primary" disabled={!off} loading={resources.isPending} onClick={() => resources.mutate()}>{tr("Save")}</Button>
        </div>
      </div>
    </Modal>
  );
}

function Console({ name, onClose }: { name: string; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null);
  const rfb = useRef<import("@novnc/novnc/lib/rfb").default | null>(null);
  const [status, setStatus] = useState("Connexion…");
  useEffect(() => {
    let cancelled = false;
    import("@novnc/novnc/lib/rfb").then((m) => {
      if (cancelled || !box.current) return;
      // noVNC ships CommonJS: depending on the bundler the class is default or default.default.
      const RFB = ((m.default as unknown as { default?: typeof m.default }).default ?? m.default) as typeof m.default;
      const r = new RFB(box.current, wsURL(`/api/vms/${encodeURIComponent(name)}/vnc`), { wsProtocols: ["binary"] });
      r.scaleViewport = true;
      r.addEventListener("connect", () => { setStatus(""); r.focus(); });
      r.addEventListener("disconnect", () => setStatus(tr("Disconnected")));
      rfb.current = r;
    });
    return () => { cancelled = true; rfb.current?.disconnect(); };
  }, [name]);

  return (
    <Modal open onClose={onClose} wide="xl" title={<span className="flex items-center gap-2"><MonitorPlay size={17} />{tr("Console of")}{" "}{name}{status && <span className="text-muted text-[12px] font-normal">— {status}</span>}</span>}
      footer={<><Button onClick={() => rfb.current?.sendCtrlAltDel()}>{tr("Send Ctrl+Alt+Del")}</Button><Button variant="primary" onClick={onClose}>{tr("Close")}</Button></>}>
      <div ref={box} className="h-[65vh] w-full overflow-hidden rounded-lg bg-black" />
    </Modal>
  );
}
