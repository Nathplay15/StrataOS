import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, Monitor, Tv, Undo2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { tr } from "../lib/i18n";
import { Badge, Button, Card, ErrorBox, Field, IconButton, Modal, Notice, Select, Switch } from "./ui";

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

interface HdmiInfo { enabled: boolean; display: { connector: string; mode: string } | null; installed: boolean; active: boolean; browser: boolean; account: string; remote: string[] }

/** The arrows of a TV remote, sent to the menu shown on the HDMI output. */
export function TvRemoteModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const sendKey = useAction((key: string) => api.post("/api/hdmi/remote/key", { key }));
  return (
    <Modal open={open} onClose={onClose} title={tr("Remote control")}>
      <p className="text-[12.5px] text-muted">{tr("Send a key to the menu shown on the TV.")}</p>
      <div className="mx-auto mt-4 grid w-44 grid-cols-3 gap-2">
        <span />
        <IconButton title={tr("Up")} onClick={() => sendKey.mutate("up")}><ChevronUp size={18} /></IconButton>
        <span />
        <IconButton title={tr("Left")} onClick={() => sendKey.mutate("left")}><ChevronLeft size={18} /></IconButton>
        <IconButton title={tr("OK")} onClick={() => sendKey.mutate("ok")}><Check size={18} /></IconButton>
        <IconButton title={tr("Right")} onClick={() => sendKey.mutate("right")}><ChevronRight size={18} /></IconButton>
        <IconButton title={tr("Back")} onClick={() => sendKey.mutate("back")}><Undo2 size={18} /></IconButton>
        <IconButton title={tr("Down")} onClick={() => sendKey.mutate("down")}><ChevronDown size={18} /></IconButton>
        <span />
      </div>
      <ErrorBox error={sendKey.error} />
    </Modal>
  );
}

/** Settings → Console screen: the cinema menu on a TV plugged to the HDMI output. */
export function HDMIOutputCard() {
  const info = useQuery({ queryKey: ["hdmi-settings"], queryFn: () => api.get<HdmiInfo>("/api/system/hdmi") });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<{ username: string }[]>("/api/users"), staleTime: 60000 });
  const [on, setOn] = useState(true);
  const [account, setAccount] = useState("");
  const [allowed, setAllowed] = useState<string[]>([]);
  const [remote, setRemote] = useState(false);
  useEffect(() => { if (info.data) { setOn(info.data.enabled); setAccount(info.data.account ?? ""); setAllowed(info.data.remote ?? []); } }, [info.data]);
  const save = useAction(() => api.put("/api/system/hdmi", { enabled: on, account, remote: allowed }), { success: tr("Settings saved") });
  const d = info.data;
  return (
    <Card title={<span className="flex items-center gap-2"><Tv size={16} /> {tr("HDMI output (TV)")}</span>} actions={<>
      {d?.installed && (d.active ? <Badge tone="ok" dot>{tr("On")}</Badge> : <Badge>{tr("Off")}</Badge>)}
      {d?.active && <Button size="sm" variant="ghost" onClick={() => setRemote(true)}>{tr("Remote control")}</Button>}
      <Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <Switch checked={on} onChange={setOn} label={tr("Show the cinema menu on a TV plugged to the HDMI output")}
          hint={tr("While a screen is connected to the HDMI output, the NAS runs a full-screen menu on it (films, series, photos), driven with a keyboard, a gamepad or the arrows of a TV remote.")} />
        {d?.display
          ? <p className="text-[12.5px] text-muted">{tr("TV detected: {0} ({1})", d.display.connector, d.display.mode)}</p>
          : <p className="text-[12.5px] text-muted">{tr("No TV detected")}</p>}
        {d && !d.browser && <Notice tone="warn">{tr("A kiosk browser (chromium or firefox) is required for the HDMI menu.")}</Notice>}
        <Field label={tr("Library shown on the TV")} hint={tr("The TV shows, read-only, the films and series this account can read, with its age limit. Nothing is shown until an account is chosen.")}>
          <Select value={account} onChange={(e) => setAccount(e.target.value)} className="w-56">
            <option value="">{tr("None")}</option>
            {(users.data ?? []).map((u) => <option key={u.username} value={u.username}>{u.username}</option>)}
          </Select>
        </Field>
        <div>
          <div className="text-[12.5px] font-medium">{tr("Accounts that may use the TV remote")}</div>
          <p className="text-[12px] text-muted">{tr("Administrators always can. The remote appears in their cinema page.")}</p>
          <div className="mt-1.5 flex flex-wrap gap-3">
            {(users.data ?? []).map((u) => (
              <label key={u.username} className="flex items-center gap-1.5 text-[12.5px]">
                <input type="checkbox" checked={allowed.includes(u.username)} onChange={(e) => setAllowed(e.target.checked ? [...allowed, u.username] : allowed.filter((x) => x !== u.username))} />
                {u.username}
              </label>
            ))}
          </div>
        </div>
        <ErrorBox error={info.error ?? save.error} />
      </div>
      <TvRemoteModal open={remote} onClose={() => setRemote(false)} />
    </Card>
  );
}
