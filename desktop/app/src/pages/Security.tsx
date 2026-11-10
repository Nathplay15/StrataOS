// Security advisor of the NAS: its score, the checks to improve by
// severity, one-click fixes (what each does is shown first) and ignored items.
// Settings a check links to open in the web interface window.
import { useQuery } from "@tanstack/react-query";
import { Eye, EyeOff, RefreshCw, ShieldCheck, Wrench } from "lucide-react";
import { Badge, Button, Card, ErrorBox, Spinner, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { dateTime } from "@nas/lib/format";
import { api } from "../lib/nasapi";
import { srv, t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

type Severity = "high" | "medium" | "low" | "info";
type Item = { id: string; severity: Severity; title: string; detail: string; fix?: string; link?: string; ignored?: boolean };
type Run = { time: string; score: number; items: Item[] };

const order: Severity[] = ["high", "medium", "low", "info"];

const severities = (): Record<Severity, { label: string; tone: "danger" | "warn" | "accent" | "neutral" }> => ({
  high: { label: t("Important"), tone: "danger" },
  medium: { label: t("Recommended"), tone: "warn" },
  low: { label: t("Minor"), tone: "accent" },
  info: { label: t("Information"), tone: "neutral" },
});

// What each fix does (same texts as the web interface), shown before applying it.
const fixTexts = (): Record<string, string> => ({
  "smb-proto": t("Sets the minimum SMB protocol to SMB 2.1. Very old devices (Windows XP, old printers and media players) can no longer connect."),
  "ssh-root": t("root can then sign in over SSH with a key only. Administrators keep their own account and the web terminal."),
  "security-updates": t("Debian security updates are installed automatically in the maintenance window (Updates)."),
  guard: t("Blocks for an hour any address that fails to sign in 5 times in 10 minutes; the local network is never blocked."),
  ransomware: t("Watches SMB shares for mass renames and deletions and alerts you (the account is not blocked unless you choose so in Antivirus)."),
  ntp: t("Turns on automatic time synchronization (NTP)."),
});

export function SecurityPage() {
  const { confirm } = useUI();
  const { data: r, isLoading, error } = useQuery({ queryKey: ["advisor"], queryFn: () => api.get<Run>("/api/security/advisor") });
  const run = useAction(() => api.post<Run>("/api/security/advisor/run"), { success: t("Checks done") });
  const fix = useAction((id: string) => api.post<Run>(`/api/security/advisor/fix/${encodeURIComponent(id)}`), { success: t("Fixed") });
  const ignore = useAction((a: { id: string; ignored: boolean }) => api.put<Run>("/api/security/advisor/ignore", a));
  const sev = severities();

  const header = <PageHeader title={t("Security advisor")} subtitle={t("Checks the settings of the NAS and tells what to improve")}
    actions={<Button icon={<RefreshCw size={15} />} loading={run.isPending} onClick={() => run.mutate()}>{t("Check again")}</Button>} />;
  if (!r) return <>{header}<ErrorBox error={error} />{isLoading && <Spinner />}</>;

  const active = r.items.filter((i) => !i.ignored);
  const ignored = r.items.filter((i) => i.ignored);
  const count = (s: Severity) => active.filter((i) => i.severity === s).length;
  const scoreTone = r.score >= 85 ? "text-ok" : r.score >= 60 ? "text-warn" : "text-danger";

  const row = (it: Item) => (
    <div key={it.id} className="flex items-start gap-3 border-b border-line px-5 py-4 last:border-0">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={it.ignored ? "neutral" : sev[it.severity].tone} dot>{sev[it.severity].label}</Badge>
          <span className={it.ignored ? "text-muted" : "font-medium"}>{srv(it.title)}</span>
        </div>
        <p className="selectable mt-1 text-[12.5px] text-muted">{srv(it.detail)}</p>
      </div>
      <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
        {it.fix && !it.ignored && (
          <Button size="sm" variant="primary" icon={<Wrench size={13} />} loading={fix.isPending && fix.variables === it.fix}
            onClick={async () => { if (await confirm({ title: srv(it.title), text: fixTexts()[it.fix!] ?? "", confirm: t("Fix") })) fix.mutate(it.fix!); }}>{t("Fix")}</Button>
        )}
        {it.link && <WebButton size="sm" variant="ghost" path={it.link} label={t("Open the setting")} title={srv(it.title)} />}
        <Button size="sm" variant="ghost" icon={it.ignored ? <Eye size={13} /> : <EyeOff size={13} />} onClick={() => ignore.mutate({ id: it.id, ignored: !it.ignored })}>
          {it.ignored ? t("Watch again") : t("Ignore")}
        </Button>
      </div>
    </div>
  );

  return (
    <>
      {header}
      <ErrorBox error={run.error || fix.error || ignore.error} />
      <div className="mb-5 grid grid-cols-[200px_1fr] gap-4">
        <Card>
          <div className="text-[12px] text-muted">{t("Security score")}</div>
          <div className={`text-[34px] font-semibold ${scoreTone}`}>{r.score}<span className="text-[16px] text-muted">/100</span></div>
          <div className="text-[11.5px] text-muted">{t("Checked {0}", dateTime(r.time))}</div>
        </Card>
        <Card>
          <div className="grid grid-cols-4 gap-3">
            {order.map((s) => (
              <div key={s} className="rounded-xl bg-panel-2 p-3">
                <div className="text-[12px] text-muted">{sev[s].label}</div>
                <div className="text-[22px] font-semibold">{count(s)}</div>
              </div>
            ))}
          </div>
          <p className="mt-3 text-[12px] text-muted">{t("The checks also run every day at 7:30 AM: you are notified when the score drops or a new important item appears.")}</p>
        </Card>
      </div>
      {active.length === 0 ? (
        <Card><div className="flex items-center gap-2 text-ok"><ShieldCheck size={18} />{t("Nothing to improve: every check passes.")}</div></Card>
      ) : (
        <div className="space-y-4">
          {order.filter((s) => count(s) > 0).map((s) => (
            <Card key={s} title={<span className="flex items-center gap-2"><Badge tone={sev[s].tone} dot>{sev[s].label}</Badge>{count(s)}</span>} pad={false}>
              {active.filter((i) => i.severity === s).map(row)}
            </Card>
          ))}
        </div>
      )}
      {ignored.length > 0 && <Card className="mt-4" title={t("Ignored")} pad={false}>{ignored.map(row)}</Card>}
    </>
  );
}
