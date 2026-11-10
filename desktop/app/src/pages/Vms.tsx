// Virtual machines and LXC containers of the NAS: state and resources,
// start, shut down, reboot, force off, and the snapshots of a VM. The
// consoles, creation and settings are in the web interface (its "Virtual
// machines" page, opened in a window of the app).
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Monitor, Play, Power, RotateCw, Square, Trash2, Undo2 } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Spinner, Tabs, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { bytes, dateTime } from "@nas/lib/format";
import { api } from "../lib/nasapi";
import { t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

type Vm = { name: string; state: string; vcpus: number; memoryMb: number; autostart: boolean; os: string; snapshots: number; disks: { device: string; size: number }[]; nics: { ip: string }[] };
type VmList = { installed: boolean; kvm: boolean; vms: Vm[] };
type Ct = { name: string; description: string; state: string; cpus: number; memoryMb: number; ips: string[]; snapshots: number; autostart: boolean; image: { distribution?: string; release?: string } };
type CtList = { installed: boolean; containers: Ct[] };
type Snap = { name: string; description: string; created: string; state: string; current: boolean };

const tone = (s: string) => (s === "running" ? "ok" : s === "paused" ? "warn" : "neutral");

export function VmsPage() {
  const [tab, setTab] = useState<"vms" | "lxc">("vms");
  return (
    <>
      <PageHeader title={t("Virtual machines")} actions={<WebButton path="/vms" label={t("Consoles and settings")} title={t("Virtual machines")} />} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "vms", label: t("Virtual machines") }, { id: "lxc", label: t("LXC containers") }]} />
      <div className="mt-4">{tab === "vms" ? <Vms /> : <Lxc />}</div>
    </>
  );
}

function Vms() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["vms"], queryFn: () => api.get<VmList>("/api/vms"), refetchInterval: 8000 });
  const act = useAction((p: { name: string; action: string }) => api.post(`/api/vms/${encodeURIComponent(p.name)}/${p.action}`, {}));
  const [snaps, setSnaps] = useState<string | null>(null);
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.installed) return <Card><Empty icon={<Monitor />} title={t("Virtualization is not installed")} action={<WebButton path="/vms" />} /></Card>;
  if (!data.vms.length) return <Card><Empty icon={<Monitor />} title={t("No virtual machines")} action={<WebButton path="/vms" label={t("Create a virtual machine")} />} /></Card>;
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {data.vms.map((v) => {
        const on = v.state === "running";
        const disk = v.disks.filter((d) => d.device === "disk").reduce((n, d) => n + d.size, 0);
        return (
          <Card key={v.name}>
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-semibold">{v.name}</span>
              <Badge tone={tone(v.state)} dot>{v.state}</Badge>
              {v.autostart && <Badge>{t("Autostart")}</Badge>}
            </div>
            <div className="mt-1.5 text-[12.5px] text-muted">{v.vcpus} vCPU · {bytes(v.memoryMb * 1024 * 1024)} · {bytes(disk)}{v.os ? ` · ${v.os}` : ""}</div>
            {v.nics.some((n) => n.ip) && <div className="mono mt-1 text-[12px] text-muted">{v.nics.map((n) => n.ip).filter(Boolean).join(", ")}</div>}
            <div className="mt-3 flex flex-wrap gap-1.5">
              {!on && <Button size="sm" icon={<Play size={14} />} onClick={() => act.mutate({ name: v.name, action: v.state === "paused" ? "resume" : "start" })}>{t("Start")}</Button>}
              {on && <Button size="sm" icon={<Power size={14} />} onClick={() => act.mutate({ name: v.name, action: "shutdown" })}>{t("Shut down")}</Button>}
              {on && <Button size="sm" icon={<RotateCw size={14} />} onClick={() => act.mutate({ name: v.name, action: "reboot" })}>{t("Restart")}</Button>}
              {on && <Button size="sm" variant="ghost" className="text-danger" icon={<Square size={14} />} onClick={async () => {
                if (await confirm({ title: t("Force off {0}?", v.name), text: t("Like pulling the plug: unsaved work in the machine is lost."), danger: true })) act.mutate({ name: v.name, action: "destroy" });
              }}>{t("Force off")}</Button>}
              <Button size="sm" variant="ghost" icon={<Camera size={14} />} onClick={() => setSnaps(v.name)}>{t("Snapshots")} ({v.snapshots})</Button>
            </div>
          </Card>
        );
      })}
      {snaps && <Snapshots vm={snaps} onClose={() => setSnaps(null)} />}
    </div>
  );
}

function Snapshots({ vm, onClose }: { vm: string; onClose: () => void }) {
  const { confirm } = useUI();
  const base = `/api/vms/${encodeURIComponent(vm)}/snapshots`;
  const { data, isLoading, error } = useQuery({ queryKey: ["vm-snaps", vm], queryFn: () => api.get<Snap[]>(base) });
  const [name, setName] = useState("");
  const create = useAction(() => api.post(base, { name, description: "" }), { success: t("Snapshot created"), onDone: () => setName("") });
  const revert = useAction((s: string) => api.post(`${base}/${encodeURIComponent(s)}/revert`, {}), { success: t("Snapshot restored") });
  const del = useAction((s: string) => api.del(`${base}/${encodeURIComponent(s)}`), { success: t("Snapshot deleted") });
  return (
    <Modal open wide onClose={onClose} title={t("Snapshots of {0}", vm)} footer={<Button onClick={onClose}>{t("Close")}</Button>}>
      <div className="mb-3 flex gap-2">
        <Field label={t("Name")} className="flex-1"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Button className="self-end" variant="primary" loading={create.isPending} disabled={!/^[A-Za-z0-9._-]+$/.test(name)} onClick={() => create.mutate()}>{t("Create")}</Button>
      </div>
      <ErrorBox error={error || create.error} />
      {isLoading ? <Spinner /> : !data?.length ? <Empty title={t("No snapshots")} /> : (
        <table className="table">
          <tbody>{data.map((s) => (
            <tr key={s.name}>
              <td><div className="font-medium">{s.name}{s.current && <Badge tone="accent">{t("Current")}</Badge>}</div><div className="text-[12px] text-muted">{dateTime(s.created)} · {s.state}</div></td>
              <td className="whitespace-nowrap text-right">
                <Button size="sm" variant="ghost" icon={<Undo2 size={14} />} title={t("Restore")} onClick={async () => { if (await confirm({ title: t("Restore {0}?", s.name), text: t("The machine goes back to the state of this snapshot; later changes are lost."), danger: true })) revert.mutate(s.name); }} />
                <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={t("Delete")} onClick={async () => { if (await confirm({ title: t("Delete {0}?", s.name), text: t("The snapshot is deleted."), danger: true })) del.mutate(s.name); }} />
              </td>
            </tr>
          ))}</tbody>
        </table>
      )}
    </Modal>
  );
}

function Lxc() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["lxc"], queryFn: () => api.get<CtList>("/api/lxc"), refetchInterval: 8000 });
  const act = useAction((p: { name: string; action: string }) => api.post(`/api/lxc/${encodeURIComponent(p.name)}/${p.action}`, {}));
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.installed) return <Card><Empty title={t("LXC is not installed")} action={<WebButton path="/vms" />} /></Card>;
  if (!data.containers.length) return <Card><Empty title={t("No LXC containers")} action={<WebButton path="/vms" label={t("Create a container")} />} /></Card>;
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {data.containers.map((c) => {
        const on = c.state === "running";
        return (
          <Card key={c.name}>
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-semibold">{c.name}</span>
              <Badge tone={tone(c.state)} dot>{c.state}</Badge>
              {c.autostart && <Badge>{t("Autostart")}</Badge>}
            </div>
            <div className="mt-1.5 text-[12.5px] text-muted">{[c.image.distribution, c.image.release].filter(Boolean).join(" ")}{c.cpus ? ` · ${c.cpus} CPU` : ""}{c.memoryMb ? ` · ${bytes(c.memoryMb * 1024 * 1024)}` : ""}</div>
            {c.ips.length > 0 && <div className="mono mt-1 text-[12px] text-muted">{c.ips.join(", ")}</div>}
            <div className="mt-3 flex flex-wrap gap-1.5">
              {!on && <Button size="sm" icon={<Play size={14} />} onClick={() => act.mutate({ name: c.name, action: "start" })}>{t("Start")}</Button>}
              {on && <Button size="sm" icon={<Power size={14} />} onClick={() => act.mutate({ name: c.name, action: "stop" })}>{t("Stop")}</Button>}
              {on && <Button size="sm" icon={<RotateCw size={14} />} onClick={() => act.mutate({ name: c.name, action: "restart" })}>{t("Restart")}</Button>}
              {on && <Button size="sm" variant="ghost" className="text-danger" icon={<Square size={14} />} onClick={async () => {
                if (await confirm({ title: t("Force off {0}?", c.name), text: t("Like pulling the plug: unsaved work in the container is lost."), danger: true })) act.mutate({ name: c.name, action: "kill" });
              }}>{t("Force off")}</Button>}
            </div>
          </Card>
        );
      })}
    </div>
  );
}
