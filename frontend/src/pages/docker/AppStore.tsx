import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { AppWindow, ArrowDownToLine, Check, Edit3, ExternalLink, Plus, Search, Trash2 } from "lucide-react";
import { AppLink } from "../../components/AppLink";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { App, AppStoreData, CustomApp, CustomAppEntry } from "../../lib/types";
import { cx } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Spinner, useUI } from "../../components/ui";
import { DockerGuard } from "./Containers";
import { tr } from "../../lib/i18n";

function randomSecret(length = 18) {
  const a = new Uint8Array(length);
  crypto.getRandomValues(a);
  return Array.from(a, (b) => "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"[b % 57]).join("");
}

function AppIcon({ app, size = 48 }: { app: { name: string; icon?: string }; size?: number }) {
  const [err, setErr] = useState(false);
  return err || !app.icon ? (
    <div className="flex shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-accent to-accent-2 font-semibold text-white" style={{ width: size, height: size }}>{(app.name[0] ?? "?").toUpperCase()}</div>
  ) : (
    <img src={app.icon} alt="" referrerPolicy="no-referrer" onError={() => setErr(true)} className="shrink-0 rounded-xl bg-panel-2 p-1.5" style={{ width: size, height: size }} />
  );
}

function InstallModal({ app, linked, onClose }: { app: App; linked: Record<string, string>; onClose: () => void }) {
  const [name, setName] = useState(app.id);
  // Linked passwords stay on the server: left empty, filled in at install.
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(app.vars.map((v) => [v.key, v.key in linked ? linked[v.key] : v.type === "password" ? randomSecret(v.length || 18) : v.default])),
  );
  const [showCompose, setShowCompose] = useState(false);
  const install = useAction(() => api.post(`/api/apps/${app.id}/install`, { name, values }), { onDone: onClose });
  const hint = (v: App["vars"][number]) => {
    if (v.key in linked) return tr("Taken from the app it works with, already installed.");
    if (v.type === "path") return tr("Created automatically. Place it on a data volume (/srv/strata/<volume>/…).");
    if (v.type === "device") return tr("Device of the adapter, preferably its stable name under /dev/serial/by-id/.");
    return undefined;
  };
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-3"><AppIcon app={app} size={30} />{tr("Install")}{" "}{app.name}</span>} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>
    </>}>
      <div className="space-y-4">
        <p className="text-muted">{tr(app.description)}</p>
        <Field label={tr("Installation name (stack)")}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          {app.vars.map((v) => (
            <Field key={v.key} label={tr(v.label)} className={v.type === "path" || v.type === "device" ? "sm:col-span-2" : ""} hint={hint(v)}>
              <Input className={v.type !== "port" && v.type !== "text" ? "mono" : ""} value={values[v.key] ?? ""} placeholder={v.key in linked && v.type === "password" ? tr("(from the installed app)") : undefined} onChange={(e) => setValues({ ...values, [v.key]: e.target.value })} />
            </Field>
          ))}
        </div>
        <button className="text-accent text-[12.5px]" onClick={() => setShowCompose(!showCompose)}>{showCompose ? tr("Hide") : tr("View")} {" "}{tr("the compose file")}</button>
        {showCompose && <pre className="mono max-h-72 overflow-auto rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{app.compose}</pre>}
        <Notice>{tr("The application is deployed as a Compose stack: you can edit, update or delete it from \"Compose Stacks\".")}</Notice>
      </div>
    </Modal>
  );
}

const sampleCompose = `services:
  whoami:
    image: traefik/whoami:latest
    restart: unless-stopped
    ports:
      - "8099:80"
`;

function MetaFields({ meta, setMeta }: { meta: CustomApp; setMeta: (m: CustomApp) => void }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={tr("Name")}><Input value={meta.name} onChange={(e) => setMeta({ ...meta, name: e.target.value })} /></Field>
      <Field label={tr("Web port")} hint={tr("Port of the NAS that the Open button uses (empty: no web interface).")}>
        <Input inputMode="numeric" value={meta.webPort || ""} onChange={(e) => setMeta({ ...meta, webPort: Number(e.target.value.replace(/\D/g, "")) || 0 })} />
      </Field>
      <Field label={tr("Icon address (optional)")} className="sm:col-span-2"><Input className="mono" placeholder="https://…/icon.svg" value={meta.icon ?? ""} onChange={(e) => setMeta({ ...meta, icon: e.target.value })} /></Field>
      <Field label={tr("Description (optional)")} className="sm:col-span-2"><Input value={meta.description ?? ""} onChange={(e) => setMeta({ ...meta, description: e.target.value })} /></Field>
    </div>
  );
}

/** Creates a custom app from a compose file. */
function CustomAppModal({ onClose }: { onClose: () => void }) {
  const [stack, setStack] = useState("");
  const [meta, setMeta] = useState<CustomApp>({ name: "", webPort: 0 });
  const [compose, setCompose] = useState(sampleCompose);
  const [env, setEnv] = useState("");
  const save = useAction(() => api.post("/api/apps/custom", { stack, compose, env, ...meta }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Add a custom app")} wide="xl" footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!stack || !meta.name.trim()} onClick={() => save.mutate()}>{tr("Install")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Installation name (stack)")} hint={tr("lowercase letters, digits, - and _")}><Input value={stack} onChange={(e) => setStack(e.target.value.toLowerCase())} /></Field>
        <MetaFields meta={meta} setMeta={setMeta} />
        <Field label="compose.yaml"><textarea spellCheck={false} className="input mono h-[34vh] resize-y leading-relaxed" value={compose} onChange={(e) => setCompose(e.target.value)} /></Field>
        <Field label={tr(".env (optional)")}><textarea spellCheck={false} className="input mono h-20 resize-y" value={env} onChange={(e) => setEnv(e.target.value)} /></Field>
        <ErrorBox error={save.error} />
        <Notice>{tr("The compose file is checked like a stack's, then deployed. Containers run with the rights you give them: only use files you trust.")}</Notice>
      </div>
    </Modal>
  );
}

/** Edits the App Center metadata of a stack (also turns a plain stack into a custom app). */
export function CustomMetaModal({ stack, initial, onClose }: { stack: string; initial?: CustomApp; onClose: () => void }) {
  const [meta, setMeta] = useState<CustomApp>(initial ?? { name: stack, webPort: 0 });
  const save = useAction(() => api.put(`/api/apps/custom/${stack}`, meta), { success: tr("App saved"), onDone: onClose });
  const unlink = useAction(() => api.del(`/api/apps/custom/${stack}`), { success: tr("Removed from My apps"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={initial ? tr("App {0}", initial.name) : tr("Add stack {0} to My apps", stack)} wide footer={<>
      {initial && <Button variant="ghost" className="mr-auto" loading={unlink.isPending} onClick={() => unlink.mutate()}>{tr("Remove from My apps (keep the stack)")}</Button>}
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!meta.name.trim()} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <MetaFields meta={meta} setMeta={setMeta} />
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

const MY_APPS = "__mine";

export default function AppStore() {
  const { confirm } = useUI();
  const { data, isLoading } = useQuery({ queryKey: ["apps"], queryFn: () => api.get<AppStoreData>("/api/apps") });
  const [cat, setCat] = useState("");
  const [search, setSearch] = useState("");
  const [install, setInstall] = useState<App | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<CustomAppEntry | null>(null);
  // Catalog texts are English in Go and translated here (custom apps keep their own text).
  const cats = useMemo(() => [...new Set((data?.apps ?? []).map((a) => a.category))].sort((a, b) => tr(a).localeCompare(tr(b))), [data]);
  const q = search.toLowerCase();
  const match = (s: string) => !q || s.toLowerCase().includes(q);
  const custom = (data?.custom ?? []).filter((c) => (cat === "" || cat === MY_APPS) && match(c.name + (c.description ?? "")));
  const apps = (data?.apps ?? []).filter((a) => (cat === "" || a.category === cat || (cat === MY_APPS && !!data?.installed[a.id])) && match([a.name, a.description, a.category, tr(a.description), tr(a.category)].join(" ")));
  const act = useAction((p: { name: string; action: string }) => api.post(`/api/docker/stacks/${p.name}/${p.action}`));
  const del = useAction((name: string) => api.del(`/api/docker/stacks/${name}?volumes=1`), { success: tr("App removed") });
  const host = location.hostname;
  const remove = async (stack: string, name: string) => {
    if (await confirm({ title: tr("Remove {0}?", name), text: tr("Its containers, networks and named Docker volumes are deleted; folders mounted from /srv/strata are kept."), danger: true, typeToConfirm: stack })) del.mutate(stack);
  };
  const actions = (stack: string, name: string, port: string | number | undefined, extra?: ReactNode) => (
    <>
      <span className="mr-auto flex items-center gap-1 text-ok text-[12.5px]"><Check size={14} /> {" "}{tr("Installed")}</span>
      {port ? <a href={`http://${host}:${port}`} target="_blank" rel="noreferrer"><Button size="sm" icon={<ExternalLink size={13} />}>{tr("Open")}</Button></a> : null}
      <Button size="sm" variant="ghost" icon={<ArrowDownToLine size={14} />} title={tr("Update")} onClick={() => act.mutate({ name: stack, action: "update" })} />
      {extra}
      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} title={tr("Remove")} onClick={() => remove(stack, name)} />
    </>
  );
  const chip = (id: string, label: string) => (
    <button key={id} onClick={() => setCat(id)} className={cx("rounded-full border px-3 py-1 text-[12.5px] transition", cat === id ? "border-accent bg-accent text-white" : "border-line bg-panel text-muted hover:text-fg")}>{label}</button>
  );
  const nothing = !isLoading && custom.length === 0 && apps.length === 0;
  return (
    <div>
      <PageHeader title={tr("App Center")} subtitle={tr("Install self-hosted applications in one click")} icon={<AppWindow size={20} />} actions={<>
        <div className="relative"><Search size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted" /><Input className="!w-60 !pl-9" placeholder={tr("Search for an app…")} value={search} onChange={(e) => setSearch(e.target.value)} /></div>
        <Button icon={<Plus size={15} />} onClick={() => setAdding(true)}>{tr("Add a custom app")}</Button>
      </>} />
      <DockerGuard>
        <div className="mb-5 flex flex-wrap gap-1.5">
          {chip("", tr("All"))}
          {chip(MY_APPS, tr("My apps"))}
          {cats.map((c) => chip(c, tr(c)))}
        </div>
        {isLoading ? <Spinner /> : nothing ? (
          <p className="text-muted">{cat === MY_APPS && !q ? tr("No app installed yet.") : tr("No app matches your search.")}</p>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            {custom.map((c) => (
              <Card key={"custom-" + c.stack} pad={false} className="flex flex-col">
                <div className="flex flex-1 gap-4 p-5">
                  <AppIcon app={c} />
                  <div className="min-w-0">
                    <div className="font-semibold">{c.name}</div>
                    <span className="flex flex-wrap items-center gap-1.5"><Badge tone="accent">{tr("My apps")}</Badge><Health value={c.status} /></span>
                    <p className="mt-2 text-[12.5px] text-muted">{c.description || tr("Custom app (stack {0})", c.stack)}</p>
                  </div>
                </div>
                <div className="flex items-center justify-end gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
                  {actions(c.stack, c.name, c.webPort, <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} title={tr("Edit")} onClick={() => setEditing(c)} />)}
                </div>
              </Card>
            ))}
            {apps.map((a) => {
              const inst = data?.installed[a.id];
              const port = data?.ports[a.id] ?? a.vars.find((v) => v.key === a.webPort)?.default;
              return (
                <Card key={a.id} pad={false} className="flex flex-col">
                  <div className="flex flex-1 gap-4 p-5">
                    <AppIcon app={a} />
                    <div className="min-w-0">
                      <div className="font-semibold">{a.name}</div>
                      <Badge>{tr(a.category)}</Badge>
                      <p className="mt-2 text-[12.5px] text-muted">{tr(a.description)}</p>
                    </div>
                  </div>
                  <div className="flex items-center justify-end gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
                    {inst ? actions(inst, a.name, a.webPort ? port : undefined, <AppLink to="/docker/stacks"><Button size="sm" variant="ghost">{tr("Manage")}</Button></AppLink>)
                      : <Button size="sm" variant="primary" onClick={() => setInstall(a)}>{tr("Install")}</Button>}
                  </div>
                </Card>
              );
            })}
          </div>
        )}
      </DockerGuard>
      {install && <InstallModal app={install} linked={data?.linked?.[install.id] ?? {}} onClose={() => setInstall(null)} />}
      {adding && <CustomAppModal onClose={() => setAdding(false)} />}
      {editing && <CustomMetaModal stack={editing.stack} initial={editing} onClose={() => setEditing(null)} />}
    </div>
  );
}
