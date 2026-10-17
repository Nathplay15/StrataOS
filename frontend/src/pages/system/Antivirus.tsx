import { useQuery } from "@tanstack/react-query";
import { Bug, CalendarClock, Plug, RotateCcw, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, dateTime } from "../../lib/format";
import { AppLink } from "../../components/AppLink";
import { Button, Card, Empty, ErrorBox, IconButton, Notice, PageHeader, Spinner, useUI } from "../../components/ui";
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
