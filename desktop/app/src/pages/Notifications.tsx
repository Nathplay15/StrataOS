// The notification centre of the NAS (administrators): its history grouped
// by kind, read state, and whether new ones show as notifications of this
// computer (lib/state.tsx reads that preference).
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Bell, CheckCheck, Info, Trash2, XCircle } from "lucide-react";
import { Button, Card, Empty, ErrorBox, Spinner, Switch } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { ago, cx } from "@nas/lib/format";
import { api } from "../lib/nasapi";
import { pref, setPref } from "../lib/prefs";
import { srv, t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

type Note = { id: number; time: string; level: string; subject: string; message: string; kind?: string; action?: string; link?: string };
type Notes = { items: Note[]; unread: number; read: number };

const kinds = (): Record<string, string> => ({
  update: t("Updates"), storage: t("Storage"), backup: t("Backups"), security: t("Security"), vm: t("Virtual machines"),
  docker: t("Containers"), surveillance: t("Surveillance"), network: t("Network"), power: t("Power"), report: t("Reports"), system: t("System"),
});

const actions = (): Record<string, string> => ({
  update: t("Update"), disk: t("Open disk"), backup: t("Open backup task"), sync: t("Open cloud sync"), review: t("Review"), open: t("Open"),
});

function LevelIcon({ level }: { level: string }) {
  if (level === "error") return <XCircle size={16} className="mt-0.5 shrink-0 text-danger" />;
  if (level === "warning") return <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warn" />;
  return <Info size={16} className="mt-0.5 shrink-0 text-accent" />;
}

export function NotificationsPage() {
  const { data, isLoading, error } = useQuery({ queryKey: ["notifications"], queryFn: () => api.get<Notes>("/api/notifications"), refetchInterval: 30000 });
  const [native, setNative] = useState(pref("notify") !== "off");
  const markRead = useAction(() => api.post("/api/notifications/read", {}));
  const clear = useAction(() => api.post("/api/notifications/clear"));
  const items = useMemo(() => data?.items ?? [], [data]);
  const K = kinds();
  const A = actions();
  const groups = useMemo(() => {
    const m = new Map<string, Note[]>();
    for (const n of items) {
      const k = n.kind && K[n.kind] ? n.kind : "system";
      m.set(k, [...(m.get(k) ?? []), n]);
    }
    return [...m.entries()];
  }, [items]);
  const read = data?.read ?? 0;

  return (
    <>
      <PageHeader title={t("Notifications")} subtitle={data?.unread ? t("Notifications ({0} unread)", data.unread) : undefined} actions={items.length > 0 && <>
        <Button icon={<CheckCheck size={15} />} loading={markRead.isPending} onClick={() => markRead.mutate()}>{t("Mark all read")}</Button>
        <Button icon={<Trash2 size={15} />} loading={clear.isPending} onClick={() => clear.mutate()}>{t("Clear all")}</Button>
      </>} />
      <Card className="mb-4">
        <Switch checked={native} onChange={(v) => { setNative(v); setPref("notify", v ? "on" : "off"); }}
          label={t("Show these as notifications of this computer")} hint={t("New notifications of every paired NAS appear on this computer, checked every minute.")} />
      </Card>
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !items.length ? (
        <Card><Empty icon={<Bell />} title={t("No notifications")} /></Card>
      ) : groups.map(([kind, list]) => (
        <Card key={kind} className="mb-3" title={K[kind]} pad={false}>
          <ul>
            {list.map((n) => (
              <li key={n.id} className={cx("flex items-start gap-2.5 border-t border-line px-4 py-3 text-[13px] first:border-t-0", n.id > read && "bg-accent-soft/50")}>
                <LevelIcon level={n.level} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="min-w-0 flex-1 font-medium">{srv(n.subject)}</span>
                    <time className="shrink-0 text-[11.5px] text-muted" dateTime={n.time}>{ago(n.time)}</time>
                  </div>
                  <div className="selectable whitespace-pre-line text-[12.5px] text-muted">{n.message.split("\n").map((l) => srv(l)).join("\n")}</div>
                  {n.action && n.link && <div className="mt-1.5"><WebButton size="sm" path={n.link} label={A[n.action] ?? A.open} title={srv(n.subject)} /></div>}
                </div>
                {n.id > read && <span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-accent" title={t("Unread")} />}
              </li>
            ))}
          </ul>
        </Card>
      ))}
    </>
  );
}
