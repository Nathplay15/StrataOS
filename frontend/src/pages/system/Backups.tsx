import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArchiveRestore, CheckCircle2, Cloud, Copy, DatabaseBackup, Edit3, FolderSync, History, Layers, Play, Plus, RotateCcw, Trash2, XCircle } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { BackupTask, ResticSnapshot } from "../../lib/types";
import { ago, bytes, cx, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { ScheduleEditor, describeCron, fromCron, toCron, type Plan } from "./Schedules";
import { tr, srv } from "../../lib/i18n";

type Sources = { folders: { path: string; label: string }[]; datasets: { path: string; label: string }[] };

const kinds = {
  rsync: { label: tr("Copie"), sub: tr("Miroir des fichiers (rsync) vers un disque, un volume ou un serveur SSH"), icon: <FolderSync size={18} /> },
  restic: { label: tr("Sauvegarde versionnée"), sub: tr("Chiffrée et dédupliquée (restic) : local, SFTP, S3, B2, MinIO…"), icon: <Cloud size={18} /> },
  zfs: { label: tr("Réplication ZFS"), sub: tr("Snapshots envoyés vers un autre pool, local ou distant"), icon: <Layers size={18} /> },
};

const blank: BackupTask = { id: "", name: "", kind: "rsync", sources: [], destType: "local", destPath: "", port: 22, cron: "0 1 * * *", enabled: true, history: [], keepDaily: 7, keepWeekly: 4, keepMonthly: 12, keepLast: 7 };

function destLabel(b: BackupTask) {
  switch (b.destType) {
    case "local": return b.destPath;
    case "ssh": case "sftp": return `${b.user}@${b.host}:${b.destPath}`;
    case "s3": return tr("S3 {0}/{1}", b.s3Endpoint?.replace(/^https?:\/\//, ""), b.destPath);
    case "rest": return b.destPath;
  }
}

function SSHKey() {
  const { data } = useQuery({ queryKey: ["backup-key"], queryFn: () => api.get<{ publicKey: string }>("/api/backups/ssh-key") });
  const { toast } = useUI();
  return (
    <Notice>
      <div className="space-y-2">
        <div>{tr("Ajoutez cette clé publique dans")}{" "}<span className="mono">~/.ssh/authorized_keys</span> {" "}{tr("du compte distant (un autre NAS StrataOS, un Synology, un serveur Linux…) :")}</div>
        <div className="flex items-center gap-2">
          <code className="mono min-w-0 flex-1 truncate rounded-lg bg-panel px-2.5 py-1.5">{data?.publicKey ?? "…"}</code>
          <IconButton title={tr("Copier")} onClick={() => { if (data) { navigator.clipboard?.writeText(data.publicKey); toast(tr("Clé copiée")); } }}><Copy size={15} /></IconButton>
        </div>
      </div>
    </Notice>
  );
}

function Editor({ initial, sources, onClose }: { initial: BackupTask | null; sources: Sources; onClose: () => void }) {
  const editing = !!initial;
  const [b, setB] = useState<BackupTask>(initial ?? blank);
  const [scheduled, setScheduled] = useState(!!(initial ?? blank).cron);
  const [plan, setPlan] = useState<Plan>(fromCron((initial ?? blank).cron || "0 1 * * *"));
  const [excludes, setExcludes] = useState((initial?.excludes ?? []).join("\n"));
  const set = (k: Partial<BackupTask>) => setB((p) => ({ ...p, ...k }));
  const payload = () => ({ ...b, cron: scheduled ? toCron(plan) : "", excludes: excludes.split("\n").map((x) => x.trim()).filter(Boolean) });
  const save = useAction(() => (editing ? api.put(`/api/backups/${b.id}`, payload()) : api.post("/api/backups", payload())), { success: editing ? tr("Sauvegarde mise à jour") : tr("Sauvegarde créée"), onDone: onClose });
  const test = useAction(() => api.post<{ message: string }>("/api/backups/test", payload()), { success: (r) => srv(r.message) });
  const remote = b.destType === "ssh" || b.destType === "sftp";
  const destTypes = b.kind === "restic" ? [["local", tr("Dossier local / disque USB")], ["sftp", tr("Serveur SFTP (SSH)")], ["s3", tr("S3 / B2 / MinIO")], ["rest", tr("Serveur REST restic")]] : [["local", b.kind === "zfs" ? tr("Pool local") : tr("Dossier local / disque USB")], ["ssh", tr("Serveur distant (SSH)")]];
  const pickKind = (k: BackupTask["kind"]) => set({ kind: k, sources: [], destType: "local", destPath: "" });

  return (
    <Modal open onClose={onClose} title={editing ? tr("Modifier « {0} »", b.name) : tr("Nouvelle sauvegarde")} wide footer={<>
      <Button className="mr-auto" loading={test.isPending} onClick={() => test.mutate()}>{tr("Tester la destination")}</Button>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!b.name || !b.sources.length || !b.destPath} onClick={() => save.mutate()}>{editing ? tr("Enregistrer") : tr("Créer")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-2 sm:grid-cols-3">
          {(Object.keys(kinds) as BackupTask["kind"][]).map((k) => (
            <button key={k} type="button" disabled={editing} onClick={() => pickKind(k)} className={cx("flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left transition disabled:opacity-60", b.kind === k ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
              <span className="mt-0.5 text-accent">{kinds[k].icon}</span>
              <span><span className="block text-[13px] font-medium">{kinds[k].label}</span><span className="block text-muted text-[11.5px]">{kinds[k].sub}</span></span>
            </button>
          ))}
        </div>
        <Field label={tr("Nom")}><Input value={b.name} onChange={(e) => set({ name: e.target.value })} placeholder={tr("ex. Photos vers disque USB")} /></Field>

        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{b.kind === "zfs" ? tr("Dataset à répliquer") : tr("Dossiers à sauvegarder")}</div>
          {b.kind === "zfs" ? (
            sources.datasets.length === 0 ? <Notice tone="warn">{tr("Aucun dataset ZFS.")}</Notice> : (
              <Select value={b.sources[0] ?? ""} onChange={(e) => set({ sources: e.target.value ? [e.target.value] : [] })}>
                <option value="">{tr("Choisir…")}</option>
                {sources.datasets.map((d) => <option key={d.path} value={d.path}>{d.label}</option>)}
              </Select>
            )
          ) : (
            <div className="grid gap-2 sm:grid-cols-2">
              {sources.folders.map((f) => (
                <Checkbox key={f.path} checked={b.sources.includes(f.path)} onChange={(v) => set({ sources: v ? [...b.sources, f.path] : b.sources.filter((x) => x !== f.path) })} label={<span><span className="text-[13px]">{srv(f.label)}</span> <span className="mono text-muted text-[11px]">{f.path}</span></span>} />
              ))}
            </div>
          )}
        </div>

        <div className="space-y-4 rounded-xl border border-line p-4">
          <div className="font-medium text-[13px]">{tr("Destination")}</div>
          <Field label={tr("Type")}>
            <Select value={b.destType} onChange={(e) => set({ destType: e.target.value as BackupTask["destType"], destPath: "" })}>
              {destTypes.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </Select>
          </Field>
          {remote && (
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label={tr("Serveur")} className="sm:col-span-2"><Input value={b.host ?? ""} onChange={(e) => set({ host: e.target.value })} placeholder={tr("nas2.local ou 192.168.1.20")} /></Field>
              <Field label={tr("Port")}><Input type="number" value={b.port ?? 22} onChange={(e) => set({ port: +e.target.value })} /></Field>
              <Field label={tr("Utilisateur")}><Input value={b.user ?? ""} onChange={(e) => set({ user: e.target.value })} /></Field>
            </div>
          )}
          {b.destType === "s3" && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Point de terminaison")} className="sm:col-span-2" hint={tr("ex. https://s3.eu-west-3.amazonaws.com, https://s3.us-west-002.backblazeb2.com, http://minio:9000")}><Input value={b.s3Endpoint ?? ""} onChange={(e) => set({ s3Endpoint: e.target.value })} /></Field>
              <Field label={tr("Clé d'accès")}><Input value={b.accessKey ?? ""} onChange={(e) => set({ accessKey: e.target.value })} /></Field>
              <Field label={tr("Clé secrète")}><Input type="password" value={b.secretKey ?? ""} onChange={(e) => set({ secretKey: e.target.value })} /></Field>
            </div>
          )}
          <Field
            label={b.kind === "zfs" ? tr("Dataset de destination") : b.destType === "s3" ? tr("Bucket / dossier") : b.destType === "rest" ? tr("URL du serveur REST") : b.destType === "local" ? tr("Dossier de destination") : tr("Chemin distant")}
            hint={b.kind === "zfs" ? tr("ex. backup/media (créé automatiquement)") : b.destType === "local" ? tr("Dans un volume, ex. /srv/strata/usb/sauvegardes (un disque USB s'importe depuis le Gestionnaire de stockage)") : undefined}
          >
            {b.destType === "local" && b.kind !== "zfs" ? (
              <div className="flex gap-2">
                <Select className="!w-56 shrink-0" value="" onChange={(e) => e.target.value && set({ destPath: `${e.target.value}/sauvegardes` })}>
                  <option value="">{tr("Volume…")}</option>
                  {sources.folders.filter((f) => f.label.startsWith("Volume") || f.label.startsWith("ZFS")).map((f) => <option key={f.path} value={f.path}>{srv(f.label)}</option>)}
                </Select>
                <Input className="mono" value={b.destPath} onChange={(e) => set({ destPath: e.target.value })} />
              </div>
            ) : <Input className="mono" value={b.destPath} onChange={(e) => set({ destPath: e.target.value })} />}
          </Field>
          {remote && <SSHKey />}
        </div>

        {b.kind === "rsync" && (
          <div className="space-y-3">
            <Switch checked={!!b.delete} onChange={(v) => set({ delete: v })} label={tr("Miroir exact")} hint={tr("Supprime de la destination les fichiers supprimés à la source. Sans cette option, rien n'est jamais effacé de la copie.")} />
            <Field label={tr("Exclusions (une par ligne)")} hint={tr("ex. *.tmp, Thumbs.db, cache/")}><textarea className="input mono h-20" value={excludes} onChange={(e) => setExcludes(e.target.value)} /></Field>
          </div>
        )}
        {b.kind === "restic" && (
          <div className="space-y-3 rounded-xl border border-line p-4">
            <Field label={tr("Mot de passe du dépôt")} hint={tr("Chiffre toutes les données. Sans lui, la sauvegarde est irrécupérable : conservez-le hors du NAS.")}><Input type="password" value={b.password ?? ""} onChange={(e) => set({ password: e.target.value })} /></Field>
            <div className="text-[12.5px] font-medium text-muted">{tr("Conserver")}</div>
            <div className="grid gap-3 sm:grid-cols-4">
              <Field label={tr("Derniers")}><Input type="number" min={0} value={b.keepLast ?? 0} onChange={(e) => set({ keepLast: +e.target.value })} /></Field>
              <Field label={tr("Quotidiens")}><Input type="number" min={0} value={b.keepDaily ?? 0} onChange={(e) => set({ keepDaily: +e.target.value })} /></Field>
              <Field label={tr("Hebdomadaires")}><Input type="number" min={0} value={b.keepWeekly ?? 0} onChange={(e) => set({ keepWeekly: +e.target.value })} /></Field>
              <Field label={tr("Mensuels")}><Input type="number" min={0} value={b.keepMonthly ?? 0} onChange={(e) => set({ keepMonthly: +e.target.value })} /></Field>
            </div>
            <Field label={tr("Exclusions (une par ligne)")}><textarea className="input mono h-16" value={excludes} onChange={(e) => setExcludes(e.target.value)} /></Field>
          </div>
        )}
        {b.kind === "zfs" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Points de réplication conservés")}><Input type="number" min={1} value={b.keepLast ?? 7} onChange={(e) => set({ keepLast: Math.max(1, +e.target.value) })} /></Field>
            <div className="pt-6"><Switch checked={!!b.recursive} onChange={(v) => set({ recursive: v })} label={tr("Inclure les datasets enfants")} /></div>
          </div>
        )}

        <div className="space-y-3">
          <Switch checked={scheduled} onChange={setScheduled} label={tr("Exécuter automatiquement")} hint={tr("Sinon, uniquement à la demande")} />
          {scheduled && <ScheduleEditor plan={plan} onChange={setPlan} />}
        </div>
        <Switch checked={b.enabled} onChange={(v) => set({ enabled: v })} label={tr("Sauvegarde active")} />
        <ErrorBox error={save.error || test.error} />
      </div>
    </Modal>
  );
}

function RestoreModal({ b, onClose }: { b: BackupTask; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["restic-snapshots", b.id], queryFn: () => api.get<ResticSnapshot[]>(`/api/backups/${b.id}/snapshots`) });
  const [snap, setSnap] = useState("");
  const [include, setInclude] = useState("");
  const base = b.sources[0]?.split("/").slice(0, 4).join("/") ?? "/srv/strata";
  const [target, setTarget] = useState(`${base}/restauration-${new Date().toISOString().slice(0, 10)}`);
  const restore = useAction(() => api.post(`/api/backups/${b.id}/restore`, { snapshot: snap, include, target }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Restaurer « {0} »", b.name)} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" disabled={!snap || !target} loading={restore.isPending} onClick={() => restore.mutate()}>{tr("Restaurer")}</Button>
    </>}>
      <div className="space-y-4">
        <ErrorBox error={error} />
        {isLoading ? <Spinner label={tr("Lecture du dépôt…")} /> : !data?.length ? <Empty icon={<ArchiveRestore />} title={tr("Aucune version")} text={tr("Lancez la sauvegarde une première fois.")} /> : (
          <div className="max-h-64 space-y-1 overflow-y-auto rounded-xl border border-line p-1">
            {data.map((s) => (
              <button key={s.id} onClick={() => setSnap(s.id)} className={cx("flex w-full items-center justify-between gap-3 rounded-lg px-3 py-2 text-left text-[13px]", snap === s.id ? "bg-accent-soft text-accent" : "hover:bg-panel-2")}>
                <span><b>{dateTime(s.time)}</b> <span className="text-muted">· {ago(s.time)}</span></span>
                <span className="text-muted text-[12px]">{s.summary ? tr("{0} fichiers · {1}", s.summary.total_files_processed, bytes(s.summary.total_bytes_processed)) : s.short_id}</span>
              </button>
            ))}
          </div>
        )}
        <Field label={tr("Restaurer uniquement (optionnel)")} hint={tr("Chemin complet d'un dossier ou d'un fichier sauvegardé, ex. {0}/2025", b.sources[0] ?? "")}><Input className="mono" value={include} onChange={(e) => setInclude(e.target.value)} /></Field>
        <Field label={tr("Restaurer dans")} hint={tr("Les fichiers sont recréés sous ce dossier avec leur chemin d'origine ; rien n'est écrasé ailleurs.")}><Input className="mono" value={target} onChange={(e) => setTarget(e.target.value)} /></Field>
      </div>
    </Modal>
  );
}

export function HistoryModal({ b, onClose }: { b: Pick<BackupTask, "name" | "history">; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={tr("Historique — {0}", b.name)} wide>
      {b.history.length === 0 ? <Empty icon={<History />} title={tr("Jamais exécutée")} /> : (
        <div className="space-y-2">
          {b.history.map((h, i) => (
            <div key={i} className="flex items-start gap-3 rounded-xl bg-panel-2 px-4 py-3">
              {h.status === "success" ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={17} className="mt-0.5 shrink-0 text-danger" />}
              <div className="min-w-0"><div className="text-[13px] break-words">{srv(h.message)}</div><div className="text-muted text-[11.5px]">{dateTime(h.started)} · {Math.max(1, Math.round(h.duration))} s{h.manual ? tr(" · manuelle") : ""}</div></div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

export default function Backups() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<BackupTask | null | undefined>(undefined);
  const [restore, setRestore] = useState<BackupTask | null>(null);
  const [hist, setHist] = useState<BackupTask | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["backups"], queryFn: () => api.get<BackupTask[]>("/api/backups"), refetchInterval: 8000 });
  const sources = useQuery({ queryKey: ["backup-sources"], queryFn: () => api.get<Sources>("/api/backups/sources") });
  const run = useAction((id: string) => api.post(`/api/backups/${id}/run`));
  const toggle = useAction((b: BackupTask) => api.put(`/api/backups/${b.id}`, { ...b, enabled: !b.enabled }));
  const del = useAction((id: string) => api.del(`/api/backups/${id}`), { success: tr("Sauvegarde supprimée (les données sauvegardées sont conservées)") });

  return (
    <div>
      <PageHeader title={tr("Sauvegarde")} subtitle={tr("Copies, sauvegardes versionnées et réplication, vers un disque, un autre NAS ou le cloud")} icon={<DatabaseBackup size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} disabled={!sources.data} onClick={() => setEdit(null)}>{tr("Nouvelle sauvegarde")}</Button>} />
      <ErrorBox error={error || sources.error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<DatabaseBackup />} title={tr("Aucune sauvegarde")} text={tr("Un RAID protège contre la panne d'un disque, pas contre une suppression, un rançongiciel ou un incendie. Gardez au moins une copie ailleurs : disque USB, autre NAS ou cloud.")} action={<Button variant="primary" disabled={!sources.data} onClick={() => setEdit(null)}>{tr("Créer une sauvegarde")}</Button>} /></Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {data.map((b) => {
            const last = b.history[0];
            return (
              <Card key={b.id} pad={false} className={b.enabled ? "" : "opacity-70"}>
                <div className="p-5">
                  <div className="flex items-start gap-3">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent">{kinds[b.kind].icon}</div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 font-semibold">{b.name}{b.running && <Badge tone="accent" dot>{tr("En cours")}</Badge>}</div>
                      <div className="text-muted text-[12px]">{kinds[b.kind].label}</div>
                    </div>
                    <Switch checked={b.enabled} onChange={() => toggle.mutate(b)} />
                  </div>
                  <div className="mt-3 space-y-1 text-[12.5px]">
                    <div className="truncate"><span className="text-muted">{tr("De")}{" "}</span><span className="mono">{b.sources.join(", ")}</span></div>
                    <div className="truncate"><span className="text-muted">{tr("Vers")}{" "}</span><span className="mono">{destLabel(b)}</span></div>
                    <div className="text-muted">{b.cron ? describeCron(b.cron) : tr("À la demande")}{b.enabled && b.nextRun ? tr(" · prochaine {0}", dateTime(b.nextRun)) : ""}</div>
                  </div>
                  {last && (
                    <button onClick={() => setHist(b)} className={cx("mt-3 flex w-full items-start gap-2 rounded-lg px-3 py-2 text-left text-[12.5px]", last.status === "success" ? "bg-ok/10" : "bg-danger/10 text-danger")}>
                      {last.status === "success" ? <CheckCircle2 size={15} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={15} className="mt-0.5 shrink-0" />}
                      <span className="min-w-0 break-words">{ago(last.started)} — {srv(last.message)}</span>
                    </button>
                  )}
                </div>
                <div className="flex flex-wrap justify-end gap-1 border-t border-line bg-panel-2/50 px-3 py-2">
                  <Button size="sm" variant="ghost" icon={<Play size={14} />} disabled={b.running} onClick={() => run.mutate(b.id)}>{tr("Exécuter")}</Button>
                  {b.kind === "restic" && <Button size="sm" variant="ghost" icon={<RotateCcw size={14} />} onClick={() => setRestore(b)}>{tr("Restaurer")}</Button>}
                  <IconButton title={tr("Historique")} onClick={() => setHist(b)}><History size={15} /></IconButton>
                  <IconButton title={tr("Modifier")} disabled={!sources.data} onClick={() => setEdit(b)}><Edit3 size={15} /></IconButton>
                  <IconButton title={tr("Supprimer")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer « {0} » ?", b.name), text: tr("La tâche ne s'exécutera plus. Les données déjà sauvegardées ne sont pas effacées."), danger: true })) del.mutate(b.id); }}><Trash2 size={15} /></IconButton>
                </div>
              </Card>
            );
          })}
        </div>
      )}
      {edit !== undefined && sources.data && <Editor initial={edit} sources={sources.data} onClose={() => setEdit(undefined)} />}
      {restore && <RestoreModal b={restore} onClose={() => setRestore(null)} />}
      {hist && <HistoryModal b={data?.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
    </div>
  );
}
