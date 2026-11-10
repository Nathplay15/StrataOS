// Settings of the app on this computer (language, theme, start at login,
// file manager menu, updates of the app), the paired NAS and the sync
// settings of each.
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { disable as autoOff, enable as autoOn, isEnabled as autoIs } from "@tauri-apps/plugin-autostart";
import { check, type Update } from "@tauri-apps/plugin-updater";
import { relaunch } from "@tauri-apps/plugin-process";
import { openUrl } from "@tauri-apps/plugin-opener";
import { Download, LogOut, Plus, RefreshCw } from "lucide-react";
import { Badge, Button, Card, ErrorBox, Field, Input, Progress, Select, Switch, useUI } from "@nas/components/ui";
import { agent } from "../lib/agent";
import { useApp } from "../lib/state";
import { useNav } from "../lib/nav";
import { languages, t, tk, type Lang } from "../lib/i18n";
import { chosenLang, setChosenLang, setTheme, theme, type Theme } from "../lib/prefs";
import { PageHeader, Section } from "../components/kit";
import type { Account } from "../lib/types";

const RELEASES = "https://github.com/Nathplay15/StrataOS/releases/latest";

export function SettingsPage() {
  const { state } = useApp();
  const { addNas } = useNav();
  const accounts = (state?.accounts ?? []).filter((a) => a.paired);
  return (
    <>
      <PageHeader title={t("Settings")} />
      <AppSettings />
      <Section title={t("Paired NAS")} actions={<Button size="sm" icon={<Plus size={14} />} onClick={addNas}>{t("Add a NAS")}</Button>}>
        <div className="space-y-3">{accounts.map((a) => <AccountCard key={a.id} a={a} />)}</div>
      </Section>
      <AppUpdate />
      <p className="selectable mt-6 text-[12px] text-muted">{tk("about", state?.version ?? "", state?.dir ?? "")}</p>
    </>
  );
}

function AppSettings() {
  const { toast } = useUI();
  const [th, setTh] = useState<Theme>(theme());
  const [auto, setAuto] = useState<boolean | null>(null);
  const integ = useQuery({ queryKey: ["integration"], queryFn: () => invoke<{ supported: boolean; enabled: boolean; how: string }>("integration_status") });
  useEffect(() => { autoIs().then(setAuto).catch(() => setAuto(null)); }, []);
  return (
    <Card>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={tk("settings.language")}>
          <Select value={chosenLang()} onChange={async (e) => {
            const l = e.target.value as Lang | "";
            setChosenLang(l);
            // The sync engine's notifications follow the same language.
            await agent.post("/v1/settings", { language: l }).catch(() => {});
            location.reload();
          }}>
            <option value="">{tk("settings.langauto")}</option>
            {languages.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
          </Select>
        </Field>
        <Field label={t("Theme")}>
          <Select value={th} onChange={(e) => { const v = e.target.value as Theme; setTh(v); setTheme(v); }}>
            <option value="system">{t("Same as the system")}</option>
            <option value="light">{t("Light")}</option>
            <option value="dark">{t("Dark")}</option>
          </Select>
        </Field>
      </div>
      <div className="mt-4 space-y-3">
        <Switch checked={!!auto} disabled={auto === null} onChange={async (v) => {
          try {
            if (v) await autoOn();
            else await autoOff();
            setAuto(await autoIs());
          } catch (e) {
            toast(String(e), "error");
          }
        }} label={tk("settings.autostart")} hint={t("The app starts in the notification area and synchronizes in the background.")} />
        {integ.data?.supported && (
          <Switch checked={integ.data.enabled} onChange={async (v) => {
            try {
              await invoke("integration_set", { on: v, labels: { share: t("Copy a StrataOS share link"), open: t("Open on the NAS") } });
              void integ.refetch();
            } catch (e) {
              toast(String(e), "error");
            }
          }} label={t("File manager menu")} hint={integ.data.how === "explorer"
            ? t("\"Copy a StrataOS share link\" and \"Open on the NAS\" on files of synchronized folders (Windows 11: under \"Show more options\").")
            : t("\"Copy a StrataOS share link\" and \"Open on the NAS\" in GNOME Files (Scripts menu) and Dolphin, for files of synchronized folders.")} />
        )}
      </div>
    </Card>
  );
}

function AccountCard({ a }: { a: Account }) {
  const { state, refresh } = useApp();
  const { toast, confirm } = useUI();
  const c = a.config;
  const [device, setDevice] = useState(c.deviceName ?? "");
  const [up, setUp] = useState(c.upKBps ?? 0);
  const [down, setDown] = useState(c.downKBps ?? 0);
  const [ignore, setIgnore] = useState((c.ignore ?? state?.defaultIgnore ?? []).join("\n"));
  const [notify, setNotify] = useState(!c.noNotify);
  const [busy, setBusy] = useState(false);
  const base = `/v1/accounts/${encodeURIComponent(a.id)}`;
  const saveAll = async () => {
    setBusy(true);
    try {
      await agent.post(`${base}/settings`, { deviceName: device, upKBps: up, downKBps: down, ignore: ignore.split("\n").map((x) => x.trim()).filter(Boolean), notify });
      toast(t("Saved"));
      refresh();
    } catch (e) {
      toast(String((e as Error).message ?? e), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="font-semibold">{c.nasName}</span>
        <Badge>{c.user}</Badge>
        <span className="mono text-[12px] text-muted">{c.server}</span>
        <Button size="sm" variant="ghost" className="ml-auto text-danger" icon={<LogOut size={14} />} onClick={async () => {
          if (!(await confirm({ title: tk("account.signout"), text: tk("account.signoutconfirm"), danger: true }))) return;
          try {
            await agent.post(`${base}/signout`, {});
            refresh();
          } catch (e) {
            toast(String((e as Error).message ?? e), "error");
          }
        }}>{tk("account.signout")}</Button>
      </div>
      <div className="mb-3 text-[12px] text-muted">{tk("account.fingerprint")}: <span className="mono selectable">{c.fingerprint || tk("account.cafp")}</span></div>
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label={tk("settings.device")}><Input value={device} onChange={(e) => setDevice(e.target.value)} /></Field>
        <Field label={tk("settings.up")} hint={tk("settings.limithint")}><Input type="number" min={0} value={up} onChange={(e) => setUp(Math.max(0, +e.target.value))} /></Field>
        <Field label={tk("settings.down")} hint={tk("settings.limithint")}><Input type="number" min={0} value={down} onChange={(e) => setDown(Math.max(0, +e.target.value))} /></Field>
      </div>
      <Field className="mt-3" label={tk("settings.ignore")} hint={tk("settings.ignorehint")}><textarea className="input mono h-24" spellCheck={false} value={ignore} onChange={(e) => setIgnore(e.target.value)} /></Field>
      <div className="mt-3"><Switch checked={notify} onChange={setNotify} label={tk("settings.notify")} /></div>
      <div className="mt-3 flex justify-end"><Button variant="primary" loading={busy} onClick={() => void saveAll()}>{tk("action.save")}</Button></div>
    </Card>
  );
}

/** Updates of the app itself: Tauri's updater reads latest.json of the
 *  latest GitHub release and checks each package's minisign signature. */
function AppUpdate() {
  const [upd, setUpd] = useState<Update | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [error, setError] = useState<unknown>(null);
  const look = async () => {
    setBusy(true);
    setError(null);
    try {
      setUpd(await check());
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const install = async () => {
    if (!upd) return;
    setBusy(true);
    setError(null);
    let total = 0;
    let got = 0;
    try {
      await upd.downloadAndInstall((ev) => {
        if (ev.event === "Started") total = ev.data.contentLength ?? 0;
        if (ev.event === "Progress") {
          got += ev.data.chunkLength;
          setProgress(total ? (got / total) * 100 : 0);
        }
      });
      await relaunch();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Section title={t("Updates of the app")}>
      <Card>
        <div className="flex flex-wrap items-center gap-3">
          <div className="flex-1 text-[13px]">
            {upd === undefined ? t("Updates are signed and checked before they are installed.")
              : upd === null ? t("The app is up to date.")
                : t("Version {0} is available.", upd.version)}
          </div>
          {upd ? <Button variant="primary" loading={busy} icon={<Download size={15} />} onClick={() => void install()}>{t("Install and restart")}</Button>
            : <Button loading={busy} icon={<RefreshCw size={15} />} onClick={() => void look()}>{t("Check for updates")}</Button>}
        </div>
        {progress !== null && <Progress className="mt-3" value={progress} />}
        {error ? (
          <div className="mt-3 space-y-2">
            <ErrorBox error={error} />
            <Button size="sm" onClick={() => void openUrl(RELEASES)}>{t("Download from the releases page")}</Button>
          </div>
        ) : null}
      </Card>
    </Section>
  );
}
