import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowDownToLine, ArrowLeftRight, ArrowUpFromLine, CheckCircle2, ChevronRight, CloudCog, Edit3, Folder, History, Play, Plug, Plus, RefreshCcw, Trash2, XCircle } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { ScheduleRun } from "../../lib/types";
import { ago, cx, dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { ScheduleEditor, describeCron, fromCron, toCron, type Plan } from "./Schedules";
import { HistoryModal } from "./Backups";

type ProviderField = { key: string; label: string; secret?: boolean; required?: boolean; options?: string[]; hint?: string };
type Provider = { id: string; label: string; fields: ProviderField[]; authorize?: string; bucket?: boolean };
type Task = {
  id: string; name: string; provider: string; params: Record<string, string>; secrets: Record<string, string>;
  localPath: string; remotePath: string; direction: "upload" | "download" | "bisync"; mirror: boolean;
  excludes?: string[]; bwLimit?: string; cron: string; enabled: boolean; synced?: boolean;
  history: ScheduleRun[]; nextRun?: string | null; running?: boolean;
};
type Data = { installed: boolean; tasks: Task[]; providers: Provider[] };
type Sources = { folders: { path: string; label: string }[] };

const directions = {
  upload: { label: "Envoi vers le cloud", sub: "Le NAS est la référence", icon: <ArrowUpFromLine size={18} /> },
  download: { label: "Téléchargement depuis le cloud", sub: "Le cloud est la référence", icon: <ArrowDownToLine size={18} /> },
  bisync: { label: "Bidirectionnelle (bêta)", sub: "Les changements des deux côtés sont propagés", icon: <ArrowLeftRight size={18} /> },
};

const blank = (p: Provider): Task => ({ id: "", name: "", provider: p.id, params: Object.fromEntries(p.fields.filter((f) => f.options).map((f) => [f.key, f.options![0]])), secrets: {}, localPath: "", remotePath: "", direction: "upload", mirror: false, cron: "0 */6 * * *", enabled: true, history: [] });

function RemoteBrowser({ task, onPick, onClose }: { task: Task; onPick: (p: string) => void; onClose: () => void }) {
  const [path, setPath] = useState(task.remotePath);
  const { data, isLoading, error } = useQuery({ queryKey: ["cloud-browse", task.provider, path], queryFn: () => api.post<string[]>(`/api/cloudsync/browse${q({ path })}`, task), retry: false });
  const parts = path.split("/").filter(Boolean);
  return (
    <Modal open onClose={onClose} title="Choisir le dossier distant" footer={<><Button onClick={onClose}>Annuler</Button><Button variant="primary" onClick={() => { onPick(path); onClose(); }}>Choisir « /{path} »</Button></>}>
      <div className="mb-3 flex flex-wrap items-center gap-1 text-[13px]">
        <button className="rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => setPath("")}>Racine</button>
        {parts.map((p, i) => <span key={i} className="flex items-center gap-1"><ChevronRight size={13} className="text-muted" /><button className="rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => setPath(parts.slice(0, i + 1).join("/"))}>{p}</button></span>)}
      </div>
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : (
        <div className="max-h-72 space-y-0.5 overflow-y-auto rounded-xl border border-line p-1">
          {data?.length === 0 && <div className="text-muted px-3 py-2 text-[13px]">Aucun sous-dossier</div>}
          {data?.map((d) => (
            <button key={d} onClick={() => setPath(path ? `${path}/${d}` : d)} className="flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left text-[13px] hover:bg-panel-2"><Folder size={15} className="text-accent" />{d}</button>
          ))}
        </div>
      )}
    </Modal>
  );
}

function Editor({ initial, providers, sources, onClose }: { initial: Task | null; providers: Provider[]; sources: Sources; onClose: () => void }) {
  const editing = !!initial;
  const [t, setT] = useState<Task>(initial ?? blank(providers[0]));
  const [scheduled, setScheduled] = useState(!!(initial?.cron ?? "0 */6 * * *"));
  const [plan, setPlan] = useState<Plan>(fromCron(initial?.cron || "0 */6 * * *"));
  const [excludes, setExcludes] = useState((initial?.excludes ?? []).join("\n"));
  const [browse, setBrowse] = useState(false);
  const p = providers.find((x) => x.id === t.provider)!;
  const set = (k: Partial<Task>) => setT((o) => ({ ...o, ...k }));
  const payload = () => ({ ...t, cron: scheduled ? toCron(plan) : "", excludes: excludes.split("\n").map((x) => x.trim()).filter(Boolean) });
  const save = useAction(() => (editing ? api.put(`/api/cloudsync/${t.id}`, payload()) : api.post("/api/cloudsync", payload())), { success: editing ? "Synchronisation mise à jour" : "Synchronisation créée", onDone: onClose });
  const test = useAction(() => api.post<{ message: string }>("/api/cloudsync/test", payload()), { success: (r) => r.message });
  const field = (f: ProviderField) => {
    const v = (f.secret ? t.secrets[f.key] : t.params[f.key]) ?? "";
    const upd = (val: string) => (f.secret ? set({ secrets: { ...t.secrets, [f.key]: val } }) : set({ params: { ...t.params, [f.key]: val } }));
    if (f.options) return <Select value={v} onChange={(e) => upd(e.target.value)}>{f.options.map((o) => <option key={o}>{o}</option>)}</Select>;
    if (f.key === "token") return <textarea className="input mono h-24 w-full text-[12px]" value={v} onChange={(e) => upd(e.target.value)} placeholder='{"access_token":"…","token_type":"Bearer","refresh_token":"…","expiry":"…"}' />;
    return <Input type={f.secret ? "password" : "text"} value={v} onChange={(e) => upd(e.target.value)} autoComplete="off" />;
  };

  return (
    <Modal open onClose={onClose} wide title={editing ? `Modifier « ${t.name} »` : "Nouvelle synchronisation cloud"} footer={<>
      <Button onClick={() => test.mutate()} loading={test.isPending}>Tester la connexion</Button>
      <div className="flex-1" />
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" loading={save.isPending} disabled={!t.name || !t.localPath} onClick={() => save.mutate()}>{editing ? "Enregistrer" : "Créer"}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Nom"><Input value={t.name} onChange={(e) => set({ name: e.target.value })} placeholder="Photos → Google Drive" autoFocus /></Field>
          <Field label="Service">
            <Select value={t.provider} disabled={editing} onChange={(e) => { const np = providers.find((x) => x.id === e.target.value)!; setT({ ...blank(np), name: t.name, localPath: t.localPath, direction: t.direction }); }}>
              {providers.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
            </Select>
          </Field>
        </div>

        <div className="space-y-4 rounded-xl border border-line p-4">
          <div className="font-medium text-[13px]">Connexion</div>
          {p.authorize && (
            <Notice>
              Sur un ordinateur avec un navigateur, installez <a className="text-accent underline" href="https://rclone.org/downloads/" target="_blank" rel="noreferrer">rclone</a> puis lancez <code className="mono rounded bg-panel px-1.5 py-0.5">rclone authorize "{p.authorize}"</code>. Connectez-vous au compte, puis collez ici le texte JSON affiché (entre les lignes ---&gt; et &lt;---).
            </Notice>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            {p.fields.map((f) => <Field key={f.key} label={f.label + (f.required ? "" : " (optionnel)")} hint={f.hint} className={f.key === "token" || f.key === "url" ? "sm:col-span-2" : ""}>{field(f)}</Field>)}
          </div>
          {p.id === "sftp" && !t.secrets.pass && <div className="text-muted text-[12px]">Sans mot de passe, la clé SSH de StrataOS est utilisée (affichée dans Sauvegarde → nouvelle sauvegarde → serveur SSH).</div>}
        </div>

        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">Sens de la synchronisation</div>
          <div className="grid gap-2 sm:grid-cols-3">
            {(Object.keys(directions) as Task["direction"][]).map((d) => (
              <button key={d} type="button" onClick={() => set({ direction: d })} className={cx("flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left transition", t.direction === d ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                <span className="mt-0.5 text-accent">{directions[d].icon}</span>
                <span><span className="block text-[13px] font-medium">{directions[d].label}</span><span className="block text-muted text-[11.5px]">{directions[d].sub}</span></span>
              </button>
            ))}
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Dossier du NAS">
            <Select value={t.localPath} onChange={(e) => set({ localPath: e.target.value })}>
              <option value="">Choisir…</option>
              {sources.folders.map((f) => <option key={f.path} value={f.path}>{f.label} ({f.path})</option>)}
              {t.localPath && !sources.folders.some((f) => f.path === t.localPath) && <option value={t.localPath}>{t.localPath}</option>}
            </Select>
          </Field>
          <Field label={p.bucket ? "Bucket / dossier distant" : "Dossier distant"} hint={p.bucket ? "ex. mon-bucket/photos" : "Vide pour la racine du compte"}>
            <div className="flex gap-2">
              <Input className="mono" value={t.remotePath} onChange={(e) => set({ remotePath: e.target.value })} placeholder={p.bucket ? "bucket/dossier" : "NAS/Photos"} />
              <Button onClick={() => setBrowse(true)} icon={<Folder size={15} />}>Parcourir</Button>
            </div>
          </Field>
        </div>

        {t.direction !== "bisync" ? (
          <Switch checked={t.mirror} onChange={(v) => set({ mirror: v })} label="Miroir exact" hint={t.direction === "upload" ? "Supprime aussi dans le cloud ce qui a été supprimé sur le NAS (sinon les fichiers sont seulement ajoutés et mis à jour)" : "Supprime aussi sur le NAS ce qui a été supprimé dans le cloud"} />
        ) : (
          <Notice tone="warn">Synchronisation bidirectionnelle : une suppression d'un côté est reproduite de l'autre. La première exécution fusionne les deux dossiers sans rien supprimer ; en cas de conflit, les deux versions sont conservées (suffixes ..path1 / ..path2).</Notice>
        )}

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Exclusions" hint="Un motif par ligne, ex. *.tmp ou Cache/**"><textarea className="input mono h-20 w-full text-[12.5px]" value={excludes} onChange={(e) => setExcludes(e.target.value)} /></Field>
          <Field label="Limite de bande passante" hint="ex. 5M = 5 Mo/s, vide = illimitée"><Input value={t.bwLimit ?? ""} onChange={(e) => set({ bwLimit: e.target.value })} placeholder="illimitée" /></Field>
        </div>

        <div className="space-y-3 rounded-xl border border-line p-4">
          <Switch checked={scheduled} onChange={setScheduled} label="Exécuter automatiquement" hint="Sinon, uniquement à la demande" />
          {scheduled && <ScheduleEditor plan={plan} onChange={setPlan} />}
        </div>
        <Switch checked={t.enabled} onChange={(v) => set({ enabled: v })} label="Synchronisation active" />
        <ErrorBox error={save.error || test.error} />
      </div>
      {browse && <RemoteBrowser task={payload()} onPick={(path) => set({ remotePath: path })} onClose={() => setBrowse(false)} />}
    </Modal>
  );
}

export default function CloudSync() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<Task | null | undefined>(undefined);
  const [hist, setHist] = useState<Task | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["cloudsync"], queryFn: () => api.get<Data>("/api/cloudsync"), refetchInterval: 8000 });
  const sources = useQuery({ queryKey: ["backup-sources"], queryFn: () => api.get<Sources>("/api/backups/sources") });
  const install = useAction(() => api.post("/api/system/components/rclone/install"));
  const run = useAction((id: string) => api.post(`/api/cloudsync/${id}/run`));
  const resync = useAction((id: string) => api.post(`/api/cloudsync/${id}/resync`));
  const toggle = useAction((t: Task) => api.put(`/api/cloudsync/${t.id}`, { ...t, enabled: !t.enabled }));
  const del = useAction((id: string) => api.del(`/api/cloudsync/${id}`), { success: "Synchronisation supprimée (les fichiers sont conservés des deux côtés)" });
  const header = <PageHeader title="Synchronisation cloud" subtitle="Google Drive, OneDrive, Dropbox, S3, Backblaze B2, WebDAV, SFTP…" icon={<CloudCog size={20} />} actions={data?.installed && <Button variant="primary" icon={<Plus size={15} />} disabled={!sources.data} onClick={() => setEdit(null)}>Nouvelle synchronisation</Button>} />;

  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) return <div>{header}<Card><Empty icon={<Plug />} title="rclone n'est pas installé" action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>Installer</Button>} /></Card></div>;
  const pLabel = (id: string) => data.providers.find((p) => p.id === id)?.label.split(" (")[0] ?? id;

  return (
    <div>
      {header}
      <ErrorBox error={error || sources.error} />
      {!data.tasks.length ? (
        <Card><Empty icon={<CloudCog />} title="Aucune synchronisation" text="Gardez un dossier du NAS synchronisé avec un service cloud, dans un sens ou dans les deux." action={<Button variant="primary" disabled={!sources.data} onClick={() => setEdit(null)}>Créer une synchronisation</Button>} /></Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {data.tasks.map((t) => {
            const last = t.history[0];
            const failedBisync = t.direction === "bisync" && last?.status === "failed" && last.message.includes("Resynchroniser");
            return (
              <Card key={t.id} pad={false} className={t.enabled ? "" : "opacity-70"}>
                <div className="p-5">
                  <div className="flex items-start gap-3">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent">{directions[t.direction].icon}</div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 font-semibold">{t.name}{t.running && <Badge tone="accent" dot>En cours</Badge>}</div>
                      <div className="text-muted text-[12px]">{pLabel(t.provider)} · {directions[t.direction].label}{t.mirror && t.direction !== "bisync" ? " (miroir)" : ""}</div>
                    </div>
                    <Switch checked={t.enabled} onChange={() => toggle.mutate(t)} />
                  </div>
                  <div className="mt-3 space-y-1 text-[12.5px]">
                    <div className="truncate"><span className="text-muted">NAS </span><span className="mono">{t.localPath}</span></div>
                    <div className="truncate"><span className="text-muted">Cloud </span><span className="mono">/{t.remotePath}</span></div>
                    <div className="text-muted">{t.cron ? describeCron(t.cron) : "À la demande"}{t.enabled && t.nextRun ? ` · prochaine ${dateTime(t.nextRun)}` : ""}</div>
                  </div>
                  {last && (
                    <button onClick={() => setHist(t)} className={cx("mt-3 flex w-full items-start gap-2 rounded-lg px-3 py-2 text-left text-[12.5px]", last.status === "success" ? "bg-ok/10" : "bg-danger/10 text-danger")}>
                      {last.status === "success" ? <CheckCircle2 size={15} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={15} className="mt-0.5 shrink-0" />}
                      <span className="min-w-0 break-words">{ago(last.started)} — {last.message}</span>
                    </button>
                  )}
                </div>
                <div className="flex flex-wrap justify-end gap-1 border-t border-line bg-panel-2/50 px-3 py-2">
                  <Button size="sm" variant="ghost" icon={<Play size={14} />} disabled={t.running} onClick={() => run.mutate(t.id)}>Synchroniser</Button>
                  {t.direction === "bisync" && (failedBisync || t.synced) && <Button size="sm" variant="ghost" icon={<RefreshCcw size={14} />} disabled={t.running} onClick={async () => { if (await confirm({ title: "Resynchroniser ?", text: "Les deux dossiers sont fusionnés à nouveau : les fichiers présents d'un seul côté sont copiés de l'autre, rien n'est supprimé. Les fichiers supprimés depuis la dernière synchronisation réapparaîtront.", confirm: "Resynchroniser" })) resync.mutate(t.id); }}>Resynchroniser</Button>}
                  <IconButton title="Historique" onClick={() => setHist(t)}><History size={15} /></IconButton>
                  <IconButton title="Modifier" disabled={!sources.data} onClick={() => setEdit(t)}><Edit3 size={15} /></IconButton>
                  <IconButton title="Supprimer" className="hover:!text-danger" onClick={async () => { if (await confirm({ title: `Supprimer « ${t.name} » ?`, text: "La synchronisation s'arrête. Aucun fichier n'est effacé, ni sur le NAS ni dans le cloud.", danger: true })) del.mutate(t.id); }}><Trash2 size={15} /></IconButton>
                </div>
              </Card>
            );
          })}
        </div>
      )}
      {edit !== undefined && sources.data && <Editor initial={edit} providers={data.providers} sources={sources.data} onClose={() => setEdit(undefined)} />}
      {hist && <HistoryModal b={data.tasks.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
    </div>
  );
}
