import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, FingerprintPattern, Image as ImageIcon, KeyRound, LayoutGrid, LayoutPanelLeft, LogOut, Monitor as MonitorIcon, Moon, Palette, Power, RefreshCcw, Search, ShieldCheck, Sun, TabletSmartphone } from "lucide-react";
import logo from "../logo.svg";
import { api } from "../lib/api";
import type { AppStoreData, Me, Overview } from "../lib/types";
import { cx } from "../lib/format";
import { useUI } from "../components/ui";
import { JobsMenu, PasswordModal, useTheme } from "../components/Layout";
import { TwoFactorModal } from "../components/TwoFactor";
import { PasskeysModal } from "../components/Passkeys";
import { TwoFactorPrompt } from "../components/TwoFactorPrompt";
import { MobileDevicesModal } from "../components/MobileDevices";
import { APPS, AppIcon, appById, type AppDef } from "./apps";
import { AppContent } from "./AppContent";
import { Window } from "./Window";
import { TASKBAR, WindowManager, useWM } from "./wm";
import { setUIMode } from "./mode";
import { CELL_H, CELL_W, GRID_PAD, cellAt, gridSize, iconToward, layoutIcons, moveIcon, type Cell, type Direction, type Layout } from "./layout";
import { OpenPath } from "./context";
import { tr, locale } from "../lib/i18n";
import { LangSwitch } from "../components/LangSwitch";
import { GlobalSearch } from "../components/GlobalSearch";
import { Splash } from "../components/Splash";
import { NotificationCenter } from "../components/NotificationCenter";
import { focusFirstItem, menuKeys, useDismiss } from "../lib/a11y";
import { WindowSwitcher } from "./WindowSwitcher";
import { AppearanceModal } from "../components/Appearance";
import { openAccountDialog, useAccountDialog } from "../lib/intent";
import { useAccent, usePrefs, useSavePrefs } from "../lib/appearance";
import { WidgetPanel, WidgetsModal } from "./widgets";
import { shownWidgets } from "./widgetList";
import { DEFAULT_WALLPAPER, WALLPAPERS, legacyWallpaper, wallpaperCSS } from "./wallpapers";

function useClock() {
  const [now, setNow] = useState(new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 15000);
    return () => clearInterval(t);
  }, []);
  return now;
}


/* ---------- Taskbar pieces ---------- */

function Launcher({ apps, onClose }: { apps: AppDef[]; onClose: () => void }) {
  const wm = useWM()!;
  const [q, setQ] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, true, onClose);
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
    <div ref={ref} role="dialog" aria-label={tr("Main menu")} className="absolute left-2 top-[52px] z-[9999] w-[560px] max-w-[calc(100vw-1rem)] rounded-2xl border border-line bg-panel/90 p-5 text-fg shadow-2xl backdrop-blur-xl">
      <div className="relative mb-4">
        <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted" />
        <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && hits[0]) go(hits[0].app.id, hits[0].section); if (e.key === "Escape") onClose(); }} placeholder={tr("Search for an app or setting…")} aria-label={tr("Search for an app or setting…")} className="input !pl-9" />
      </div>
      {q ? (
        <div className="max-h-[360px] space-y-0.5 overflow-y-auto">
          {hits.length === 0 && <div className="py-6 text-center text-muted text-[13px]">{tr("No results")}</div>}
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
            <button key={a.id} onClick={() => go(a.id)} className="flex flex-col items-center gap-2 rounded-xl px-2 py-3 text-center outline-none hover:bg-panel-2 focus-visible:ring-2 focus-visible:ring-accent">
              <AppIcon app={a} size={48} />
              <span className="text-[12px] leading-tight">{a.title}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function TrayBtn({ title, onClick, children }: { title: string; onClick: () => void; children: ReactNode }) {
  return (
    <button title={title} aria-label={title} onClick={onClick} className="relative flex h-9 w-9 items-center justify-center rounded-lg text-dk-fg/80 transition hover:bg-dk-hover hover:text-dk-fg">
      {children}
    </button>
  );
}

function UserMenu({ me }: { me: Me }) {
  const [open, setOpen] = useState(false);
  const [pw, setPw] = useState(false);
  const [tfa, setTfa] = useState(false);
  const [passkeys, setPasskeys] = useState(false);
  const [mobile, setMobile] = useState(false);
  const [look, setLook] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  useDismiss(ref, open, () => { setOpen(false); button.current?.focus(); });
  const { confirm } = useUI();
  useAccountDialog((d) => {
    if (d === "appearance") setLook(true);
    else if (d === "password") setPw(true);
    else if (d === "2fa") setTfa(true);
    else if (d === "passkeys") setPasskeys(true);
    else if (d === "devices") setMobile(true);
  });
  const qc = useQueryClient();
  const item = "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left outline-none hover:bg-panel-2 focus-visible:bg-panel-2 focus-visible:ring-2 focus-visible:ring-accent/60";
  const power = async (action: "reboot" | "poweroff") => {
    setOpen(false);
    if (await confirm({ title: action === "reboot" ? tr("Restart the NAS?") : tr("Shut down the NAS?"), text: tr("Shares and applications will be interrupted."), danger: true, confirm: action === "reboot" ? tr("Restart") : tr("Shut down") })) await api.post(`/api/system/power/${action}`);
  };
  return (
    <div className="relative" ref={ref}>
      <button ref={button} onClick={() => setOpen(!open)} aria-haspopup="menu" aria-expanded={open} aria-label={tr("Account menu of {0}", me.fullName || me.username)} className="flex h-9 items-center gap-2 rounded-lg pl-1 pr-2 text-dk-fg/90 hover:bg-dk-hover">
        <span aria-hidden className="flex h-7 w-7 items-center justify-center rounded-full bg-gradient-to-br from-accent to-accent-2 text-[12px] font-semibold text-white">{me.username[0]?.toUpperCase()}</span>
        <span className="hidden text-[13px] md:inline">{me.fullName || me.username}</span>
      </button>
      {open && (
        <div role="menu" aria-label={tr("Account")} ref={focusFirstItem} onKeyDown={menuKeys} className="card absolute right-0 top-11 z-[9999] w-60 p-1.5 text-[13px] text-fg">
          <div className="px-3 py-2 text-muted text-[12px]">{me.role === "admin" ? tr("Administrator") : tr("User")} · {me.username}</div>
          <button role="menuitem" className={item} onClick={() => { setOpen(false); setLook(true); }}><Palette size={15} /> {" "}{tr("Appearance")}</button>
          <button role="menuitem" className={item} onClick={() => { setOpen(false); setPw(true); }}><KeyRound size={15} /> {" "}{tr("Password")}</button>
          <button role="menuitem" className={item} onClick={() => { setOpen(false); setTfa(true); }}><ShieldCheck size={15} /> {" "}{tr("Two-factor authentication")}</button>
          <button role="menuitem" className={item} onClick={() => { setOpen(false); setPasskeys(true); }}><FingerprintPattern size={15} /> {" "}{tr("Passkeys")}</button>
          <button role="menuitem" className={item} onClick={() => { setOpen(false); setMobile(true); }}><TabletSmartphone size={15} /> {" "}{tr("Phones and computers")}</button>
          <button role="menuitem" className={item} onClick={() => setUIMode("classic")}><MonitorIcon size={15} /> {" "}{tr("Classic interface")}</button>
          {me.role === "admin" && (
            <>
              <button role="menuitem" className={item} onClick={() => power("reboot")}><RefreshCcw size={15} /> {" "}{tr("Restart")}</button>
              <button role="menuitem" className={item} onClick={() => power("poweroff")}><Power size={15} /> {" "}{tr("Shut down")}</button>
            </>
          )}
          <LangSwitch className={item} />
          <div className="my-1 border-t border-line" />
          <button role="menuitem" className={cx(item, "text-danger")} onClick={async () => { await api.post("/api/logout").catch(() => {}); qc.clear(); window.location.href = "/"; }}><LogOut size={15} /> {" "}{tr("Sign out")}</button>
        </div>
      )}
      <PasswordModal open={pw} onClose={() => setPw(false)} />
      <TwoFactorModal open={tfa} onClose={() => setTfa(false)} />
      <PasskeysModal open={passkeys} onClose={() => setPasskeys(false)} />
      <MobileDevicesModal open={mobile} onClose={() => setMobile(false)} />
      <AppearanceModal open={look} onClose={() => setLook(false)} />
    </div>
  );
}

function Taskbar({ me, apps, alerts }: { me: Me; apps: AppDef[]; alerts: Overview["alerts"] }) {
  const wm = useWM()!;
  const [launcher, setLauncher] = useState(false);
  const [theme, setTheme] = useTheme();
  const now = useClock();
  const openPage = (p: string) => { if (!wm.openPath(p)) wm.open("overview"); };
  return (
    <nav id="strata-taskbar" aria-label={tr("Taskbar")} className="relative z-[9000] flex shrink-0 items-center gap-1 border-b border-dk-line bg-dk-bar px-2 text-dk-fg backdrop-blur-xl [&_button]:outline-none [&_button:focus-visible]:ring-2 [&_button:focus-visible]:ring-accent" style={{ height: TASKBAR }}>
      <button
        onClick={() => setLauncher(!launcher)}
        onMouseDown={(e) => launcher && e.stopPropagation()} // the menu's own outside click would reopen it
        className={cx("flex h-9 items-center gap-2 rounded-lg px-2.5 transition hover:bg-dk-hover", launcher && "bg-dk-active")}
        title={tr("Main menu")}
        aria-label={tr("Main menu")}
        aria-haspopup="dialog"
        aria-expanded={launcher}
      >
        <LayoutGrid size={18} />
      </button>
      <div className="mx-1 h-6 w-px bg-dk-line-strong" aria-hidden />
      <div role="toolbar" aria-label={tr("Open windows")} className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
        {wm.windows.map((w) => {
          const a = appById(w.app)!;
          const active = wm.focused === w.app && !w.minimized;
          return (
            <button key={w.app} onClick={() => (active ? wm.minimize(w.app) : wm.focus(w.app))} aria-pressed={active} aria-label={w.minimized ? tr("{0} (collapsed)", a.title) : a.title} className={cx("flex h-9 max-w-[200px] shrink-0 items-center gap-2 rounded-lg px-2.5 text-[12.5px] transition", active ? "bg-dk-active" : "hover:bg-dk-hover", w.minimized && "opacity-60")} title={a.title}>
              <AppIcon app={a} size={20} />
              <span className="truncate">{a.title}</span>
            </button>
          );
        })}
      </div>
      <GlobalSearch me={me} dark openPage={openPage} />
      {/* Only the trigger takes the taskbar colours: its list is a themed card. */}
      <div className="[&>div>button:first-child]:text-dk-fg/80 [&>div>button:first-child:hover]:bg-dk-hover [&>div>button:first-child:hover]:text-dk-fg"><JobsMenu /></div>
      <NotificationCenter me={me} alerts={alerts} openPage={openPage} tray />
      <TrayBtn title={theme === "dark" ? tr("Light theme") : tr("Dark theme")} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>{theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}</TrayBtn>
      <div className="hidden px-2 text-right leading-tight sm:block" role="timer" aria-label={now.toLocaleString(locale, { weekday: "long", day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" })}>
        <div className="text-[13px] font-medium" aria-hidden>{now.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}</div>
        <div className="text-[10.5px] text-dk-fg/70" aria-hidden>{now.toLocaleDateString(locale, { weekday: "short", day: "numeric", month: "short" })}</div>
      </div>
      <UserMenu me={me} />
      {launcher && <Launcher apps={apps} onClose={() => setLauncher(false)} />}
    </nav>
  );
}

/* ---------- Desktop ---------- */

interface WebApp { id: string; name: string; icon: string; url: string }

/** One cell of the icon grid: fixed size, icon centered, label on two lines at most. */
function DesktopIcon({ id, label, hint, cell, onOpen, onDragEnd, tabbable, onFocus, onArrow, children }: {
  id: string;
  label: string;
  /** Said by screen readers and shown on hover, not under the icon. */
  hint?: string;
  cell: Cell;
  onOpen: () => void;
  onDragEnd: (x: number, y: number) => void;
  /** The icon reached by Tab (one per grid; arrows move between them). */
  tabbable: boolean;
  onFocus: () => void;
  onArrow: (d: Direction) => void;
  children: ReactNode;
}) {
  const drag = useRef<{ x: number; y: number; id: number; moved: boolean } | null>(null);
  const moved = useRef(false); // the click that ends a drag must not open the app
  const [offset, setOffset] = useState<[number, number] | null>(null);
  return (
    <button
      type="button"
      title={hint ?? label}
      aria-label={hint ?? label}
      data-icon={id}
      tabIndex={tabbable ? 0 : -1}
      onFocus={onFocus}
      onKeyDown={(e) => {
        const d = ({ ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" } as Record<string, Direction>)[e.key];
        if (d && !e.altKey && !e.ctrlKey && !e.metaKey) {
          e.preventDefault();
          onArrow(d);
        }
      }}
      onPointerDown={(e) => {
        if (e.button !== 0) return;
        drag.current = { x: e.clientX, y: e.clientY, id: e.pointerId, moved: false };
        moved.current = false;
      }}
      onPointerMove={(e) => {
        const d = drag.current;
        if (!d || d.id !== e.pointerId) return;
        const dx = e.clientX - d.x, dy = e.clientY - d.y;
        if (!d.moved && Math.hypot(dx, dy) < 6) return;
        if (!d.moved) (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
        d.moved = true;
        setOffset([dx, dy]);
      }}
      onPointerUp={(e) => {
        const d = drag.current;
        drag.current = null;
        setOffset(null);
        if (d?.moved) {
          moved.current = true;
          onDragEnd(e.clientX, e.clientY);
        }
      }}
      onPointerCancel={() => { drag.current = null; setOffset(null); }}
      onClick={() => {
        if (moved.current) {
          moved.current = false;
          return;
        }
        onOpen();
      }}
      className={cx(
        "group absolute flex touch-none select-none flex-col items-center rounded-xl px-1 pt-2 text-center outline-none transition-colors hover:bg-white/10 focus-visible:bg-white/20 focus-visible:ring-2 focus-visible:ring-white/80",
        offset && "z-10 bg-white/10",
      )}
      style={{
        left: GRID_PAD + cell[0] * CELL_W, top: GRID_PAD + cell[1] * CELL_H, width: CELL_W - 4, height: CELL_H - 4,
        transform: offset ? `translate(${offset[0]}px, ${offset[1]}px)` : undefined,
      }}
    >
      <div className="flex h-14 w-14 shrink-0 items-center justify-center transition group-hover:scale-105 group-active:scale-95">{children}</div>
      <span className="mt-1.5 line-clamp-2 w-full break-words text-[12px] leading-[15px] text-white [text-shadow:0_1px_3px_rgba(0,0,0,0.8)]">{label}</span>
    </button>
  );
}

const NO_LAYOUT: Layout = {};
const menuItem = "flex w-full items-center gap-2.5 rounded-lg px-3 py-1.5 text-left outline-none hover:bg-panel-2 focus-visible:bg-panel-2 focus-visible:ring-2 focus-visible:ring-accent/60";

/** Size of an element, followed when it changes. */
function useSize(ref: React.RefObject<HTMLElement | null>) {
  const [size, setSize] = useState({ w: window.innerWidth, h: window.innerHeight - TASKBAR });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return size;
}

function DesktopInner({ me }: { me: Me }) {
  const wm = useWM()!;
  const prefs = usePrefs();
  const savePrefs = useSavePrefs();
  useAccent();
  const wall = prefs.data?.wallpaper || legacyWallpaper() || DEFAULT_WALLPAPER;
  const setWall = (wallpaper: string) => savePrefs({ wallpaper }).catch(() => {});
  const [widgetsOpen, setWidgetsOpen] = useState(false);
  useAccountDialog((d) => d === "widgets" && setWidgetsOpen(true));
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  useDismiss(menuRef, !!menu, () => setMenu(null));
  const apps = APPS.filter((a) => !a.admin || me.role === "admin");
  const ov = useQuery({ queryKey: ["overview"], queryFn: () => api.get<Overview>("/api/system/overview"), refetchInterval: 15000 });
  const store = useQuery({ queryKey: ["apps"], queryFn: () => api.get<AppStoreData>("/api/apps"), enabled: me.role === "admin" });

  // Installed App Center applications get a desktop shortcut to their web interface.
  const webApps: WebApp[] = useMemo(() => {
    const d = store.data;
    if (!d) return [];
    const url = (port: string | number) => `${location.protocol}//${location.hostname}:${port}`;
    return [
      ...Object.keys(d.installed).flatMap((id) => {
        const a = d.apps.find((x) => x.id === id);
        const port = d.ports[id];
        return a && port ? [{ id, name: a.name, icon: a.icon, url: url(port) }] : [];
      }),
      ...(d.custom ?? []).flatMap((c) => (c.webPort ? [{ id: "custom-" + c.stack, name: c.name, icon: c.icon ?? "", url: url(c.webPort) }] : [])),
    ];
  }, [store.data]);

  // Icon positions, saved on the NAS per user (grid coordinates).
  const saveLayout = (icons: Layout) => savePrefs({ icons }).catch(() => {});
  const mainRef = useRef<HTMLElement>(null);
  const size = useSize(mainRef);
  // The widgets column (xl screens) keeps its place on the right.
  const panel = prefs.data && shownWidgets(prefs.data.widgets, me.role === "admin").length > 0;
  const { cols, rows } = gridSize(size.w - (panel && window.innerWidth >= 1280 ? 304 : 0), size.h);
  const ids = useMemo(() => [...apps.map((a) => a.id), ...webApps.map((w) => w.id)], [apps, webApps]);
  const saved = prefs.data?.icons ?? NO_LAYOUT;
  const layout = useMemo(() => layoutIcons(ids, saved, cols, rows), [ids, saved, cols, rows]);
  const drop = (id: string, x: number, y: number) => {
    const r = mainRef.current?.getBoundingClientRect();
    if (!r || !prefs.data) return;
    const to = cellAt(x - r.left, y - r.top, cols, rows);
    const next = moveIcon(layout, id, to);
    // Keep the saved places of icons hidden by a smaller screen; record the others as shown.
    const merged: Layout = {};
    const taken = new Set<string>();
    for (const k of ids) {
      const changed = next[k] !== layout[k];
      const c = changed || !saved[k] ? next[k] : saved[k];
      if (!c || taken.has(c.join())) continue;
      merged[k] = c;
      taken.add(c.join());
    }
    saveLayout(merged);
  };
  // Keyboard: one icon takes the Tab stop, the arrows move between icons.
  const [current, setCurrent] = useState<string | null>(null);
  const tabStop = current && layout[current] ? current : ids[0];
  const focusIcon = (id: string) => mainRef.current?.querySelector<HTMLElement>(`[data-icon="${CSS.escape(id)}"]`)?.focus();
  const iconProps = (id: string) => ({
    id,
    cell: layout[id] ?? ([0, 0] as Cell),
    onDragEnd: (x: number, y: number) => drop(id, x, y),
    tabbable: id === tabStop,
    onFocus: () => setCurrent(id),
    onArrow: (d: Direction) => { const to = iconToward(layout, id, d); if (to) focusIcon(to); },
  });

  // Desktop shortcuts: Alt+Shift+T the taskbar, Alt+Shift+D the icons,
  // Shift+F10 or the context menu key on the desktop opens its menu.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.altKey && e.shiftKey && !e.ctrlKey && !e.metaKey && (e.code === "KeyT" || e.code === "KeyD")) {
        e.preventDefault();
        if (e.code === "KeyT") document.querySelector<HTMLElement>("#strata-taskbar button")?.focus();
        else if (tabStop) focusIcon(tabStop);
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  });

  return (
    <div className="fixed inset-0 flex flex-col overflow-hidden" style={{ background: wallpaperCSS(wall, prefs.data?.customWallpaper) }} onContextMenu={(e) => {
      e.preventDefault();
      // From the keyboard (Shift+F10, menu key) the pointer position means nothing: open by the focused element.
      const r = e.clientX || e.clientY ? null : (document.activeElement as HTMLElement | null)?.getBoundingClientRect();
      setMenu(r ? { x: r.left + 8, y: r.bottom } : { x: e.clientX, y: e.clientY });
    }}>
      <Taskbar me={me} apps={apps} alerts={ov.data?.alerts ?? []} />
      <main ref={mainRef} className="relative flex-1" onMouseDown={() => setMenu(null)}>
        {/* Icons wait for the saved layout, so they do not jump once it arrives. */}
        <div role="group" aria-label={tr("Desktop icons")} aria-describedby="strata-desktop-keys" className={cx("absolute inset-0", prefs.isLoading && "invisible")}>
          <p id="strata-desktop-keys" className="sr-only">{tr("Arrow keys move between the icons, Enter opens. Alt+Shift+T goes to the taskbar, Ctrl+` switches between windows.")}</p>
          {apps.map((a) => (
            <DesktopIcon key={a.id} label={a.title} onOpen={() => wm.open(a.id)} {...iconProps(a.id)}>
              <AppIcon app={a} size={56} />
            </DesktopIcon>
          ))}
          {webApps.map((w) => (
            <DesktopIcon key={w.id} label={w.name} hint={tr("{0} (opens in a new tab)", w.name)} onOpen={() => window.open(w.url, "_blank", "noopener")} {...iconProps(w.id)}>
              <div className="relative flex h-14 w-14 items-center justify-center rounded-[15px] bg-white/90 p-2 shadow-lg">
                {w.icon ? <img src={w.icon} alt="" referrerPolicy="no-referrer" className="h-full w-full object-contain" onError={(e) => ((e.target as HTMLImageElement).style.display = "none")} /> : <span className="text-[22px] font-semibold text-slate-700">{w.name[0]?.toUpperCase()}</span>}
                <ExternalLink size={12} className="absolute -bottom-1 -right-1 rounded bg-black/60 p-0.5 text-white" />
              </div>
            </DesktopIcon>
          ))}
        </div>
        <WidgetPanel me={me} ov={ov.data} onCustomize={() => setWidgetsOpen(true)} />
        {wm.windows.map((w) => {
          const a = appById(w.app);
          return a ? <Window key={w.app} win={w} app={a}><AppContent app={a} win={w} me={me} /></Window> : null;
        })}
        <div className="pointer-events-none absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-2 text-[11px] text-white/40">
          <img src={logo} alt="" className="h-4 w-4 opacity-60" /> StrataOS
        </div>
      </main>
      {menu && (
        <div
          role="menu"
          aria-label={tr("Desktop")}
          className="card fixed z-[9999] w-56 p-1.5 text-[13px] text-fg"
          style={{ left: Math.min(menu.x, window.innerWidth - 240), top: menu.y }}
          // Kept on screen with its real height, which grows with the wallpaper list.
          ref={(el) => {
            menuRef.current = el;
            if (!el) return;
            el.style.top = Math.max(8, Math.min(menu.y, window.innerHeight - el.offsetHeight - 8)) + "px";
            if (!el.contains(document.activeElement)) focusFirstItem(el);
          }}
          onKeyDown={menuKeys}
          onMouseLeave={() => setMenu(null)}
        >
          <div className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-muted"><ImageIcon size={14} /> {" "}{tr("Wallpaper")}</div>
          {Object.entries(WALLPAPERS).map(([k, w]) => (
            <button key={k} role="menuitemradio" aria-checked={wall === k} onClick={() => { setWall(k); setMenu(null); }} className={cx(menuItem, wall === k && "text-accent")}>
              <span className="h-4 w-6 rounded" style={{ background: w.css }} aria-hidden /> {w.label}
            </button>
          ))}
          <div className="my-1 border-t border-line" />
          <button onClick={() => { openAccountDialog("appearance"); setMenu(null); }} role="menuitem" className={menuItem}><Palette size={14} /> {" "}{tr("Appearance…")}</button>
          <button onClick={() => { setWidgetsOpen(true); setMenu(null); }} role="menuitem" className={menuItem}><LayoutPanelLeft size={14} /> {" "}{tr("Customize widgets")}</button>
          <button onClick={() => { saveLayout({}); setMenu(null); }} role="menuitem" className={menuItem}><LayoutGrid size={14} /> {" "}{tr("Rearrange icons")}</button>
          <button onClick={() => setUIMode("classic")} role="menuitem" className={menuItem}><MonitorIcon size={14} /> {" "}{tr("Classic interface")}</button>
        </div>
      )}
      <WidgetsModal me={me} open={widgetsOpen} onClose={() => setWidgetsOpen(false)} />
      <WindowSwitcher />
      {prefs.isLoading && <Splash />}
    </div>
  );
}

function WithOpenPath({ children }: { children: ReactNode }) {
  const wm = useWM()!;
  return <OpenPath.Provider value={wm.openPath}>{children}</OpenPath.Provider>;
}

export function Desktop({ me }: { me: Me }) {
  const allowed = useCallback((a: AppDef) => !a.admin || me.role === "admin", [me.role]);
  return (
    <WindowManager allowed={allowed}>
      <WithOpenPath>
        <DesktopInner me={me} />
        <TwoFactorPrompt me={me} />
      </WithOpenPath>
    </WindowManager>
  );
}
