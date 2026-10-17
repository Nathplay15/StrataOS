import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, RefreshCcw } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Button, Card, Empty, ErrorBox, PageHeader, Spinner, useUI } from "../../components/ui";

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
          toast(`StrataOS mis à jour : ${from} → ${r.version}`);
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
        <div><div className="text-muted text-[12px]">Version installée</div><div className="mono text-[15px] font-semibold">{data.running}</div></div>
        {available && <div><div className="text-muted text-[12px]">Disponible</div><div className="mono text-[15px] font-semibold text-ok">{data.candidate}</div></div>}
        <div className="flex-1" />
        {waiting ? <Spinner label="Mise à jour et redémarrage du service…" /> : available ? (
          <Button variant="primary" loading={upgrade.isPending} onClick={async () => { if (await confirm({ title: `Mettre à jour vers ${data.candidate} ?`, text: "Le service StrataOS redémarre à la fin : l'interface se recharge automatiquement. Les partages et les applications ne sont pas interrompus." })) upgrade.mutate(); }}>Mettre à jour StrataOS</Button>
        ) : <Button onClick={() => refetch()}>{data.repository ? "À jour — vérifier" : "Vérifier"}</Button>}
      </div>
      {!data.repository && <div className="mt-3 text-muted text-[12.5px]">Aucun dépôt StrataOS configuré : les mises à jour s'installent en déposant un nouveau paquet .deb.</div>}
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
      <PageHeader title="Mises à jour" subtitle="Paquets Debian et StrataOS" icon={<RefreshCcw size={20} />} actions={<>
        <Button icon={<RefreshCcw size={15} />} loading={refresh.isPending} onClick={() => refresh.mutate()}>Rechercher</Button>
        <Button variant="primary" disabled={!data?.length} loading={upgrade.isPending} onClick={async () => { if (await confirm({ title: "Installer les mises à jour ?", text: `${data?.length} paquet(s) seront mis à jour. Certains services peuvent redémarrer.` })) upgrade.mutate(); }}>Tout mettre à jour</Button>
      </>} />
      <StrataCard />
      <ErrorBox error={error} />
      <Card pad={false} title={data ? `${data.length} mise(s) à jour disponible(s)${security ? ` dont ${security} de sécurité` : ""}` : undefined}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<CheckCircle2 />} title="Le système est à jour" text="Cliquez sur « Rechercher » pour actualiser la liste des paquets." /> : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>Paquet</th><th>Version actuelle</th><th>Nouvelle version</th><th>Source</th></tr></thead>
              <tbody>{data.map((u) => <tr key={u.package}><td className="font-medium">{u.package}</td><td className="mono text-muted">{u.current}</td><td className="mono text-ok">{u.new}</td><td className="text-muted text-[12.5px]">{u.source}</td></tr>)}</tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
