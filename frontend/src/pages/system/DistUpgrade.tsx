import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, CalendarClock, Power, RefreshCcw } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import type { Job } from "../../lib/types";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, Spinner, Switch } from "../../components/ui";
import { dateTime } from "../../lib/format";
import { srv, tr } from "../../lib/i18n";

// Updates → System upgrade (Debian): the next major Debian release, found
// by the NAS in the Debian archive, proposed once it has had its first
// point release and the daily compatibility checks pass.

type Check = { id: string; level: "ok" | "warn" | "block"; text: string; fix?: string };
type Run = {
  from: string; to: string; fromNumber?: string; toNumber?: string; started: string; finished?: string; phase: string; error?: string; restorePoint?: string;
  kernel?: string; postChecks?: Check[]; auto?: boolean; rollback?: boolean; rolledBack?: string; forced?: boolean;
};
type Dist = {
  current: string; currentVersion: string; target: string; targetVersion: string; targetPoint?: string; testing?: boolean; available: boolean; reason?: string;
  allowTesting: boolean; run?: Run; check?: { at: string; target: string; number: string; checks: Check[]; blocked: boolean };
  proposal: boolean; dismissed?: boolean; scheduled?: { target: string; force?: boolean; by: string; at: string }; nextWindow?: string;
};

const tone = (l: Check["level"]) => (l === "ok" ? "ok" : l === "warn" ? "warn" : "danger");

const phaseLabel = (p: string) => ({
  sources: tr("Preparing"), download: tr("Downloading the packages"), upgrade: tr("Upgrading the packages"), finish: tr("Finishing"),
  reboot: tr("Waiting for the restart"), "post-checks": tr("Checking after the restart"), done: tr("Finished"), failed: tr("Failed"),
  "rolling-back": tr("Going back to the restore point"), "rolled-back": tr("Back to the previous release"),
}[p] ?? p);

function Checks({ checks }: { checks: Check[] }) {
  return (
    <ul className="space-y-1.5">
      {checks.map((c) => (
        <li key={c.id + c.text}>
          <Badge tone={tone(c.level)} dot>{srv(c.text)}</Badge>
          {c.fix && c.level !== "ok" && <div className="mt-0.5 pl-4 text-muted text-[12px]">{srv(c.fix)}</div>}
        </li>
      ))}
    </ul>
  );
}

/** Updates → System upgrade (Debian). */
export function DistUpgradeCard() {
  const qc = useQueryClient();
  const { track } = useJobs();
  const { data, error } = useQuery({ queryKey: ["dist-upgrade"], queryFn: () => api.get<Dist>("/api/system/dist-upgrade"), refetchInterval: 15000 });
  const [yes, setYes] = useState(false);
  const [force, setForce] = useState(false);
  const [typed, setTyped] = useState("");
  const done = () => qc.invalidateQueries({ queryKey: ["dist-upgrade"] });
  const setTesting = useAction((v: boolean) => api.put("/api/system/dist-upgrade", { allowTesting: v }), { onDone: done });
  const check = useAction(() => api.post<Job>("/api/system/dist-upgrade/check"), { onDone: (j) => { track(j); setTimeout(done, 3000); } });
  const dismiss = useAction(() => api.post("/api/system/dist-upgrade/dismiss"), { onDone: done });
  const show = useAction(() => api.post("/api/system/dist-upgrade/show"), { onDone: done });
  const start = useAction((when: "now" | "window") => api.post<Job | Dist>("/api/system/dist-upgrade/start", { when, force, confirm: typed }), {
    onDone: (r) => { if (r && "status" in r) track(r as Job); setYes(false); setForce(false); setTyped(""); done(); },
  });
  const unplan = useAction(() => api.del("/api/system/dist-upgrade/scheduled"), { onDone: done });
  const reboot = useAction(() => api.post("/api/system/dist-upgrade/reboot"), { success: tr("The NAS is restarting") });
  const title = <span className="flex items-center gap-2"><ArrowUpCircle size={16} className="text-accent" />{tr("System upgrade (Debian)")}</span>;
  if (!data) return <Card title={title}><ErrorBox error={error} /><Spinner /></Card>;
  const run = data.run;
  const busy = !!run && !["done", "failed", "rolled-back"].includes(run.phase);
  const chk = data.check && data.check.target === data.target ? data.check : undefined;
  const failed = (chk?.checks ?? []).filter((c) => c.level === "block");
  const n = data.targetVersion;
  return (
    <Card title={title} className="mb-5" actions={data.target && !busy && <Button size="sm" variant="ghost" icon={<RefreshCcw size={14} />} loading={check.isPending} onClick={() => check.mutate()}>{tr("Check again")}</Button>}>
      <div className="space-y-3 text-[13px]">
        <p>{tr("This NAS runs Debian {0} ({1}).", data.currentVersion, data.current)} {!data.available && srv(data.reason ?? "")}</p>

        {data.proposal && !busy && (
          <div className="rounded-xl border border-accent/40 bg-accent-soft p-3">
            <div className="font-medium">{tr("Do you want to upgrade the system to Debian {0}?", n)}</div>
            <div className="mt-1 text-muted text-[12.5px]">{tr("Debian {0} ({1}) is out{2}, and the checks of this NAS pass. StrataOS stays as it is; the system under it moves to the new release, with a restore point first.", n, data.target, data.targetPoint ? ` (${data.targetPoint})` : "")}</div>
            <div className="mt-2 flex gap-2">
              <Button size="sm" variant="primary" onClick={() => setYes(true)}>{tr("Yes")}</Button>
              <Button size="sm" loading={dismiss.isPending} onClick={() => dismiss.mutate()}>{tr("No")}</Button>
            </div>
          </div>
        )}
        {data.dismissed && !busy && <p className="text-muted">{tr("The upgrade to Debian {0} is hidden until the next check.", n)} <button className="text-accent hover:underline" onClick={() => show.mutate()}>{tr("Show it again")}</button></p>}

        {data.scheduled && (
          <Notice>
            <span className="inline-flex items-center gap-2"><CalendarClock size={14} />{tr("The upgrade to Debian {0} starts in the next maintenance window ({1}).", n, data.nextWindow ? dateTime(data.nextWindow) : "—")}</span>
            <Button size="sm" variant="ghost" className="ml-2" loading={unplan.isPending} onClick={() => unplan.mutate()}>{tr("Cancel")}</Button>
          </Notice>
        )}

        {data.available && chk?.blocked && !busy && (
          <div className="rounded-xl border border-line p-3">
            <div className="mb-2 font-medium">{tr("Debian {0} is out, but this NAS is not ready for it yet:", n)}</div>
            <Checks checks={failed} />
            <div className="mt-2 text-muted text-[12px]">{tr("The checks run again every day; the upgrade is proposed once they pass.")}</div>
            <Button size="sm" variant="danger" className="mt-3" onClick={() => { setForce(true); setYes(true); }}>{tr("Force the upgrade…")}</Button>
          </div>
        )}
        {chk && <div className="text-muted text-[12px]">{tr("Last check: {0}", dateTime(chk.at))}</div>}
        {data.target && !chk && !busy && <p className="text-muted">{tr("Not checked yet: the check runs once a day.")}</p>}

        {run && (
          <div className="rounded-lg border border-line p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={run.phase === "failed" || (run.error && run.phase !== "rolled-back") ? "danger" : run.phase === "rolled-back" ? "warn" : run.phase === "done" ? "ok" : "accent"}>{phaseLabel(run.phase)}</Badge>
              <span>{tr("Debian {0} → {1}", run.fromNumber || run.from, run.toNumber || run.to)}</span>
              {run.forced && <Badge tone="warn">{tr("Forced")}</Badge>}
              <span className="text-muted">{dateTime(run.started)}</span>
              {run.restorePoint && <span className="text-muted">{tr("Restore point")} <span className="mono">{run.restorePoint}</span></span>}
            </div>
            {run.phase === "rolled-back" && <p className="mt-2">{tr("The upgrade did not succeed, so the NAS went back to the restore point and runs Debian {0} again, as before.", run.fromNumber || run.from)}</p>}
            {run.error && <p className="mt-2 text-danger">{srv(run.error)}</p>}
            {busy && <p className="mt-2 text-muted">{tr("Everything is automatic: the NAS restarts into the new release, checks itself, and goes back to the restore point if something fails. Do not power it off.")}</p>}
            {run.phase === "reboot" && !run.auto && (
              <div className="mt-2 flex flex-wrap items-center gap-3">
                <span>{tr("The new release is installed{0}: restart the NAS to finish.", run.kernel ? ` (${tr("kernel")} ${run.kernel})` : "")}</span>
                <Button size="sm" variant="primary" icon={<Power size={14} />} loading={reboot.isPending} onClick={() => reboot.mutate()}>{tr("Restart now")}</Button>
              </div>
            )}
            {(run.postChecks ?? []).length > 0 && <div className="mt-2"><Checks checks={run.postChecks!} /></div>}
          </div>
        )}
        {data.target && <Switch checked={data.allowTesting} onChange={(v) => setTesting.mutate(v)} label={tr("Offer it while it is still Debian testing (not recommended)")}
          hint={tr("For tests only: testing changes every day and some packages come and go (docs/DEBIAN-14.md).")} />}
        <ErrorBox error={setTesting.error || check.error || reboot.error || dismiss.error || unplan.error} />
      </div>

      {yes && chk && (
        <Modal open wide onClose={() => { setYes(false); setForce(false); setTyped(""); }} title={force ? tr("Force the upgrade to Debian {0}", n) : tr("Upgrade to Debian {0}", n)} footer={<>
          <Button onClick={() => { setYes(false); setForce(false); setTyped(""); }}>{tr("Cancel")}</Button>
          <Button icon={<CalendarClock size={15} />} disabled={force && typed !== data.target} loading={start.isPending && start.variables === "window"} onClick={() => start.mutate("window")}>{tr("In the maintenance window")}</Button>
          <Button variant={force ? "danger" : "primary"} disabled={force && typed !== data.target} loading={start.isPending && start.variables === "now"} onClick={() => start.mutate("now")}>{tr("Now")}</Button>
        </>}>
          <div className="space-y-3 text-[13px]">
            {force && (
              <Notice tone="warn">
                <div className="mb-1 font-medium">{tr("These checks failed: the upgrade may leave the NAS unable to start or without some of its functions.")}</div>
                <Checks checks={failed} />
              </Notice>
            )}
            <Checks checks={chk.checks.filter((c) => c.level !== "block")} />
            <p>{tr("A restore point is taken first. The upgrade runs as a task with its log, the NAS restarts into Debian {0} and checks itself; if anything fails, it goes back to the restore point by itself. It takes from 20 minutes to an hour.", n)}</p>
            <p className="text-muted">{tr("Next maintenance window: {0}", data.nextWindow ? dateTime(data.nextWindow) : "—")}</p>
            {force && <Field label={tr("Type {0} to force the upgrade", data.target)}><Input className="mono" value={typed} onChange={(e) => setTyped(e.target.value.trim())} /></Field>}
            <ErrorBox error={start.error} />
          </div>
        </Modal>
      )}
    </Card>
  );
}
