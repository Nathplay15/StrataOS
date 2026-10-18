import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { BookUser, CheckCircle2, LogOut, Plug, XCircle } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, PageHeader, Spinner, Switch, Tabs } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";

type Config = { adminGroup?: string; type: "" | "ad" | "ldap"; realm: string; workgroup: string; server: string; uri?: string; baseDn?: string; bindDn?: string; bindPassword?: string; startTls?: boolean; joinedAt: string };
type Data = { config: Config; adInstalled: boolean; ldapInstalled: boolean; joined: boolean; status?: { connected: boolean; message: string } };

export default function Directory() {
  const { data, isLoading, error } = useQuery({ queryKey: ["directory"], queryFn: () => api.get<Data>("/api/directory"), refetchInterval: 15000 });
  const install = useAction(() => api.post("/api/system/components/directory/install"));
  const [kind, setKind] = useState<"ad" | "ldap">("ad");
  const [ad, setAD] = useState({ realm: "", workgroup: "", server: "", admin: "Administrator", password: "", adminGroup: "" });
  const [ldap, setLDAP] = useState({ uri: "ldap://", baseDn: "", bindDn: "", bindPassword: "", startTls: true, adminGroup: "" });
  const [leaving, setLeaving] = useState(false);
  const join = useAction(() => api.post("/api/directory/join", kind === "ad" ? { type: "ad", ...ad } : { type: "ldap", ...ldap }), { onDone: () => setAD((x) => ({ ...x, password: "" })) });
  const header = <PageHeader title={tr("Domain / LDAP")} subtitle={tr("Use accounts from an Active Directory or LDAP directory")} icon={<BookUser size={20} />} />;

  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.adInstalled || !data.ldapInstalled) {
    return <div>{header}<Card><div className="flex flex-wrap items-center justify-between gap-3"><div className="text-muted text-[13px]">{tr("The Active Directory (Samba winbind, Kerberos) and LDAP (sssd) clients are not installed.")}</div><Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button></div></Card></div>;
  }

  if (data.joined) {
    const c = data.config;
    return (
      <div>
        {header}
        <Card title={tr("Directory")} actions={<Button variant="danger" icon={<LogOut size={15} />} onClick={() => setLeaving(true)}>{tr("Quit")}</Button>}>
          <div className="space-y-3 text-[13px]">
            <div className="flex items-center gap-2">{data.status?.connected ? <CheckCircle2 size={17} className="text-ok" /> : <XCircle size={17} className="text-danger" />}<span>{srv(data.status?.message)}</span></div>
            <div className="grid gap-3 sm:grid-cols-3">
              <div><div className="text-muted text-[12px]">{tr("Type")}</div><Badge tone="accent">{c.type === "ad" ? tr("Active Directory") : "LDAP"}</Badge></div>
              <div><div className="text-muted text-[12px]">{c.type === "ad" ? tr("Domain") : tr("Server")}</div><div className="mono">{c.type === "ad" ? `${c.realm} (${c.workgroup})` : c.uri}</div></div>
              <div><div className="text-muted text-[12px]">{tr("Since")}</div><div>{dateTime(c.joinedAt)}</div></div>
            </div>
            <AdminGroup initial={c.adminGroup ?? ""} />
            <Notice>{tr("Directory accounts sign in to the web interface and WebDAV with their usual username and password (for example marie, or DOMAIN\\marie for Active Directory). Their StrataOS profile is created at the first sign-in.")}</Notice>
            {c.type === "ad" ? (
              <Notice>{tr("Domain users and groups are added to shared folder permissions (Control Panel → Shared Folders), in the form")}{" "}<span className="mono">{c.workgroup}\{tr("user")}</span>. {tr("They connect over SMB with their domain account.")}</Notice>
            ) : (
              <Notice>{tr("LDAP (POSIX) accounts are recognized by the system: they can be granted folder permissions (ACLs, NFS with consistent UIDs). SMB access requires a directory with the Samba schema; prefer Active Directory for Windows clients.")}</Notice>
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
                <Field label={tr("Domain")} hint={tr("DNS name of the domain, e.g. company.local")}><Input value={ad.realm} onChange={(e) => setAD({ ...ad, realm: e.target.value })} placeholder="entreprise.local" /></Field>
                <Field label={tr("NetBIOS name (optional)")} hint={tr("Derived from the domain if empty, e.g. COMPANY")}><Input value={ad.workgroup} onChange={(e) => setAD({ ...ad, workgroup: e.target.value })} /></Field>
                <Field label={tr("Domain controller (optional)")} hint={tr("Found via DNS if empty")} className="sm:col-span-2"><Input value={ad.server} onChange={(e) => setAD({ ...ad, server: e.target.value })} placeholder="dc1.entreprise.local" /></Field>
                <Field label={tr("StrataOS administrators group (optional)")} hint={tr("Its members administer the NAS, e.g. Domain Admins")} className="sm:col-span-2"><Input value={ad.adminGroup} onChange={(e) => setAD({ ...ad, adminGroup: e.target.value })} placeholder="Domain Admins" /></Field>
                <Field label={tr("Domain administrator")}><Input value={ad.admin} onChange={(e) => setAD({ ...ad, admin: e.target.value })} autoComplete="off" /></Field>
                <Field label={tr("Password")} hint={tr("Used only once for joining, never saved")}><Input type="password" value={ad.password} onChange={(e) => setAD({ ...ad, password: e.target.value })} autoComplete="new-password" /></Field>
              </div>
              <Notice tone="warn">{tr("The NAS DNS server must be the domain's (usually the domain controller): set it in Network if needed. Samba becomes a domain member server; local StrataOS accounts keep working.")}</Notice>
            </>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Server")} hint={tr("ldap://host or ldaps://host")}><Input className="mono" value={ldap.uri} onChange={(e) => setLDAP({ ...ldap, uri: e.target.value })} /></Field>
              <Field label={tr("Base DN")}><Input className="mono" value={ldap.baseDn} onChange={(e) => setLDAP({ ...ldap, baseDn: e.target.value })} placeholder="dc=entreprise,dc=local" /></Field>
              <Field label={tr("Read-only account (optional)")} hint={tr("Empty for an anonymous connection")}><Input className="mono" value={ldap.bindDn} onChange={(e) => setLDAP({ ...ldap, bindDn: e.target.value })} placeholder="cn=lecteur,dc=entreprise,dc=local" /></Field>
              <Field label={tr("Account password")}><Input type="password" value={ldap.bindPassword} onChange={(e) => setLDAP({ ...ldap, bindPassword: e.target.value })} autoComplete="new-password" /></Field>
              <Field label={tr("StrataOS administrators group (optional)")} hint={tr("Its members administer the NAS")} className="sm:col-span-2"><Input value={ldap.adminGroup} onChange={(e) => setLDAP({ ...ldap, adminGroup: e.target.value })} /></Field>
              <div className="sm:col-span-2"><Switch checked={ldap.startTls} onChange={(v) => setLDAP({ ...ldap, startTls: v })} label={tr("Encrypt with StartTLS")} hint={tr("Not needed with ldaps://")} /></div>
            </div>
          )}
          <div className="flex justify-end"><Button variant="primary" loading={join.isPending} disabled={kind === "ad" ? !ad.realm || !ad.admin || !ad.password : !ldap.baseDn} onClick={() => join.mutate()}>{kind === "ad" ? tr("Join the domain") : tr("Sign in to the directory")}</Button></div>
        </div>
      </Card>
    </div>
  );
}

function AdminGroup({ initial }: { initial: string }) {
  const [g, setG] = useState(initial);
  const save = useAction(() => api.put("/api/directory/settings", { adminGroup: g }), { success: tr("Administrators group saved") });
  return (
    <div className="flex flex-wrap items-end gap-2">
      <Field label={tr("StrataOS administrators group")} hint={tr("Empty: directory accounts are regular users (can be changed in Users)")} className="min-w-64 flex-1"><Input value={g} onChange={(e) => setG(e.target.value)} placeholder="Domain Admins" /></Field>
      <Button disabled={g === initial} loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </div>
  );
}

function Leave({ ad, onClose }: { ad: boolean; onClose: () => void }) {
  const [admin, setAdmin] = useState("Administrator");
  const [password, setPassword] = useState("");
  const leave = useAction(() => api.post<{ warning?: string } | null>("/api/directory/leave", ad ? { admin, password } : {}), { success: (r) => r?.warning ?? tr("Directory left"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Leave directory")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="danger" loading={leave.isPending} onClick={() => leave.mutate()}>{tr("Quit")}</Button></>}>
      <div className="space-y-4">
        <p className="text-[13px]">{tr("Directory accounts lose access to the NAS. Their permissions remain stored on the shared folders and will return if the NAS rejoins the same directory.")}</p>
        {ad && (
          <>
            <Notice>{tr("With an administrator account, the NAS computer account is also removed from the domain. Without one, only the local configuration is removed.")}</Notice>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Domain administrator")}><Input value={admin} onChange={(e) => setAdmin(e.target.value)} /></Field>
              <Field label={tr("Password")}><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" /></Field>
            </div>
          </>
        )}
        <ErrorBox error={leave.error} />
      </div>
    </Modal>
  );
}
