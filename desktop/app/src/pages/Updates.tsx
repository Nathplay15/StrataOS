// Updates of the NAS (StrataOS, then the Debian packages, channel) and of
// its fleet: the other NAS it manages, their versions and health, and
// updates started on several of them (rolling, like the Android app).
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowUpCircle, RefreshCw, Server } from "lucide-react";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Progress, Spinner, Switch, Tabs, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { dateTime } from "@nas/lib/format";
import { api } from "../lib/nasapi";
import { srv, t } from "../lib/i18n";
import { PageHeader, Section, WebButton } from "../components/kit";

type StrataVersion = { running: string; installed: string; candidate: string; repository: boolean };
type Up = { package: string; current: string; new: string; source: string };
type Auto = { channel: "stable" | "beta"; beta: boolean; installed: string };
type FleetNas = {
  id: string; name: string; host: string; online: boolean; error: string;
  summary?: { version: string; alerts: { level: string; message: string }[]; updates: { candidate: string; installed: string; system: number; rebootRequired: boolean; checked: string } };
};
type FleetRun = { id: string; kind: string; status: string; started: string; totals: { success: number; failed: number; progress: number }; items: { name: string; state: string; error: string }[] };

export function UpdatesPage() {
  const [tab, setTab] = useState<"nas" | "fleet">("nas");
  return (
    <>
      <PageHeader title={t("Updates")} actions={<WebButton path={tab === "nas" ? "/system/updates" : "/fleet"} title={t("Updates")} />} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "nas", label: t("This NAS") }, { id: "fleet", label: t("Fleet") }]} />
      <div className="mt-4">{tab === "nas" ? <NasUpdates /> : <Fleet />}</div>
    </>
  );
}

function NasUpdates() {
  const { confirm } = useUI();
  const v = useQuery({ queryKey: ["strata-version"], queryFn: () => api.get<StrataVersion>("/api/system/updates/strata"), refetchInterval: 15_000 });
  const ups = useQuery({ queryKey: ["system-updates"], queryFn: () => api.get<Up[]>("/api/system/updates") });
  const auto = useQuery({ queryKey: ["auto-updates"], queryFn: () => api.get<Auto>("/api/system/updates/auto") });
  const strata = useAction(() => api.post("/api/system/updates/strata"), { success: t("Update started: the NAS restarts its service, the app reconnects by itself.") });
  const refresh = useAction(() => api.post("/api/system/updates/refresh"));
  const upgrade = useAction(() => api.post("/api/system/updates/upgrade"));
  const channel = useAction((c: string) => api.put("/api/system/updates/channel", { channel: c }), { success: t("Update channel changed") });
  const newer = v.data && v.data.candidate && v.data.candidate !== v.data.installed;
  return (
    <>
      <ErrorBox error={v.error || ups.error} />
      <Card>
        {v.isLoading ? <Spinner /> : v.data && (
          <div className="flex flex-wrap items-center gap-3">
            <ArrowUpCircle className={newer ? "text-accent" : "text-ok"} />
            <div className="flex-1">
              <div className="font-semibold">StrataOS {v.data.running}</div>
              <div className="text-[12.5px] text-muted">{newer ? t("Version {0} is available.", v.data.candidate) : t("StrataOS is up to date.")}</div>
            </div>
            {newer && <Button variant="primary" loading={strata.isPending} onClick={async () => {
              if (await confirm({ title: t("Update StrataOS?"), text: t("The NAS installs StrataOS {0}; its interface restarts. Synchronization resumes by itself.", v.data!.candidate) })) strata.mutate();
            }}>{t("Update")}</Button>}
          </div>
        )}
        {auto.data && (
          <div className="mt-4 border-t border-line pt-3">
            <Switch checked={auto.data.channel === "beta"} onChange={async (on) => {
              if (on && !(await confirm({ title: t("Switch to the beta channel?"), text: t("Beta versions bring new features before everyone else, but they may have bugs, and an update may need a manual fix. Keep backups of your data, and don't use the beta channel on a NAS you can't afford to lose for a while."), confirm: t("Use beta versions"), danger: true }))) return;
              channel.mutate(on ? "beta" : "stable");
            }} label={t("Beta versions")} hint={t("Automatic updates and their schedule: in the web interface.")} />
          </div>
        )}
      </Card>
      <Section title={t("System packages")} actions={<>
        <Button size="sm" icon={<RefreshCw size={14} />} loading={refresh.isPending} onClick={() => refresh.mutate()}>{t("Check now")}</Button>
        {!!ups.data?.length && <Button size="sm" variant="primary" loading={upgrade.isPending} onClick={() => upgrade.mutate()}>{t("Install all")}</Button>}
      </>}>
        <Card pad={false}>
          {ups.isLoading ? <Spinner /> : !ups.data?.length ? <Empty title={t("The system is up to date")} /> : (
            <table className="table">
              <thead><tr><th>{t("Package")}</th><th>{t("Installed")}</th><th>{t("New")}</th></tr></thead>
              <tbody>{ups.data.map((u) => <tr key={u.package}><td className="mono text-[12.5px]">{u.package}</td><td className="text-[12.5px] text-muted">{u.current}</td><td className="text-[12.5px]">{u.new}</td></tr>)}</tbody>
            </table>
          )}
        </Card>
      </Section>
    </>
  );
}

function Fleet() {
  const { confirm } = useUI();
  const list = useQuery({ queryKey: ["fleet"], queryFn: () => api.get<FleetNas[]>("/api/fleet"), refetchInterval: 30_000 });
  const runs = useQuery({ queryKey: ["fleet-runs"], queryFn: () => api.get<FleetRun[]>("/api/fleet/updates"), refetchInterval: (q) => ((q.state.data ?? []).some((r) => r.status === "running") ? 3000 : 30_000) });
  const [sel, setSel] = useState<string[]>([]);
  const check = useAction((ids: string[]) => api.post("/api/fleet/check", { ids }));
  const start = useAction((kind: string) => api.post("/api/fleet/updates", { ids: sel, kind, strategy: "rolling", reboot: false, dryRun: false }), { onDone: () => setSel([]) });
  const members = list.data ?? [];
  return (
    <>
      <div className="mb-3 flex flex-wrap justify-end gap-2">
        <Button size="sm" icon={<RefreshCw size={14} />} loading={check.isPending} onClick={() => check.mutate(members.map((m) => m.id))}>{t("Check now")}</Button>
        <Button size="sm" variant="primary" disabled={!sel.length} loading={start.isPending} onClick={async () => {
          if (await confirm({ title: t("Update {0} NAS?", sel.length), text: t("StrataOS is updated on the selected NAS one after the other; a NAS that fails stops the run.") })) start.mutate("strata");
        }}>{t("Update StrataOS")}</Button>
        <Button size="sm" disabled={!sel.length} onClick={() => start.mutate("system")}>{t("Update the system packages")}</Button>
      </div>
      <ErrorBox error={list.error || runs.error} />
      <Card pad={false}>
        {list.isLoading ? <Spinner /> : members.length <= 1 ? <Empty icon={<Server />} title={t("No other NAS in the fleet")} action={<WebButton path="/fleet" label={t("Add a NAS")} />} /> : (
          <table className="table">
            <thead><tr><th className="w-8" /><th>NAS</th><th>{t("Version")}</th><th>{t("Updates")}</th><th>{t("Alerts")}</th></tr></thead>
            <tbody>{members.map((m) => {
              const u = m.summary?.updates;
              const errs = (m.summary?.alerts ?? []).filter((a) => a.level === "error" || a.level === "danger").length;
              return (
                <tr key={m.id}>
                  <td><Checkbox checked={sel.includes(m.id)} onChange={(v) => setSel(v ? [...sel, m.id] : sel.filter((x) => x !== m.id))} /></td>
                  <td><div className="flex items-center gap-2 font-medium">{m.name}{!m.online && <Badge tone="danger">{t("Offline")}</Badge>}</div><div className="text-[11.5px] text-muted">{m.host}{m.error ? ` · ${srv(m.error)}` : ""}</div></td>
                  <td className="text-[12.5px]">{m.summary?.version ?? "—"}</td>
                  <td className="text-[12.5px]">
                    {u?.candidate && u.candidate !== u.installed ? <Badge tone="accent">{u.candidate}</Badge> : null}
                    {u?.system ? <Badge>{t("{0} packages", u.system)}</Badge> : null}
                    {u?.rebootRequired && <Badge tone="warn">{t("Restart needed")}</Badge>}
                    {u?.checked && <div className="text-[11px] text-muted">{dateTime(u.checked)}</div>}
                  </td>
                  <td>{errs ? <Badge tone="danger">{errs}</Badge> : <Badge tone="ok">0</Badge>}</td>
                </tr>
              );
            })}</tbody>
          </table>
        )}
      </Card>
      {!!runs.data?.length && (
        <Section title={t("Recent update runs")}>
          <div className="space-y-2">{runs.data.slice(0, 5).map((r) => (
            <Card key={r.id}>
              <div className="flex flex-wrap items-center gap-2 text-[13px]">
                <span className="font-medium">{r.kind === "strata" ? "StrataOS" : t("System packages")}</span>
                <Badge tone={r.status === "running" ? "accent" : r.totals.failed ? "danger" : "ok"}>{r.status}</Badge>
                <span className="text-muted">{dateTime(r.started)} · {t("{0} succeeded, {1} failed", r.totals.success, r.totals.failed)}</span>
              </div>
              {r.status === "running" && <Progress className="mt-2" value={r.totals.progress} />}
              {r.items.filter((i) => i.error).map((i) => <div key={i.name} className="mt-1 text-[12px] text-danger">{i.name}: {srv(i.error)}</div>)}
            </Card>
          ))}</div>
        </Section>
      )}
    </>
  );
}
