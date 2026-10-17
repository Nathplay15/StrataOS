import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, History, Plus, RefreshCcw, Trash2, Undo2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Button, Card, Empty, ErrorBox, Notice, PageHeader, Spinner, useUI } from "../../components/ui";
import { dateTime } from "../../lib/format";
import { tr } from "../../lib/i18n";

type Up = { package: string; current: string; new: string; source: string };

type StrataVersion = { running: string; installed: string; candidate: string; repository: boolean };

function StrataCard() {
  const { toast, confirm } = useUI();
  const { data, refetch } = useQuery({ queryKey: ["strata-version"], queryFn: () => api.get<StrataVersion>("/api/system/updates/strata") });
  const [waiting, setWaiting] = useState(false);
  const upgrade = useAction(() => api.post("/api/system/updates/strata"), { onDone: () => setWaiting(true) });
  // The service restarts during the update: wait for a new version to answer.
  useEffect(() => {
    if (!waiting || !data) return;
    const from = data.running;
    const t = setInterval(async () => {
      try {
        const r = await fetch("/api/setup").then((x) => x.json());
        if (r.version && r.version !== from) {
          clearInterval(t);
          toast(tr("StrataOS mis à jour : {0} → {1}", from, r.version));
          setTimeout(() => location.reload(), 1500);
        }
      } catch {
        /* restarting */
      }
    }, 3000);
    return () => clearInterval(t);
  }, [waiting, data, toast]);
  if (!data) return null;
  const available = data.candidate && data.candidate !== "(none)" && data.candidate !== data.installed;
  return (
    <Card title="StrataOS" className="mb-5">
      <div className="flex flex-wrap items-center gap-4">
        <div><div className="text-muted text-[12px]">{tr("Version installée")}</div><div className="mono text-[15px] font-semibold">{data.running}</div></div>
        {available && <div><div className="text-muted text-[12px]">{tr("Disponible")}</div><div className="mono text-[15px] font-semibold text-ok">{data.candidate}</div></div>}
        <div className="flex-1" />
        {waiting ? <Spinner label={tr("Mise à jour et redémarrage du service…")} /> : available ? (
          <Button variant="primary" loading={upgrade.isPending} onClick={async () => { if (await confirm({ title: tr("Mettre à jour vers {0} ?", data.candidate), text: tr("Le service StrataOS redémarre à la fin : l'interface se recharge automatiquement. Les partages et les applications ne sont pas interrompus.") })) upgrade.mutate(); }}>{tr("Mettre à jour StrataOS")}</Button>
        ) : <Button onClick={() => refetch()}>{data.repository ? tr("À jour — vérifier") : tr("Vérifier")}</Button>}
      </div>
      {!data.repository && <div className="mt-3 text-muted text-[12.5px]">{tr("Aucun dépôt StrataOS configuré : les mises à jour s'installent en déposant un nouveau paquet .deb.")}</div>}
    </Card>
  );
}

export default function Updates() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["updates"], queryFn: () => api.get<Up[]>("/api/system/updates") });
  const refresh = useAction(() => api.post("/api/system/updates/refresh"));
  const upgrade = useAction(() => api.post("/api/system/updates/upgrade"));
  const security = data?.filter((u) => u.source.includes("security")).length ?? 0;
  return (
    <div>
      <PageHeader title={tr("Mises à jour")} subtitle={tr("Paquets Debian et StrataOS")} icon={<RefreshCcw size={20} />} actions={<>
        <Button icon={<RefreshCcw size={15} />} loading={refresh.isPending} onClick={() => refresh.mutate()}>{tr("Rechercher")}</Button>
        <Button variant="primary" disabled={!data?.length} loading={upgrade.isPending} onClick={async () => { if (await confirm({ title: tr("Installer les mises à jour ?"), text: tr("{0} paquet(s) seront mis à jour. Certains services peuvent redémarrer.", data?.length) })) upgrade.mutate(); }}>{tr("Tout mettre à jour")}</Button>
      </>} />
      <StrataCard />
      <ErrorBox error={error} />
      <Card pad={false} title={data ? tr("{0} mise(s) à jour disponible(s){1}", data.length, security ? ` dont ${security} de sécurité` : "") : undefined}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<CheckCircle2 />} title={tr("Le système est à jour")} text={tr("Cliquez sur « Rechercher » pour actualiser la liste des paquets.")} /> : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Paquet")}</th><th>{tr("Version actuelle")}</th><th>{tr("Nouvelle version")}</th><th>{tr("Source")}</th></tr></thead>
              <tbody>{data.map((u) => <tr key={u.package}><td className="font-medium">{u.package}</td><td className="mono text-muted">{u.current}</td><td className="mono text-ok">{u.new}</td><td className="text-muted text-[12.5px]">{u.source}</td></tr>)}</tbody>
            </table>
          </div>
        )}
      </Card>
      <div className="mt-5"><RestorePoints /></div>
    </div>
  );
}

type Points = { supported: boolean; reason: string; root: { fsType: string; subvol: string }; points: { name: string; created: string; label: string }[] };

function RestorePoints() {
  const { confirm } = useUI();
  const { data } = useQuery({ queryKey: ["restore-points"], queryFn: () => api.get<Points>("/api/system/restore-points") });
  const create = useAction(() => api.post("/api/system/restore-points"), { success: tr("Point de restauration créé") });
  const del = useAction((n: string) => api.del(`/api/system/restore-points/${n}`), { success: tr("Point de restauration supprimé") });
  const rollback = useAction((n: string) => api.post(`/api/system/restore-points/${n}/rollback`), { success: tr("Retour au point de restauration : le NAS redémarre") });
  if (!data) return null;
  const label = (l: string) => (l === "avant-mise-a-jour" ? tr("Avant une mise à jour") : l === "manuel" ? tr("Manuel") : l);
  return (
    <Card title={<span className="flex items-center gap-2"><History size={16} className="text-accent" />{tr("Points de restauration du système")}</span>} actions={data.supported && <Button size="sm" icon={<Plus size={14} />} loading={create.isPending} onClick={() => create.mutate()}>{tr("Créer maintenant")}</Button>}>
      {!data.supported ? (
        <Notice>{tr("Indisponible sur ce NAS : le système est installé sur {0}. Les points de restauration demandent un système Btrfs, comme l'installe l'ISO StrataOS.", data.root.fsType || "?")}</Notice>
      ) : (
        <div className="space-y-3">
          <div className="text-muted text-[13px]">{tr("Un point est créé automatiquement avant chaque mise à jour (les 6 derniers sont gardés). Revenir à un point restaure le système et ses programmes, puis redémarre ; vos données et les réglages de StrataOS ne changent pas.")}</div>
          {data.points.length === 0 ? <div className="text-muted text-[13px]">{tr("Aucun point de restauration pour le moment.")}</div> : (
            <div className="divide-y divide-line rounded-xl border border-line">
              {data.points.map((p) => (
                <div key={p.name} className="flex flex-wrap items-center gap-3 px-4 py-2.5 text-[13px]">
                  <div className="flex-1"><b>{dateTime(p.created)}</b> <span className="text-muted">· {label(p.label)}</span></div>
                  <Button size="sm" icon={<Undo2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Revenir à ce point ?"), text: tr("Le système revient à son état du {0} et le NAS redémarre. Les programmes installés ou mis à jour depuis seront annulés ; les comptes créés depuis devront voir leur mot de passe redéfini.", dateTime(p.created)), confirm: tr("Restaurer et redémarrer"), danger: true })) rollback.mutate(p.name); }}>{tr("Restaurer")}</Button>
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={() => del.mutate(p.name)} />
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
