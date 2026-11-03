import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { appById, appForPath, pathOfApp, type AppDef } from "./apps";
import { clamp, startGeometry, type Geometry, type SavedGeometry } from "./geometry";

export type { Geometry } from "./geometry";

export interface Win extends Geometry {
  app: string;
  section?: string;
  z: number;
  minimized: boolean;
  maximized: boolean;
  /** When the window was last opened (from an icon, the menu…): see Window's click guard. */
  openedAt: number;
}

interface WM {
  windows: Win[];
  focused?: string;
  open: (app: string, section?: string) => void;
  openPath: (path: string) => boolean;
  close: (app: string) => void;
  focus: (app: string) => void;
  minimize: (app: string) => void;
  toggleMaximize: (app: string) => void;
  /** persist false: only kept on screen (browser resized), not remembered. */
  setGeometry: (app: string, g: Partial<Geometry>, persist?: boolean) => void;
  setSection: (app: string, section: string) => void;
  /** Area available to windows (below the taskbar). */
  area: () => { w: number; h: number };
}

const Ctx = createContext<WM | null>(null);
export const useWM = () => useContext(Ctx);

export { InWindow } from "./context";

export const TASKBAR = 48;
const SESSION_KEY = "strata-open-windows";

const store = {
  get(app: string): SavedGeometry | null {
    try {
      const v = localStorage.getItem(`strata-win-${app}`);
      return v ? (JSON.parse(v) as SavedGeometry) : null;
    } catch {
      return null;
    }
  },
  set(app: string, g: Geometry) {
    try {
      // With the area it was chosen on (startGeometry).
      const saved: SavedGeometry = { ...g, sw: window.innerWidth, sh: window.innerHeight - TASKBAR };
      localStorage.setItem(`strata-win-${app}`, JSON.stringify(saved));
    } catch {
      /* storage unavailable */
    }
  },
};


export function WindowManager({ children, allowed }: { children: ReactNode; allowed: (app: AppDef) => boolean }) {
  const [windows, setWindows] = useState<Win[]>([]);
  const navigate = useNavigate();
  const zRef = useRef(10);
  const area = useCallback(() => ({ w: window.innerWidth, h: window.innerHeight - TASKBAR }), []);

  const focus = useCallback((app: string) => {
    setWindows((ws) => {
      const top = Math.max(0, ...ws.map((w) => w.z));
      const cur = ws.find((w) => w.app === app);
      if (!cur || (cur.z === top && !cur.minimized)) return ws;
      zRef.current = top + 1;
      return ws.map((w) => (w.app === app ? { ...w, z: top + 1, minimized: false } : w));
    });
  }, []);

  const open = useCallback((app: string, section?: string) => {
    const def = appById(app);
    if (!def) return;
    setWindows((ws) => {
      const z = Math.max(zRef.current, ...ws.map((w) => w.z)) + 1;
      zRef.current = z;
      const existing = ws.find((w) => w.app === app);
      if (existing) return ws.map((w) => (w.app === app ? { ...w, z, minimized: false, section: section ?? w.section, openedAt: Date.now() } : w));
      const a = { w: window.innerWidth, h: window.innerHeight - TASKBAR };
      const n = ws.length;
      const g = startGeometry(store.get(app), def.size, a, n);
      // Small screens: open maximized.
      const maximized = a.w < 1100 || a.h < 640;
      return [...ws, { app, section, ...g, z, minimized: false, maximized, openedAt: Date.now() }];
    });
  }, []);

  const openPath = useCallback((path: string) => {
    const hit = appForPath(path);
    if (!hit) return false;
    open(hit.app.id, hit.section);
    return true;
  }, [open]);

  const close = useCallback((app: string) => setWindows((ws) => ws.filter((w) => w.app !== app)), []);
  const minimize = useCallback((app: string) => setWindows((ws) => ws.map((w) => (w.app === app ? { ...w, minimized: true } : w))), []);
  const toggleMaximize = useCallback((app: string) => setWindows((ws) => ws.map((w) => (w.app === app ? { ...w, maximized: !w.maximized } : w))), []);
  const setSection = useCallback((app: string, section: string) => setWindows((ws) => ws.map((w) => (w.app === app ? { ...w, section } : w))), []);
  const setGeometry = useCallback((app: string, g: Partial<Geometry>, persist = true) => {
    setWindows((ws) =>
      ws.map((w) => {
        if (w.app !== app) return w;
        const next = clamp({ x: g.x ?? w.x, y: g.y ?? w.y, w: g.w ?? w.w, h: g.h ?? w.h }, { w: window.innerWidth, h: window.innerHeight - TASKBAR });
        if (persist) store.set(app, next);
        return { ...w, ...next };
      }),
    );
  }, []);

  // Restore the windows that were open before a reload (per browser tab),
  // then open the page of the address (a bookmark, a link, a reload) on top.
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    try {
      const saved = JSON.parse(sessionStorage.getItem(SESSION_KEY) ?? "[]") as { app: string; section?: string; minimized?: boolean; maximized?: boolean }[];
      for (const w of saved) open(w.app, w.section);
      setWindows((ws) => ws.map((w) => {
        const s = saved.find((x) => x.app === w.app);
        return s ? { ...w, minimized: !!s.minimized, maximized: !!s.maximized } : w;
      }));
    } catch {
      /* nothing to restore */
    }
    const hit = location.pathname !== "/" ? appForPath(location.pathname.replace(/\/+$/, "")) : undefined;
    if (hit && allowed(hit.app)) open(hit.app.id, hit.section);
  }, [open, allowed]);
  useEffect(() => {
    try {
      const list = [...windows].sort((a, b) => a.z - b.z).map(({ app, section, minimized, maximized }) => ({ app, section, minimized, maximized }));
      sessionStorage.setItem(SESSION_KEY, JSON.stringify(list));
    } catch {
      /* storage unavailable */
    }
  }, [windows]);

  const focused = useMemo(() => {
    const visible = windows.filter((w) => !w.minimized);
    return visible.length ? visible.reduce((a, b) => (a.z > b.z ? a : b)).app : undefined;
  }, [windows]);

  // The address follows the focused window, so a reload or a bookmark opens
  // it again; the bare desktop is "/".
  const focusedWin = windows.find((w) => w.app === focused);
  const here = focusedWin ? pathOfApp(focusedWin.app, focusedWin.section) : "/";
  useEffect(() => {
    if (here && location.pathname !== here) navigate(here, { replace: true });
  }, [here, navigate]);

  const value = useMemo<WM>(() => ({ windows, focused, open, openPath, close, focus, minimize, toggleMaximize, setGeometry, setSection, area }), [windows, focused, open, openPath, close, focus, minimize, toggleMaximize, setGeometry, setSection, area]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
