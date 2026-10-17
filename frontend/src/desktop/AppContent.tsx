import { Suspense } from "react";
import type { Me } from "../lib/types";
import { cx } from "../lib/format";
import { Spinner } from "../components/ui";
import type { AppDef } from "./apps";
import { useWM, type Win } from "./wm";

/** Body of a window: one page, or a suite with a side menu (DSM-style control panel). */
export function AppContent({ app, win, me }: { app: AppDef; win: Win; me: Me }) {
  const wm = useWM()!;
  if (app.component) {
    const C = app.component;
    return (
      <div className="h-full overflow-y-auto px-5 py-5">
        <C me={me} />
      </div>
    );
  }
  const sections = app.sections ?? [];
  const current = sections.find((s) => s.id === win.section) ?? sections[0];
  const C = current.component;
  return (
    <div className="flex h-full">
      <nav className="w-52 shrink-0 space-y-0.5 overflow-y-auto border-r border-line bg-panel p-2">
        {sections.map((s) => (
          <button
            key={s.id}
            onClick={() => wm.setSection(app.id, s.id)}
            className={cx("flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left text-[13px] transition", s.id === current.id ? "bg-accent-soft font-medium text-accent" : "text-muted hover:bg-panel-2 hover:text-fg")}
          >
            {s.icon}
            {s.label}
          </button>
        ))}
      </nav>
      <div className="min-w-0 flex-1 overflow-y-auto px-5 py-5">
        <Suspense fallback={<Spinner />}>
          <C key={current.id} me={me} />
        </Suspense>
      </div>
    </div>
  );
}
