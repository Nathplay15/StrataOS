import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArchiveRestore, Copy, HardDriveDownload, Layers, Server, ShieldCheck } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import type { Job } from "../../lib/types";
import { bytes, cx, dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, Progress, Spinner, useUI } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";

// System disk (Storage → Disks): where the system lives — its own disk,
// mirrored on a second disk, or on the data disks (each keeping a data
// partition) — moving it between these, and a system backup. The server
// plans everything again before acting (internal/sysdisk) and asks for the
// names of the disks it erases.

type DiskRef = { name: string; path: string; serial?: string; model?: string; size: number; esp?: string };
type SysArray = { name: string; device: string; label?: string; role: string; health: string; devices: number; active: number; sync?: string; percent?: number };
export type Layout = {
  boot: string; arch: string; rootOn: string; root: { fstype: string; source: string; subvol?: string };
  disks: DiskRef[]; arrays?: SysArray[]; used: number; mirrored: boolean; degraded?: boolean; problems?: string[];
  placement?: "own" | "mirror" | "datadisks"; dataParts?: string[];
};
type Kind = "mirror" | "move" | "datadisks";
type Candidate = { path: string; name: string; model?: string; serial?: string; size: number; kind?: string; ok: boolean; reason?: string };
type Plan = {
  kind: string; boot: string; fsType: string; old?: DiskRef; oldDisks?: DiskRef[]; need: number; used: number; candidates: Candidate[]; target?: Candidate;
  targets?: Candidate[]; confirm?: string;
  parts?: { num: number; role: string; mib: number }[]; refusal?: string; warnings?: string[]; steps?: string[];
};
type Op = {
  id: string; jobId?: string; kind: string; phase: string; message?: string; error?: string; warnings?: string[]; unreadable?: string[]; unreadableCount?: number;
  boot: string; old: string; oldSerial?: string; oldModel?: string; new: string; newSerial?: string; newModel?: string; started: string; finished?: string;
  extra?: string[]; oldDisks?: string[];
};
type Backup = { id: string; path: string; size: number; created: string; hostname?: string; version?: string };
type SysDisk = { layout: Layout; mirror: Plan; move: Plan; datadisks?: Plan; ops?: Op[]; backups?: Backup[]; smart?: { Failing: boolean; Weak?: string[] } };

export const useSysDisk = () => useQuery({ queryKey: ["sysdisk"], queryFn: () => api.get<SysDisk>("/api/system/disk"), refetchInterval: 5000 });

const phaseText: Record<string, string> = {
  prepare: tr("Preparing the new disk"),
  copy: tr("Copying the system"),
  ready: tr("Copied: ready to restart"),
  switching: tr("Final copy and boot loader — do not power off"),
  rebooting: tr("Restarting"),
  booted: tr("Running from the new disk"),
  "fell-back": tr("Started from the old disk again"),
  completing: tr("Adding the old disk to the mirror"),
  finishing: tr("Finishing the move"),
  cancelling: tr("Cancelling"),
  done: tr("Done"),
  failed: tr("Failed"),
  cancelled: tr("Cancelled"),
};

const kindTitle = (k: Kind) => k === "mirror" ? tr("Mirror the system disk") : k === "datadisks" ? tr("Move the system onto the data disks") : tr("Move the system to another disk");

function Wizard({ kind, onClose }: { kind: Kind; onClose: () => void }) {
  const [targets, setTargets] = useState<string[]>([]);
  const [typed, setTyped] = useState("");
  const { track } = useJobs();
  const multi = kind === "datadisks";
  const q = targets.map((t) => `&target=${encodeURIComponent(t)}`).join("");
  const plan = useQuery({ queryKey: ["sysdisk-plan", kind, targets], queryFn: () => api.get<Plan>(`/api/system/disk/plan?kind=${kind}${q}`) });
  const start = useAction(() => api.post<{ job: Job }>("/api/system/disk/start", multi ? { kind, targets, confirm: typed } : { kind, target: targets[0] ?? "", confirm: typed }), { onDone: (r) => { track(r.job); onClose(); } });
  const p = plan.data;
  const tname = p?.confirm ?? p?.target?.name ?? "";
  const pick = (path: string) => { setTyped(""); setTargets(multi ? (targets.includes(path) ? targets.filter((t) => t !== path) : [...targets, path]) : [path]); };
  const old = p?.oldDisks?.length ? p.oldDisks.map((d) => d.name).join(", ") : p?.old ? `${p.old.name} · ${bytes(p.old.size)}` : "—";
  return (
    <Modal open onClose={onClose} wide title={kindTitle(kind)} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="danger" disabled={!p?.target || !!p.refusal || typed !== tname} loading={start.isPending} onClick={() => start.mutate()}>{tr("Erase {0} and copy the system", tname || "…")}</Button>
    </>}>
      {plan.isLoading ? <Spinner /> : plan.error ? <ErrorBox error={plan.error} /> : p && (
        <div className="space-y-4 text-[13px]">
          <p className="text-muted">
            {kind === "mirror"
              ? tr("The system is copied onto a second disk as a RAID 1 mirror: the NAS keeps starting when one of the two disks fails. The data volumes are not touched.")
              : kind === "datadisks"
                ? tr("For a NAS without a separate system disk: each chosen disk gets a copy of the system (RAID 1 over all of them, about 20 GB) and a data partition with the rest of its space, where you create volumes afterwards. The NAS keeps starting while one disk is left.")
                : tr("The system is copied onto another disk (to replace a failing disk, or move to an SSD), which then starts the NAS. The data volumes are not touched.")}
          </p>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="rounded-xl bg-panel-2 p-3"><div className="text-muted text-[12px]">{tr("System disk")}</div><div className="font-semibold">{old}</div></div>
            <div className="rounded-xl bg-panel-2 p-3"><div className="text-muted text-[12px]">{tr("Space used by the system")}</div><div className="font-semibold">{bytes(p.used)}</div></div>
            <div className="rounded-xl bg-panel-2 p-3"><div className="text-muted text-[12px]">{tr("Start mode, file system")}</div><div className="font-semibold">{p.boot.toUpperCase()} · {p.fsType}</div></div>
          </div>
          {p.refusal && !p.target ? <Notice tone="warn">{srv(p.refusal)}</Notice> : (
            <>
              <div>
                <div className="mb-2 font-medium">{multi ? tr("Data disks (two or more)") : tr("New disk")}</div>
                <div className="space-y-1.5">
                  {p.candidates.length === 0 && <div className="text-muted">{tr("No other disk is connected.")}</div>}
                  {p.candidates.map((c) => (
                    <label key={c.path} className={cx("flex items-center gap-3 rounded-xl border px-3 py-2", c.ok ? "cursor-pointer border-line hover:border-accent/40" : "border-line opacity-60", targets.includes(c.path) && "border-accent bg-accent-soft")}>
                      <input type={multi ? "checkbox" : "radio"} disabled={!c.ok} checked={targets.includes(c.path)} onChange={() => pick(c.path)} />
                      <span className="mono">{c.name}</span>
                      <span className="flex-1 truncate">{c.model} {c.serial && <span className="text-muted">· {c.serial}</span>}</span>
                      <span>{bytes(c.size)}</span>
                      {!c.ok && <span className="text-muted text-[12px]">{srv(c.reason)}</span>}
                    </label>
                  ))}
                </div>
                <div className="mt-1 text-muted text-[12px]">{tr("At least {0}. Disks holding data are not offered: erase them first if you are sure.", bytes(p.need))}</div>
              </div>
              {p.refusal && <Notice tone="warn">{srv(p.refusal)}</Notice>}
              {!!p.warnings?.length && <Notice tone="warn"><ul className="list-disc pl-4">{p.warnings.map((w) => <li key={w}>{srv(w)}</li>)}</ul></Notice>}
              {!!p.steps?.length && (
                <div>
                  <div className="mb-1 font-medium">{tr("Steps")}</div>
                  <ol className="list-decimal space-y-0.5 pl-5">{p.steps.map((s) => <li key={s}>{srv(s)}</li>)}</ol>
                </div>
              )}
              {p.target && !p.refusal && (
                <>
                  {(p.targets?.length ?? 0) > 1 && (
                    <Notice tone="warn">
                      <div className="mb-1 font-medium">{tr("These disks are completely erased:")}</div>
                      <ul className="list-disc pl-4">{p.targets!.map((c) => <li key={c.path}><span className="mono">{c.name}</span> · {c.model} {c.serial && `· ${c.serial}`} · {bytes(c.size)}</li>)}</ul>
                    </Notice>
                  )}
                  <Field label={(p.targets?.length ?? 0) > 1 ? tr("Type {0} to confirm: everything on these disks is erased", tname) : tr("Type {0} to confirm: everything on this disk is erased", tname)}>
                    <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={tname} />
                  </Field>
                </>
              )}
            </>
          )}
        </div>
      )}
    </Modal>
  );
}

function OpPanel({ op }: { op: Op }) {
  const { confirm } = useUI();
  const { jobs, track } = useJobs();
  const job = jobs.find((j) => j.id === op.jobId && j.status === "running");
  const act = useAction((a: { path: string; body?: unknown }) => api.post<{ job?: Job }>(`/api/system/disk/${op.id}/${a.path}`, a.body ?? {}), { onDone: (r) => r?.job && track(r.job) });
  const forget = useAction(() => api.del(`/api/system/disk/${op.id}`));
  const oldName = op.oldDisks?.length ? op.oldDisks.map((d) => d.replace("/dev/", "")).join(", ") : op.old.replace("/dev/", "");
  const newNames = [op.new, ...(op.extra ?? [])].map((d) => d.replace("/dev/", "")).join(", ");
  const running = !op.finished;
  const tone = op.phase === "failed" || op.phase === "fell-back" ? "danger" : op.phase === "done" ? "ok" : "accent";

  const restart = async () => {
    if (await confirm({ title: tr("Restart from the new disk?"), text: tr("The last changes are copied, the boot loader is installed on {0}, then the NAS restarts once from it. Do not power off the NAS until it answers again (a few minutes). If it does not start, the next restart uses the old disk, which is not changed.", op.new), confirm: tr("Restart") })) {
      act.mutate({ path: "switch" });
    }
  };
  const complete = async () => {
    if (op.kind === "mirror") {
      if (await confirm({ title: tr("Complete the mirror?"), text: tr("{0} (the old system disk) is erased, partitioned like the new disk and added to the mirror. Its data is copied back from the new disk (rebuild).", op.old), danger: true, typeToConfirm: oldName, confirm: tr("Complete the mirror") })) {
        act.mutate({ path: "complete", body: { confirm: oldName } });
      }
    } else if (await confirm({ title: tr("Finish the move?"), text: tr("The system stays on the new disk. Power off the NAS afterwards and remove the old disk, or erase it below to use it for data."), confirm: tr("Finish") })) {
      act.mutate({ path: "complete" });
    }
  };
  const erase = async () => {
    const text = (op.oldDisks?.length ?? 0) > 1
      ? tr("The old system is erased from {0}: whole disks become free for data, and disks that keep a data partition only lose their system partitions.", oldName)
      : tr("{0} is erased: it can then hold data. The old system on it is lost.", op.old);
    if (await confirm({ title: tr("Finish and erase the old disk?"), text, danger: true, typeToConfirm: oldName, confirm: tr("Erase") })) {
      act.mutate({ path: "complete", body: { erase: true, confirm: oldName } });
    }
  };
  return (
    <div className="mt-4 rounded-xl border border-line p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-semibold">{op.kind === "mirror" ? tr("Mirroring onto {0}", op.new) : op.kind === "datadisks" ? tr("Move onto the data disks {0}", newNames) : tr("Move to {0}", op.new)}</span>
        <Badge tone={tone} dot>{phaseText[op.phase] ?? op.phase}</Badge>
        <span className="text-muted text-[12px]">{dateTime(op.started)}</span>
      </div>
      {job && <div className="mt-3"><Progress value={job.progress} tone="accent" indeterminate={job.progress <= 0} /></div>}
      {["switching", "rebooting", "completing", "prepare"].includes(op.phase) && <div className="mt-3 flex items-center gap-2 text-warn text-[13px]"><AlertTriangle size={15} />{tr("Do not power off the NAS during this step.")}</div>}
      {op.message && <p className="mt-3 text-[13px]">{srv(op.message)}</p>}
      {op.error && <p className="mt-2 text-danger text-[13px]">{srv(op.error)}</p>}
      {!!op.unreadableCount && (
        <Notice tone="warn">
          {tr("{0} file(s) could not be read on the old disk and are missing from the copy:", op.unreadableCount)}
          <div className="mono mt-1 max-h-32 overflow-auto text-[12px]">{op.unreadable?.map((f) => <div key={f}>{f}</div>)}</div>
        </Notice>
      )}
      <div className="mt-3 flex flex-wrap gap-2">
        {running && (op.phase === "ready" || op.phase === "fell-back") && <Button variant="primary" onClick={restart} loading={act.isPending}>{op.phase === "ready" ? tr("Restart from the new disk") : tr("Try again")}</Button>}
        {running && op.phase === "booted" && <Button variant="primary" onClick={complete} loading={act.isPending}>{op.kind === "mirror" ? tr("Complete the mirror") : tr("Finish (keep the old disk)")}</Button>}
        {running && op.phase === "booted" && op.kind !== "mirror" && <Button variant="danger" onClick={erase}>{(op.oldDisks?.length ?? 0) > 1 ? tr("Finish and erase the old system disks") : tr("Finish and erase the old disk")}</Button>}
        {running && op.phase === "booted" && <Button onClick={async () => { if (await confirm({ title: tr("Go back to the old disk?"), text: tr("The NAS restarts from the old disk, unchanged since the copy: changes made since then on the new disk are not carried back."), confirm: tr("Restart") })) act.mutate({ path: "back" }); }}>{tr("Go back to the old disk")}</Button>}
        {running && ["ready", "fell-back", "failed"].includes(op.phase) && <Button variant="ghost" onClick={async () => { if (await confirm({ title: tr("Cancel?"), text: tr("The system stays on the old disk; {0} is erased.", op.new), danger: true })) act.mutate({ path: "cancel" }); }}>{tr("Cancel")}</Button>}
        {!running && <Button size="sm" variant="ghost" onClick={() => forget.mutate()}>{tr("Hide")}</Button>}
      </div>
    </div>
  );
}

function BackupBox({ backups }: { backups: Backup[] }) {
  const [folder, setFolder] = useState("");
  const { track } = useJobs();
  const run = useAction(() => api.post<{ job: Job }>("/api/system/disk/backup", { folder }), { onDone: (r) => track(r.job) });
  return (
    <div className="mt-5 border-t border-line pt-4">
      <div className="flex items-center gap-2 font-medium"><HardDriveDownload size={16} />{tr("System backup")}</div>
      <p className="mt-1 text-muted text-[12.5px]">
        {tr("An archive of the whole system (settings included) on a data volume. To start again on a new disk: boot the StrataOS installer and choose “Restore a StrataOS system backup”, with the disk holding the backup connected.")}
      </p>
      <div className="mt-2 flex flex-wrap gap-2">
        <Input className="min-w-64 flex-1" value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="/srv/strata/volume1/backups" />
        <Button icon={<ArchiveRestore size={14} />} disabled={!folder} loading={run.isPending} onClick={() => run.mutate()}>{tr("Back up the system")}</Button>
      </div>
      {backups.length > 0 && (
        <div className="mt-3 space-y-1 text-[12.5px]">
          {backups.slice(0, 5).map((b) => (
            <div key={b.id} className="flex justify-between gap-3"><span className="mono truncate">{b.path}</span><span className="text-muted">{dateTime(b.created)} · {bytes(b.size)}</span></div>
          ))}
        </div>
      )}
    </div>
  );
}

/** The System disk card of the Disks page (#system-disk). */
export function SystemDiskCard() {
  const { data, error } = useSysDisk();
  // The setup wizard asks for one at the end of the setup.
  const [wizard, setWizard] = useState<Kind | null>(() => {
    try {
      const k = sessionStorage.getItem("strata-sysdisk-wizard");
      sessionStorage.removeItem("strata-sysdisk-wizard");
      return k === "mirror" || k === "datadisks" ? k : null;
    } catch { return null; }
  });
  if (error) return <ErrorBox error={error} />;
  if (!data) return null;
  const l = data.layout;
  const op = data.ops?.find((o) => !o.finished) ?? data.ops?.[0];
  const busy = !!data.ops?.some((o) => !o.finished);
  const arrays = (l.arrays ?? []).filter((a) => a.role !== "other");
  return (
    <Card id="system-disk" className="mb-5"
      title={<span className="flex flex-wrap items-center gap-2"><Server size={17} />{tr("System disk")}
        {l.mirrored ? (l.degraded ? <Badge tone="danger" dot>{tr("Mirror degraded")}</Badge> : <Badge tone="ok" dot>{tr("Mirrored")}</Badge>) : <Badge tone="warn">{tr("No redundancy")}</Badge>}
        <Badge>{l.placement === "datadisks" ? tr("On the data disks") : l.placement === "mirror" ? tr("Own disks (mirror)") : tr("Own disk")}</Badge>
        <Badge>{l.boot.toUpperCase()}</Badge><Badge>{l.root.fstype}</Badge></span>}
      actions={<>
        {!l.mirrored && <Button size="sm" variant="primary" icon={<ShieldCheck size={14} />} disabled={busy} onClick={() => setWizard("mirror")}>{tr("Mirror the system disk")}</Button>}
        {!l.mirrored && <Button size="sm" icon={<Layers size={14} />} disabled={busy} onClick={() => setWizard("datadisks")}>{tr("Move onto the data disks")}</Button>}
        <Button size="sm" icon={<Copy size={14} />} disabled={busy || (l.mirrored && !!l.degraded)} onClick={() => setWizard("move")}>{l.mirrored ? tr("Move the system to a disk of its own") : tr("Move the system to another disk")}</Button>
      </>}>
      <div className="flex flex-wrap gap-x-6 gap-y-1 text-[13px]">
        {l.disks.map((d) => <span key={d.path}><span className="mono">{d.name}</span> <span className="text-muted">{d.model} · {bytes(d.size)}</span></span>)}
        <span className="text-muted">{tr("{0} used", bytes(l.used))}</span>
      </div>
      {arrays.map((a) => (
        <div key={a.name} className="mt-2 flex items-center gap-2 text-[12.5px]">
          <span className="mono">{a.label || a.name}</span><Badge tone={a.health === "healthy" ? "ok" : "warn"}>{a.health}</Badge>
          <span className="text-muted">{a.role === "root" ? tr("system") : a.role === "swap" ? tr("swap") : "/boot"} · {a.active}/{a.devices}</span>
          {a.sync && <span className="text-muted">{a.sync} {a.percent?.toFixed(1)} %</span>}
        </div>
      ))}
      {l.placement === "datadisks" && <p className="mt-2 text-muted text-[12.5px]">{tr("Each of these disks also has a data partition ({0}): create volumes on them in Storage like on free disks. A replaced disk gets the same partitions; add its data partition back to your data array afterwards.", (l.dataParts ?? []).map((d) => d.replace("/dev/", "")).join(", "))}</p>}
      {l.mirrored && l.degraded && <div className="mt-3"><Notice tone="warn">{tr("The system runs from one disk only. Replace the missing or failed disk with Replace on its card below (or from the alert).")}</Notice></div>}
      {data.smart?.Failing && !l.mirrored && <div className="mt-3"><Notice tone="warn">{tr("The system disk fails its SMART health check: move the system to a new disk now.")}</Notice></div>}
      {!l.mirrored && !!l.problems?.length && (
        <div className="mt-3 text-muted text-[12.5px]">{tr("Mirroring and moving are not available:")} {srv(l.problems[0])}</div>
      )}
      {op && <OpPanel op={op} />}
      <BackupBox backups={data.backups ?? []} />
      {wizard && <Wizard kind={wizard} onClose={() => setWizard(null)} />}
    </Card>
  );
}
