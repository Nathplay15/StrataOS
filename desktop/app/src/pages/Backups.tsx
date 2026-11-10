// Backups of the NAS: the 3-2-1 assessment, the backup tasks with their
// last and next runs and restore tests, "Run now". Creating tasks and
// restoring (guided, file by file or whole) are in the web interface.
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, DatabaseBackup, Play, XCircle } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Spinner } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { ago, dateTime } from "@nas/lib/format";
import type { BackupTask } from "@nas/lib/types";
import { api } from "../lib/nasapi";
import { srv, t } from "../lib/i18n";
import { PageHeader, Section, WebButton } from "../components/kit";

type Protection = { rule: { copies: number; media: number; offsite: number; ok: boolean; missing: string[] } };

export function BackupsPage() {
  const tasks = useQuery({ queryKey: ["backups"], queryFn: () => api.get<BackupTask[]>("/api/backups"), refetchInterval: (q) => ((q.state.data ?? []).some((b) => b.running) ? 3000 : 30_000) });
  const prot = useQuery({ queryKey: ["protection"], queryFn: () => api.get<Protection>("/api/protection") });
  const run = useAction((id: string) => api.post(`/api/backups/${id}/run`, {}), { success: t("Backup started") });
  const r = prot.data?.rule;
  return (
    <>
      <PageHeader title={t("Backups")} actions={<>
        <WebButton path="/backup" label={t("Restore")} title={t("Backups")} />
        <WebButton path="/backup" label={t("New backup task")} title={t("Backups")} />
      </>} />
      {r && (
        <Card>
          <div className="flex flex-wrap items-center gap-3">
            {r.ok ? <CheckCircle2 className="text-ok" /> : <XCircle className="text-warn" />}
            <div className="flex-1">
              <div className="font-semibold">{r.ok ? t("The 3-2-1 rule is met") : t("The 3-2-1 rule is not met yet")}</div>
              <div className="text-[12.5px] text-muted">{t("{0} copies of the data, on {1} kinds of media, {2} off site", r.copies, r.media, r.offsite)}</div>
            </div>
            {r.missing.map((m) => <Badge key={m} tone="warn">{m === "copies" ? t("Add a copy") : m === "media" ? t("Another medium") : t("A copy off site")}</Badge>)}
          </div>
        </Card>
      )}
      <Section title={t("Backup tasks")}>
        <ErrorBox error={tasks.error || prot.error} />
        <Card pad={false}>
          {tasks.isLoading ? <Spinner /> : !tasks.data?.length ? <Empty icon={<DatabaseBackup />} title={t("No backup tasks")} action={<WebButton path="/backup" label={t("New backup task")} />} /> : (
            <table className="table">
              <thead><tr><th>{t("Task")}</th><th>{t("Last run")}</th><th>{t("Next run")}</th><th>{t("Restore test")}</th><th /></tr></thead>
              <tbody>{tasks.data.map((b) => {
                const last = b.history?.[b.history.length - 1];
                return (
                  <tr key={b.id}>
                    <td>
                      <div className="flex items-center gap-2 font-medium">{b.name}{!b.enabled && <Badge>{t("Disabled")}</Badge>}{b.offsite && <Badge tone="accent">{t("Off site")}</Badge>}</div>
                      <div className="text-[11.5px] text-muted">{b.kind} → {b.destType}{b.host ? ` ${b.host}` : ""}</div>
                    </td>
                    <td>
                      {b.running ? <Badge tone="accent" dot>{t("Running")}</Badge> : last ? (
                        <div title={srv(last.message)}><Badge tone={last.status === "success" ? "ok" : "danger"}>{last.status === "success" ? t("Succeeded") : t("Failed")}</Badge> <span className="text-[12px] text-muted">{ago(last.started)}</span></div>
                      ) : <span className="text-muted">—</span>}
                    </td>
                    <td className="text-[12.5px] text-muted">{b.nextRun ? dateTime(b.nextRun) : "—"}</td>
                    <td className="text-[12.5px]">{b.restoreTest ? <Badge tone={b.restoreTest.status === "success" ? "ok" : "danger"}>{dateTime(b.restoreTest.time)}</Badge> : <span className="text-muted">—</span>}</td>
                    <td className="text-right"><Button size="sm" icon={<Play size={14} />} disabled={b.running} loading={run.isPending && run.variables === b.id} onClick={() => run.mutate(b.id)}>{t("Run now")}</Button></td>
                  </tr>
                );
              })}</tbody>
            </table>
          )}
        </Card>
      </Section>
    </>
  );
}
