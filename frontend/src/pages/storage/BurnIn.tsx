import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FlaskConical, Square } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Disk } from "../../lib/types";
import { bytes, dateTime, duration } from "../../lib/format";
import { Badge, Button, Checkbox, Field, Input, Modal, Notice, Progress } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

export type BurnInStatus = { jobId: string; device: string; serial: string; step: string; progress: number; stepProgress: number; eta: number; errors: number; started: string };
export type BurnInReport = {
  id: string; device: string; serial: string; model: string; size: number; mode: "read" | "write"; started: string; finished: string;
  result: "passed" | "failed" | "cancelled"; problems?: string[]; warnings?: string[]; badBlocks: number;
  steps: { name: string; status: string; detail?: string }[]; attrs?: { id: number; name: string; before: number; after: number }[]; blocks?: number;
};
export type BurnInData = { running: BurnInStatus[]; reports: BurnInReport[] };

export const useBurnIns = () => useQuery({
  queryKey: ["burnin"], queryFn: () => api.get<BurnInData>("/api/storage/burnin"),
  refetchInterval: (q) => (q.state.data?.running.length ? 3000 : 30000),
});

const stepLabel = (s: string) => ({ "smart-short": tr("SMART short test"), badblocks: tr("Surface test (badblocks)"), "smart-long": tr("SMART extended test") }[s] ?? s);
const statusTone = (s: string): "ok" | "danger" | "warn" | "neutral" => (s === "passed" ? "ok" : s === "failed" ? "danger" : s === "cancelled" ? "warn" : "neutral");
const statusLabel = (s: string) => ({ passed: tr("Passed"), failed: tr("Failed"), cancelled: tr("Cancelled"), skipped: tr("Skipped") }[s] ?? s);

export function BurnInBadge({ report, onClick }: { report: BurnInReport; onClick: () => void }) {
  return (
    <button onClick={onClick} title={dateTime(report.finished)}>
      <Badge tone={statusTone(report.result)} dot>{report.result === "passed" ? tr("Burn-in passed") : report.result === "failed" ? tr("Burn-in failed") : tr("Burn-in cancelled")}</Badge>
    </button>
  );
}

export function ReportView({ r }: { r: BurnInReport }) {
  return (
    <div className="space-y-3 text-[13px]">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={statusTone(r.result)} dot>{statusLabel(r.result)}</Badge>
        <span className="text-muted">{r.mode === "write" ? tr("Destructive write test") : tr("Non-destructive read test")} · {dateTime(r.started)} → {dateTime(r.finished)}{r.blocks ? ` · ${tr("partial test")}` : ""}</span>
      </div>
      {!!r.problems?.length && <Notice tone="warn">{r.problems.map((p) => srv(p)).join(" · ")}</Notice>}
      {!!r.warnings?.length && <div className="text-warn">{r.warnings.map((p) => srv(p)).join(" · ")}</div>}
      <div className="space-y-1">
        {r.steps.map((s) => (
          <div key={s.name} className="flex items-center gap-2">
            <Badge tone={statusTone(s.status)}>{statusLabel(s.status)}</Badge><span>{stepLabel(s.name)}</span>{s.detail && <span className="truncate text-muted text-[12px]">{srv(s.detail)}</span>}
          </div>
        ))}
      </div>
      {!!r.attrs?.length && (
        <table className="table">
          <thead><tr><th>{tr("SMART counter")}</th><th className="text-right">{tr("Before")}</th><th className="text-right">{tr("After")}</th></tr></thead>
          <tbody>{r.attrs.map((a) => <tr key={a.id} className={a.after > a.before ? "text-danger" : ""}><td>{srv(a.name)}</td><td className="text-right">{a.before}</td><td className="text-right">{a.after}</td></tr>)}</tbody>
        </table>
      )}
      <div className="text-muted text-[12px]">{tr("Bad blocks: {0}", r.badBlocks)}</div>
    </div>
  );
}

export function BurnInModal({ disk, data, onClose }: { disk: Disk | null; data?: BurnInData; onClose: () => void }) {
  const [mode, setMode] = useState<"read" | "write">("read");
  const [shortTest, setShort] = useState(true);
  const [longTest, setLong] = useState(true);
  const [typed, setTyped] = useState("");
  const running = data?.running.find((r) => r.device === disk?.path);
  const reports = (data?.reports ?? []).filter((r) => disk && r.serial === disk.serial && (disk.serial || r.device === disk.path));
  const start = useAction(() => api.post("/api/storage/burnin", { device: disk!.path, mode, confirm: typed, shortTest, longTest }));
  const cancel = useAction((id: string) => api.post(`/api/jobs/${id}/cancel`), { success: tr("Burn-in test cancelled") });
  const free = disk?.usage === "free" && !disk.system;
  return (
    <Modal open={!!disk} onClose={onClose} wide title={tr("Burn-in test — {0}", disk?.model || disk?.path)} footer={running
      ? <Button variant="danger" icon={<Square size={14} />} loading={cancel.isPending} onClick={() => cancel.mutate(running.jobId)}>{tr("Stop the test")}</Button>
      : free && <Button variant={mode === "write" ? "danger" : "primary"} icon={<FlaskConical size={15} />} loading={start.isPending} disabled={mode === "write" && typed !== disk?.name} onClick={() => start.mutate()}>{tr("Start the test")}</Button>}>
      {disk && <div className="space-y-5">
        <p className="text-muted text-[13px]">{tr("Checks a new disk before trusting it with data: SMART short test, surface test of every block, SMART extended test, then compares the reallocated and pending sectors before and after.")}</p>
        {running ? (
          <div className="space-y-2">
            <div className="flex justify-between text-[13px]"><span>{stepLabel(running.step)}</span><span className="text-muted">{running.eta > 0 ? tr("About {0} left", duration(running.eta)) : ""}</span></div>
            <Progress value={running.progress} tone="accent" />
            <div className="text-muted text-[12px]">{tr("{0}% done · started {1}", running.progress.toFixed(1), dateTime(running.started))}{running.errors > 0 && <span className="text-danger"> · {tr("{0} error(s)", running.errors)}</span>}</div>
          </div>
        ) : !free ? <Notice tone="warn">{tr("Only a free disk (not part of a volume, array or pool) can be tested.")}</Notice> : (
          <div className="space-y-4">
            <div className="grid gap-2 sm:grid-cols-2">
              {(["read", "write"] as const).map((m) => (
                <button key={m} onClick={() => setMode(m)} className={`rounded-xl border p-3 text-left text-[13px] ${mode === m ? (m === "write" ? "border-danger bg-danger/10" : "border-accent bg-accent-soft") : "border-line"}`}>
                  <div className="font-semibold">{m === "read" ? tr("Read-only (non-destructive)") : tr("Write test (erases the disk)")}</div>
                  <div className="text-muted text-[12px]">{m === "read" ? tr("Reads every block. Data on the disk is left untouched.") : tr("Writes and checks 4 patterns on every block: the most thorough test, about 8 times longer than a full read.")}</div>
                </button>
              ))}
            </div>
            <div className="space-y-1.5">
              <Checkbox checked={shortTest} onChange={setShort} label={tr("SMART short test first (about 2 minutes)")} />
              <Checkbox checked={longTest} onChange={setLong} label={tr("SMART extended test at the end (several hours on a large disk)")} />
            </div>
            {mode === "write" && (
              <Notice tone="warn">
                <b>{tr("This erases the disk.")}</b>{" "}{tr("Everything on {0} ({1}) is overwritten, partitions and signatures included.", disk.path, bytes(disk.size))}
                <Field label={tr("Type \"{0}\" to confirm", disk.name)} className="mt-3"><Input value={typed} onChange={(e) => setTyped(e.target.value)} /></Field>
              </Notice>
            )}
            <p className="text-muted text-[12px]">{tr("A full test of a large disk takes a day or more. The disk stays reserved until the end; the result is notified.")}</p>
          </div>
        )}
        {reports.length > 0 && (
          <div className="space-y-4 border-t border-line pt-4">
            <div className="font-semibold text-[13.5px]">{tr("Previous results")}</div>
            {reports.slice(0, 3).map((r) => <ReportView key={r.id} r={r} />)}
          </div>
        )}
      </div>}
    </Modal>
  );
}
