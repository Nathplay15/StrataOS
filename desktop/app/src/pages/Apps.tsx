// Applications of the NAS: the installed apps (Compose stacks), the App
// Center catalog, containers, and the one-click container updates like the
// Android app. Custom stacks, images and logs stay in the web interface.
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowUpCircle, Box, Download, Play, RotateCw, Search, Square, Trash2 } from "lucide-react";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Spinner, Tabs, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { dateTime } from "@nas/lib/format";
import type { App, AppStoreData, Container, Job, Stack } from "@nas/lib/types";
import { api } from "../lib/nasapi";
import { t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

type Tab = "installed" | "catalog" | "containers" | "updates";
type Updates = { checked: string; running: boolean; available: number; containers: { id: string; name: string; image: string; stack: string; status: string; error: string }[]; stacks: Record<string, boolean> };
type Plan = { stack: string; name: string; containers: number; volumes?: string[]; folders?: string[]; images?: string[] };

export function AppsPage() {
  const [tab, setTab] = useState<Tab>("installed");
  const updates = useQuery({ queryKey: ["docker-updates"], queryFn: () => api.get<Updates>("/api/docker/updates"), refetchInterval: (q) => (q.state.data?.running ? 2000 : 60_000) });
  return (
    <>
      <PageHeader title={t("Applications")} actions={<WebButton path={tab === "containers" ? "/docker/containers" : "/apps"} title={t("Applications")} />} />
      <Tabs value={tab} onChange={setTab} tabs={[
        { id: "installed", label: t("My apps") }, { id: "catalog", label: t("App Center") }, { id: "containers", label: t("Containers") },
        { id: "updates", label: updates.data?.available ? `${t("Updates")} (${updates.data.available})` : t("Updates") },
      ]} />
      <div className="mt-4">
        {tab === "installed" ? <Installed /> : tab === "catalog" ? <Catalog /> : tab === "containers" ? <Containers /> : <UpdatesTab u={updates.data} error={updates.error} />}
      </div>
    </>
  );
}

function Installed() {
  const stacks = useQuery({ queryKey: ["stacks"], queryFn: () => api.get<Stack[]>("/api/docker/stacks"), refetchInterval: 15_000 });
  const action = useAction((p: { name: string; action: string }) => api.post<Job>(`/api/docker/stacks/${encodeURIComponent(p.name)}/${p.action}`, {}));
  const [remove, setRemove] = useState<string | null>(null);
  if (stacks.isLoading) return <Spinner />;
  if (stacks.error) return <ErrorBox error={stacks.error} />;
  if (!stacks.data?.length) return <Card><Empty icon={<Box />} title={t("No apps installed")} /></Card>;
  return (
    <Card pad={false}>
      <table className="table">
        <thead><tr><th>{t("App")}</th><th>{t("State")}</th><th>{t("Modified")}</th><th /></tr></thead>
        <tbody>{stacks.data.map((s) => (
          <tr key={s.name}>
            <td><div className="font-medium">{s.custom?.name ?? s.name}</div>{s.app && s.app !== s.name && <div className="text-[11.5px] text-muted">{s.app}</div>}</td>
            <td><Badge tone={s.status === "running" ? "ok" : s.status === "partial" ? "warn" : "neutral"} dot>{s.status === "running" ? t("Running") : s.status === "partial" ? t("Partly running") : t("Stopped")}</Badge> <span className="text-[12px] text-muted">{s.running}/{s.services}</span></td>
            <td className="text-[12.5px] text-muted">{dateTime(s.modifiedAt)}</td>
            <td className="whitespace-nowrap text-right">
              {s.status !== "running" && <Button size="sm" variant="ghost" icon={<Play size={14} />} title={t("Start")} onClick={() => action.mutate({ name: s.name, action: "up" })} />}
              {s.status !== "stopped" && <Button size="sm" variant="ghost" icon={<Square size={14} />} title={t("Stop")} onClick={() => action.mutate({ name: s.name, action: "stop" })} />}
              <Button size="sm" variant="ghost" icon={<RotateCw size={14} />} title={t("Restart")} onClick={() => action.mutate({ name: s.name, action: "restart" })} />
              {s.app && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={t("Uninstall")} onClick={() => setRemove(s.name)} />}
            </td>
          </tr>
        ))}</tbody>
      </table>
      {remove && <Uninstall stack={remove} onClose={() => setRemove(null)} />}
    </Card>
  );
}

/** Uninstalling shows what goes first (the plan of the NAS). */
function Uninstall({ stack, onClose }: { stack: string; onClose: () => void }) {
  const plan = useQuery({ queryKey: ["uninstall", stack], queryFn: () => api.get<Plan>(`/api/apps/${encodeURIComponent(stack)}/uninstall`) });
  const [data, setData] = useState(false);
  const [images, setImages] = useState(true);
  const go = useAction(() => api.post<Job>(`/api/apps/${encodeURIComponent(stack)}/uninstall`, { deleteData: data, removeImages: images }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={t("Uninstall {0}", plan.data?.name ?? stack)} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="danger" loading={go.isPending} onClick={() => go.mutate()}>{t("Uninstall")}</Button>
    </>}>
      {plan.isLoading ? <Spinner /> : <ErrorBox error={plan.error} />}
      {plan.data && (
        <div className="space-y-3 text-[13px]">
          <p>{t("{0} container(s) are removed.", plan.data.containers)}</p>
          {!!(plan.data.volumes?.length || plan.data.folders?.length) && (
            <Checkbox checked={data} onChange={setData} label={t("Also delete its data: {0}", [...(plan.data.volumes ?? []), ...(plan.data.folders ?? [])].join(", "))} />
          )}
          {!!plan.data.images?.length && <Checkbox checked={images} onChange={setImages} label={t("Remove its images ({0})", plan.data.images.length)} />}
        </div>
      )}
    </Modal>
  );
}

function Catalog() {
  const { data, isLoading, error } = useQuery({ queryKey: ["apps"], queryFn: () => api.get<AppStoreData>("/api/apps") });
  const [search, setSearch] = useState("");
  const [install, setInstall] = useState<App | null>(null);
  const list = useMemo(() => {
    const s = search.toLowerCase();
    return (data?.apps ?? []).filter((a) => !s || a.name.toLowerCase().includes(s) || t(a.description).toLowerCase().includes(s) || t(a.category).toLowerCase().includes(s));
  }, [data, search]);
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  return (
    <>
      <div className="relative mb-3 max-w-[320px]">
        <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
        <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t("Search")} className="!pl-8" />
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {list.map((a) => {
          const installed = data?.installed[a.id];
          return (
            <Card key={a.id}>
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="font-semibold">{a.name}</div>
                  <div className="text-[11.5px] text-muted">{t(a.category)}</div>
                </div>
                {installed ? <Badge tone="ok">{t("Installed")}</Badge> : <Button size="sm" icon={<Download size={14} />} onClick={() => setInstall(a)}>{t("Install")}</Button>}
              </div>
              <p className="mt-2 line-clamp-3 text-[12.5px] text-muted">{t(a.description)}</p>
            </Card>
          );
        })}
      </div>
      {install && data && <Install app={install} linked={data.linked?.[install.id] ?? {}} onClose={() => setInstall(null)} />}
    </>
  );
}

/** Installing with the catalog's settings; empty passwords are made up by the NAS. */
function Install({ app, linked, onClose }: { app: App; linked: Record<string, string>; onClose: () => void }) {
  const [name, setName] = useState(app.id);
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(app.vars.map((v) => [v.key, linked[v.key] || v.default])));
  const go = useAction(() => api.post<Job>(`/api/apps/${encodeURIComponent(app.id)}/install`, { name, values: Object.fromEntries(Object.entries(values).filter(([, v]) => v !== "")) }), { onDone: onClose });
  return (
    <Modal open wide onClose={onClose} title={t("Install {0}", app.name)} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={go.isPending} disabled={!/^[a-z0-9][a-z0-9_-]*$/.test(name)} onClick={() => go.mutate()}>{t("Install")}</Button>
    </>}>
      <div className="space-y-3">
        <p className="text-[13px] text-muted">{t(app.description)}</p>
        <Field label={t("Name")}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <div className="grid gap-3 sm:grid-cols-2">
          {app.vars.map((v) => (
            <Field key={v.key} label={t(v.label)} hint={v.type === "password" ? t("Leave empty: the NAS chooses a strong password") : undefined}>
              <Input type={v.type === "password" ? "password" : "text"} value={values[v.key] ?? ""} className={v.type === "path" ? "mono" : ""} onChange={(e) => setValues({ ...values, [v.key]: e.target.value })} />
            </Field>
          ))}
        </div>
        <ErrorBox error={go.error} />
      </div>
    </Modal>
  );
}

function Containers() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["containers"], queryFn: () => api.get<Container[]>("/api/docker/containers"), refetchInterval: 10_000 });
  const action = useAction((p: { id: string; action: string }) => api.post(`/api/docker/containers/${p.id}/${p.action}`, {}));
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.length) return <Card><Empty icon={<Box />} title={t("No containers")} /></Card>;
  return (
    <Card pad={false}>
      <table className="table">
        <thead><tr><th>{t("Container")}</th><th>{t("Image")}</th><th>{t("State")}</th><th /></tr></thead>
        <tbody>{data.map((c) => {
          const name = c.Names[0]?.replace(/^\//, "") ?? c.Id.slice(0, 12);
          const running = c.State === "running";
          return (
            <tr key={c.Id}>
              <td><div className="font-medium">{name}</div><div className="text-[11.5px] text-muted">{c.Labels?.["com.docker.compose.project"] ?? ""}</div></td>
              <td className="mono max-w-[260px] truncate text-[12px] text-muted">{c.Image}</td>
              <td><Badge tone={running ? "ok" : "neutral"} dot>{c.State}</Badge><div className="text-[11.5px] text-muted">{c.Status}</div></td>
              <td className="whitespace-nowrap text-right">
                {running ? <>
                  <Button size="sm" variant="ghost" icon={<RotateCw size={14} />} title={t("Restart")} onClick={() => action.mutate({ id: c.Id, action: "restart" })} />
                  <Button size="sm" variant="ghost" icon={<Square size={14} />} title={t("Stop")} onClick={async () => { if (await confirm({ title: t("Stop {0}?", name), text: t("The services of this container stop until it is started again.") })) action.mutate({ id: c.Id, action: "stop" }); }} />
                </> : <Button size="sm" variant="ghost" icon={<Play size={14} />} title={t("Start")} onClick={() => action.mutate({ id: c.Id, action: "start" })} />}
              </td>
            </tr>
          );
        })}</tbody>
      </table>
    </Card>
  );
}

function UpdatesTab({ u, error }: { u?: Updates; error: unknown }) {
  const check = useAction(() => api.post<Job>("/api/docker/updates/check", {}));
  const one = useAction((p: { container?: string; stack?: string }) => api.post<Job>("/api/docker/updates/apply", p));
  const all = useAction(() => api.post<Job>("/api/docker/updates/apply-all", {}));
  const avail = (u?.containers ?? []).filter((c) => c.status === "available");
  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="flex-1 text-[13px] text-muted">{u?.checked ? t("Last check {0}", dateTime(u.checked)) : t("Never checked")}</span>
        <Button icon={<RotateCw size={15} />} loading={check.isPending || u?.running} onClick={() => check.mutate()}>{t("Check now")}</Button>
        {avail.length > 0 && <Button variant="primary" icon={<ArrowUpCircle size={15} />} loading={all.isPending} onClick={() => all.mutate()}>{t("Update all")}</Button>}
      </div>
      <ErrorBox error={error} />
      <Card pad={false}>
        {!avail.length ? <Empty icon={<ArrowUpCircle />} title={t("Everything is up to date")} /> : (
          <table className="table">
            <thead><tr><th>{t("Container")}</th><th>{t("Image")}</th><th /></tr></thead>
            <tbody>{avail.map((c) => (
              <tr key={c.id}>
                <td className="font-medium">{c.name}{c.stack && <span className="ml-2 text-[11.5px] text-muted">{c.stack}</span>}</td>
                <td className="mono text-[12px] text-muted">{c.image}</td>
                <td className="text-right"><Button size="sm" onClick={() => one.mutate(c.stack && u?.stacks[c.stack] ? { stack: c.stack } : { container: c.id })}>{t("Update")}</Button></td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </Card>
    </>
  );
}
