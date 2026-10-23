import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Monitor } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { tr } from "../lib/i18n";
import { Badge, Button, Card, ErrorBox, Notice, Switch } from "./ui";

interface ConsoleInfo { enabled: boolean; recovery: boolean; graphical: boolean; installed: boolean; active: boolean }

/** Settings → Console screen: what the NAS shows on its own display (tty1). */
export function ApplianceConsoleCard() {
  const info = useQuery({ queryKey: ["console-settings"], queryFn: () => api.get<ConsoleInfo>("/api/system/console") });
  const [c, setC] = useState({ enabled: true, recovery: true });
  useEffect(() => { if (info.data) setC({ enabled: info.data.enabled, recovery: info.data.recovery }); }, [info.data]);
  const save = useAction(() => api.put("/api/system/console", c), { success: tr("Settings saved") });
  const d = info.data;
  return (
    <Card title={<span className="flex items-center gap-2"><Monitor size={16} /> {tr("Console screen")}</span>} actions={<>
      {d?.installed && (d.active ? <Badge tone="ok" dot>{tr("On")}</Badge> : <Badge>{tr("Off")}</Badge>)}
      <Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("StrataOS screen on the NAS display")}
          hint={tr("A screen or TV plugged into the NAS shows its web address with a QR code, its health and a few local actions (network reset, restart). The first console (Alt+F1) shows it; the others (Alt+F2 to Alt+F6) and serial consoles keep a normal login prompt.")} />
        <Switch checked={c.recovery} disabled={!c.enabled} onChange={(v) => setC({ ...c, recovery: v })} label={tr("Administrator recovery codes from the screen")}
          hint={tr("Someone at the NAS can show a one-time code, valid 15 minutes, to reset an administrator password from the sign-in page (\"Forgot password?\"). Each code is logged and notified to administrators.")} />
        {d?.graphical && <Notice tone="warn">{tr("A graphical login is installed on this machine: it may use the first console too.")}</Notice>}
        {d && !d.installed && <p className="text-[12.5px] text-muted">{tr("This machine has no local console.")}</p>}
        <ErrorBox error={info.error ?? save.error} />
      </div>
    </Card>
  );
}
