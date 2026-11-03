import { useEffect, useRef, useState } from "react";
import { tr } from "../lib/i18n";
import { cx } from "../lib/format";
import { AppIcon, appById } from "./apps";
import { useWM } from "./wm";

// Ctrl+` (the key under Escape, whatever the layout) switches between the
// windows like Alt+Tab, which the system keeps for itself: hold Ctrl, press
// ` again to go further (Shift: backwards), release Ctrl to bring the chosen
// window to the front. Escape cancels.

export function WindowSwitcher() {
  const wm = useWM()!;
  const [sel, setSel] = useState<{ list: string[]; i: number } | null>(null);
  const cur = useRef(sel);
  cur.current = sel;
  const windows = useRef(wm.windows);
  windows.current = wm.windows;
  const focus = wm.focus;

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.ctrlKey && !e.altKey && !e.metaKey && e.code === "Backquote") {
        e.preventDefault();
        const order = [...windows.current].sort((a, b) => b.z - a.z).map((w) => w.app);
        if (!order.length) return;
        const s = cur.current;
        const list = s?.list ?? order;
        const i = ((s ? s.i : 0) + (e.shiftKey ? -1 : 1) + list.length) % list.length;
        setSel({ list, i });
      } else if (e.key === "Escape" && cur.current) {
        e.preventDefault();
        e.stopPropagation();
        setSel(null);
      }
    };
    const up = (e: KeyboardEvent) => {
      const s = cur.current;
      if (e.key === "Control" && s) {
        setSel(null);
        focus(s.list[s.i]);
      }
    };
    const cancel = () => setSel(null);
    window.addEventListener("keydown", down, true);
    window.addEventListener("keyup", up, true);
    window.addEventListener("blur", cancel);
    return () => {
      window.removeEventListener("keydown", down, true);
      window.removeEventListener("keyup", up, true);
      window.removeEventListener("blur", cancel);
    };
  }, [focus]);

  if (!sel) return null;
  const chosen = appById(sel.list[sel.i]);
  return (
    <div className="pointer-events-none fixed inset-0 z-[10001] flex items-center justify-center">
      <div role="dialog" aria-label={tr("Switch windows")} className="card flex max-w-[90vw] flex-wrap justify-center gap-2 p-3 text-fg">
        {sel.list.map((id, i) => {
          const a = appById(id);
          return a ? (
            <div key={id} className={cx("flex w-28 flex-col items-center gap-2 rounded-xl px-2 py-3 text-center", i === sel.i ? "bg-accent-soft ring-2 ring-accent" : "opacity-70")}>
              <AppIcon app={a} size={44} />
              <span className="line-clamp-2 text-[12px] leading-tight">{a.title}</span>
            </div>
          ) : null;
        })}
      </div>
      <div className="sr-only" aria-live="assertive">{chosen?.title}</div>
    </div>
  );
}
