import { Box, Camera, Cpu, HardDrive, Play, Power, RotateCcw, Settings2, Square, TerminalSquare } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { bytes } from "../../../lib/format";
import { Badge, Button, Card, IconButton, useUI } from "../../../components/ui";
import { tr } from "../../../lib/i18n";
import type { Container } from "./types";

export const lxcState: Record<string, [string, "ok" | "warn" | "neutral" | "danger"]> = {
  running: [tr("Running"), "ok"],
  stopped: [tr("Stopped"), "neutral"],
  starting: [tr("Starting"), "warn"],
  stopping: [tr("Shutting down"), "warn"],
  frozen: [tr("Paused"), "warn"],
  aborting: [tr("Crashed"), "danger"],
};

export const backendLabel: Record<string, string> = {
  dir: tr("Folder"), image: tr("Disk image"), btrfs: tr("Btrfs subvolume"), zfs: tr("ZFS dataset"),
};

export const memLabel = (mb?: number) => (!mb ? tr("No limit") : mb >= 1024 ? tr("{0} GiB", (mb / 1024).toFixed(1)) : tr("{0} MiB", mb));

export function useLXCAction(name: string) {
  return useAction((a: string) => api.post(`/api/lxc/${encodeURIComponent(name)}/${a}`), { success: tr("Command sent") });
}

/** A container in the machine list of the Virtual machines app. */
export function ContainerCard({ c, onOpen, onConsole }: { c: Container; onOpen: (tab?: string) => void; onConsole: () => void }) {
  const { confirm } = useUI();
  const act = useLXCAction(c.name);
  const [label, tone] = lxcState[c.state] ?? [c.state, "neutral"];
  const running = c.state === "running";
  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <button type="button" className="flex min-w-0 items-center gap-3 text-left" onClick={() => onOpen()}>
          <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-ok/10 text-ok"><Box size={22} /></div>
          <div className="min-w-0">
            <div className="flex items-center gap-2 font-semibold">{c.name}<Badge tone="accent">{tr("Container")}</Badge></div>
            <div className="text-muted truncate text-[12px]">
              {c.image.distro} {c.image.release}{c.privileged ? ` · ${tr("privileged")}` : ""}{c.autostart && ` · ${tr("auto-start #{0}", c.autostartOrder ?? 0)}`}{c.snapshots > 0 && ` · ${tr("{0} snapshot(s)", c.snapshots)}`}
            </div>
          </div>
        </button>
        <Badge tone={c.missing ? "danger" : tone} dot>{c.missing ? tr("Storage missing") : label}</Badge>
      </div>
      <div className="mt-4 grid grid-cols-2 gap-3 text-[12.5px] sm:grid-cols-4">
        <div><div className="text-muted flex items-center gap-1"><Cpu size={12} />{tr("CPU")}</div><b>{c.cpus ? tr("{0} core(s)", c.cpus) : tr("No limit")}</b></div>
        <div><div className="text-muted">{tr("Memory")}</div><b>{memLabel(c.memoryMb)}</b>{running && c.memoryBytes ? <span className="text-muted"> ({bytes(c.memoryBytes)})</span> : null}</div>
        <div><div className="text-muted flex items-center gap-1"><HardDrive size={12} />{tr("Disk")}</div><b>{c.storage.sizeGb ? tr("{0} GB", c.storage.sizeGb) : "—"}</b> <span className="text-muted">{backendLabel[c.storage.backend]}</span></div>
        <div><div className="text-muted">{tr("IP address")}</div><b className="mono">{c.ips[0] ?? (running ? "…" : "—")}</b></div>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-3">
        {!running && <Button size="sm" variant="primary" icon={<Play size={14} />} disabled={c.missing} loading={act.isPending} onClick={() => act.mutate("start")}>{tr("Start")}</Button>}
        {running && <Button size="sm" variant="primary" icon={<TerminalSquare size={14} />} onClick={onConsole}>{tr("Console")}</Button>}
        {running && <Button size="sm" icon={<Power size={14} />} onClick={() => act.mutate("stop")}>{tr("Stop")}</Button>}
        {running && <Button size="sm" icon={<RotateCcw size={14} />} onClick={() => act.mutate("restart")}>{tr("Restart")}</Button>}
        {(running || c.state === "stopping" || c.state === "starting") && <IconButton title={tr("Force stop")} onClick={async () => (await confirm({ title: tr("Force stop?"), text: tr("The container's processes are killed at once: unsaved data is lost."), confirm: tr("Force"), danger: true })) && act.mutate("kill")}><Square size={15} /></IconButton>}
        <div className="flex-1" />
        <IconButton title={tr("Snapshots")} onClick={() => onOpen("snapshots")}><Camera size={15} /></IconButton>
        <IconButton title={tr("Settings")} onClick={() => onOpen()}><Settings2 size={15} /></IconButton>
      </div>
    </Card>
  );
}
