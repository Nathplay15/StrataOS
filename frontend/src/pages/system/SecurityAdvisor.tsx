import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Eye, EyeOff, RefreshCw, ShieldCheck, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { dateTime } from "../../lib/format";
import { AppLink } from "../../components/AppLink";
import { revealElement } from "../../lib/intent";
import { Ring } from "../../components/charts";
import { Badge, Button, Card, ErrorBox, PageHeader, Spinner, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type Severity = "high" | "medium" | "low" | "info";
type Item = { id: string; severity: Severity; title: string; detail: string; fix?: string; link?: string; ignored?: boolean };
type Run = { time: string; score: number; items: Item[] };

const sev: Record<Severity, { label: string; tone: "danger" | "warn" | "accent" | "neutral" }> = {
  high: { label: tr("Important"), tone: "danger" },
  medium: { label: tr("Recommended"), tone: "warn" },
  low: { label: tr("Minor"), tone: "accent" },
  info: { label: tr("Information"), tone: "neutral" },
};

// What each fix does, shown before it is applied.
const fixText: Record<string, string> = {
  "smb-proto": tr("Sets the minimum SMB protocol to SMB 2.1. Very old devices (Windows XP, old printers and media players) can no longer connect."),
  "ssh-root": tr("root can then sign in over SSH with a key only. Administrators keep their own account and the web terminal."),
  "security-updates": tr("Debian security updates are installed automatically in the maintenance window (Updates)."),
  guard: tr("Blocks for an hour any address that fails to sign in 5 times in 10 minutes; the local network is never blocked."),
  ransomware: tr("Watches SMB shares for mass renames and deletions and alerts you (the account is not blocked unless you choose so in Antivirus)."),
  ntp: tr("Turns on automatic time synchronization (NTP)."),
};

const scoreColor = (s: number) => (s >= 85 ? "var(--ok)" : s >= 60 ? "var(--warn)" : "var(--danger)");

export default function SecurityAdvisor() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["advisor"], queryFn: () => api.get<Run>("/api/security/advisor") });
  const run = useAction(() => api.post<Run>("/api/security/advisor/run"), { success: tr("Checks done") });
  const fix = useAction((id: string) => api.post<Run>(`/api/security/advisor/fix/${id}`), { success: tr("Fixed") });
  const ignore = useAction((a: { id: string; ignored: boolean }) => api.put<Run>("/api/security/advisor/ignore", a));
  const r = data; // every action saves the run: the refetch shows it

  const header = (
    <PageHeader title={tr("Security advisor")} subtitle={tr("Checks the settings of the NAS and tells what to improve")} icon={<ShieldCheck size={20} />}
      actions={<Button icon={<RefreshCw size={15} />} loading={run.isPending} onClick={() => run.mutate()}>{tr("Check again")}</Button>} />
  );
  if (isLoading || !r) return <div>{header}<ErrorBox error={error} />{isLoading && <Spinner />}</div>;
  const active = r.items.filter((i) => !i.ignored);
  const ignored = r.items.filter((i) => i.ignored);
  const count = (s: Severity) => active.filter((i) => i.severity === s).length;

  const doFix = async (it: Item) => {
    if (await confirm({ title: srv(it.title), text: fixText[it.fix!] ?? "", confirm: tr("Fix") })) fix.mutate(it.fix!);
  };

  const row = (it: Item) => (
    <div key={it.id} className="flex flex-col gap-2 border-b border-line px-5 py-4 last:border-0 sm:flex-row sm:items-start">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={it.ignored ? "neutral" : sev[it.severity].tone} dot>{sev[it.severity].label}</Badge>
          <span className={it.ignored ? "text-muted" : "font-medium"}>{srv(it.title)}</span>
        </div>
        <p className="mt-1 text-[12.5px] text-muted">{srv(it.detail)}</p>
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-1.5">
        {it.fix && !it.ignored && <Button size="sm" variant="primary" icon={<Wrench size={13} />} loading={fix.isPending && fix.variables === it.fix} onClick={() => doFix(it)}>{tr("Fix")}</Button>}
        {it.link && <span onClick={() => { const h = it.link!.split("#")[1]; if (h) revealElement(h); }}><AppLink to={it.link.split("#")[0]}><Button size="sm" variant="ghost" icon={<ExternalLink size={13} />}>{tr("Open the setting")}</Button></AppLink></span>}
        <Button size="sm" variant="ghost" icon={it.ignored ? <Eye size={13} /> : <EyeOff size={13} />} onClick={() => ignore.mutate({ id: it.id, ignored: !it.ignored })}>
          {it.ignored ? tr("Watch again") : tr("Ignore")}
        </Button>
      </div>
    </div>
  );

  return (
    <div>
      {header}
      <ErrorBox error={run.error || fix.error || ignore.error} />
      <div className="mb-5 grid gap-5 md:grid-cols-[auto_1fr]">
        <Card>
          <Ring value={r.score} size={110} label={tr("Security score")} sub={tr("Checked {0}", dateTime(r.time))} color={scoreColor(r.score)} />
        </Card>
        <Card>
          <div className="grid h-full grid-cols-2 gap-3 sm:grid-cols-4">
            {(["high", "medium", "low", "info"] as Severity[]).map((s) => (
              <div key={s} className="rounded-xl bg-panel-2 p-3">
                <div className="text-[12px] text-muted">{sev[s].label}</div>
                <div className="text-[22px] font-semibold">{count(s)}</div>
              </div>
            ))}
          </div>
          <p className="mt-3 text-[12px] text-muted">{tr("The checks also run every day at 7:30 AM: you are notified when the score drops or a new important item appears.")}</p>
        </Card>
      </div>
      {active.length === 0 ? (
        <Card><div className="flex items-center gap-2 text-ok"><ShieldCheck size={18} />{tr("Nothing to improve: every check passes.")}</div></Card>
      ) : (
        <div className="space-y-5">
          {(["high", "medium", "low", "info"] as Severity[]).filter((s) => count(s) > 0).map((s) => (
            <Card key={s} title={<span className="flex items-center gap-2"><Badge tone={sev[s].tone} dot>{sev[s].label}</Badge>{count(s)}</span>} pad={false}>
              {active.filter((i) => i.severity === s).map(row)}
            </Card>
          ))}
        </div>
      )}
      {ignored.length > 0 && (
        <Card className="mt-5" title={tr("Ignored")} pad={false}>
          {ignored.map(row)}
        </Card>
      )}
    </div>
  );
}
