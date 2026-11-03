import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowDown, ArrowUp, CheckCircle2, GripVertical, Settings2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { Container, Disk, Me, Overview, Sample, ScheduleRun, SystemInfo } from "../lib/types";
import type { VMData } from "../pages/vms/types";
import type { LXCData } from "../pages/vms/containers/types";
import { ago, bytes, cx, duration, pct, rate } from "../lib/format";
import { Button, Checkbox, Modal, Progress, useUI } from "../components/ui";
import { tr, srv } from "../lib/i18n";
import { usePrefs, useSavePrefs } from "../lib/appearance";
import { useWM } from "./wm";
import { ADMIN_WIDGETS, WIDGET_IDS, dropWidget, moveWidget, shownWidgets, toggleWidget, type WidgetId } from "./widgetList";

// Side panel of the desktop mode: widgets chosen and ordered by each user
// (kept on the NAS with the icon layout). A widget only polls while it is on
// screen and the browser tab is visible.

export const WIDGET_LABELS: Record<WidgetId, string> = {
  status: tr("System status"),
  resources: tr("Resources"),
  storage: tr("Storage"),
  apps: tr("Applications"),
  network: tr("Network throughput"),
  docker: tr("Docker containers"),
  vms: tr("Virtual machines and containers"),
  ups: tr("UPS"),
  backups: tr("Last backups"),
  temps: tr("Disk temperatures"),
  notifications: tr("Notifications"),
};

/** True while the element is on screen (false when hidden, e.g. the panel on smaller screens). */
function useInView(ref: React.RefObject<HTMLElement | null>) {
  const [on, setOn] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver((e) => setOn(e.some((x) => x.isIntersecting)));
    io.observe(el);
    return () => io.disconnect();
  }, [ref]);
  return on;
}

function Frame({ title, children, onClick }: { title: string; children: ReactNode; onClick?: () => void }) {
  return (
    <section className={cx("rounded-2xl border border-white/10 bg-black/30 p-4 text-white shadow-xl backdrop-blur-xl", onClick && "cursor-pointer transition hover:bg-black/40")} onClick={onClick}>
      <h3 className="mb-3 text-[12px] font-semibold uppercase tracking-wider text-white/60">{title}</h3>
      {children}
    </section>
  );
}

function Bar({ value, label, sub }: { value: number; label: string; sub: string }) {
  return (
    <div>
      <div className="mb-1 flex justify-between gap-2 text-[12px]"><span className="truncate">{label}</span><span className="shrink-0 text-white/70">{sub}</span></div>
      <Progress value={value} className="!border-white/10 !bg-white/10" />
    </div>
  );
}

const Muted = ({ children }: { children: ReactNode }) => <div className="text-[12.5px] text-white/60">{children}</div>;

/** Loading, error and empty states shared by the widgets. */
function State({ q, empty, children }: { q: { isLoading: boolean; error: unknown; data?: unknown }; empty?: string | false; children: () => ReactNode }) {
  if (q.isLoading && !q.data) return <Muted>{tr("Loading…")}</Muted>;
  if (q.error && !q.data) return <div className="flex items-center gap-1.5 text-[12.5px] text-warn"><AlertTriangle size={14} /> {" "}{tr("Unavailable")}</div>;
  if (empty) return <Muted>{empty}</Muted>;
  return <>{children()}</>;
}

/** Throughput as a small line chart (last minutes of the stats history). */
function Spark({ values, colour }: { values: number[]; colour: string }) {
  const max = Math.max(1, ...values);
  const pts = values.map((v, i) => `${(i / Math.max(1, values.length - 1)) * 100},${30 - (v / max) * 28}`).join(" ");
  return <svg viewBox="0 0 100 30" preserveAspectRatio="none" className="h-8 w-full"><polyline points={pts} fill="none" stroke={colour} strokeWidth={1.5} vectorEffect="non-scaling-stroke" /></svg>;
}

type BackupView = { id: string; name: string; enabled: boolean; running?: boolean; history: ScheduleRun[] };
type UPSView = { installed: boolean; config: { mode: string }; status?: { status: string; charge: number | null; runtime: number | null }; error?: string };

function WidgetBody({ id, me, ov, live }: { id: WidgetId; me: Me; ov?: Overview; live: boolean }) {
  const wm = useWM()!;
  const admin = me.role === "admin";
  const poll = (ms: number) => (live ? ms : false);
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => api.get<{ history: Sample[] }>("/api/system/stats"), refetchInterval: poll(3000), enabled: live && (id === "resources" || id === "network") });
  const info = useQuery({ queryKey: ["sysinfo"], queryFn: () => api.get<SystemInfo>("/api/system/info"), refetchInterval: poll(60000), enabled: live && id === "status" });
  const docker = useQuery({ queryKey: ["containers"], queryFn: () => api.get<Container[]>("/api/docker/containers"), refetchInterval: poll(10000), enabled: live && admin && id === "docker", retry: false });
  const vms = useQuery({ queryKey: ["vms"], queryFn: () => api.get<VMData>("/api/vms"), refetchInterval: poll(10000), enabled: live && admin && id === "vms", retry: false });
  const lxc = useQuery({ queryKey: ["lxc-list"], queryFn: () => api.get<LXCData>("/api/lxc"), refetchInterval: poll(10000), enabled: live && admin && id === "vms", retry: false });
  const ups = useQuery({ queryKey: ["ups"], queryFn: () => api.get<UPSView>("/api/ups"), refetchInterval: poll(10000), enabled: live && admin && id === "ups", retry: false });
  const backups = useQuery({ queryKey: ["backups"], queryFn: () => api.get<BackupView[]>("/api/backups"), refetchInterval: poll(30000), enabled: live && admin && id === "backups", retry: false });
  const disks = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks"), refetchInterval: poll(60000), enabled: live && admin && id === "temps", retry: false });
  const ovQ = { isLoading: !ov, error: null, data: ov };
  const last = stats.data?.history.at(-1);

  switch (id) {
    case "status": {
      const errors = ov?.alerts.filter((a) => a.level === "error").length ?? 0;
      const healthy = ov && ov.alerts.length === 0;
      return (
        <Frame title={WIDGET_LABELS.status} onClick={() => wm.open("overview")}>
          <div className="flex items-center gap-3">
            <div className={cx("flex h-11 w-11 items-center justify-center rounded-full", healthy ? "bg-ok/25 text-ok" : errors ? "bg-danger/25 text-danger" : "bg-warn/25 text-warn")}>
              {healthy ? <CheckCircle2 size={22} /> : <AlertTriangle size={22} />}
            </div>
            <div className="min-w-0">
              <div className="font-semibold">{!ov ? "…" : healthy ? tr("Healthy") : errors ? tr("Attention required") : tr("Warning")}</div>
              <div className="truncate text-[12px] text-white/70">{srv(ov?.alerts[0]?.message) || (info.data ? tr("{0} · active since {1}", info.data.hostname, duration(info.data.uptime)) : "")}</div>
            </div>
          </div>
        </Frame>
      );
    }
    case "resources":
      return (
        <Frame title={WIDGET_LABELS.resources} onClick={admin ? () => wm.open("monitor") : undefined}>
          <State q={stats} empty={!last && tr("No data yet")}>{() => last && (
            <div className="space-y-3">
              <Bar value={last.cpu} label={tr("Processor")} sub={`${last.cpu.toFixed(0)} %`} />
              <Bar value={pct(last.memUsed, last.memTotal)} label={tr("Memory")} sub={`${bytes(last.memUsed)} / ${bytes(last.memTotal)}`} />
              <div className="flex justify-between text-[12px] text-white/80"><span>↓ {rate(last.netRx)}</span><span>↑ {rate(last.netTx)}</span></div>
            </div>
          )}</State>
        </Frame>
      );
    case "network": {
      const h = stats.data?.history.slice(-60) ?? [];
      return (
        <Frame title={WIDGET_LABELS.network} onClick={admin ? () => wm.open("monitor") : undefined}>
          <State q={stats} empty={h.length < 2 && tr("No data yet")}>{() => (
            <div>
              <div className="flex justify-between text-[12.5px]"><span className="text-sky-300">↓ {rate(last?.netRx ?? 0)}</span><span className="text-emerald-300">↑ {rate(last?.netTx ?? 0)}</span></div>
              <Spark values={h.map((s) => s.netRx)} colour="#7dd3fc" />
              <Spark values={h.map((s) => s.netTx)} colour="#6ee7b7" />
            </div>
          )}</State>
        </Frame>
      );
    }
    case "storage":
      return (
        <Frame title={WIDGET_LABELS.storage} onClick={admin ? () => wm.open("storage", "volumes") : undefined}>
          <State q={ovQ} empty={ov?.volumes.length === 0 && tr("No volume")}>{() => (
            <div className="space-y-3">
              {ov!.volumes.slice(0, 4).map((v) => <Bar key={v.name} value={pct(v.used, v.total)} label={v.name} sub={tr("{0} free", bytes(v.free))} />)}
            </div>
          )}</State>
        </Frame>
      );
    case "apps":
      return (
        <Frame title={WIDGET_LABELS.apps} onClick={() => wm.open("docker", "containers")}>
          <State q={ovQ}>{() => <div className="text-[13px]"><b className="text-[20px]">{ov!.containers.running}</b> <span className="text-white/70">/ {ov!.containers.total} {" "}{tr("active containers")}</span></div>}</State>
        </Frame>
      );
    case "docker": {
      const list = Array.isArray(docker.data) ? docker.data : [];
      const running = list.filter((c) => c.State === "running");
      return (
        <Frame title={WIDGET_LABELS.docker} onClick={() => wm.open("docker", "containers")}>
          <State q={docker} empty={list.length === 0 && tr("No containers")}>{() => (
            <div className="space-y-1.5 text-[12.5px]">
              <div className="text-white/70">{tr("{0} running of {1}", running.length, list.length)}</div>
              {list.slice(0, 5).map((c) => (
                <div key={c.Id} className="flex items-center gap-2"><span className={cx("h-2 w-2 shrink-0 rounded-full", c.State === "running" ? "bg-ok" : "bg-white/30")} /><span className="truncate">{(c.Names[0] ?? c.Id).replace(/^\//, "")}</span></div>
              ))}
            </div>
          )}</State>
        </Frame>
      );
    }
    case "vms": {
      const items = [...(vms.data?.vms ?? []).filter((v) => !v.template).map((v) => ({ name: v.name, on: v.state === "running", kind: "VM" })), ...(lxc.data?.containers ?? []).map((c) => ({ name: c.name, on: c.state === "running", kind: "LXC" }))];
      return (
        <Frame title={WIDGET_LABELS.vms} onClick={() => wm.open("vms")}>
          <State q={{ isLoading: vms.isLoading && lxc.isLoading, error: vms.error && lxc.error, data: vms.data ?? lxc.data }} empty={items.length === 0 && tr("No virtual machines or containers")}>{() => (
            <div className="space-y-1.5 text-[12.5px]">
              <div className="text-white/70">{tr("{0} running of {1}", items.filter((x) => x.on).length, items.length)}</div>
              {items.slice(0, 5).map((x) => (
                <div key={x.kind + x.name} className="flex items-center gap-2"><span className={cx("h-2 w-2 shrink-0 rounded-full", x.on ? "bg-ok" : "bg-white/30")} /><span className="truncate">{x.name}</span><span className="ml-auto text-[11px] text-white/50">{x.kind}</span></div>
              ))}
            </div>
          )}</State>
        </Frame>
      );
    }
    case "ups": {
      const u = ups.data;
      const st = u?.status;
      return (
        <Frame title={WIDGET_LABELS.ups} onClick={() => wm.open("control", "ups")}>
          <State q={ups} empty={(!u?.installed || u.config.mode === "off") && tr("No UPS configured")}>{() => u?.error ? <div className="text-[12.5px] text-warn">{srv(u.error)}</div> : !st ? <Muted>{tr("Unavailable")}</Muted> : (
            <div className="space-y-2">
              <div className="flex items-center gap-2 text-[13px]">
                <span className={cx("h-2 w-2 rounded-full", st.status.includes("LB") ? "bg-danger" : st.status.startsWith("OB") ? "bg-warn" : "bg-ok")} />
                {st.status.includes("LB") ? tr("Low battery") : st.status.startsWith("OB") ? tr("On battery") : tr("On mains power")}
              </div>
              {st.charge != null && <Bar value={st.charge} label={tr("Battery")} sub={`${st.charge.toFixed(0)} %${st.runtime ? ` · ${duration(st.runtime)}` : ""}`} />}
            </div>
          )}</State>
        </Frame>
      );
    }
    case "backups": {
      const list = (backups.data ?? []).map((b) => ({ b, run: b.history.at(-1) })).sort((x, y) => (y.run?.started ?? "").localeCompare(x.run?.started ?? ""));
      return (
        <Frame title={WIDGET_LABELS.backups} onClick={() => wm.open("backup")}>
          <State q={backups} empty={list.length === 0 && tr("No backup tasks")}>{() => (
            <div className="space-y-1.5 text-[12.5px]">
              {list.slice(0, 4).map(({ b, run }) => (
                <div key={b.id} className="flex items-center gap-2">
                  {b.running ? <span className="h-2 w-2 shrink-0 animate-pulse rounded-full bg-accent" /> : run ? (run.status === "success" ? <CheckCircle2 size={13} className="shrink-0 text-ok" /> : <XCircle size={13} className="shrink-0 text-danger" />) : <span className="h-2 w-2 shrink-0 rounded-full bg-white/30" />}
                  <span className="truncate">{b.name}</span>
                  <span className="ml-auto shrink-0 text-[11px] text-white/60">{b.running ? tr("running") : run ? ago(run.started) : tr("never")}</span>
                </div>
              ))}
            </div>
          )}</State>
        </Frame>
      );
    }
    case "temps": {
      const list = (disks.data ?? []).filter((d) => d.temperature > 0);
      return (
        <Frame title={WIDGET_LABELS.temps} onClick={() => wm.open("storage", "disks")}>
          <State q={disks} empty={list.length === 0 && tr("No temperature reported")}>{() => (
            <div className="space-y-1.5 text-[12.5px]">
              {list.slice(0, 6).map((d) => (
                <div key={d.name} className="flex items-center gap-2">
                  <span className="truncate">{d.name}</span><span className="truncate text-[11px] text-white/50">{d.model}</span>
                  <span className={cx("ml-auto shrink-0 font-medium tabular-nums", d.temperature >= 55 ? "text-danger" : d.temperature >= 45 ? "text-warn" : "text-white")}>{d.standby ? "—" : `${d.temperature} °C`}</span>
                </div>
              ))}
            </div>
          )}</State>
        </Frame>
      );
    }
    case "notifications":
      return (
        <Frame title={WIDGET_LABELS.notifications}>
          <State q={ovQ}>{() => ov!.alerts.length === 0 ? (
            <div className="flex items-center gap-2 text-[12.5px] text-ok"><CheckCircle2 size={15} /> {" "}{tr("Everything is working normally")}</div>
          ) : (
            <div className="space-y-1.5 text-[12.5px]">
              {ov!.alerts.slice(0, 5).map((a) => (
                <div key={a.message} className="flex items-start gap-2">
                  {a.level === "error" ? <XCircle size={14} className="mt-0.5 shrink-0 text-danger" /> : <AlertTriangle size={14} className="mt-0.5 shrink-0 text-warn" />}
                  <span className="line-clamp-2">{srv(a.message)}</span>
                </div>
              ))}
            </div>
          )}</State>
        </Frame>
      );
  }
}

function WidgetSlot({ id, me, ov }: { id: WidgetId; me: Me; ov?: Overview }) {
  const ref = useRef<HTMLDivElement>(null);
  const live = useInView(ref);
  return <div ref={ref}><WidgetBody id={id} me={me} ov={ov} live={live} /></div>;
}

/** The panel on the right of the desktop (xl screens). */
export function WidgetPanel({ me, ov, onCustomize }: { me: Me; ov?: Overview; onCustomize: () => void }) {
  const prefs = usePrefs();
  if (!prefs.data) return null;
  const ids = shownWidgets(prefs.data.widgets, me.role === "admin");
  if (ids.length === 0) return null;
  return (
    <aside className="group/panel absolute bottom-4 right-4 top-4 z-[1] hidden w-72 space-y-3 overflow-y-auto pb-8 [scrollbar-width:none] xl:block">
      {ids.map((id) => <WidgetSlot key={id} id={id} me={me} ov={ov} />)}
      <button type="button" onClick={onCustomize} className="mx-auto flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] text-white/60 opacity-0 transition hover:bg-white/10 hover:text-white focus-visible:opacity-100 group-hover/panel:opacity-100">
        <Settings2 size={14} /> {" "}{tr("Customize widgets")}
      </button>
    </aside>
  );
}

/** Choosing and ordering the widgets (drag, or the arrows). */
export function WidgetsModal({ me, open, onClose }: { me: Me; open: boolean; onClose: () => void }) {
  const prefs = usePrefs();
  const save = useSavePrefs();
  const { toast } = useUI();
  const admin = me.role === "admin";
  const [list, setList] = useState<WidgetId[] | null>(null);
  const [drag, setDrag] = useState<WidgetId | null>(null);
  useEffect(() => {
    if (open && prefs.data) setList(shownWidgets(prefs.data.widgets, admin));
    if (!open) setList(null);
  }, [open, prefs.data, admin]);
  if (!list) return null;
  const others = WIDGET_IDS.filter((id) => !list.includes(id) && (admin || !ADMIN_WIDGETS.has(id)));
  const submit = () => save({ widgets: list }).then(onClose, (e) => toast(srv((e as Error).message), "error"));
  const row = "flex items-center gap-2 rounded-lg border border-line bg-panel-2 px-2.5 py-2 text-[13px]";
  return (
    <Modal open={open} onClose={onClose} title={tr("Desktop widgets")} footer={<>
      <Button variant="ghost" className="mr-auto" onClick={() => setList(shownWidgets(null, admin))}>{tr("Default widgets")}</Button>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" onClick={submit}>{tr("Save")}</Button>
    </>}>
      <p className="mb-3 text-muted text-[12.5px]">{tr("Shown on the right of the desktop on large screens. Drag them to change their order.")}</p>
      <div className="space-y-1.5">
        {list.map((id, i) => (
          <div
            key={id}
            draggable
            onDragStart={() => setDrag(id)}
            onDragOver={(e) => { e.preventDefault(); if (drag && drag !== id) setList(dropWidget(list, drag, id)); }}
            onDragEnd={() => setDrag(null)}
            className={cx(row, "cursor-grab", drag === id && "opacity-50")}
          >
            <GripVertical size={15} className="text-muted" />
            <Checkbox checked onChange={() => setList(toggleWidget(list, id, false))} label={WIDGET_LABELS[id]} />
            <span className="ml-auto flex gap-1">
              <button type="button" disabled={i === 0} onClick={() => setList(moveWidget(list, id, -1))} className="rounded p-1 text-muted hover:bg-panel hover:text-fg disabled:opacity-30" aria-label={tr("Move up")} title={tr("Move up")}><ArrowUp size={14} /></button>
              <button type="button" disabled={i === list.length - 1} onClick={() => setList(moveWidget(list, id, 1))} className="rounded p-1 text-muted hover:bg-panel hover:text-fg disabled:opacity-30" aria-label={tr("Move down")} title={tr("Move down")}><ArrowDown size={14} /></button>
            </span>
          </div>
        ))}
        {list.length === 0 && <div className="py-3 text-center text-muted text-[12.5px]">{tr("No widgets: the panel is hidden.")}</div>}
      </div>
      {others.length > 0 && (
        <>
          <div className="mb-1.5 mt-4 text-[12px] font-medium text-muted">{tr("Available widgets")}</div>
          <div className="space-y-1.5">
            {others.map((id) => (
              <div key={id} className={row}><Checkbox checked={false} onChange={() => setList(toggleWidget(list, id, true))} label={WIDGET_LABELS[id]} /></div>
            ))}
          </div>
        </>
      )}
    </Modal>
  );
}
