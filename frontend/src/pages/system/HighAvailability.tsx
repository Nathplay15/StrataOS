import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeftRight, Copy, KeyRound, Plug, Plus, RefreshCcw, Server, ServerCog, Unlink, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Notice, Modal, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type Config = { continuous?: boolean; maintenance?: boolean; autoReturn?: boolean; role: "" | "primary" | "secondary"; peerHost: string; peerName: string; vip: string; interface: string; vrid: number; interval: number; lastSync: string; lastResult: string; pairedAt: string };
type Sec = { host: string; name: string; priority: number; pathMap?: Record<string, string>; lastSync: string; lastResult: string; state: string };
type Cont = { pending: number; last?: string; error?: string; watchLimit?: boolean; active: boolean };
type Data = { installed: boolean; config: Config; continuous?: Cont; secondaries?: Sec[]; state: string; pairingCode?: string; pairingExpires?: string; interfaces?: Record<string, string> };

/** Volume mapping: the volume of the secondary for each volume of this NAS. */
function VolumeMap({ value, onChange }: { value: Record<string, string>; onChange: (m: Record<string, string>) => void }) {
  const locs = useQuery({ queryKey: ["locations"], queryFn: () => api.get<{ path: string; label: string }[]>("/api/storage/locations") });
  const vols = (locs.data ?? []).filter((l) => l.path.split("/").length === 4); // /srv/strata/<volume>
  if (!vols.length) return null;
  return (
    <div className="space-y-2">
      <div className="text-muted text-[12.5px]">{tr("Volume correspondant sur le secondaire (laissez le même nom s'il existe là-bas) :")}</div>
      {vols.map((v) => {
        const name = v.path.split("/").pop()!;
        const cur = (value[v.path] ?? v.path).split("/").pop()!;
        return (
          <div key={v.path} className="flex items-center gap-2 text-[13px]">
            <span className="mono w-40 truncate">{name}</span><span className="text-muted">→</span>
            <Input className="mono max-w-[200px]" value={cur} onChange={(e) => { const n = e.target.value.trim().replace(/[^A-Za-z0-9_.-]/g, ""); const m = { ...value }; if (!n || n === name) delete m[v.path]; else m[v.path] = `/srv/strata/${n}`; onChange(m); }} />
          </div>
        );
      })}
    </div>
  );
}

function Secondaries({ data }: { data: Data }) {
  const { confirm } = useUI();
  const [f, setF] = useState<{ peerHost: string; code: string; pathMap: Record<string, string> } | null>(null);
  const add = useAction(() => api.post("/api/ha/pair", f), { success: tr("NAS secondaire ajouté : la réplication démarre"), onDone: () => setF(null) });
  const remove = useAction((host: string) => api.del<{ warning?: string } | null>(`/api/ha/secondaries/${encodeURIComponent(host)}`), { success: (r) => r?.warning ?? tr("NAS secondaire retiré") });
  const list = data.secondaries ?? [];
  return (
    <Card title={tr("NAS secondaires")} actions={<Button size="sm" icon={<Plus size={14} />} disabled={data.state !== "MASTER"} onClick={() => setF({ peerHost: "", code: "", pathMap: {} })}>{tr("Ajouter un NAS secondaire")}</Button>}>
      <div className="space-y-2">
        {list.map((x, i) => (
          <div key={x.host} className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-2.5 text-[13px]">
            <div className="min-w-0 flex-1">
              <div><b>{x.name || x.host}</b> <span className="mono text-muted">{x.host}</span> <span className="text-muted">· {tr("ordre de relève {0}", i + 1)}</span></div>
              <div className="text-muted text-[12px]">{x.lastSync && !x.lastSync.startsWith("0001") ? tr("Dernière réplication {0}", ago(x.lastSync)) : tr("Pas encore répliqué")}{x.lastResult && x.lastResult !== "OK" ? <span className="text-danger"> · {srv(x.lastResult)}</span> : ""}</div>
              {x.pathMap && Object.keys(x.pathMap).length > 0 && <div className="mono text-muted text-[11.5px]">{Object.entries(x.pathMap).map(([a, b]) => `${a.split("/").pop()} → ${b.split("/").pop()}`).join(", ")}</div>}
            </div>
            <Badge tone={x.state === "BACKUP" ? "ok" : x.state === "MASTER" ? "warn" : "danger"} dot>{x.state === "BACKUP" ? tr("Prêt") : x.state === "MASTER" ? tr("Actif") : tr("Injoignable")}</Badge>
            {list.length > 1 && <Button size="sm" variant="ghost" icon={<Unlink size={14} />} onClick={async () => { if (await confirm({ title: tr("Retirer {0} ?", x.name || x.host), text: tr("Ce NAS ne reçoit plus les réplications et ne prend plus le relais. Ses données restent en place."), danger: true })) remove.mutate(x.host); }} />}
          </div>
        ))}
        <p className="text-muted text-[12px]">{tr("En cas de panne, le premier secondaire disponible dans l'ordre de relève prend l'adresse partagée.")}</p>
      </div>
      <Modal open={!!f} onClose={() => setF(null)} title={tr("Ajouter un NAS secondaire")} footer={<><Button onClick={() => setF(null)}>{tr("Annuler")}</Button><Button variant="primary" loading={add.isPending} disabled={!f?.peerHost || !f.code} onClick={() => add.mutate()}>{tr("Appairer")}</Button></>}>
        {f && <div className="space-y-4">
          <Notice>{tr("Sur le nouveau NAS, ouvrez Haute disponibilité et affichez un code d'appairage.")}</Notice>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Adresse du NAS secondaire")}><Input className="mono" value={f.peerHost} onChange={(e) => setF({ ...f, peerHost: e.target.value.trim() })} placeholder="192.168.1.22" /></Field>
            <Field label={tr("Code d'appairage")}><Input className="mono uppercase" value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} placeholder="ABCDE-FGHIJ" /></Field>
          </div>
          <VolumeMap value={f.pathMap} onChange={(m) => setF({ ...f, pathMap: m })} />
          <ErrorBox error={add.error} />
        </div>}
      </Modal>
    </Card>
  );
}

export default function HighAvailability() {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["ha"], queryFn: () => api.get<Data>("/api/ha"), refetchInterval: 3000 });
  const install = useAction(() => api.post("/api/system/components/ha/install"));
  const prepare = useAction(() => api.post("/api/ha/prepare"));
  const [f, setF] = useState<{ peerHost: string; vip: string; interface: string; interval: number; code: string; continuous: boolean; pathMap: Record<string, string> }>({ peerHost: "", vip: "", interface: "", interval: 15, code: "", continuous: true, pathMap: {} });
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
          {c.role === "primary" && <Secondaries data={data} />}
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
      <div className="mb-5"><Notice>{tr("Il faut au moins deux NAS StrataOS sur le même réseau local. Préparez d'abord le secondaire pour obtenir un code d'appairage, puis saisissez-le sur le principal ; si ses volumes portent d'autres noms, indiquez la correspondance. D'autres secondaires pourront être ajoutés ensuite.")}</Notice></div>
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
            <VolumeMap value={f.pathMap} onChange={(m) => setF({ ...f, pathMap: m })} />
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
