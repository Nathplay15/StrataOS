import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Bell, CheckCheck, CheckCircle2, Info, Trash2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { Me, Overview } from "../lib/types";
import { ago, cx } from "../lib/format";
import { srv, tr } from "../lib/i18n";
import { useDismiss } from "../lib/a11y";

// The bell: what is wrong now (the alerts of the overview) and the history
// of the NAS notifications (GET /api/notifications, administrators), grouped
// by kind, with a direct action that opens the right page or window.

type Note = { id: number; time: string; level: string; subject: string; message: string; kind?: string; action?: string; link?: string };
type Notes = { items: Note[]; unread: number; read: number };

const KINDS: Record<string, string> = {
  update: tr("Updates"),
  storage: tr("Storage"),
  backup: tr("Backups"),
  security: tr("Security"),
  vm: tr("Virtual machines"),
  docker: tr("Containers"),
  surveillance: tr("Surveillance"),
  network: tr("Network"),
  power: tr("Power"),
  report: tr("Reports"),
  system: tr("System"),
};

const ACTIONS: Record<string, string> = {
  update: tr("Update"),
  disk: tr("Open disk"),
  backup: tr("Open backup task"),
  sync: tr("Open cloud sync"),
  review: tr("Review"),
  open: tr("Open"),
};

function LevelIcon({ level }: { level: string }) {
  if (level === "error") return <XCircle size={15} className="mt-0.5 shrink-0 text-danger" aria-label={tr("Error")} />;
  if (level === "warning") return <AlertTriangle size={15} className="mt-0.5 shrink-0 text-warn" aria-label={tr("Warning")} />;
  return <Info size={15} className="mt-0.5 shrink-0 text-accent" aria-label={tr("Information")} />;
}

/** Groups the notifications by kind, the group with the newest one first. */
function groups(items: Note[]): [string, Note[]][] {
  const m = new Map<string, Note[]>();
  for (const n of items) {
    const k = n.kind && KINDS[n.kind] ? n.kind : "system";
    m.set(k, [...(m.get(k) ?? []), n]);
  }
  return [...m.entries()];
}

export function NotificationCenter({ me, alerts, openPage, tray }: {
  me: Me;
  alerts: Overview["alerts"];
  openPage: (path: string) => void;
  /** Styled for the desktop taskbar. */
  tray?: boolean;
}) {
  const admin = me.role === "admin";
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useDismiss(ref, open, () => { setOpen(false); button.current?.focus(); });
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["notifications"], queryFn: () => api.get<Notes>("/api/notifications"), enabled: admin, refetchInterval: 30000 });
  const items = useMemo(() => q.data?.items ?? [], [q.data]);
  const unread = q.data?.unread ?? 0;
  // What was unread when the panel opened stays marked while it is open.
  const [seen, setSeen] = useState(0);
  useEffect(() => {
    if (!open || !q.data) return;
    setSeen(q.data.read);
    if (q.data.unread > 0) api.post("/api/notifications/read", { upTo: items[0]?.id ?? 0 }).then(() => qc.invalidateQueries({ queryKey: ["notifications"] }), () => {});
  }, [open]);
  const clear = () => api.post("/api/notifications/clear").then(() => qc.invalidateQueries({ queryKey: ["notifications"] }), () => {});
  const markRead = () => api.post("/api/notifications/read", {}).then(() => { setSeen(items[0]?.id ?? 0); qc.invalidateQueries({ queryKey: ["notifications"] }); }, () => {});
  const go = (path: string) => {
    setOpen(false);
    openPage(path);
  };

  const label = unread > 0 ? tr("Notifications ({0} unread)", unread) : tr("Notifications");
  return (
    <div className="relative" ref={ref}>
      <button
        ref={button}
        title={label}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={cx("relative flex h-9 w-9 items-center justify-center rounded-lg transition", tray ? "text-dk-fg/80 hover:bg-dk-hover hover:text-dk-fg" : "text-muted hover:bg-panel-2 hover:text-fg")}
      >
        <Bell size={17} />
        {unread > 0 ? (
          <span className="absolute -right-0.5 -top-0.5 min-w-[17px] rounded-full bg-danger px-1 text-center text-[10.5px] font-semibold leading-[17px] text-white">{unread > 99 ? "99+" : unread}</span>
        ) : alerts.length > 0 && <span className="absolute right-1 top-1 h-2 w-2 rounded-full bg-danger" />}
      </button>
      {open && (
        <div role="dialog" aria-label={tr("Notifications")} className="card absolute right-0 top-11 z-[9999] flex max-h-[min(640px,calc(100vh-80px))] w-[400px] max-w-[calc(100vw-1rem)] flex-col p-2 text-fg">
          <div className="flex items-center gap-1 px-2 py-1.5">
            <span className="flex-1 text-[12px] font-medium text-muted">{tr("Notifications")}</span>
            {admin && items.length > 0 && (
              <>
                <button type="button" onClick={markRead} className="flex items-center gap-1 rounded-md px-2 py-1 text-[12px] text-muted hover:bg-panel-2 hover:text-fg"><CheckCheck size={13} /> {" "}{tr("Mark all read")}</button>
                <button type="button" onClick={clear} className="flex items-center gap-1 rounded-md px-2 py-1 text-[12px] text-muted hover:bg-panel-2 hover:text-fg"><Trash2 size={13} /> {" "}{tr("Clear all")}</button>
              </>
            )}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">
            {alerts.length === 0 ? (
              <div className="flex items-center gap-2 px-2 py-3 text-[13px] text-ok"><CheckCircle2 size={16} /> {" "}{tr("Everything is working normally")}</div>
            ) : (
              <section aria-label={tr("Current problems")} className="mb-1">
                <h3 className="px-2 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wider text-muted">{tr("Current problems")}</h3>
                {alerts.map((a) => (
                  <div key={a.message} className="flex items-start gap-2 rounded-lg px-2 py-2 text-[13px]">
                    <LevelIcon level={a.level} />
                    {srv(a.message)}
                  </div>
                ))}
              </section>
            )}
            {admin && groups(items).map(([kind, list]) => (
              <section key={kind} aria-label={KINDS[kind]}>
                <h3 className="px-2 pb-1 pt-3 text-[11px] font-semibold uppercase tracking-wider text-muted">{KINDS[kind]}</h3>
                <ul>
                  {list.map((n) => (
                    <li key={n.id} className={cx("flex items-start gap-2 rounded-lg px-2 py-2 text-[13px]", n.id > seen && "bg-accent-soft/60")}>
                      <LevelIcon level={n.level} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-baseline gap-2">
                          <span className="min-w-0 flex-1 truncate font-medium">{srv(n.subject)}</span>
                          <time className="shrink-0 text-[11px] text-muted" dateTime={n.time}>{ago(n.time)}</time>
                        </div>
                        <div className="line-clamp-3 whitespace-pre-line text-[12.5px] text-muted">{n.message.split("\n").map((l) => srv(l)).join("\n")}</div>
                        {n.action && n.link && (
                          <button type="button" onClick={() => go(n.link!)} className="mt-1 rounded-md bg-panel-2 px-2 py-0.5 text-[12px] font-medium text-accent hover:bg-accent-soft">
                            {ACTIONS[n.action] ?? ACTIONS.open}
                          </button>
                        )}
                      </div>
                      {n.id > seen && <span className="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-accent" aria-label={tr("Unread")} />}
                    </li>
                  ))}
                </ul>
              </section>
            ))}
            {admin && q.data && items.length === 0 && <div className="px-2 py-4 text-center text-[12.5px] text-muted">{tr("No notifications")}</div>}
          </div>
        </div>
      )}
    </div>
  );
}
