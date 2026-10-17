import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { appById, appForPath } from "./apps";

export interface Geometry { x: number; y: number; w: number; h: number }

export interface Win extends Geometry {
  app: string;
  section?: string;
  z: number;
  minimized: boolean;
  maximized: boolean;
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
  setGeometry: (app: string, g: Partial<Geometry>) => void;
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
  get(app: string): Geometry | null {
    try {
      const v = localStorage.getItem(`strata-win-${app}`);
      return v ? (JSON.parse(v) as Geometry) : null;
    } catch {
      return null;
    }
  },
  set(app: string, g: Geometry) {
    try {
      localStorage.setItem(`strata-win-${app}`, JSON.stringify(g));
    } catch {
      /* storage unavailable */
    }
  },
};

function clamp(g: Geometry, a: { w: number; h: number }): Geometry {
  const w = Math.min(Math.max(g.w, 420), a.w);
  const h = Math.min(Math.max(g.h, 280), a.h);
  return { w, h, x: Math.min(Math.max(g.x, 0), Math.max(0, a.w - w)), y: Math.min(Math.max(g.y, 0), Math.max(0, a.h - h)) };
}

export function WindowManager({ children }: { children: ReactNode }) {
  const [windows, setWindows] = useState<Win[]>([]);
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
      if (existing) return ws.map((w) => (w.app === app ? { ...w, z, minimized: false, section: section ?? w.section } : w));
      const a = { w: window.innerWidth, h: window.innerHeight - TASKBAR };
      const n = ws.length;
      const saved = store.get(app);
      const g = clamp(saved ?? { w: def.size[0], h: def.size[1], x: Math.max(16, (a.w - def.size[0]) / 2 + n * 28 - 60), y: Math.max(12, (a.h - def.size[1]) / 2 + n * 24 - 40) }, a);
      // Small screens: open maximized.
      const maximized = a.w < 1100 || a.h < 640;
      return [...ws, { app, section, ...g, z, minimized: false, maximized }];
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
  const setGeometry = useCallback((app: string, g: Partial<Geometry>) => {
    setWindows((ws) =>
      ws.map((w) => {
        if (w.app !== app) return w;
        const next = clamp({ x: g.x ?? w.x, y: g.y ?? w.y, w: g.w ?? w.w, h: g.h ?? w.h }, { w: window.innerWidth, h: window.innerHeight - TASKBAR });
        store.set(app, next);
        return { ...w, ...next };
      }),
    );
  }, []);

  // Restore the windows that were open before a reload (per browser tab).
  useEffect(() => {
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
  }, [open]);
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

  const value = useMemo<WM>(() => ({ windows, focused, open, openPath, close, focus, minimize, toggleMaximize, setGeometry, setSection, area }), [windows, focused, open, openPath, close, focus, minimize, toggleMaximize, setGeometry, setSection, area]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
