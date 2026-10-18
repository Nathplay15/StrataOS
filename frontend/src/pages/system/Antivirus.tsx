import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bug, CalendarClock, Plug, RotateCcw, ShieldAlert, ShieldCheck, Trash2, Unlock } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, dateTime } from "../../lib/format";
import { AppLink } from "../../components/AppLink";
import { Button, Card, Empty, ErrorBox, Field, IconButton, Input, Notice, PageHeader, Spinner, Switch, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Item = { id: string; path: string; virus: string; size: number; date: string };
type Data = { status: { installed: boolean; version: string }; quarantine: Item[] };

export default function Antivirus() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["antivirus"], queryFn: () => api.get<Data>("/api/antivirus"), refetchInterval: 15000 });
  const install = useAction(() => api.post("/api/system/components/antivirus/install"));
  const restore = useAction((id: string) => api.post<{ path: string }>(`/api/antivirus/quarantine/${id}/restore`), { success: (r) => tr("Fichier restauré : {0}", r.path) });
  const del = useAction((id: string) => api.del(`/api/antivirus/quarantine/${id}`), { success: tr("Fichier supprimé définitivement") });
  const header = <PageHeader title={tr("Antivirus")} subtitle={tr("Analyse des dossiers partagés avec ClamAV")} icon={<ShieldCheck size={20} />} />;
  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.status.installed) {
    return <div>{header}<Card><Empty icon={<Plug />} title={tr("ClamAV n'est pas installé")} text={tr("Les signatures de virus sont ensuite mises à jour automatiquement plusieurs fois par jour.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button>} /></Card></div>;
  }
  const [version, db, dbDate] = data.status.version.split("/");
  return (
    <div>
      {header}
      <div className="space-y-5">
        <Card title={tr("Moteur")}>
          <div className="grid gap-4 text-[13px] sm:grid-cols-3">
            <div><div className="text-muted text-[12px]">{tr("Version")}</div><div>{version}</div></div>
            <div><div className="text-muted text-[12px]">{tr("Base de signatures")}</div><div>{db ?? "—"}</div></div>
            <div><div className="text-muted text-[12px]">{tr("Mise à jour")}</div><div>{dbDate ?? "—"}</div></div>
          </div>
          <div className="mt-4"><Notice><span className="flex flex-wrap items-center gap-2">{tr("Les analyses se programment dans les tâches planifiées (type « Analyse antivirus »). Elles tournent en priorité basse pour ne pas ralentir le NAS.")} <AppLink to="/system/schedules"><Button size="sm" icon={<CalendarClock size={14} />}>{tr("Tâches planifiées")}</Button></AppLink></span></Notice></div>
        </Card>
        <RansomwareCard />
        <Card title={tr("Quarantaine")} pad={false}>
          {data.quarantine.length === 0 ? <Empty icon={<Bug />} title={tr("Aucun fichier en quarantaine")} /> : (
            <table className="w-full text-[13px]">
              <thead><tr className="text-left text-muted text-[12px]"><th className="px-5 py-2">{tr("Fichier")}</th><th>{tr("Menace")}</th><th>{tr("Taille")}</th><th>{tr("Date")}</th><th /></tr></thead>
              <tbody>
                {data.quarantine.map((it) => (
                  <tr key={it.id} className="border-t border-line">
                    <td className="mono max-w-[340px] truncate px-5 py-2.5" title={it.path}>{it.path}</td>
                    <td className="text-danger">{it.virus}</td>
                    <td>{bytes(it.size)}</td>
                    <td>{dateTime(it.date)}</td>
                    <td className="whitespace-nowrap pr-3 text-right">
                      <IconButton title={tr("Restaurer")} onClick={async () => { if (await confirm({ title: tr("Restaurer ce fichier ?"), text: tr("Il a été détecté comme {0}. Ne le restaurez que s'il s'agit d'une fausse alerte.", it.virus), confirm: tr("Restaurer") })) restore.mutate(it.id); }}><RotateCcw size={15} /></IconButton>
                      <IconButton title={tr("Supprimer")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer définitivement ?"), text: it.path, danger: true })) del.mutate(it.id); }}><Trash2 size={15} /></IconButton>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </div>
    </div>
  );
}

type Ransom = { enabled: boolean; threshold: number; block: boolean; blocked: string[]; traps?: boolean; nfsPaused?: boolean; frozen?: string[] };

function RansomwareCard() {
  const { data } = useQuery({ queryKey: ["ransomware"], queryFn: () => api.get<Ransom>("/api/ransomware"), refetchInterval: 10000 });
  const [c, setC] = useState<Ransom | null>(null);
  useEffect(() => { if (data && !c) setC(data); }, [data, c]);
  const save = useAction(() => api.put("/api/ransomware", c), { success: tr("Protection anti-rançongiciel enregistrée") });
  const unblock = useAction((user: string) => api.post("/api/ransomware/unblock", { user }), { success: tr("Compte débloqué") });
  const unfreeze = useAction((share: string) => api.post("/api/ransomware/unfreeze", { share }), { success: tr("Partage rétabli") });
  const resumeNFS = useAction(() => api.post("/api/ransomware/nfs-resume"), { success: tr("Partages NFS rétablis") });
  if (!c || !data) return null;
  const clean = (x: Ransom) => JSON.stringify({ enabled: x.enabled, threshold: x.threshold, block: x.block, traps: !!x.traps });
  const dirty = clean(c) !== clean(data);
  return (
    <Card title={<span className="flex items-center gap-2"><ShieldAlert size={16} className="text-accent" />{tr("Protection anti-rançongiciel")}</span>} actions={<Button size="sm" variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate()}>{tr("Enregistrer")}</Button>}>
      <div className="space-y-4">
        <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Surveiller les partages (SMB, WebDAV, SFTP, FTP)")} hint={tr("Détecte les renommages et suppressions en masse et les extensions typiques des rançongiciels (.locked, .encrypted, .wncry…) ; le compte est bloqué sur tous ces protocoles")} />
        {c.enabled && (
          <>
            <Field label={tr("Seuil (fichiers renommés ou supprimés par minute et par compte)")} className="max-w-sm"><Input type="number" min={5} value={c.threshold} onChange={(e) => setC({ ...c, threshold: Math.max(5, +e.target.value) })} /></Field>
            <Switch checked={c.block} onChange={(v) => setC({ ...c, block: v })} label={tr("Bloquer le compte et couper ses connexions")} hint={tr("Recommandé : arrête le chiffrement en cours. Le compte reste utilisable dans l'interface web.")} />
            <Switch checked={!!c.traps} onChange={(v) => setC({ ...c, traps: v })} label={tr("Fichiers pièges")} hint={tr("Deux fichiers « NE PAS MODIFIER » dans chaque partage : s'ils sont modifiés, renommés ou supprimés, le partage passe en lecture seule sur tous les protocoles, NFS compris. Les renommages en masse par NFS suspendent aussi les partages NFS.")} />
            <div className="text-muted text-[12.5px]">{tr("Pour récupérer les fichiers, programmez des snapshots verrouillés dans les tâches planifiées : même un rançongiciel avec un compte administrateur ne peut pas les supprimer.")}</div>
          </>
        )}
        {!!data.frozen?.length && (
          <Notice tone="warn">
            <div className="space-y-2">
              <div>{tr("Partages en lecture seule après la modification d'un fichier piège :")}</div>
              <div className="flex flex-wrap gap-2">{data.frozen.map((s) => <Button key={s} size="sm" icon={<Unlock size={14} />} loading={unfreeze.isPending} onClick={() => unfreeze.mutate(s)}>{tr("Rétablir {0}", s)}</Button>)}</div>
            </div>
          </Notice>
        )}
        {data.nfsPaused && (
          <Notice tone="warn">
            <div className="flex flex-wrap items-center gap-3"><span>{tr("Les partages NFS sont suspendus après des renommages en masse par NFS.")}</span><Button size="sm" icon={<Unlock size={14} />} loading={resumeNFS.isPending} onClick={() => resumeNFS.mutate()}>{tr("Rétablir NFS")}</Button></div>
          </Notice>
        )}
        {data.blocked.length > 0 && (
          <Notice tone="warn">
            <div className="space-y-2">
              <div>{tr("Comptes bloqués par la protection :")}</div>
              <div className="flex flex-wrap gap-2">{data.blocked.map((u) => <Button key={u} size="sm" icon={<Unlock size={14} />} loading={unblock.isPending} onClick={() => unblock.mutate(u)}>{tr("Débloquer {0}", u)}</Button>)}</div>
            </div>
          </Notice>
        )}
      </div>
    </Card>
  );
}
