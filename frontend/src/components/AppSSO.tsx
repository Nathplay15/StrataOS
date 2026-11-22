import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { KeySquare } from "lucide-react";
import { api } from "../lib/api";
import { useJobs } from "../lib/jobs";
import { srv, tr } from "../lib/i18n";
import { AppLink } from "./AppLink";
import { Button, Modal, Notice, Switch, useUI } from "./ui";
import { ManualSettingsModal, useSSO, type ManualSettings, type SSOAppView } from "../pages/system/SingleSignOn";

/** "Sign in with StrataOS" of an installed app (App Center card). */
export function AppSSOButton({ stack }: { stack: string }) {
  const sso = useSSO();
  const [open, setOpen] = useState(false);
  const app = sso.data?.apps.find((a) => a.stack === stack);
  if (!app) return null;
  return (
    <>
      <Button size="sm" variant="ghost" icon={<KeySquare size={14} className={app.client ? "text-ok" : undefined} />} title={tr("Sign in with StrataOS")} onClick={() => setOpen(true)} />
      {open && <AppSSOModal app={app} enabled={!!sso.data?.enabled} onClose={() => setOpen(false)} />}
    </>
  );
}

function AppSSOModal({ app, enabled, onClose }: { app: SSOAppView; enabled: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const { track } = useJobs();
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState<{ client: string; settings?: ManualSettings; secret?: string } | null>(null);
  const toggle = async (on: boolean) => {
    setBusy(true);
    try {
      const r = await api.put<{ job?: unknown; settings?: ManualSettings; secret?: string; client?: string } | null>(`/api/sso/apps/${app.stack}`, { enabled: on });
      if (r?.job) track(r.job);
      if (r?.settings && r.client) setShown({ client: r.client, settings: r.settings, secret: r.secret });
    } catch (e) {
      toast(srv((e as Error).message), "error");
    } finally {
      setBusy(false);
      qc.invalidateQueries({ queryKey: ["sso"] });
    }
  };
  if (shown) return <ManualSettingsModal client={shown.client} settings={shown.settings} secret={shown.secret} app={app.app} onClose={onClose} />;
  return (
    <Modal open onClose={onClose} title={tr("Sign in with StrataOS")} footer={<Button onClick={onClose}>{tr("Close")}</Button>}>
      <div className="space-y-3 text-[13.5px]">
        <p>{app.support.auto
          ? tr("{0} will offer \"Sign in with StrataOS\": StrataOS registers it and writes its settings, then restarts it.", app.stack)
          : tr("StrataOS registers {0}; you then type the settings shown into the app.", app.stack)}</p>
        {!enabled && <Notice tone="warn">{tr("Single sign-on is off.")} <AppLink to="/system/sso" className="text-accent">{tr("Turn it on")}</AppLink></Notice>}
        <Switch checked={!!app.client} disabled={!enabled || busy} onChange={toggle} label={tr("Sign in with StrataOS")} />
        {app.client && !app.support.auto && <Button size="sm" onClick={() => setShown({ client: app.client! })}>{tr("Settings for the app")}</Button>}
        <p className="text-[12.5px] text-muted">{tr("Who may use it, and the other options, are on the Single sign-on page.")}</p>
      </div>
    </Modal>
  );
}
