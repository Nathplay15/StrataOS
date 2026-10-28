import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Box, Camera, ChevronDown, Cloud, Copy, Cpu, Disc3, FileInput, HardDrive, LayoutTemplate, Monitor, MonitorPlay, Pause, Play, Plug, Plus, Power, RotateCcw, ScrollText, Settings2, Square, Trash2, Usb } from "lucide-react";
import { api, wsURL } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, cx } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, Tabs, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";
import type { CloudImage, ImportInfo, VM, VMData } from "./types";
import type { LXCData } from "./containers/types";
import { ContainerCard } from "./containers/ContainerCard";
import { CreateContainer, ImportContainer } from "./containers/CreateContainer";
import { ContainerDetail, type DetailTab } from "./containers/ContainerDetail";
import { AutostartPanel, BackupsPanel, CloneVM, ConsoleLog, PCIPanel, Snapshots, StorageSelect, USBPanel } from "./panels";

type Tab = "vms" | "templates" | "usb" | "autostart" | "backups" | "pci";

function useAdvanced(): [boolean, (v: boolean) => void] {
  const [v, setV] = useState(() => {
    try { return localStorage.getItem("strata-vms-advanced") === "1"; } catch { return false; }
  });
  return [v, (x: boolean) => { setV(x); try { localStorage.setItem("strata-vms-advanced", x ? "1" : "0"); } catch { /* private mode */ } }];
}

const stateLabel: Record<string, [string, "ok" | "warn" | "neutral" | "danger"]> = {
  running: [tr("Running"), "ok"],
  paused: [tr("Paused"), "warn"],
  "shut off": [tr("Stopped"), "neutral"],
  crashed: [tr("Crashed"), "danger"],
  "in shutdown": [tr("Shutting down"), "warn"],
};

type Kind = "all" | "vm" | "lxc";

export default function VirtualMachines() {
  const vmq = useQuery({ queryKey: ["vms"], queryFn: () => api.get<VMData>("/api/vms"), refetchInterval: 4000 });
  const lxq = useQuery({ queryKey: ["lxc-list"], queryFn: () => api.get<LXCData>("/api/lxc"), refetchInterval: 4000 });
  const data = vmq.data;
  const lx = lxq.data;
  const error = vmq.error || lxq.error;
  const install = useAction(() => api.post("/api/system/components/vms/install"));
  const installLXC = useAction(() => api.post("/api/system/components/lxc/install"));
  const [creating, setCreating] = useState(false);
  const [creatingCT, setCreatingCT] = useState(false);
  const [importing, setImporting] = useState(false);
  const [menu, setMenu] = useState(false);
  const [console, setConsole] = useState<string | null>(null);
  const [editing, setEditing] = useState<VM | null>(null);
  const [snaps, setSnaps] = useState<VM | null>(null);
  const [cloning, setCloning] = useState<VM | null>(null);
  const [log, setLog] = useState<VM | null>(null);
  const [tab, setTab] = useState<Tab>("vms");
  const [kind, setKind] = useState<Kind>("all");
  const [detail, setDetail] = useState<{ name: string; tab: DetailTab } | null>(null);
  const [advanced, setAdvanced] = useAdvanced();

  const header = (actions?: React.ReactNode) => <PageHeader title={tr("Virtual machines")} subtitle={tr("KVM machines and LXC containers, with in-browser console")} icon={<Monitor size={20} />} actions={actions} />;
  if ((vmq.isLoading && !data) || (lxq.isLoading && !lx)) return <div>{header()}<ErrorBox error={error} /><Spinner /></div>;
  const vmOK = !!data?.installed;
  const lxcOK = !!lx?.installed;
  if (!vmOK && !lxcOK) {
    return (
      <div>
        {header()}
        <ErrorBox error={error} />
        <div className="grid gap-4 lg:grid-cols-2">
          <Card><Empty icon={<Plug />} title={tr("Virtualization is not installed")} text={tr("Installs QEMU/KVM, libvirt and the UEFI firmware (OVMF).")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card>
          <Card><Empty icon={<Box />} title={tr("Containers (LXC) are not installed")} text={tr("Installs LXC: light Linux systems sharing the NAS's kernel, ready in seconds.")} action={<Button variant="primary" loading={installLXC.isPending} onClick={() => installLXC.mutate()}>{tr("Install")}</Button>} /></Card>
        </div>
      </div>
    );
  }
  const bridges = data?.bridges ?? lx?.bridges ?? [];
  if (detail && lxcOK) {
    return <div><ContainerDetail name={detail.name} bridges={lx?.bridges ?? bridges} tab={detail.tab} setTab={(t) => setDetail({ ...detail, tab: t })} onBack={() => setDetail(null)} /></div>;
  }
  const allVMs = data?.vms ?? [];
  const machines = allVMs.filter((v) => !v.template);
  const templates = allVMs.filter((v) => v.template);
  const containers = lx?.containers ?? [];
  const tabs: { id: Tab; label: React.ReactNode }[] = [
    { id: "vms", label: tr("Machines ({0})", machines.length + containers.length) },
    ...(vmOK ? [
      { id: "templates" as Tab, label: tr("Templates ({0})", templates.length) },
      { id: "usb" as Tab, label: tr("USB devices") },
      { id: "autostart" as Tab, label: tr("Start order") },
      { id: "backups" as Tab, label: tr("Backups") },
    ] : []),
    ...(advanced && vmOK ? [{ id: "pci" as Tab, label: tr("PCI passthrough") }] : []),
  ];
  const cardProps = (vm: VM) => ({ vm, mem: data?.stats?.[vm.name]?.[1], onConsole: () => setConsole(vm.name), onEdit: () => setEditing(vm), onSnapshots: () => setSnaps(vm), onClone: () => setCloning(vm), onLog: () => setLog(vm) });
  const shownVMs = kind === "lxc" ? [] : machines;
  const shownCTs = kind === "vm" ? [] : containers;
  const createMenu = (
    <div className="relative">
      <Button variant="primary" icon={<Plus size={15} />} onClick={() => setMenu(!menu)}>{tr("Create")}<ChevronDown size={14} className="ml-1" /></Button>
      {menu && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setMenu(false)} />
          <div className="card absolute right-0 z-50 mt-1 w-64 overflow-hidden p-1">
            <button type="button" className="flex w-full items-start gap-2.5 rounded-lg px-3 py-2 text-left hover:bg-panel-2" onClick={() => { setMenu(false); if (vmOK) setCreating(true); else install.mutate(); }}>
              <Monitor size={17} className="mt-0.5 text-accent" /><span><span className="block text-[13px] font-medium">{tr("Virtual machine")}</span><span className="text-muted block text-[11.5px]">{vmOK ? tr("Any operating system, its own kernel") : tr("Install virtualization first")}</span></span>
            </button>
            <button type="button" className="flex w-full items-start gap-2.5 rounded-lg px-3 py-2 text-left hover:bg-panel-2" onClick={() => { setMenu(false); if (lxcOK) setCreatingCT(true); else installLXC.mutate(); }}>
              <Box size={17} className="mt-0.5 text-ok" /><span><span className="block text-[13px] font-medium">{tr("Container (LXC)")}</span><span className="text-muted block text-[11.5px]">{lxcOK ? tr("A light Linux system, ready in seconds") : tr("Install LXC first")}</span></span>
            </button>
          </div>
        </>
      )}
    </div>
  );

  return (
    <div>
      {header(<div className="flex items-center gap-4">{vmOK && <Switch checked={advanced} onChange={(v) => { setAdvanced(v); if (!v && tab === "pci") setTab("vms"); }} label={tr("Advanced")} />}{createMenu}</div>)}
      <ErrorBox error={error} />
      {vmOK && data && !data.kvm && <div className="mb-4"><Notice tone="warn">{tr("Hardware acceleration (/dev/kvm) is unavailable: enable virtualization (VT-x / AMD-V) in the BIOS. VMs will run very slowly.")}</Notice></div>}
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {tab === "vms" && (
        <>
          {(machines.length > 0 || containers.length > 0) && (
            <div className="mb-4 flex flex-wrap gap-2">
              {([["all", tr("All ({0})", machines.length + containers.length)], ["vm", tr("Virtual machines ({0})", machines.length)], ["lxc", tr("Containers ({0})", containers.length)]] as [Kind, string][]).map(([k, l]) => (
                <button key={k} type="button" onClick={() => setKind(k)} className={cx("rounded-full border px-3 py-1 text-[12.5px]", kind === k ? "border-accent bg-accent-soft text-accent" : "border-line text-muted hover:text-fg")}>{l}</button>
              ))}
              {!lxcOK && <Button size="sm" variant="ghost" icon={<Box size={14} />} loading={installLXC.isPending} onClick={() => installLXC.mutate()}>{tr("Install containers (LXC)")}</Button>}
            </div>
          )}
          {shownVMs.length + shownCTs.length === 0 ? (
            <Card><Empty icon={<Monitor />} title={kind === "lxc" ? tr("No containers") : tr("No virtual machines")} text={kind === "lxc" ? tr("A container is a light Linux system sharing the NAS's kernel: Debian, Ubuntu, Alpine… ready in seconds.") : tr("Start from a cloud image (ready in a minute), an ISO dropped into a shared folder, or import a disk from another hypervisor.")} action={createMenu} /></Card>
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {shownVMs.map((vm) => <VMCard key={`vm-${vm.name}`} {...cardProps(vm)} />)}
              {shownCTs.map((c) => <ContainerCard key={`ct-${c.name}`} c={c} onOpen={(t) => setDetail({ name: c.name, tab: (t as DetailTab) ?? "summary" })} onConsole={() => setDetail({ name: c.name, tab: "console" })} />)}
            </div>
          )}
        </>
      )}
      {tab === "templates" && (templates.length === 0 ? (
        <Card><Empty icon={<LayoutTemplate />} title={tr("No templates")} text={tr("Prepare a VM (updates, software), shut it down, then convert it to a template to create identical VMs from it.")} /></Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {templates.map((vm) => <TemplateCard key={vm.name} vm={vm} onCreate={() => setCloning(vm)} />)}
        </div>
      ))}
      {tab === "usb" && <USBPanel vms={allVMs} />}
      {tab === "autostart" && <AutostartPanel vms={allVMs} />}
      {tab === "backups" && <BackupsPanel />}
      {tab === "pci" && advanced && <PCIPanel vms={allVMs} />}
      {creating && <CreateVM bridges={bridges} onClose={() => setCreating(false)} />}
      {creatingCT && <CreateContainer bridges={lx?.bridges ?? bridges} onClose={() => setCreatingCT(false)} onImport={() => { setCreatingCT(false); setImporting(true); }} />}
      {importing && <ImportContainer onClose={() => setImporting(false)} />}
      {console && <Console name={console} onClose={() => setConsole(null)} />}
      {editing && <EditVM vm={editing} onClose={() => setEditing(null)} />}
      {snaps && <Snapshots vm={snaps} onClose={() => setSnaps(null)} />}
      {cloning && <CloneVM vm={cloning} onClose={() => setCloning(null)} />}
      {log && <ConsoleLog vm={log} onClose={() => setLog(null)} />}
    </div>
  );
}

function TemplateCard({ vm, onCreate }: { vm: VM; onCreate: () => void }) {
  const { confirm } = useUI();
  const base = `/api/vms/${encodeURIComponent(vm.name)}`;
  const back = useAction(() => api.post(`${base}/template`, { template: false }), { success: tr("Converted back to a VM") });
  const del = useAction(() => api.del(`${base}?disks=1`), { success: tr("Template deleted") });
  const disk = vm.disks.find((d) => d.device === "disk");
  return (
    <Card>
      <div className="flex items-center gap-3">
        <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-accent-soft text-accent"><LayoutTemplate size={22} /></div>
        <div className="min-w-0 flex-1">
          <div className="font-semibold">{vm.name}</div>
          <div className="text-muted text-[12px]">{tr("{0} vCPU · {1} MiB · disk {2}", vm.vcpus, vm.memoryMb, disk?.size ? bytes(disk.size) : "—")} · {vm.firmware.toUpperCase()}{vm.cloud && ` · ${tr("cloud image")}`}</div>
        </div>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-3">
        <Button size="sm" variant="primary" icon={<Plus size={14} />} onClick={onCreate}>{tr("Create a VM")}</Button>
        <Button size="sm" onClick={() => back.mutate()}>{tr("Convert to VM")}</Button>
        <div className="flex-1" />
        <IconButton title={tr("Delete")} onClick={async () => (await confirm({ title: tr("Delete {0}?", vm.name), text: tr("The template and its disks are deleted. VMs created from it are not affected."), confirm: tr("Delete"), danger: true, typeToConfirm: vm.name })) && del.mutate()}><Trash2 size={15} /></IconButton>
      </div>
    </Card>
  );
}

function VMCard({ vm, mem, onConsole, onEdit, onSnapshots, onClone, onLog }: { vm: VM; mem?: number; onConsole: () => void; onEdit: () => void; onSnapshots: () => void; onClone: () => void; onLog: () => void }) {
  const { confirm } = useUI();
  const act = useAction((a: string) => api.post(`/api/vms/${encodeURIComponent(vm.name)}/${a}`), { success: tr("Command sent") });
  const del = useAction((disks: boolean) => api.del(`/api/vms/${encodeURIComponent(vm.name)}${disks ? "?disks=1" : ""}`), { success: tr("VM deleted") });
  const template = useAction(() => api.post(`/api/vms/${encodeURIComponent(vm.name)}/template`, { template: true }), { success: tr("Converted to a template") });
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
            <div className="text-muted text-[12px]">{vm.os === "windows" ? tr("Windows") : vm.os === "linux" ? tr("Linux") : tr("Other")} · {vm.firmware.toUpperCase()}{vm.autostart && ` · ${tr("auto-start #{0}", vm.autostartOrder ?? 0)}`}{vm.snapshots > 0 && ` · ${tr("{0} snapshot(s)", vm.snapshots)}`}</div>
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
      {(vm.usb.length > 0 || vm.pci.length > 0) && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {vm.usb.map((u) => <Badge key={`${u.vendor}:${u.product}`}><Usb size={11} className="mr-1 inline" />{u.vendor}:{u.product}</Badge>)}
          {vm.pci.map((a) => <Badge key={a} tone="warn"><Cpu size={11} className="mr-1 inline" />{a}</Badge>)}
        </div>
      )}
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-3">
        {!running && vm.state !== "paused" && <Button size="sm" variant="primary" icon={<Play size={14} />} onClick={() => act.mutate("start")}>{tr("Start")}</Button>}
        {running && <Button size="sm" variant="primary" icon={<MonitorPlay size={14} />} onClick={onConsole}>{tr("Console")}</Button>}
        {running && <Button size="sm" icon={<Power size={14} />} onClick={() => act.mutate("shutdown")}>{tr("Stop")}</Button>}
        {running && <Button size="sm" icon={<RotateCcw size={14} />} onClick={() => act.mutate("reboot")}>{tr("Restart")}</Button>}
        {running && <IconButton title={tr("Pause")} onClick={() => act.mutate("suspend")}><Pause size={15} /></IconButton>}
        {vm.state === "paused" && <Button size="sm" variant="primary" icon={<Play size={14} />} onClick={() => act.mutate("resume")}>{tr("Resume")}</Button>}
        {(running || vm.state === "paused" || vm.state === "in shutdown") && <IconButton title={tr("Force stop")} onClick={async () => (await confirm({ title: tr("Force stop?"), text: tr("Equivalent to pulling the plug: unsaved data in the VM is lost."), confirm: tr("Force"), danger: true })) && act.mutate("destroy")}><Square size={15} /></IconButton>}
        <div className="flex-1" />
        <IconButton title={tr("Snapshots")} onClick={onSnapshots}><Camera size={15} /></IconButton>
        <IconButton title={tr("Clone")} disabled={vm.state !== "shut off"} onClick={onClone}><Copy size={15} /></IconButton>
        <IconButton title={tr("Convert to template")} disabled={vm.state !== "shut off"} onClick={async () => (await confirm({ title: tr("Convert {0} to a template?", vm.name), text: tr("A template cannot start: it is used to create identical VMs. You can convert it back later."), confirm: tr("Convert") })) && template.mutate()}><LayoutTemplate size={15} /></IconButton>
        <IconButton title={tr("Boot log")} onClick={onLog}><ScrollText size={15} /></IconButton>
        <IconButton title={tr("Settings")} onClick={onEdit}><Settings2 size={15} /></IconButton>
        <IconButton title={tr("Delete")} disabled={running} onClick={remove}><Trash2 size={15} /></IconButton>
      </div>
    </Card>
  );
}

type Source = "cloud" | "iso" | "import";

const sources: { id: Source; icon: React.ReactNode; label: string; sub: string }[] = [
  { id: "cloud", icon: <Cloud size={18} />, label: tr("Cloud image"), sub: tr("Debian or Ubuntu, configured by cloud-init: ready over SSH in a minute") },
  { id: "iso", icon: <Disc3 size={18} />, label: tr("Install from an ISO"), sub: tr("Any operating system, installed from the console") },
  { id: "import", icon: <FileInput size={18} />, label: tr("Import"), sub: tr("OVA / OVF, VMDK, VHD(X), qcow2 or raw disk from another hypervisor") },
];

function CreateVM({ bridges, onClose }: { bridges: string[]; onClose: () => void }) {
  const [source, setSource] = useState<Source>("cloud");
  const [f, setF] = useState({ name: "", os: "linux", vcpus: 2, memoryMb: 2048, diskGb: 20, iso: "", storage: "", network: "nat", firmware: "uefi", autostart: false });
  const [ci, setCi] = useState({ image: "debian13", user: "debian", password: "", sshKeys: "", packages: "", address: "", gateway: "", dns: "" });
  const [imp, setImp] = useState({ source: "" });
  const [info, setInfo] = useState<ImportInfo | null>(null);
  const set = (k: Partial<typeof f>) => setF({ ...f, ...k });
  const isos = useQuery({ queryKey: ["vm-isos"], queryFn: () => api.get<string[]>("/api/vms/isos"), enabled: source === "iso" });
  const images = useQuery({ queryKey: ["vm-cloud-images"], queryFn: () => api.get<CloudImage[]>("/api/vms/cloud-images"), enabled: source === "cloud" });
  const keys = useQuery({ queryKey: ["vm-ssh-keys"], queryFn: () => api.get<string[]>("/api/vms/ssh-keys"), enabled: source === "cloud" });
  const files = useQuery({ queryKey: ["vm-importable"], queryFn: () => api.get<string[]>("/api/vms/importable"), enabled: source === "import" });
  const inspect = useAction((p: string) => api.post<ImportInfo>("/api/vms/import/inspect", { source: p }), {
    onDone: (r) => {
      setInfo(r);
      if (r.ovf) set({ name: f.name || r.ovf.name.replace(/[^A-Za-z0-9_.-]/g, "-").slice(0, 63), vcpus: r.ovf.vcpus, memoryMb: r.ovf.memoryMb, firmware: r.ovf.firmware, os: r.ovf.os });
    },
  });
  const list = (s: string) => s.split(/[\s,]+/).filter(Boolean);
  const create = useAction(() => {
    if (source === "iso") return api.post("/api/vms", f);
    if (source === "import") return api.post("/api/vms/import", { ...f, source: imp.source, diskGb: 0 });
    return api.post("/api/vms/cloud", {
      ...f, os: "linux", image: ci.image,
      cloudInit: { hostname: "", user: ci.user, password: ci.password, sshKeys: ci.sshKeys ? [ci.sshKeys] : [], packages: list(ci.packages), address: ci.address, gateway: ci.gateway, dns: list(ci.dns) },
    });
  }, { onDone: onClose });
  const pick = (s: Source) => {
    setSource(s);
    if (s === "cloud") set({ os: "linux", diskGb: Math.max(f.diskGb, 8), memoryMb: f.memoryMb });
  };
  const canCreate = !!f.name && !!f.storage && (source !== "import" || !!imp.source) && (source !== "cloud" || (!!ci.user && (!!ci.password || !!ci.sshKeys)));

  return (
    <Modal open onClose={onClose} title={tr("New virtual machine")} wide footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={create.isPending} disabled={!canCreate} onClick={() => create.mutate()}>{source === "import" ? tr("Import and start") : tr("Create and start")}</Button></>}>
      <ErrorBox error={create.error || inspect.error} />
      <div className="mb-5 grid gap-2 sm:grid-cols-3">
        {sources.map((s) => (
          <button key={s.id} type="button" onClick={() => pick(s.id)} className={cx("flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left transition", source === s.id ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
            <span className="mt-0.5 text-accent">{s.icon}</span>
            <span><span className="block text-[13px] font-medium">{s.label}</span><span className="block text-muted text-[11.5px]">{s.sub}</span></span>
          </button>
        ))}
      </div>
      {source === "import" && (
        <div className="mb-4 space-y-3 rounded-xl border border-line p-4">
          <div className="flex items-end gap-2">
            <Field label={tr("File to import")} hint={tr("Copy it into a shared folder first. It is converted to qcow2 in the folder of the new VM (the original is kept).")} className="flex-1">
              <Select value={imp.source} onChange={(e) => { setImp({ source: e.target.value }); setInfo(null); if (e.target.value) inspect.mutate(e.target.value); }}>
                <option value="">{tr("Choose…")}</option>
                {files.data?.map((p) => <option key={p} value={p}>{p}</option>)}
              </Select>
            </Field>
          </div>
          {info && <div className="text-muted text-[12.5px]">{tr("Format: {0}", info.format.toUpperCase())} · {bytes(info.size)}{info.ovf && ` · ${tr("{0} disk(s), settings read from the OVF", info.ovf.disks.length)}`}</div>}
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => set({ name: e.target.value })} placeholder="debian-server" autoFocus /></Field>
        {source !== "cloud" ? (
          <Field label={tr("Guest OS")}>
            <Select value={f.os} onChange={(e) => set({ os: e.target.value, ...(e.target.value === "windows" ? { memoryMb: Math.max(f.memoryMb, 4096), diskGb: Math.max(f.diskGb, 64), firmware: "uefi" } : {}) })}>
              <option value="linux">{tr("Linux")}</option>
              <option value="windows">{tr("Windows 10 / 11")}</option>
              <option value="other">{tr("Other")}</option>
            </Select>
          </Field>
        ) : (
          <Field label={tr("Cloud image")} hint={images.data?.find((i) => i.id === ci.image)?.cached ? tr("Already downloaded") : tr("Downloaded once (about 400 MB), checked against the official checksums")}>
            <Select value={ci.image} onChange={(e) => setCi({ ...ci, image: e.target.value, user: e.target.value.startsWith("ubuntu") ? "ubuntu" : "debian" })}>
              {(images.data ?? [{ id: "debian13", name: "Debian 13 (trixie)" }]).map((i) => <option key={i.id} value={i.id}>{i.name}</option>)}
            </Select>
          </Field>
        )}
        <Field label={tr("Virtual CPUs")}><Input type="number" min={1} max={64} value={f.vcpus} onChange={(e) => set({ vcpus: +e.target.value })} /></Field>
        <Field label={tr("Memory (MiB)")}><Input type="number" min={256} step={256} value={f.memoryMb} onChange={(e) => set({ memoryMb: +e.target.value })} /></Field>
        {source !== "import" && <Field label={tr("Disk size (GiB)")} hint={tr("Thin-provisioned (qcow2)")}><Input type="number" min={1} value={f.diskGb} onChange={(e) => set({ diskGb: +e.target.value })} /></Field>}
        <Field label={tr("Disk locations")} hint={tr("Shared folder that will hold the VM")}><StorageSelect value={f.storage} onChange={(v) => set({ storage: v })} /></Field>
        {source === "iso" && (
          <Field label={tr("Installation image (ISO)")} hint={tr(".iso files found in volumes")} className="sm:col-span-2">
            <Select value={f.iso} onChange={(e) => set({ iso: e.target.value })}>
              <option value="">{tr("None")}</option>
              {isos.data?.map((p) => <option key={p} value={p}>{p}</option>)}
            </Select>
          </Field>
        )}
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
      {source === "cloud" && (
        <div className="mt-4 space-y-4 rounded-xl border border-line p-4">
          <div className="font-medium text-[13px]">{tr("First boot (cloud-init)")}</div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("User")}><Input value={ci.user} onChange={(e) => setCi({ ...ci, user: e.target.value })} /></Field>
            <Field label={tr("Password")} hint={tr("Stored hashed in the VM. Leave empty to sign in with the SSH key only.")}><Input type="password" autoComplete="new-password" value={ci.password} onChange={(e) => setCi({ ...ci, password: e.target.value })} /></Field>
            <Field label={tr("SSH public key")} className="sm:col-span-2" hint={keys.data?.length ? <span className="flex flex-wrap gap-2">{tr("Your keys on the NAS:")}{keys.data.map((k) => <button key={k} type="button" className="text-accent underline" onClick={() => setCi({ ...ci, sshKeys: ci.sshKeys ? `${ci.sshKeys}\n${k}` : k })}>{k.split(" ")[2] || k.slice(0, 24)}</button>)}</span> : tr("Paste one or more keys (ssh-ed25519 …), one per line.")}>
              <textarea className="input mono min-h-[70px] text-[11.5px]" value={ci.sshKeys} onChange={(e) => setCi({ ...ci, sshKeys: e.target.value })} placeholder="ssh-ed25519 AAAA… me@laptop" />
            </Field>
            <Field label={tr("Packages to install")} className="sm:col-span-2" hint={tr("Separated by spaces, e.g. nginx docker.io (the guest agent is always installed)")}><Input value={ci.packages} onChange={(e) => setCi({ ...ci, packages: e.target.value })} /></Field>
            <Field label={tr("Static address (optional)")} hint={tr("e.g. 192.168.1.50/24; empty: DHCP")}><Input value={ci.address} onChange={(e) => setCi({ ...ci, address: e.target.value })} /></Field>
            <Field label={tr("Gateway")}><Input value={ci.gateway} disabled={!ci.address} onChange={(e) => setCi({ ...ci, gateway: e.target.value })} /></Field>
            <Field label={tr("DNS servers")} className="sm:col-span-2"><Input value={ci.dns} disabled={!ci.address} onChange={(e) => setCi({ ...ci, dns: e.target.value })} placeholder="1.1.1.1 9.9.9.9" /></Field>
          </div>
        </div>
      )}
      {bridges.length === 0 && <div className="mt-4"><Notice>{tr("For the VM to appear directly on your local network, create a bridge in Control Panel → Network.")}</Notice></div>}
      {source !== "cloud" && f.os === "windows" && <div className="mt-4"><Notice>{tr("Windows 11: a virtual TPM 2.0 chip and Secure Boot are provided. The disk is SATA so the installer can see it without an extra driver.")}</Notice></div>}
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
