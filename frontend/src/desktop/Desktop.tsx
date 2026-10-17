import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Bell, CheckCircle2, ExternalLink, Image as ImageIcon, KeyRound, LayoutGrid, LogOut, Monitor as MonitorIcon, Moon, Power, RefreshCcw, Search, ShieldCheck, Sun, XCircle } from "lucide-react";
import logo from "../logo.svg";
import { api } from "../lib/api";
import type { App, Me, Overview, Sample, SystemInfo } from "../lib/types";
import { bytes, cx, duration, pct, rate } from "../lib/format";
import { Progress, useUI } from "../components/ui";
import { JobsMenu, PasswordModal, useTheme } from "../components/Layout";
import { TwoFactorModal } from "../components/TwoFactor";
import { APPS, AppIcon, appById, type AppDef } from "./apps";
import { AppContent } from "./AppContent";
import { Window } from "./Window";
import { TASKBAR, WindowManager, useWM } from "./wm";
import { setUIMode } from "./mode";
import { OpenPath } from "./context";
import { tr, locale } from "../lib/i18n";
import { LangSwitch } from "../components/LangSwitch";

/* ---------- Wallpapers (pure CSS, no download) ---------- */

const wallpapers: Record<string, { label: string; css: string }> = {
  aurora: { label: tr("Aurore"), css: "radial-gradient(1200px 700px at 15% 10%, #3b4cca 0%, transparent 60%), radial-gradient(900px 600px at 85% 30%, #0e7490 0%, transparent 55%), radial-gradient(1000px 800px at 60% 100%, #7c3aed 0%, transparent 60%), #0b1022" },
  dusk: { label: tr("Crépuscule"), css: "radial-gradient(1100px 700px at 80% 0%, #f97316 0%, transparent 55%), radial-gradient(1000px 700px at 10% 90%, #be185d 0%, transparent 55%), linear-gradient(160deg, #1e1b4b, #0f172a)" },
  ocean: { label: tr("Océan"), css: "radial-gradient(1200px 800px at 50% -10%, #38bdf8 0%, transparent 55%), radial-gradient(900px 700px at 0% 100%, #0f766e 0%, transparent 60%), linear-gradient(180deg, #0c4a6e, #082f49)" },
  forest: { label: tr("Forêt"), css: "radial-gradient(1000px 700px at 20% 20%, #16a34a 0%, transparent 55%), radial-gradient(900px 700px at 90% 80%, #0f766e 0%, transparent 55%), linear-gradient(160deg, #052e16, #022c22)" },
  graphite: { label: tr("Graphite"), css: "radial-gradient(1000px 700px at 70% 20%, #334155 0%, transparent 60%), linear-gradient(160deg, #111827, #030712)" },
};

function usePref(key: string, def: string): [string, (v: string) => void] {
  const [v, setV] = useState(() => {
    try {
      return localStorage.getItem(key) ?? def;
    } catch {
      return def;
    }
  });
  return [v, (x: string) => {
    setV(x);
    try {
      localStorage.setItem(key, x);
    } catch {
      /* storage unavailable */
    }
  }];
}

function useClock() {
  const [now, setNow] = useState(new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 15000);
    return () => clearInterval(t);
  }, []);
  return now;
}

function useOutside(ref: React.RefObject<HTMLElement | null>, onOut: () => void) {
  useEffect(() => {
    const h = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && onOut();
    document.addEventListener("mousedown", h);
    return () => document.removeEventListener("mousedown", h);
  }, [ref, onOut]);
}

/* ---------- Taskbar pieces ---------- */

function Launcher({ apps, onClose }: { apps: AppDef[]; onClose: () => void }) {
  const wm = useWM()!;
  const [q, setQ] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  useOutside(ref, onClose);
  const entries = useMemo(() => {
    const out: { app: AppDef; section?: string; label: string; sub?: string }[] = [];
    for (const a of apps) {
      out.push({ app: a, label: a.title });
      for (const s of a.sections ?? []) out.push({ app: a, section: s.id, label: s.label, sub: a.title });
    }
    return out;
  }, [apps]);
  const hits = q ? entries.filter((e) => (e.label + " " + (e.sub ?? "")).toLowerCase().includes(q.toLowerCase())) : [];
  const go = (app: string, section?: string) => {
    wm.open(app, section);
    onClose();
  };
  return (
    <div ref={ref} className="absolute left-2 top-[52px] z-[9999] w-[560px] max-w-[calc(100vw-1rem)] rounded-2xl border border-white/10 bg-panel/90 p-5 shadow-2xl backdrop-blur-xl">
      <div className="relative mb-4">
        <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted" />
        <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && hits[0]) go(hits[0].app.id, hits[0].section); if (e.key === "Escape") onClose(); }} placeholder={tr("Rechercher une application ou un réglage…")} className="input !pl-9" />
      </div>
      {q ? (
        <div className="max-h-[360px] space-y-0.5 overflow-y-auto">
          {hits.length === 0 && <div className="py-6 text-center text-muted text-[13px]">{tr("Aucun résultat")}</div>}
          {hits.map((h) => (
            <button key={h.app.id + (h.section ?? "")} onClick={() => go(h.app.id, h.section)} className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left hover:bg-panel-2">
              <AppIcon app={h.app} size={30} />
              <div><div className="text-[13.5px]">{h.label}</div>{h.sub && <div className="text-muted text-[11.5px]">{h.sub}</div>}</div>
            </button>
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-4 gap-2">
          {apps.map((a) => (
            <button key={a.id} onClick={() => go(a.id)} className="flex flex-col items-center gap-2 rounded-xl px-2 py-3 text-center hover:bg-panel-2">
              <AppIcon app={a} size={48} />
              <span className="text-[12px] leading-tight">{a.title}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function Alerts({ alerts }: { alerts: Overview["alerts"] }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useOutside(ref, () => setOpen(false));
  return (
    <div className="relative" ref={ref}>
      <TrayBtn title={tr("Notifications")} onClick={() => setOpen(!open)}>
        <Bell size={17} />
        {alerts.length > 0 && <span className="absolute right-1 top-1 h-2 w-2 rounded-full bg-danger" />}
      </TrayBtn>
      {open && (
        <div className="card absolute right-0 top-11 z-[9999] w-[360px] p-2">
          <div className="px-2 py-1.5 text-[12px] font-medium text-muted">{tr("Notifications")}</div>
          {alerts.length === 0 ? (
            <div className="flex items-center gap-2 px-2 py-4 text-[13px] text-ok"><CheckCircle2 size={16} /> {" "}{tr("Tout fonctionne normalement")}</div>
          ) : alerts.map((a) => (
            <div key={a.message} className="flex items-start gap-2 rounded-lg px-2 py-2 text-[13px]">
              {a.level === "error" ? <XCircle size={16} className="mt-0.5 shrink-0 text-danger" /> : <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warn" />}
              {a.message}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function TrayBtn({ title, onClick, children }: { title: string; onClick: () => void; children: ReactNode }) {
  return (
    <button title={title} aria-label={title} onClick={onClick} className="relative flex h-9 w-9 items-center justify-center rounded-lg text-white/80 transition hover:bg-white/10 hover:text-white">
      {children}
    </button>
  );
}

function UserMenu({ me }: { me: Me }) {
  const [open, setOpen] = useState(false);
  const [pw, setPw] = useState(false);
  const [tfa, setTfa] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useOutside(ref, () => setOpen(false));
  const { confirm } = useUI();
  const qc = useQueryClient();
  const item = "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left hover:bg-panel-2";
  const power = async (action: "reboot" | "poweroff") => {
    setOpen(false);
    if (await confirm({ title: action === "reboot" ? tr("Redémarrer le NAS ?") : tr("Éteindre le NAS ?"), text: tr("Les partages et les applications seront interrompus."), danger: true, confirm: action === "reboot" ? tr("Redémarrer") : tr("Éteindre") })) await api.post(`/api/system/power/${action}`);
  };
  return (
    <div className="relative" ref={ref}>
      <button onClick={() => setOpen(!open)} className="flex h-9 items-center gap-2 rounded-lg pl-1 pr-2 text-white/90 hover:bg-white/10">
        <span className="flex h-7 w-7 items-center justify-center rounded-full bg-gradient-to-br from-accent to-accent-2 text-[12px] font-semibold text-white">{me.username[0]?.toUpperCase()}</span>
        <span className="hidden text-[13px] md:inline">{me.fullName || me.username}</span>
      </button>
      {open && (
        <div className="card absolute right-0 top-11 z-[9999] w-60 p-1.5 text-[13px]">
          <div className="px-3 py-2 text-muted text-[12px]">{me.role === "admin" ? tr("Administrateur") : tr("Utilisateur")} · {me.username}</div>
          <button className={item} onClick={() => { setOpen(false); setPw(true); }}><KeyRound size={15} /> {" "}{tr("Mot de passe")}</button>
          <button className={item} onClick={() => { setOpen(false); setTfa(true); }}><ShieldCheck size={15} /> {" "}{tr("Double authentification")}</button>
          <button className={item} onClick={() => setUIMode("classic")}><MonitorIcon size={15} /> {" "}{tr("Interface classique")}</button>
          {me.role === "admin" && (
            <>
              <button className={item} onClick={() => power("reboot")}><RefreshCcw size={15} /> {" "}{tr("Redémarrer")}</button>
              <button className={item} onClick={() => power("poweroff")}><Power size={15} /> {" "}{tr("Éteindre")}</button>
            </>
          )}
          <LangSwitch className={item} />
          <div className="my-1 border-t border-line" />
          <button className={cx(item, "text-danger")} onClick={async () => { await api.post("/api/logout").catch(() => {}); qc.clear(); window.location.href = "/"; }}><LogOut size={15} /> {" "}{tr("Déconnexion")}</button>
        </div>
      )}
      <PasswordModal open={pw} onClose={() => setPw(false)} />
      <TwoFactorModal open={tfa} onClose={() => setTfa(false)} />
    </div>
  );
}

function Taskbar({ me, apps, alerts }: { me: Me; apps: AppDef[]; alerts: Overview["alerts"] }) {
  const wm = useWM()!;
  const [launcher, setLauncher] = useState(false);
  const [theme, setTheme] = useTheme();
  const now = useClock();
  return (
    <header className="relative z-[9000] flex shrink-0 items-center gap-1 border-b border-white/10 bg-black/35 px-2 text-white backdrop-blur-xl" style={{ height: TASKBAR }}>
      <button onClick={() => setLauncher(!launcher)} className={cx("flex h-9 items-center gap-2 rounded-lg px-2.5 transition hover:bg-white/10", launcher && "bg-white/15")} title={tr("Menu principal")}>
        <LayoutGrid size={18} />
      </button>
      <div className="mx-1 h-6 w-px bg-white/15" />
      <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        {wm.windows.map((w) => {
          const a = appById(w.app)!;
          const active = wm.focused === w.app && !w.minimized;
          return (
            <button key={w.app} onClick={() => (active ? wm.minimize(w.app) : wm.focus(w.app))} className={cx("flex h-9 max-w-[200px] shrink-0 items-center gap-2 rounded-lg px-2.5 text-[12.5px] transition", active ? "bg-white/20" : "hover:bg-white/10", w.minimized && "opacity-60")} title={a.title}>
              <AppIcon app={a} size={20} />
              <span className="truncate">{a.title}</span>
            </button>
          );
        })}
      </div>
      <div className="[&_button]:text-white/80 [&_button:hover]:text-white"><JobsMenu /></div>
      <Alerts alerts={alerts} />
      <TrayBtn title={tr("Thème")} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>{theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}</TrayBtn>
      <div className="hidden px-2 text-right leading-tight sm:block">
        <div className="text-[13px] font-medium">{now.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}</div>
        <div className="text-[10.5px] text-white/70">{now.toLocaleDateString(locale, { weekday: "short", day: "numeric", month: "short" })}</div>
      </div>
      <UserMenu me={me} />
      {launcher && <Launcher apps={apps} onClose={() => setLauncher(false)} />}
    </header>
  );
}

/* ---------- Widgets ---------- */

function Widget({ title, children, onClick }: { title: string; children: ReactNode; onClick?: () => void }) {
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
      <div className="mb-1 flex justify-between text-[12px]"><span>{label}</span><span className="text-white/70">{sub}</span></div>
      <Progress value={value} className="!border-white/10 !bg-white/10" />
    </div>
  );
}

function Widgets({ me, ov }: { me: Me; ov?: Overview }) {
  const wm = useWM()!;
  const stats = useQuery({ queryKey: ["stats"], queryFn: () => api.get<{ history: Sample[] }>("/api/system/stats"), refetchInterval: 3000 });
  const info = useQuery({ queryKey: ["sysinfo"], queryFn: () => api.get<SystemInfo>("/api/system/info"), refetchInterval: 60000 });
  const last = stats.data?.history.at(-1);
  const errors = ov?.alerts.filter((a) => a.level === "error").length ?? 0;
  const healthy = ov && ov.alerts.length === 0;
  return (
    <aside className="absolute right-4 top-4 z-[1] hidden w-72 space-y-3 xl:block">
      <Widget title={tr("État du système")} onClick={() => wm.open("overview")}>
        <div className="flex items-center gap-3">
          <div className={cx("flex h-11 w-11 items-center justify-center rounded-full", healthy ? "bg-ok/25 text-ok" : errors ? "bg-danger/25 text-danger" : "bg-warn/25 text-warn")}>
            {healthy ? <CheckCircle2 size={22} /> : <AlertTriangle size={22} />}
          </div>
          <div className="min-w-0">
            <div className="font-semibold">{!ov ? "…" : healthy ? tr("Sain") : errors ? tr("Attention requise") : tr("Avertissement")}</div>
            <div className="truncate text-[12px] text-white/70">{ov?.alerts[0]?.message ?? (info.data ? tr("{0} · actif depuis {1}", info.data.hostname, duration(info.data.uptime)) : "")}</div>
          </div>
        </div>
      </Widget>
      {last && (
        <Widget title={tr("Ressources")} onClick={me.role === "admin" ? () => wm.open("monitor") : undefined}>
          <div className="space-y-3">
            <Bar value={last.cpu} label={tr("Processeur")} sub={`${last.cpu.toFixed(0)} %`} />
            <Bar value={pct(last.memUsed, last.memTotal)} label={tr("Mémoire")} sub={`${bytes(last.memUsed)} / ${bytes(last.memTotal)}`} />
            <div className="flex justify-between text-[12px] text-white/80"><span>↓ {rate(last.netRx)}</span><span>↑ {rate(last.netTx)}</span></div>
          </div>
        </Widget>
      )}
      {ov && ov.volumes.length > 0 && (
        <Widget title={tr("Stockage")} onClick={me.role === "admin" ? () => wm.open("storage", "volumes") : undefined}>
          <div className="space-y-3">
            {ov.volumes.slice(0, 4).map((v) => <Bar key={v.name} value={pct(v.used, v.total)} label={v.name} sub={tr("{0} libres", bytes(v.free))} />)}
          </div>
        </Widget>
      )}
      {ov && me.role === "admin" && (
        <Widget title={tr("Applications")} onClick={() => wm.open("docker", "containers")}>
          <div className="text-[13px]"><b className="text-[20px]">{ov.containers.running}</b> <span className="text-white/70">/ {ov.containers.total} {" "}{tr("conteneurs actifs")}</span></div>
        </Widget>
      )}
    </aside>
  );
}

/* ---------- Desktop ---------- */

interface WebApp { id: string; name: string; icon: string; url: string }

function DesktopIcon({ label, onOpen, children }: { label: string; onOpen: () => void; children: ReactNode }) {
  return (
    <button onClick={onOpen} className="group flex w-[92px] flex-col items-center gap-1.5 rounded-xl px-1 py-2 text-center outline-none transition hover:bg-white/10 focus-visible:bg-white/15">
      <div className="transition group-hover:scale-105 group-active:scale-95">{children}</div>
      <span className="line-clamp-2 text-[12px] leading-tight text-white [text-shadow:0_1px_3px_rgba(0,0,0,0.8)]">{label}</span>
    </button>
  );
}

function DesktopInner({ me }: { me: Me }) {
  const wm = useWM()!;
  const [wall, setWall] = usePref("strata-wallpaper", "aurora");
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const apps = APPS.filter((a) => !a.admin || me.role === "admin");
  const ov = useQuery({ queryKey: ["overview"], queryFn: () => api.get<Overview>("/api/system/overview"), refetchInterval: 15000 });
  const store = useQuery({ queryKey: ["apps"], queryFn: () => api.get<{ apps: App[]; installed: Record<string, string>; ports: Record<string, string> }>("/api/apps"), enabled: me.role === "admin" });

  // Installed App Center applications get a desktop shortcut to their web interface.
  const webApps: WebApp[] = useMemo(() => {
    const d = store.data;
    if (!d) return [];
    return Object.keys(d.installed).flatMap((id) => {
      const a = d.apps.find((x) => x.id === id);
      const port = d.ports[id];
      return a && port ? [{ id, name: a.name, icon: a.icon, url: `${location.protocol}//${location.hostname}:${port}` }] : [];
    });
  }, [store.data]);

  return (
    <div className="fixed inset-0 flex flex-col overflow-hidden" style={{ background: (wallpapers[wall] ?? wallpapers.aurora).css }} onContextMenu={(e) => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY }); }}>
      <Taskbar me={me} apps={apps} alerts={ov.data?.alerts ?? []} />
      <main className="relative flex-1" onMouseDown={() => setMenu(null)}>
        <div className="absolute inset-0 flex flex-col flex-wrap content-start gap-1 p-3">
          {apps.map((a) => (
            <DesktopIcon key={a.id} label={a.title} onOpen={() => wm.open(a.id)}>
              <AppIcon app={a} size={56} />
            </DesktopIcon>
          ))}
          {webApps.map((w) => (
            <DesktopIcon key={w.id} label={w.name} onOpen={() => window.open(w.url, "_blank", "noopener")}>
              <div className="relative flex h-14 w-14 items-center justify-center rounded-[15px] bg-white/90 p-2 shadow-lg">
                <img src={w.icon} alt="" className="h-full w-full object-contain" onError={(e) => ((e.target as HTMLImageElement).style.display = "none")} />
                <ExternalLink size={12} className="absolute -bottom-1 -right-1 rounded bg-black/60 p-0.5 text-white" />
              </div>
            </DesktopIcon>
          ))}
        </div>
        <Widgets me={me} ov={ov.data} />
        {wm.windows.map((w) => {
          const a = appById(w.app);
          return a ? <Window key={w.app} win={w} app={a}><AppContent app={a} win={w} me={me} /></Window> : null;
        })}
        <div className="pointer-events-none absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-2 text-[11px] text-white/40">
          <img src={logo} alt="" className="h-4 w-4 opacity-60" /> StrataOS
        </div>
      </main>
      {menu && (
        <div className="card fixed z-[9999] w-56 p-1.5 text-[13px]" style={{ left: Math.min(menu.x, window.innerWidth - 240), top: Math.min(menu.y, window.innerHeight - 280) }} onMouseLeave={() => setMenu(null)}>
          <div className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-muted"><ImageIcon size={14} /> {" "}{tr("Fond d'écran")}</div>
          {Object.entries(wallpapers).map(([k, w]) => (
            <button key={k} onClick={() => { setWall(k); setMenu(null); }} className={cx("flex w-full items-center gap-2.5 rounded-lg px-3 py-1.5 text-left hover:bg-panel-2", wall === k && "text-accent")}>
              <span className="h-4 w-6 rounded" style={{ background: w.css }} /> {w.label}
            </button>
          ))}
          <div className="my-1 border-t border-line" />
          <button onClick={() => setUIMode("classic")} className="flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left hover:bg-panel-2"><MonitorIcon size={14} /> {" "}{tr("Interface classique")}</button>
        </div>
      )}
    </div>
  );
}

function WithOpenPath({ children }: { children: ReactNode }) {
  const wm = useWM()!;
  return <OpenPath.Provider value={wm.openPath}>{children}</OpenPath.Provider>;
}

export function Desktop({ me }: { me: Me }) {
  return (
    <WindowManager>
      <WithOpenPath>
        <DesktopInner me={me} />
      </WithOpenPath>
    </WindowManager>
  );
}
