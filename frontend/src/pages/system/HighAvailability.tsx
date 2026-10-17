import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftRight, Copy, KeyRound, Plug, RefreshCcw, Server, ServerCog, Unlink, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type Config = { continuous?: boolean; maintenance?: boolean; autoReturn?: boolean; role: "" | "primary" | "secondary"; peerHost: string; peerName: string; vip: string; interface: string; vrid: number; interval: number; lastSync: string; lastResult: string; pairedAt: string };
type Cont = { pending: number; last?: string; error?: string; watchLimit?: boolean; active: boolean };
type Data = { installed: boolean; config: Config; continuous?: Cont; state: string; pairingCode?: string; pairingExpires?: string; interfaces?: Record<string, string> };

export default function HighAvailability() {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["ha"], queryFn: () => api.get<Data>("/api/ha"), refetchInterval: 3000 });
  const install = useAction(() => api.post("/api/system/components/ha/install"));
  const prepare = useAction(() => api.post("/api/ha/prepare"));
  const [f, setF] = useState({ peerHost: "", vip: "", interface: "", interval: 15, code: "", continuous: true });
  const [mode, setMode] = useState<{ continuous: boolean; interval: number } | null>(null);
  const saveMode = useAction(() => api.put("/api/ha/settings", mode), { success: tr("Mode de réplication enregistré"), onDone: () => setMode(null) });
  const pair = useAction(() => api.post("/api/ha/pair", f), { success: tr("NAS appairés : la première réplication démarre") });
  const sync = useAction(() => api.post("/api/ha/sync"));
  const back = useAction(() => api.post("/api/ha/switchback"));
  const maint = useAction(() => api.post("/api/ha/maintenance"));
  const endMaint = useAction(() => api.post("/api/ha/maintenance/end"), { success: tr("Maintenance terminée : la main revient automatiquement à ce NAS") });
  const unpair = useAction(() => api.post<{ warning?: string } | null>("/api/ha/unpair"), { success: (r) => r?.warning ?? tr("Appairage supprimé") });
  useEffect(() => {
    if (data?.interfaces && !f.interface) {
      const first = Object.keys(data.interfaces)[0];
      if (first) setF((x) => ({ ...x, interface: first }));
    }
  }, [data, f.interface]);

  const header = <PageHeader title={tr("Haute disponibilité")} subtitle={tr("Deux NAS, une adresse : le secondaire prend le relais si le principal tombe en panne")} icon={<ServerCog size={20} />} />;
  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) {
    return <div>{header}<Card><div className="flex flex-wrap items-center justify-between gap-3"><div className="text-muted text-[13px]">{tr("Les composants (keepalived, rsync) ne sont pas installés. Installez-les sur les deux NAS.")}</div><Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button></div></Card></div>;
  }
  const c = data.config;
  const active = data.state === "MASTER";

  if (c.role) {
    return (
      <div>
        {header}
        <ErrorBox error={sync.error || back.error || unpair.error || maint.error || endMaint.error} />
        <div className="space-y-5">
          <Card title={tr("État")} actions={<Badge tone={active ? "ok" : "neutral"} dot>{active ? tr("Actif : sert l'adresse {0}", c.vip) : data.state === "FAULT" ? tr("En défaut") : tr("En attente")}</Badge>}>
            <div className="grid gap-4 text-[13px] sm:grid-cols-3">
              <div><div className="text-muted text-[12px]">{tr("Rôle de ce NAS")}</div><b>{c.role === "primary" ? tr("Principal") : tr("Secondaire")}</b></div>
              <div><div className="text-muted text-[12px]">{tr("Autre NAS")}</div><div className="mono">{c.peerName} ({c.peerHost})</div></div>
              <div><div className="text-muted text-[12px]">{tr("Adresse partagée")}</div><div className="mono">{c.vip} · {c.interface}</div></div>
              <div><div className="text-muted text-[12px]">{tr("Dernière réplication")}</div><div>{c.lastSync && !c.lastSync.startsWith("0001") ? `${dateTime(c.lastSync)} (${ago(c.lastSync)})` : "—"}</div></div>
              <div className="sm:col-span-2"><div className="text-muted text-[12px]">{tr("Résultat")}</div><div className={c.lastResult && c.lastResult !== "OK" ? "text-danger" : ""}>{c.lastResult ? srv(c.lastResult) : "—"}</div></div>
              {c.role === "primary" && c.continuous && data.continuous && (
                <div className="sm:col-span-3">
                  <div className="text-muted text-[12px]">{tr("Réplication continue")}</div>
                  {!data.continuous.active ? <div className="text-muted">{tr("En pause (ce NAS ne sert pas l'adresse partagée)")}</div>
                    : data.continuous.error ? <div className="text-danger">{srv(data.continuous.error)}</div>
                    : data.continuous.pending > 0 ? <div>{tr("{0} dossier(s) en cours de copie", data.continuous.pending)}</div>
                    : <div className="text-ok">{tr("À jour")}{data.continuous.last && !data.continuous.last.startsWith("0001") ? ` · ${tr("dernière copie {0}", ago(data.continuous.last))}` : ""}</div>}
                  {data.continuous.watchLimit && <div className="text-warn text-[12px]">{tr("Trop de dossiers à suivre : certains ne sont copiés que par la réplication complète.")}</div>}
                </div>
              )}
            </div>
            {c.role === "secondary" && active && !c.autoReturn && <div className="mt-4"><Notice tone="warn">{tr("Ce NAS secondaire sert actuellement les partages : le principal ne répondait plus. Une fois le principal réparé et redémarré, rendez-lui la main : les modifications faites ici lui seront d'abord copiées.")}</Notice></div>}
            {c.role === "primary" && !active && c.maintenance && <div className="mt-4"><Notice>{tr("Maintenance en cours : le secondaire sert les partages. Vous pouvez redémarrer ou éteindre ce NAS ; à son retour, la main lui revient automatiquement après recopie des modifications.")}</Notice></div>}
            {c.role === "secondary" && active && c.autoReturn && <div className="mt-4"><Notice>{tr("Maintenance du principal : ce NAS sert les partages et rendra la main automatiquement dès le retour du principal.")}</Notice></div>}
            {c.role === "primary" && !active && !c.maintenance && <div className="mt-4"><Notice tone="warn">{tr("Le secondaire sert les partages depuis une panne de ce NAS. Rendez la main depuis le secondaire pour que ce NAS reprenne son rôle (ses données seront mises à jour).")}</Notice></div>}
            <div className="mt-5 flex flex-wrap gap-2">
              {c.role === "primary" && <Button icon={<RefreshCcw size={15} />} disabled={!active} loading={sync.isPending} onClick={() => sync.mutate()}>{tr("Répliquer maintenant")}</Button>}
              {c.role === "primary" && active && <Button icon={<Wrench size={15} />} loading={maint.isPending} onClick={async () => { if (await confirm({ title: tr("Passer en maintenance ?"), text: tr("Une dernière réplication est faite, puis le secondaire prend le relais. Vous pourrez redémarrer ce NAS ; au retour, la main lui revient automatiquement."), confirm: tr("Passer en maintenance") })) maint.mutate(); }}>{tr("Maintenance")}</Button>}
              {c.role === "primary" && !active && c.maintenance && <Button variant="primary" icon={<ArrowLeftRight size={15} />} loading={endMaint.isPending} onClick={() => endMaint.mutate()}>{tr("Terminer la maintenance")}</Button>}
              {c.role === "secondary" && <Button variant="primary" icon={<ArrowLeftRight size={15} />} disabled={!active} loading={back.isPending} onClick={async () => { if (await confirm({ title: tr("Rendre la main au principal ?"), text: tr("Les fichiers modifiés ici sont copiés vers le principal, puis l'adresse partagée lui revient. Les postes peuvent perdre la connexion quelques secondes."), confirm: tr("Rendre la main") })) back.mutate(); }}>{tr("Rendre la main au principal")}</Button>}
              <div className="flex-1" />
              <Button variant="danger" icon={<Unlink size={15} />} onClick={async () => { if (await confirm({ title: tr("Supprimer l'appairage ?"), text: tr("Les deux NAS redeviennent indépendants ; l'adresse partagée n'est plus servie. Les données déjà répliquées restent sur le secondaire."), danger: true })) unpair.mutate(); }}>{tr("Désappairer")}</Button>
            </div>
          </Card>
          {c.role === "primary" && (
            <Card title={tr("Mode de réplication")}>
              <div className="space-y-4">
                <Switch checked={(mode ?? c).continuous ?? false} onChange={(v) => setMode({ continuous: v, interval: (mode ?? c).interval })} label={tr("Réplication continue")} hint={tr("Les dossiers modifiés sont copiés sur le secondaire quelques secondes après chaque changement : en cas de panne, presque rien n'est perdu.")} />
                <Field label={tr("Réplication complète toutes les (minutes)")} hint={tr("Comptes, partages et vérification de tous les fichiers")}><Input type="number" min={1} className="max-w-[160px]" value={(mode ?? c).interval} onChange={(e) => setMode({ continuous: (mode ?? c).continuous ?? false, interval: Math.max(1, +e.target.value) })} /></Field>
                {mode && <div className="flex justify-end"><Button variant="primary" loading={saveMode.isPending} onClick={() => saveMode.mutate()}>{tr("Enregistrer")}</Button></div>}
                <ErrorBox error={saveMode.error} />
              </div>
            </Card>
          )}
          <Card title={tr("Fonctionnement")}>
            <ul className="text-muted list-disc space-y-1 pl-5 text-[13px]">
              <li>{tr("Les postes se connectent à l'adresse partagée {0} (SMB, NFS, WebDAV, interface web).", c.vip.split("/")[0])}</li>
              <li>{c.continuous ? tr("Chaque modification est copiée sur le secondaire en quelques secondes ; toutes les {0} minutes, une réplication complète vérifie aussi les comptes et tous les fichiers.", c.interval) : tr("Toutes les {0} minutes, le principal copie sur le secondaire les comptes, les dossiers partagés et leurs fichiers.", c.interval)}</li>
              <li>{tr("Si le principal ne répond plus pendant quelques secondes, le secondaire prend l'adresse et publie les partages, avec les données de la dernière réplication.")}</li>
              <li>{tr("Le principal ne reprend jamais la main tout seul : cela évite d'écraser des fichiers modifiés sur le secondaire pendant la panne.")}</li>
            </ul>
          </Card>
        </div>
      </div>
    );
  }

  return (
    <div>
      {header}
      <ErrorBox error={pair.error || prepare.error} />
      <div className="mb-5"><Notice>{tr("Il faut deux NAS StrataOS sur le même réseau local, avec des volumes portant les mêmes noms. Préparez d'abord le secondaire pour obtenir un code d'appairage, puis saisissez-le sur le principal.")}</Notice></div>
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Server size={16} className="text-accent" />{tr("Ce NAS sera le principal")}</span>}>
          <div className="space-y-4">
            <Field label={tr("Adresse du NAS secondaire")}><Input className="mono" value={f.peerHost} onChange={(e) => setF({ ...f, peerHost: e.target.value.trim() })} placeholder="192.168.1.21" /></Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Adresse partagée")} hint={tr("Libre sur le réseau, avec son préfixe")}><Input className="mono" value={f.vip} onChange={(e) => setF({ ...f, vip: e.target.value.trim() })} placeholder="192.168.1.20/24" /></Field>
              <Field label={tr("Interface")}>
                <Select value={f.interface} onChange={(e) => setF({ ...f, interface: e.target.value })}>
                  {Object.entries(data.interfaces ?? {}).map(([n, ip]) => <option key={n} value={n}>{n} ({ip})</option>)}
                </Select>
              </Field>
              <Field label={tr("Réplication toutes les (minutes)")}><Input type="number" min={1} value={f.interval} onChange={(e) => setF({ ...f, interval: Math.max(1, +e.target.value) })} /></Field>
              <Field label={tr("Code d'appairage")} hint={tr("Affiché sur le secondaire")}><Input className="mono uppercase" value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} placeholder="ABCDE-FGHIJ" /></Field>
            </div>
            <Switch checked={f.continuous} onChange={(v) => setF({ ...f, continuous: v })} label={tr("Réplication continue")} hint={tr("Copie chaque modification en quelques secondes, en plus de la réplication complète.")} />
            <div className="flex justify-end"><Button variant="primary" icon={<KeyRound size={15} />} loading={pair.isPending} disabled={!f.peerHost || !f.vip || !f.code} onClick={() => pair.mutate()}>{tr("Appairer")}</Button></div>
          </div>
        </Card>
        <Card title={<span className="flex items-center gap-2"><ServerCog size={16} className="text-accent" />{tr("Ce NAS sera le secondaire")}</span>}>
          <div className="space-y-4 text-[13px]">
            <p className="text-muted">{tr("Ses dossiers partagés seront remplacés par ceux du principal. Il ne les publie que pendant une panne du principal.")}</p>
            {data.pairingCode ? (
              <div className="space-y-2">
                <div className="text-muted text-[12px]">{tr("Code à saisir sur le principal (valable jusqu'à {0})", new Date(data.pairingExpires!).toLocaleTimeString())}</div>
                <div className="flex items-center gap-2"><code className="mono rounded-lg bg-panel-2 px-3 py-2 text-[18px] tracking-widest">{data.pairingCode}</code><Button size="sm" icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(data.pairingCode!); toast(tr("Code copié")); }}>{tr("Copier")}</Button></div>
                <div className="text-muted text-[12px]">{tr("Adresses de ce NAS : {0}", Object.values(data.interfaces ?? {}).map((x) => x.split("/")[0]).join(", "))}</div>
              </div>
            ) : (
              <Button icon={<KeyRound size={15} />} loading={prepare.isPending} onClick={() => prepare.mutate()}>{tr("Afficher un code d'appairage")}</Button>
            )}
          </div>
        </Card>
      </div>
    </div>
  );
}
