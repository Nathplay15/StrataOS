import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Edit3, LogIn, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { srv, tr } from "../lib/i18n";
import { Badge, Button, Card, Checkbox, ErrorBox, Field, Input, Modal, Notice, Spinner, Switch, useUI } from "./ui";
import { TLSTrustField } from "./TLSTrust";

interface Provider {
  id: string;
  name: string;
  issuer: string;
  clientId: string;
  clientSecret?: string;
  hasSecret?: boolean;
  scopes?: string[];
  enabled: boolean;
  allowHttp?: boolean;
  usernameClaim?: string;
  emailClaim?: string;
  groupsClaim?: string;
  autoCreate?: boolean;
  adminGroups?: string[];
  allowedGroups?: string[];
  groupMap?: Record<string, string>;
  trustMfa?: boolean;
  mfaValues?: string[];
  acrValues?: string;
  accounts?: number;
  tlsCert?: string;
  tlsFingerprint?: string;
}

const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);
const empty: Provider = { id: "", name: "", issuer: "", clientId: "", enabled: true, autoCreate: false, trustMfa: false };

/** Settings card "Sign-in with an identity provider" (OpenID Connect). */
export function SignInProviders() {
  const qc = useQueryClient();
  const { confirm, toast } = useUI();
  const { data, error } = useQuery({ queryKey: ["oidc-providers"], queryFn: () => api.get<{ providers: Provider[]; redirectUri: string }>("/api/security/oidc") });
  const [edit, setEdit] = useState<{ p: Provider; isNew: boolean } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [test, setTest] = useState<{ ok: boolean; error?: string; keys?: number; userinfo?: string } | null>(null);
  // The comma-separated fields, as typed.
  const [txt, setTxt] = useState({ scopes: "", admin: "", allowed: "", map: "", mfa: "" });

  const open = (p: Provider, isNew: boolean) => {
    setEdit({ p: { ...p, clientSecret: "" }, isNew });
    setTxt({ scopes: (p.scopes ?? []).join(", "), admin: (p.adminGroups ?? []).join(", "), allowed: (p.allowedGroups ?? []).join(", "),
      map: Object.entries(p.groupMap ?? {}).map(([k, v]) => `${k}=${v}`).join(", "), mfa: (p.mfaValues ?? []).join(", ") });
    setErr(null);
    setTest(null);
  };
  const save = async () => {
    if (!edit) return;
    setBusy(true);
    setErr(null);
    const groupMap: Record<string, string> = {};
    for (const pair of list(txt.map)) {
      const [k, v] = pair.split("=");
      if (k && v) groupMap[k.trim()] = v.trim();
    }
    const body = { ...edit.p, scopes: list(txt.scopes), adminGroups: list(txt.admin), allowedGroups: list(txt.allowed), groupMap, mfaValues: list(txt.mfa) };
    try {
      if (edit.isNew) await api.post("/api/security/oidc", body);
      else await api.put(`/api/security/oidc/${edit.p.id}`, body);
      toast(tr("Saved"));
      setEdit(null);
      qc.invalidateQueries({ queryKey: ["oidc-providers"] });
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const check = async () => {
    if (!edit) return;
    setTest(null);
    try { setTest(await api.post("/api/security/oidc/test", { issuer: edit.p.issuer, allowHttp: edit.p.allowHttp, tlsCert: edit.p.tlsCert })); } catch (e) { setTest({ ok: false, error: String((e as Error).message) }); }
  };
  const remove = async (p: Provider) => {
    if (!(await confirm({ title: tr("Remove {0}?", p.name), text: tr("Accounts linked to it can no longer sign in with it (their password keeps working)."), danger: true }))) return;
    await api.del(`/api/security/oidc/${p.id}`);
    qc.invalidateQueries({ queryKey: ["oidc-providers"] });
  };
  const set = (k: Partial<Provider>) => edit && setEdit({ ...edit, p: { ...edit.p, ...k } });

  return (
    <Card id="settings-sso" className="xl:col-span-2" title={<span className="flex items-center gap-2"><LogIn size={16} /> {" "}{tr("Sign-in with an identity provider (OpenID Connect)")}</span>}
      actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => open(empty, true)}>{tr("Add")}</Button>}>
      {!data ? <><ErrorBox error={error} /><Spinner /></> : (
        <div className="space-y-3">
          <p className="text-[13px] text-muted">{tr("Authentik, Keycloak, Authelia, Google, Microsoft Entra… add a \"Sign in with\" button to the sign-in page. The password sign-in of local accounts always stays, as a way in if the provider is down.")}</p>
          <Field label={tr("Redirect URI to register at the provider")} hint={tr("One per address you open the NAS with (name, domain, port).")}>
            <div className="flex items-center gap-2">
              <code className="mono min-w-0 flex-1 truncate rounded-lg bg-panel-2 px-2.5 py-1.5 text-[12.5px]">{data.redirectUri}</code>
              <Button size="sm" variant="ghost" icon={<Copy size={14} />} title={tr("Copy")} onClick={() => { navigator.clipboard?.writeText(data.redirectUri); toast(tr("Copied")); }} />
            </div>
          </Field>
          {data.providers.length === 0 ? <p className="text-muted text-[13px]">{tr("No provider.")}</p> : (
            <div className="divide-y divide-border rounded-xl border border-border">
              {data.providers.map((p) => (
                <div key={p.id} className="flex items-center gap-3 px-4 py-2.5">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5 font-medium">{p.name} {!p.enabled && <Badge>{tr("Off")}</Badge>} {p.autoCreate && <Badge tone="accent">{tr("Creates accounts")}</Badge>} {p.trustMfa && <Badge tone="ok">{tr("Trusts its second factor")}</Badge>}</div>
                    <div className="mono truncate text-muted text-[12px]">{p.issuer} · {tr("{0} account(s)", p.accounts ?? 0)}</div>
                  </div>
                  <Button size="sm" variant="ghost" title={tr("Edit")} icon={<Edit3 size={14} />} onClick={() => open(p, false)} />
                  <Button size="sm" variant="ghost" className="text-danger" title={tr("Delete")} icon={<Trash2 size={14} />} onClick={() => remove(p)} />
                </div>
              ))}
            </div>
          )}
        </div>
      )}
      <Modal open={!!edit} onClose={() => setEdit(null)} title={edit?.isNew ? tr("New identity provider") : edit?.p.name ?? ""} wide
        footer={<><Button onClick={() => setEdit(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={busy} onClick={save}>{tr("Save")}</Button></>}>
        {edit && (
          <div className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={tr("Identifier")} hint={tr("Lowercase letters, digits and -; in the addresses")}><Input value={edit.p.id} disabled={!edit.isNew} onChange={(e) => set({ id: e.target.value })} placeholder="authentik" /></Field>
              <Field label={tr("Button name")}><Input value={edit.p.name} onChange={(e) => set({ name: e.target.value })} placeholder="Authentik" /></Field>
              <Field label={tr("Issuer URL")} className="sm:col-span-2" hint={tr("e.g. https://auth.example.com/application/o/nas/ or https://login.microsoftonline.com/<tenant>/v2.0")}>
                <div className="flex gap-2"><Input value={edit.p.issuer} onChange={(e) => set({ issuer: e.target.value })} /><Button onClick={check}>{tr("Test")}</Button></div>
              </Field>
              <div className="sm:col-span-2"><TLSTrustField address={edit.p.issuer} value={{ tlsCert: edit.p.tlsCert, tlsFingerprint: edit.p.tlsFingerprint }} onChange={(v) => set({ tlsCert: v.tlsCert, tlsFingerprint: v.tlsFingerprint })} allowInsecure={false} note={tr("A self-hosted provider whose certificate comes from a private authority is refused: check its certificate, then trust it for this provider only. Certificate errors can never be ignored for sign-in.")} /></div>
              {test && <div className="sm:col-span-2">{test.ok ? <Notice>{tr("Provider found: {0} signing key(s).", test.keys ?? 0)}</Notice> : <Notice tone="warn">{srv(test.error)}</Notice>}</div>}
              <Field label={tr("Client ID")}><Input value={edit.p.clientId} onChange={(e) => set({ clientId: e.target.value })} /></Field>
              <Field label={tr("Client secret")} hint={edit.p.hasSecret ? tr("Saved; leave blank to keep it, \"-\" to remove it") : tr("Blank for a public client (PKCE only)")}><Input type="password" autoComplete="new-password" value={edit.p.clientSecret ?? ""} onChange={(e) => set({ clientSecret: e.target.value })} /></Field>
              <Field label={tr("Scopes")} hint={tr("Besides openid; default: profile, email")}><Input value={txt.scopes} onChange={(e) => setTxt({ ...txt, scopes: e.target.value })} placeholder="profile, email, groups" /></Field>
              <Field label={tr("Claims: user name, email, groups")} hint={tr("Default: preferred_username, email, groups. A dotted path reads a nested claim (realm_access.roles).")}>
                <div className="grid grid-cols-3 gap-1">
                  <Input value={edit.p.usernameClaim ?? ""} placeholder="preferred_username" onChange={(e) => set({ usernameClaim: e.target.value })} />
                  <Input value={edit.p.emailClaim ?? ""} placeholder="email" onChange={(e) => set({ emailClaim: e.target.value })} />
                  <Input value={edit.p.groupsClaim ?? ""} placeholder="groups" onChange={(e) => set({ groupsClaim: e.target.value })} />
                </div>
              </Field>
            </div>
            <Switch checked={edit.p.enabled} onChange={(v) => set({ enabled: v })} label={tr("Show the button on the sign-in page")} />
            <Switch checked={!!edit.p.autoCreate} onChange={(v) => set({ autoCreate: v })} label={tr("Create an account at the first sign-in")}
              hint={tr("Otherwise only accounts linked by their owner (My account → Security keys and sign-in) may sign in. An existing account of the same name is never taken over.")} />
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={tr("Groups allowed to sign in")} hint={tr("Comma-separated; empty: everybody the provider accepts")}><Input value={txt.allowed} onChange={(e) => setTxt({ ...txt, allowed: e.target.value })} /></Field>
              <Field label={tr("Administrator groups")} hint={tr("For accounts created by the provider: members are administrators, others users")}><Input value={txt.admin} onChange={(e) => setTxt({ ...txt, admin: e.target.value })} placeholder="nas-admins" /></Field>
              <Field label={tr("Group mapping")} className="sm:col-span-2" hint={tr("provider group=StrataOS group, comma-separated; kept in sync at each sign-in of accounts it created")}><Input value={txt.map} onChange={(e) => setTxt({ ...txt, map: e.target.value })} placeholder="family=famille, staff=bureau" /></Field>
            </div>
            <Switch checked={!!edit.p.trustMfa} onChange={(v) => set({ trustMfa: v })} label={tr("Trust the provider's second factor")}
              hint={tr("When the ID token says a second factor was checked (amr: otp, hwk…, or one of the values below), the NAS does not ask for its own and the administrators' two-factor rule is met (a security key rule needs amr hwk).")} />
            {edit.p.trustMfa && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label={tr("amr/acr values meaning a second factor")} hint={tr("Default: mfa, otp, hwk, swk, fpt…")}><Input value={txt.mfa} onChange={(e) => setTxt({ ...txt, mfa: e.target.value })} /></Field>
                <Field label={tr("acr_values to ask for")} hint={tr("Optional, e.g. to make the provider require its second factor")}><Input value={edit.p.acrValues ?? ""} onChange={(e) => set({ acrValues: e.target.value })} /></Field>
              </div>
            )}
            <Checkbox checked={!!edit.p.allowHttp} onChange={(v) => set({ allowHttp: v })} label={<span className="text-[13px]">{tr("Allow a provider over plain HTTP (test only: tokens travel unencrypted)")}</span>} />
            <ErrorBox error={err} />
          </div>
        )}
      </Modal>
    </Card>
  );
}
