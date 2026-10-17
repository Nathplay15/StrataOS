import { Suspense, useEffect, useRef, useState, type PointerEvent as RPointerEvent, type ReactNode } from "react";
import { Maximize2, Minimize2, Minus, X } from "lucide-react";
import { cx } from "../lib/format";
import { Spinner } from "../components/ui";
import { AppIcon, type AppDef } from "./apps";
import { InWindow, TASKBAR, useWM, type Geometry, type Win } from "./wm";

type Edge = "n" | "s" | "e" | "w" | "ne" | "nw" | "se" | "sw";
const edges: { e: Edge; cls: string; cursor: string }[] = [
  { e: "n", cls: "top-0 left-2 right-2 h-1.5", cursor: "ns-resize" },
  { e: "s", cls: "bottom-0 left-2 right-2 h-1.5", cursor: "ns-resize" },
  { e: "e", cls: "right-0 top-2 bottom-2 w-1.5", cursor: "ew-resize" },
  { e: "w", cls: "left-0 top-2 bottom-2 w-1.5", cursor: "ew-resize" },
  { e: "ne", cls: "right-0 top-0 h-3 w-3", cursor: "nesw-resize" },
  { e: "sw", cls: "left-0 bottom-0 h-3 w-3", cursor: "nesw-resize" },
  { e: "nw", cls: "left-0 top-0 h-3 w-3", cursor: "nwse-resize" },
  { e: "se", cls: "right-0 bottom-0 h-3 w-3", cursor: "nwse-resize" },
];

/** A desktop window: drag by the title bar, resize from any edge, double-click to maximize. */
export function Window({ win, app, children }: { win: Win; app: AppDef; children: ReactNode }) {
  const wm = useWM()!;
  const [live, setLive] = useState<Geometry | null>(null); // geometry while dragging/resizing
  const drag = useRef<{ mode: "move" | Edge; sx: number; sy: number; g: Geometry; px: number; py: number } | null>(null);
  const [snap, setSnap] = useState<"max" | "left" | "right" | null>(null);
  const focused = wm.focused === win.app;
  const g = live ?? win;

  // Keep windows reachable when the browser window shrinks.
  useEffect(() => {
    const h = () => wm.setGeometry(win.app, {});
    window.addEventListener("resize", h);
    return () => window.removeEventListener("resize", h);
  }, [wm, win.app]);

  const start = (mode: "move" | Edge) => (e: RPointerEvent) => {
    if (e.button !== 0 || win.maximized) return;
    e.preventDefault();
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
    drag.current = { mode, sx: e.clientX, sy: e.clientY, px: e.clientX, py: e.clientY, g: { x: win.x, y: win.y, w: win.w, h: win.h } };
    wm.focus(win.app);
  };
  const move = (e: RPointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.sx, dy = e.clientY - d.sy;
    d.px = e.clientX;
    d.py = e.clientY;
    if (d.mode === "move") {
      // Aero-snap preview: top edge maximizes, side edges tile half the screen.
      setSnap(e.clientY <= TASKBAR + 2 ? "max" : e.clientX <= 2 ? "left" : e.clientX >= window.innerWidth - 3 ? "right" : null);
    }
    const n = { ...d.g };
    if (d.mode === "move") {
      n.x = d.g.x + dx;
      n.y = Math.max(0, d.g.y + dy);
    } else {
      if (d.mode.includes("e")) n.w = Math.max(420, d.g.w + dx);
      if (d.mode.includes("s")) n.h = Math.max(280, d.g.h + dy);
      if (d.mode.includes("w")) { n.w = Math.max(420, d.g.w - dx); n.x = d.g.x + d.g.w - n.w; }
      if (d.mode.includes("n")) { n.h = Math.max(280, d.g.h - dy); n.y = Math.max(0, d.g.y + d.g.h - n.h); }
    }
    setLive(n);
  };
  const end = () => {
    const a = wm.area();
    if (drag.current && snap === "max") wm.toggleMaximize(win.app);
    else if (drag.current && snap) wm.setGeometry(win.app, { x: snap === "left" ? 0 : Math.floor(a.w / 2), y: 0, w: Math.ceil(a.w / 2), h: a.h });
    else if (drag.current && live) wm.setGeometry(win.app, live);
    drag.current = null;
    setLive(null);
    setSnap(null);
  };

  const style = win.maximized ? { left: 0, top: 0, width: "100%", height: "100%", zIndex: win.z } : { left: g.x, top: g.y, width: g.w, height: g.h, zIndex: win.z };

  return (
    <>
    {snap && (
      <div
        className="pointer-events-none absolute rounded-xl border-2 border-accent/70 bg-accent/15 backdrop-blur-sm transition-all"
        style={{ zIndex: win.z, top: 4, bottom: 4, left: snap === "right" ? "50%" : 4, right: snap === "left" ? "50%" : 4 }}
      />
    )}
    <div
      className={cx(
        "absolute flex flex-col overflow-hidden border bg-bg transition-[box-shadow,opacity,transform] duration-150",
        win.maximized ? "rounded-none border-transparent" : "rounded-xl",
        focused ? "border-white/15 shadow-[0_24px_70px_-12px_rgba(0,0,0,0.6)]" : "border-white/10 shadow-[0_12px_40px_-16px_rgba(0,0,0,0.5)]",
        win.minimized && "pointer-events-none scale-95 opacity-0",
      )}
      style={style}
      onPointerDown={() => !focused && wm.focus(win.app)}
      onPointerMove={move}
      onPointerUp={end}
    >
      <header
        className={cx("flex h-11 shrink-0 select-none items-center gap-2.5 border-b border-line px-3", focused ? "bg-panel" : "bg-panel-2")}
        onPointerDown={start("move")}
        onDoubleClick={() => wm.toggleMaximize(win.app)}
      >
        <AppIcon app={app} size={22} />
        <span className={cx("flex-1 truncate text-[13px] font-medium", !focused && "text-muted")}>{app.title}</span>
        <div className="flex items-center gap-0.5" onPointerDown={(e) => e.stopPropagation()}>
          <WinBtn title="Réduire" onClick={() => wm.minimize(win.app)}><Minus size={14} /></WinBtn>
          <WinBtn title={win.maximized ? "Restaurer" : "Agrandir"} onClick={() => wm.toggleMaximize(win.app)}>{win.maximized ? <Minimize2 size={13} /> : <Maximize2 size={13} />}</WinBtn>
          <WinBtn title="Fermer" danger onClick={() => wm.close(win.app)}><X size={15} /></WinBtn>
        </div>
      </header>
      <div className="relative min-h-0 flex-1">
        <InWindow.Provider value={true}>
          <Suspense fallback={<Spinner />}>{children}</Suspense>
        </InWindow.Provider>
        {/* While dragging, an overlay keeps iframes/terminals from swallowing pointer events. */}
        {live && <div className="absolute inset-0" />}
      </div>
      {!win.maximized && edges.map((x) => <div key={x.e} className={cx("absolute z-10", x.cls)} style={{ cursor: x.cursor }} onPointerDown={start(x.e)} />)}
    </div>
    </>
  );
}

function WinBtn({ title, onClick, danger, children }: { title: string; onClick: () => void; danger?: boolean; children: ReactNode }) {
  return (
    <button title={title} aria-label={title} onClick={onClick} className={cx("flex h-7 w-7 items-center justify-center rounded-md text-muted transition hover:text-white", danger ? "hover:bg-danger" : "hover:bg-white/10 hover:text-fg")}>
      {children}
    </button>
  );
}
