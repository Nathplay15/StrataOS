import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, Power } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, Spinner, Switch } from "../../components/ui";
import { dateTime } from "../../lib/format";
import { srv, tr } from "../../lib/i18n";

type Check = { id: string; level: "ok" | "warn" | "block"; text: string };
type Run = { from: string; to: string; started: string; finished?: string; phase: string; error?: string; restorePoint?: string; kernel?: string; postChecks?: Check[] };
type Dist = { current: string; currentVersion: string; target: string; targetVersion: string; stable: string; testing: string; available: boolean; reason?: string; allowTesting: boolean; run?: Run };
type CheckResult = { checks: Check[]; blocked: boolean; sources: { uri: string; from: string; to: string; kept?: boolean }[]; available: boolean; reason?: string };

const tone = (l: Check["level"]) => (l === "ok" ? "ok" : l === "warn" ? "warn" : "danger");

const phaseLabel = (p: string) => ({
  sources: tr("Preparing"), download: tr("Downloading the packages"), upgrade: tr("Upgrading the packages"), finish: tr("Finishing"),
  reboot: tr("Waiting for the restart"), "post-checks": tr("Checking after the restart"), done: tr("Finished"), failed: tr("Failed"),
}[p] ?? p);

/** Updates → Debian release upgrade (13 → 14), offered once the next release is out. */
export function DistUpgradeCard() {
  const qc = useQueryClient();
  const { data, error } = useQuery({ queryKey: ["dist-upgrade"], queryFn: () => api.get<Dist>("/api/system/dist-upgrade"), refetchInterval: 15000 });
  const [checks, setChecks] = useState<CheckResult | null>(null);
  const [typed, setTyped] = useState("");
  const done = () => qc.invalidateQueries({ queryKey: ["dist-upgrade"] });
  const setTesting = useAction((v: boolean) => api.put("/api/system/dist-upgrade", { allowTesting: v }), { onDone: done });
  const check = useAction(() => api.post<CheckResult>("/api/system/dist-upgrade/check"), { onDone: (r) => { setChecks(r); setTyped(""); } });
  const start = useAction(() => api.post("/api/system/dist-upgrade/start", { confirm: typed }), { onDone: () => { setChecks(null); done(); } });
  const reboot = useAction(() => api.post("/api/system/dist-upgrade/reboot"), { success: tr("The NAS is restarting") });
  const title = <span className="flex items-center gap-2"><ArrowUpCircle size={16} className="text-accent" />{tr("Debian release upgrade")}</span>;
  if (!data) return <Card title={title}><ErrorBox error={error} /><Spinner /></Card>;
  const run = data.run;
  const busy = !!run && !["done", "failed"].includes(run.phase);
  return (
    <Card title={title} className="mb-5">
      <div className="space-y-3 text-[13px]">
        <p>{tr("This NAS runs Debian {0} ({1}).", data.currentVersion, data.current)} {data.target && (data.available
          ? tr("Debian {0} ({1}) can be installed.", data.targetVersion, data.target)
          : srv(data.reason ?? ""))}</p>
        {data.target && <Switch checked={data.allowTesting} onChange={(v) => setTesting.mutate(v)} label={tr("Offer it while it is still Debian testing (not recommended)")}
          hint={tr("For tests only: testing changes every day and some packages come and go (docs/DEBIAN-14.md).")} />}
        {run && (
          <div className="rounded-lg border border-line p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={run.phase === "failed" || run.error ? "danger" : run.phase === "done" ? "ok" : "accent"}>{phaseLabel(run.phase)}</Badge>
              <span>{tr("Debian {0} → {1}", run.from, run.to)}</span>
              <span className="text-muted">{dateTime(run.started)}</span>
              {run.restorePoint && <span className="text-muted">{tr("Restore point")} <span className="mono">{run.restorePoint}</span></span>}
            </div>
            {run.error && <p className="mt-2 text-danger">{srv(run.error)}</p>}
            {run.phase === "reboot" && (
              <div className="mt-2 flex flex-wrap items-center gap-3">
                <span>{tr("The new release is installed{0}: restart the NAS to finish.", run.kernel ? ` (${tr("kernel")} ${run.kernel})` : "")}</span>
                <Button size="sm" variant="primary" icon={<Power size={14} />} loading={reboot.isPending} onClick={() => reboot.mutate()}>{tr("Restart now")}</Button>
              </div>
            )}
            {(run.postChecks ?? []).length > 0 && <ul className="mt-2 space-y-1">{run.postChecks!.map((c) => <li key={c.id}><Badge tone={tone(c.level)} dot>{srv(c.text)}</Badge></li>)}</ul>}
          </div>
        )}
        {data.target && (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={busy} loading={check.isPending} onClick={() => check.mutate()}>{data.available ? tr("Check and upgrade…") : tr("Run the pre-checks")}</Button>
          </div>
        )}
        <ErrorBox error={setTesting.error || check.error || reboot.error} />
      </div>
      {checks && (
        <Modal open wide onClose={() => setChecks(null)} title={tr("Upgrade to Debian {0}", data.targetVersion)} footer={<>
          <Button onClick={() => setChecks(null)}>{tr("Cancel")}</Button>
          <Button variant="primary" disabled={checks.blocked || typed !== data.target} loading={start.isPending} onClick={() => start.mutate()}>{tr("Upgrade")}</Button>
        </>}>
          <div className="space-y-3 text-[13px]">
            <ul className="space-y-1">{checks.checks.map((c) => <li key={c.id + c.text}><Badge tone={tone(c.level)} dot>{srv(c.text)}</Badge></li>)}</ul>
            {checks.sources.length > 0 && (
              <div>
                <p className="mb-1 font-medium">{tr("Package sources")}</p>
                <ul className="mono space-y-0.5 text-[12px]">{checks.sources.map((s, i) => <li key={i}>{s.uri}: {s.from} → {s.kept ? tr("kept") : s.to}</li>)}</ul>
              </div>
            )}
            <Notice tone="warn">{tr("A release upgrade takes from 20 minutes to an hour and ends with a restart. The configuration files StrataOS manages are kept. A restore point brings the packages back only before that restart: keep a recent backup of your data and of the configuration (Settings → Configuration backup).")}</Notice>
            {!checks.blocked && <Field label={tr("Type {0} to confirm", data.target)}><Input className="mono" value={typed} onChange={(e) => setTyped(e.target.value.trim())} /></Field>}
            <ErrorBox error={start.error} />
          </div>
        </Modal>
      )}
    </Card>
  );
}
