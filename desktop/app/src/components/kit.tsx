// Building blocks of the app's pages, on top of the web interface's
// components (@nas/components/ui).
import { useState, type ReactNode } from "react";
import { ExternalLink } from "lucide-react";
import { save } from "@tauri-apps/plugin-dialog";
import { Button, PageHeader } from "@nas/components/ui";
import { agent } from "../lib/agent";
import { currentAccount } from "../lib/nasapi";
import { openWeb } from "../lib/shell";
import { useApp } from "../lib/state";
import { t } from "../lib/i18n";
import { useUI } from "@nas/components/ui";

export { PageHeader };

/** Opens a page of the NAS web interface in a window of the app. */
export function WebButton({ path, label, title, variant = "secondary", size = "md" }: { path: string; label?: string; title?: string; variant?: "secondary" | "ghost" | "primary"; size?: "sm" | "md" }) {
  const { account } = useApp();
  const { toast } = useUI();
  const [busy, setBusy] = useState(false);
  return (
    <Button variant={variant} size={size} loading={busy} icon={<ExternalLink size={14} />} title={title}
      onClick={async () => {
        setBusy(true);
        try {
          await openWeb(currentAccount(), path, `${account?.config.nasName ?? "StrataOS"} · ${title ?? label ?? t("Web interface")}`);
        } catch (e) {
          toast(String((e as Error).message ?? e), "error");
        } finally {
          setBusy(false);
        }
      }}>
      {label ?? t("Web interface")}
    </Button>
  );
}

/** Asks where to save a NAS file (a folder: as a ZIP) and downloads it in the background. */
export async function downloadToComputer(path: string, name: string, dir: boolean, size?: number) {
  const dest = await save({ defaultPath: dir ? `${name}.zip` : name, title: t("Download") });
  if (!dest) return false;
  await agent.post(`/v1/accounts/${encodeURIComponent(currentAccount())}/download`, { path, dest, size: size ?? 0 });
  return true;
}

/** A section title inside a page. */
export function Section({ title, actions, children }: { title: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="mt-6">
      <div className="mb-2.5 flex items-center justify-between gap-3">
        <h2 className="text-[14px] font-semibold">{title}</h2>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}

/** A thin usage bar. */
export function UsageBar({ used, total }: { used: number; total: number }) {
  const p = total ? Math.min(100, (used / total) * 100) : 0;
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-panel-2">
      <div className={`h-full rounded-full ${p > 90 ? "bg-danger" : p > 75 ? "bg-warn" : "bg-accent"}`} style={{ width: `${p}%` }} />
    </div>
  );
}
