import { useQuery } from "@tanstack/react-query";
import { Cpu, Undo2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import { Badge, Button, Card, Notice, useUI } from "../../components/ui";
import { dateTime } from "../../lib/format";
import { tr, srv } from "../../lib/i18n";

type RPiKernel = {
  model: string;
  pi5: boolean;
  kernel: string;
  applicable: boolean;
  reason?: string;
  phase?: "" | "running" | "switched" | "fell-back" | "failed" | "done";
  message?: string;
  updated?: string;
  backup?: string;
  restorePoint?: string;
  canUndo: boolean;
};

/** Switch of a Raspberry Pi installed from an older image (Debian's kernel) to the Raspberry Pi kernels. */
export function RPiKernelCard() {
  const { confirm } = useUI();
  const { jobs } = useJobs();
  const { data } = useQuery({ queryKey: ["rpi-kernel"], queryFn: () => api.get<RPiKernel>("/api/system/rpi-kernel") });
  const run = useAction(() => api.post("/api/system/rpi-kernel/switch"));
  const undo = useAction(() => api.post("/api/system/rpi-kernel/undo"));
  if (!data?.model) return null;
  const phase = data.phase ?? "";
  const busy = jobs.some((j) => j.type === "rpi-kernel" && j.status === "running");
  // Nothing to show on a Raspberry Pi that already runs its own kernel, except right after the switch.
  const recent = data.updated && Date.now() - new Date(data.updated).getTime() < 7 * 86400e3;
  if (!data.applicable && !data.canUndo && !busy && !(phase === "done" && recent) && phase !== "switched" && phase !== "failed") return null;

  const start = async () => {
    const ok = await confirm({
      title: tr("Switch to the Raspberry Pi kernel?"),
      text: (
        <div className="space-y-2">
          <p>{tr("StrataOS creates a restore point, keeps a copy of the firmware partition, installs the Raspberry Pi kernels and boot firmware from the Raspberry Pi archive (nothing else comes from it), writes the boot configuration of the current Raspberry Pi image and restarts the NAS. Your own additions to config.txt and cmdline.txt are kept.")}</p>
          <p>{tr("The Debian kernel stays on the card until the NAS has started on the Raspberry Pi kernel, then it is removed.")}</p>
          <p className="text-warn">{tr("There is no automatic way back if the NAS does not start: put the card in a computer, rename config.txt to config-rpi.txt and config-debian.txt to config.txt (see README-StrataOS.txt on the card). The Raspberry Pi then starts the Debian kernel again.")}</p>
        </div>
      ),
      confirm: tr("Switch and restart"),
    });
    if (ok) run.mutate();
  };

  return (
    <Card title={<span className="flex items-center gap-2"><Cpu size={16} className="text-accent" />{tr("Raspberry Pi kernel")}</span>} className="mb-5">
      <div className="space-y-3 text-[13px]">
        <div className="flex flex-wrap items-center gap-2">
          <b>{data.model}</b>
          <span className="mono text-muted">{data.kernel}</span>
          {phase === "done" ? <Badge tone="ok">{tr("Raspberry Pi kernel")}</Badge> : data.applicable && <Badge tone={data.pi5 ? "danger" : "warn"}>{tr("Debian kernel")}</Badge>}
        </div>
        {phase === "done" && <Notice>{srv(data.message)}</Notice>}
        {phase === "switched" && !busy && <Notice tone="warn">{tr("The switch is made: the NAS restarts on the Raspberry Pi kernel, then StrataOS confirms it and removes the Debian kernel.")}</Notice>}
        {phase === "fell-back" && <Notice tone="warn">{tr("The Raspberry Pi started on the Debian kernel again (config-debian.txt). \"Undo\" removes the Raspberry Pi kernels and puts the previous firmware partition back.")}</Notice>}
        {phase === "failed" && <Notice tone="warn">{srv(data.message)} {data.canUndo && tr("\"Undo\" removes the Raspberry Pi kernels and puts the previous firmware partition back.")}</Notice>}
        {phase === "" && data.message && <Notice>{srv(data.message)}</Notice>}
        {data.applicable && (
          <>
            <div className="text-muted">
              {data.pi5
                ? tr("This Raspberry Pi 5 runs Debian's kernel, which has no driver for its Ethernet and USB ports. The Raspberry Pi kernel is needed.")
                : tr("This system was installed from an older StrataOS image and runs Debian's kernel. The Raspberry Pi kernel (used by the current image) supports every model, including the Raspberry Pi 5, and is recommended.")}
            </div>
            {data.reason && <Notice tone="warn">{srv(data.reason)}</Notice>}
          </>
        )}
        {data.backup && phase !== "done" && phase !== "" && <div className="text-muted text-[12px]">{tr("Copy of the previous firmware partition: {0}", data.backup)}{data.updated ? ` · ${dateTime(data.updated)}` : ""}</div>}
        <div className="flex flex-wrap gap-2">
          {data.applicable && <Button variant="primary" disabled={busy || !!data.reason} loading={run.isPending} onClick={start}>{tr("Switch to the Raspberry Pi kernel (needed for the Raspberry Pi 5, recommended for Pi 3/4)")}</Button>}
          {data.canUndo && <Button icon={<Undo2 size={14} />} disabled={busy} loading={undo.isPending} onClick={async () => { if (await confirm({ title: tr("Undo the switch?"), text: tr("The Raspberry Pi archive and kernels are removed, the previous packages and firmware partition are put back. The NAS keeps running on the Debian kernel."), confirm: tr("Undo"), danger: true })) undo.mutate(); }}>{tr("Undo")}</Button>}
        </div>
      </div>
    </Card>
  );
}
