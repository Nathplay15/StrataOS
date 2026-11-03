import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArchiveRestore, CheckCircle2, Cloud, FlaskConical, HardDrive, Server, ShieldCheck, XCircle } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, cx } from "../../lib/format";
import type { BackupTask } from "../../lib/types";
import { Badge, Button, Card, Modal, Notice, Switch } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";

type Copy = { id: string; name: string; kind: string; media: "nas" | "usb" | "remote" | "cloud"; offsite: boolean; healthy: boolean; lastOk?: string; why?: string };
type RestoreTest = { time: string; status: "success" | "failed"; files: number; verified: number; bytes: number; message: string };
export type ProtectionState = {
  rule: { copies: number; media: number; offsite: number; ok: boolean; missing: string[]; list: Copy[] };
  restoreTestOff: boolean;
  lastRestoreTest?: string;
  testable: string[];
  tests: { id: string; name: string; result: RestoreTest }[];
  usbVolumes: { name: string; path: string }[];
};

const mediaLabel = { nas: tr("This NAS"), usb: tr("USB disk"), remote: tr("Another NAS or server"), cloud: tr("Cloud") };

/** Last restore test of a task, for its card. */
export function RestoreTestBadge({ t }: { t?: RestoreTest }) {
  if (!t) return null;
  return (
    <div className={cx("mt-2 flex items-start gap-2 rounded-lg px-3 py-1.5 text-[12px]", t.status === "success" ? "bg-ok/10" : "bg-danger/10 text-danger")} title={srv(t.message)}>
      <FlaskConical size={14} className="mt-0.5 shrink-0" />
      <span className="min-w-0 break-words">{t.status === "success" ? tr("Restore test passed {0}: {1} file(s), {2} checked against the original", ago(t.time), t.files, t.verified) : tr("Restore test failed {0}: {1}", ago(t.time), srv(t.message))}</span>
    </div>
  );
}

function Gauge({ n, want, label }: { n: number; want: number; label: string }) {
  const ok = n >= want;
  return (
    <div className={cx("rounded-xl px-4 py-3", ok ? "bg-ok/10" : "bg-warn/10")}>
      <div className="flex items-baseline gap-1"><span className="text-[22px] font-semibold">{n}</span><span className="text-muted text-[12px]">/ {want}</span></div>
      <div className="flex items-center gap-1 text-[12px]">{ok ? <CheckCircle2 size={13} className="text-ok" /> : <XCircle size={13} className="text-warn" />}{label}</div>
    </div>
  );
}

/** Guided choice of an off-site (or other medium) copy; hands a prefilled task to the editor. */
function OffsiteModal({ data, sources, onPreset, onClose }: { data: ProtectionState; sources: string[]; onPreset: (b: Partial<BackupTask>) => void; onClose: () => void }) {
  const fleet = useQuery({ queryKey: ["fleet"], queryFn: () => api.get<{ id: string; name: string; host: string }[]>("/api/fleet") });
  const members = (fleet.data ?? []).filter((m) => m.id !== "local");
  const base: Partial<BackupTask> = { kind: "restic", sources, cron: "0 2 * * *", enabled: true, keepDaily: 7, keepWeekly: 4, keepMonthly: 12 };
  return (
    <Modal open onClose={onClose} wide title={tr("Add an off-site copy")}>
      <p className="mb-4 text-[13px] text-muted">{tr("A copy away from the NAS survives theft, fire, a power surge or ransomware reaching the NAS. Choose where it goes: the backup editor opens with everything prefilled, you only add the access details.")}</p>
      <div className="grid gap-3 sm:grid-cols-3">
        <button type="button" className="rounded-xl border border-line p-4 text-left hover:border-accent" onClick={() => onPreset({ ...base, name: tr("Off-site copy to the cloud"), destType: "s3", destPath: "", s3Endpoint: "https://" })}>
          <Cloud className="mb-2 text-accent" /><div className="font-medium text-[13px]">{tr("Cloud")}</div>
          <div className="text-muted text-[12px]">{tr("S3, Backblaze B2, Wasabi, MinIO… Encrypted on the NAS before leaving: the provider cannot read it.")}</div>
        </button>
        <button type="button" className="rounded-xl border border-line p-4 text-left hover:border-accent" onClick={() => onPreset({ ...base, name: tr("Off-site copy to another NAS"), destType: "strataos", destPath: "" })}>
          <Server className="mb-2 text-accent" /><div className="font-medium text-[13px]">{tr("Another StrataOS NAS")}</div>
          <div className="text-muted text-[12px]">{tr("At a friend's or at the office: create a vault there (Backup → Backup vaults), then pair with its code.")}</div>
          {members.length > 0 && <div className="mt-2 text-[11.5px]">{tr("NAS of your fleet:")} {members.map((m) => `${m.name} (${m.host})`).join(", ")}</div>}
        </button>
        <button type="button" className="rounded-xl border border-line p-4 text-left hover:border-accent" disabled={!data.usbVolumes.length} onClick={() => onPreset({ ...base, name: tr("Copy to USB disk"), destType: "local", destPath: `${data.usbVolumes[0].path}/strataos-backup`, offsite: true })}>
          <HardDrive className="mb-2 text-accent" /><div className="font-medium text-[13px]">{tr("USB disk")}</div>
          <div className="text-muted text-[12px]">{data.usbVolumes.length ? tr("On {0}. It counts as off-site only if you keep it elsewhere and bring it back for the backups.", data.usbVolumes.map((v) => v.name).join(", ")) : tr("Plug a USB disk and create a volume on it first (Storage → Volumes).")}</div>
        </button>
      </div>
    </Modal>
  );
}

/** 3-2-1 assistant and restore tests, at the top of Backup. */
export function ProtectionCard({ sources, onPreset }: { sources: string[]; onPreset: (b: Partial<BackupTask>) => void }) {
  const { data } = useQuery({ queryKey: ["protection"], queryFn: () => api.get<ProtectionState>("/api/protection"), refetchInterval: 30000 });
  const [offsite, setOffsite] = useState(false);
  const [explain, setExplain] = useState(false);
  const test = useAction(() => api.post("/api/protection/restore-test", {}));
  const toggle = useAction((off: boolean) => api.put("/api/protection", { restoreTestOff: off }));
  if (!data) return null;
  const r = data.rule;
  return (
    <Card className="mb-6" title={<span className="flex items-center gap-2"><ShieldCheck size={16} />{tr("3-2-1 rule")}{r.ok ? <Badge tone="ok">{tr("Met")}</Badge> : <Badge tone="warn">{tr("Not met")}</Badge>}</span>}
      actions={<><Button size="sm" variant="ghost" onClick={() => setExplain(true)}>{tr("What is it?")}</Button>{!r.ok && <Button size="sm" variant="primary" onClick={() => setOffsite(true)}>{tr("Add an off-site copy")}</Button>}</>}>
      <div className="grid gap-3 sm:grid-cols-3">
        <Gauge n={r.copies} want={3} label={tr("copies of the data (the original counts)")} />
        <Gauge n={r.media} want={2} label={tr("different media")} />
        <Gauge n={r.offsite} want={1} label={tr("copy off-site")} />
      </div>
      {r.list.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1.5">
          {r.list.map((c) => <Badge key={c.id} tone={c.healthy ? "ok" : "warn"} title={c.why ? srv(c.why) : undefined}>{c.name} · {mediaLabel[c.media]}{c.offsite ? ` · ${tr("off-site")}` : ""}{c.why ? ` · ${srv(c.why)}` : ""}</Badge>)}
        </div>
      )}
      <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-line pt-3">
        <FlaskConical size={16} className="text-accent" />
        <div className="min-w-0 flex-1 text-[12.5px]">
          <div className="font-medium">{tr("Monthly restore test")}</div>
          <div className="text-muted">{tr("Each month, a sample of files is restored from every backup to a temporary folder and compared with the originals.")}{data.lastRestoreTest ? " " + tr("Last run {0}.", ago(data.lastRestoreTest)) : ""}</div>
        </div>
        <Switch aria-label={tr("Monthly restore test")} checked={!data.restoreTestOff} onChange={(v) => toggle.mutate(!v)} />
        <Button size="sm" icon={<ArchiveRestore size={14} />} disabled={!data.testable.length} loading={test.isPending} onClick={() => test.mutate()}>{tr("Test now")}</Button>
      </div>
      <Modal open={explain} onClose={() => setExplain(false)} title={tr("The 3-2-1 rule")} footer={<Button onClick={() => setExplain(false)}>{tr("Close")}</Button>}>
        <div className="space-y-3 text-[13px]">
          <p>{tr("Keep 3 copies of your data: the original on the NAS and two backups.")}</p>
          <p>{tr("On 2 different media: the disks of the NAS, plus a USB disk, another NAS or the cloud. RAID and snapshots are not backups: they live on the same disks.")}</p>
          <p>{tr("1 copy off-site: in another building or in the cloud, out of reach of theft, fire and ransomware on your network.")}</p>
          <Notice>{tr("Only tasks that succeeded recently count. A copy on a USB disk counts as off-site when you tick “kept off-site” in the task.")}</Notice>
        </div>
      </Modal>
      {offsite && <OffsiteModal data={data} sources={sources} onClose={() => setOffsite(false)} onPreset={(b) => { setOffsite(false); onPreset(b); }} />}
    </Card>
  );
}
