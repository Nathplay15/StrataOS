import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { BookUser, CheckCircle2, LogOut, Plug, XCircle } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, PageHeader, Spinner, Switch, Tabs } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Config = { type: "" | "ad" | "ldap"; realm: string; workgroup: string; server: string; uri?: string; baseDn?: string; bindDn?: string; bindPassword?: string; startTls?: boolean; joinedAt: string };
type Data = { config: Config; adInstalled: boolean; ldapInstalled: boolean; joined: boolean; status?: { connected: boolean; message: string } };

export default function Directory() {
  const { data, isLoading, error } = useQuery({ queryKey: ["directory"], queryFn: () => api.get<Data>("/api/directory"), refetchInterval: 15000 });
  const install = useAction(() => api.post("/api/system/components/directory/install"));
  const [kind, setKind] = useState<"ad" | "ldap">("ad");
  const [ad, setAD] = useState({ realm: "", workgroup: "", server: "", admin: "Administrator", password: "" });
  const [ldap, setLDAP] = useState({ uri: "ldap://", baseDn: "", bindDn: "", bindPassword: "", startTls: true });
  const [leaving, setLeaving] = useState(false);
  const join = useAction(() => api.post("/api/directory/join", kind === "ad" ? { type: "ad", ...ad } : { type: "ldap", ...ldap }), { onDone: () => setAD((x) => ({ ...x, password: "" })) });
  const header = <PageHeader title={tr("Domaine / LDAP")} subtitle={tr("Utiliser les comptes d'un annuaire Active Directory ou LDAP")} icon={<BookUser size={20} />} />;

  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.adInstalled || !data.ldapInstalled) {
    return <div>{header}<Card><div className="flex flex-wrap items-center justify-between gap-3"><div className="text-muted text-[13px]">{tr("Les clients Active Directory (Samba winbind, Kerberos) et LDAP (sssd) ne sont pas installés.")}</div><Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button></div></Card></div>;
  }

  if (data.joined) {
    const c = data.config;
    return (
      <div>
        {header}
        <Card title={tr("Annuaire")} actions={<Button variant="danger" icon={<LogOut size={15} />} onClick={() => setLeaving(true)}>{tr("Quitter")}</Button>}>
          <div className="space-y-3 text-[13px]">
            <div className="flex items-center gap-2">{data.status?.connected ? <CheckCircle2 size={17} className="text-ok" /> : <XCircle size={17} className="text-danger" />}<span>{data.status?.message}</span></div>
            <div className="grid gap-3 sm:grid-cols-3">
              <div><div className="text-muted text-[12px]">{tr("Type")}</div><Badge tone="accent">{c.type === "ad" ? tr("Active Directory") : "LDAP"}</Badge></div>
              <div><div className="text-muted text-[12px]">{c.type === "ad" ? tr("Domaine") : tr("Serveur")}</div><div className="mono">{c.type === "ad" ? `${c.realm} (${c.workgroup})` : c.uri}</div></div>
              <div><div className="text-muted text-[12px]">{tr("Depuis")}</div><div>{dateTime(c.joinedAt)}</div></div>
            </div>
            {c.type === "ad" ? (
              <Notice>{tr("Les utilisateurs et groupes du domaine s'ajoutent aux droits des dossiers partagés (Panneau de configuration → Dossiers partagés), sous la forme")}{" "}<span className="mono">{c.workgroup}\{tr("utilisateur")}</span>. {tr("Ils se connectent en SMB avec leur compte du domaine.")}</Notice>
            ) : (
              <Notice>{tr("Les comptes LDAP (POSIX) sont reconnus par le système : ils peuvent recevoir des droits sur les dossiers (ACL, NFS avec des UID cohérents). L'accès SMB demande un annuaire avec le schéma Samba ; préférez Active Directory pour les postes Windows.")}</Notice>
            )}
          </div>
        </Card>
        {leaving && <Leave ad={c.type === "ad"} onClose={() => setLeaving(false)} />}
      </div>
    );
  }

  return (
    <div>
      {header}
      <ErrorBox error={join.error} />
      <Card>
        <div className="space-y-5">
          <Tabs tabs={[{ id: "ad", label: tr("Active Directory") }, { id: "ldap", label: "LDAP" }]} value={kind} onChange={setKind} />
          {kind === "ad" ? (
            <>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Domaine")} hint={tr("Nom DNS du domaine, ex. entreprise.local")}><Input value={ad.realm} onChange={(e) => setAD({ ...ad, realm: e.target.value })} placeholder="entreprise.local" /></Field>
                <Field label={tr("Nom NetBIOS (optionnel)")} hint={tr("Déduit du domaine si vide, ex. ENTREPRISE")}><Input value={ad.workgroup} onChange={(e) => setAD({ ...ad, workgroup: e.target.value })} /></Field>
                <Field label={tr("Contrôleur de domaine (optionnel)")} hint={tr("Trouvé par le DNS si vide")} className="sm:col-span-2"><Input value={ad.server} onChange={(e) => setAD({ ...ad, server: e.target.value })} placeholder="dc1.entreprise.local" /></Field>
                <Field label={tr("Administrateur du domaine")}><Input value={ad.admin} onChange={(e) => setAD({ ...ad, admin: e.target.value })} autoComplete="off" /></Field>
                <Field label={tr("Mot de passe")} hint={tr("Utilisé une seule fois pour la jonction, jamais enregistré")}><Input type="password" value={ad.password} onChange={(e) => setAD({ ...ad, password: e.target.value })} autoComplete="new-password" /></Field>
              </div>
              <Notice tone="warn">{tr("Le serveur DNS du NAS doit être celui du domaine (en général le contrôleur de domaine) : réglez-le dans Réseau si besoin. Samba devient serveur membre du domaine ; les comptes StrataOS locaux continuent de fonctionner.")}</Notice>
            </>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Serveur")} hint={tr("ldap://hôte ou ldaps://hôte")}><Input className="mono" value={ldap.uri} onChange={(e) => setLDAP({ ...ldap, uri: e.target.value })} /></Field>
              <Field label={tr("Base DN")}><Input className="mono" value={ldap.baseDn} onChange={(e) => setLDAP({ ...ldap, baseDn: e.target.value })} placeholder="dc=entreprise,dc=local" /></Field>
              <Field label={tr("Compte de lecture (optionnel)")} hint={tr("Vide pour une connexion anonyme")}><Input className="mono" value={ldap.bindDn} onChange={(e) => setLDAP({ ...ldap, bindDn: e.target.value })} placeholder="cn=lecteur,dc=entreprise,dc=local" /></Field>
              <Field label={tr("Mot de passe du compte")}><Input type="password" value={ldap.bindPassword} onChange={(e) => setLDAP({ ...ldap, bindPassword: e.target.value })} autoComplete="new-password" /></Field>
              <div className="sm:col-span-2"><Switch checked={ldap.startTls} onChange={(v) => setLDAP({ ...ldap, startTls: v })} label={tr("Chiffrer avec StartTLS")} hint={tr("Inutile avec ldaps://")} /></div>
            </div>
          )}
          <div className="flex justify-end"><Button variant="primary" loading={join.isPending} disabled={kind === "ad" ? !ad.realm || !ad.admin || !ad.password : !ldap.baseDn} onClick={() => join.mutate()}>{kind === "ad" ? tr("Joindre le domaine") : tr("Se connecter à l'annuaire")}</Button></div>
        </div>
      </Card>
    </div>
  );
}

function Leave({ ad, onClose }: { ad: boolean; onClose: () => void }) {
  const [admin, setAdmin] = useState("Administrator");
  const [password, setPassword] = useState("");
  const leave = useAction(() => api.post<{ warning?: string } | null>("/api/directory/leave", ad ? { admin, password } : {}), { success: (r) => r?.warning ?? tr("Annuaire quitté"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Quitter l'annuaire")} footer={<><Button onClick={onClose}>{tr("Annuler")}</Button><Button variant="danger" loading={leave.isPending} onClick={() => leave.mutate()}>{tr("Quitter")}</Button></>}>
      <div className="space-y-4">
        <p className="text-[13px]">{tr("Les comptes de l'annuaire perdent l'accès au NAS. Leurs droits restent enregistrés sur les dossiers partagés et reviendront si le NAS rejoint le même annuaire.")}</p>
        {ad && (
          <>
            <Notice>{tr("Avec un compte administrateur, le compte ordinateur du NAS est aussi supprimé du domaine. Sans, seule la configuration locale est retirée.")}</Notice>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Administrateur du domaine")}><Input value={admin} onChange={(e) => setAdmin(e.target.value)} /></Field>
              <Field label={tr("Mot de passe")}><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" /></Field>
            </div>
          </>
        )}
        <ErrorBox error={leave.error} />
      </div>
    </Modal>
  );
}
