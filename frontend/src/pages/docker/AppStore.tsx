import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AppWindow, Check, ExternalLink, Search } from "lucide-react";
import { AppLink } from "../../components/AppLink";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { App } from "../../lib/types";
import { cx } from "../../lib/format";
import { Badge, Button, Card, Field, Input, Modal, Notice, PageHeader, Spinner } from "../../components/ui";
import { DockerGuard } from "./Containers";
import { tr } from "../../lib/i18n";

function randomSecret() {
  const a = new Uint8Array(18);
  crypto.getRandomValues(a);
  return Array.from(a, (b) => "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"[b % 57]).join("");
}

function AppIcon({ app, size = 48 }: { app: App; size?: number }) {
  const [err, setErr] = useState(false);
  return err ? (
    <div className="flex items-center justify-center rounded-xl bg-gradient-to-br from-accent to-accent-2 font-semibold text-white" style={{ width: size, height: size }}>{app.name[0]}</div>
  ) : (
    <img src={app.icon} alt="" onError={() => setErr(true)} className="rounded-xl bg-panel-2 p-1.5" style={{ width: size, height: size }} />
  );
}

function InstallModal({ app, onClose }: { app: App; onClose: () => void }) {
  const [name, setName] = useState(app.id);
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(app.vars.map((v) => [v.key, v.type === "password" ? randomSecret() : v.default])));
  const [showCompose, setShowCompose] = useState(false);
  const install = useAction(() => api.post(`/api/apps/${app.id}/install`, { name, values }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-3"><AppIcon app={app} size={30} />{tr("Installer")}{" "}{app.name}</span>} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button>
    </>}>
      <div className="space-y-4">
        <p className="text-muted">{app.description}</p>
        <Field label={tr("Nom de l'installation (stack)")}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          {app.vars.map((v) => (
            <Field key={v.key} label={v.label} className={v.type === "path" ? "sm:col-span-2" : ""} hint={v.type === "path" ? tr("Créé automatiquement. Placez-le sur un volume de données (/srv/strata/<volume>/…).") : undefined}>
              <Input className={v.type === "path" || v.type === "password" ? "mono" : ""} value={values[v.key] ?? ""} onChange={(e) => setValues({ ...values, [v.key]: e.target.value })} />
            </Field>
          ))}
        </div>
        <button className="text-accent text-[12.5px]" onClick={() => setShowCompose(!showCompose)}>{showCompose ? tr("Masquer") : tr("Voir")} {" "}{tr("le fichier compose")}</button>
        {showCompose && <pre className="mono max-h-72 overflow-auto rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{app.compose}</pre>}
        <Notice>{tr("L'application est déployée comme une stack Compose : vous pourrez la modifier, la mettre à jour ou la supprimer depuis « Stacks Compose ».")}</Notice>
      </div>
    </Modal>
  );
}

export default function AppStore() {
  const { data, isLoading } = useQuery({ queryKey: ["apps"], queryFn: () => api.get<{ apps: App[]; installed: Record<string, string>; ports: Record<string, string> }>("/api/apps") });
  const [cat, setCat] = useState(tr("Toutes"));
  const [search, setSearch] = useState("");
  const [install, setInstall] = useState<App | null>(null);
  const cats = useMemo(() => [tr("Toutes"), ...new Set((data?.apps ?? []).map((a) => a.category))], [data]);
  const apps = (data?.apps ?? []).filter((a) => (cat === tr("Toutes") || a.category === cat) && (!search || (a.name + a.description).toLowerCase().includes(search.toLowerCase())));
  const host = location.hostname;
  return (
    <div>
      <PageHeader title={tr("App Center")} subtitle={tr("Installez des applications auto-hébergées en un clic")} icon={<AppWindow size={20} />} actions={
        <div className="relative"><Search size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted" /><Input className="!w-60 !pl-9" placeholder={tr("Rechercher une app…")} value={search} onChange={(e) => setSearch(e.target.value)} /></div>
      } />
      <DockerGuard>
        <div className="mb-5 flex flex-wrap gap-1.5">
          {cats.map((c) => <button key={c} onClick={() => setCat(c)} className={cx("rounded-full border px-3 py-1 text-[12.5px] transition", cat === c ? "border-accent bg-accent text-white" : "border-line bg-panel text-muted hover:text-fg")}>{c}</button>)}
        </div>
        {isLoading ? <Spinner /> : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            {apps.map((a) => {
              const inst = data?.installed[a.id];
              const port = data?.ports[a.id] ?? a.vars.find((v) => v.key === a.webPort)?.default;
              return (
                <Card key={a.id} pad={false} className="flex flex-col">
                  <div className="flex flex-1 gap-4 p-5">
                    <AppIcon app={a} />
                    <div className="min-w-0">
                      <div className="font-semibold">{a.name}</div>
                      <Badge>{a.category}</Badge>
                      <p className="mt-2 text-[12.5px] text-muted">{a.description}</p>
                    </div>
                  </div>
                  <div className="flex items-center justify-end gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
                    {inst ? (
                      <>
                        <span className="mr-auto flex items-center gap-1 text-ok text-[12.5px]"><Check size={14} /> {" "}{tr("Installée")}</span>
                        {port && <a href={`http://${host}:${port}`} target="_blank" rel="noreferrer"><Button size="sm" icon={<ExternalLink size={13} />}>{tr("Ouvrir")}</Button></a>}
                        <AppLink to="/docker/stacks"><Button size="sm" variant="ghost">{tr("Gérer")}</Button></AppLink>
                      </>
                    ) : <Button size="sm" variant="primary" onClick={() => setInstall(a)}>{tr("Installer")}</Button>}
                  </div>
                </Card>
              );
            })}
          </div>
        )}
      </DockerGuard>
      {install && <InstallModal app={install} onClose={() => setInstall(null)} />}
    </div>
  );
}
