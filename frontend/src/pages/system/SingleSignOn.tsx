import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, KeySquare, LogOut, Pencil, Plus, RefreshCcw, Settings2, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useJobs } from "../../lib/jobs";
import { dateTime } from "../../lib/format";
import { srv, tr } from "../../lib/i18n";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Spinner, Switch, useUI } from "../../components/ui";

export interface SSOClient {
  id: string;
  name: string;
  kind: "oidc" | "forward";
  domain?: string;
  app?: string;
  catalog?: string;
  public?: boolean;
  redirectUris?: string[];
  postLogoutUris?: string[];
  requirePkce?: boolean;
  trusted?: boolean;
  require2fa?: boolean;
  allUsers?: boolean;
  users?: string[];
  groups?: string[];
  disabled?: boolean;
  created: string;
  hasSecret: boolean;
  sessions: number;
  grants: number;
  host?: string;
}

export interface SSOAppView {
  stack: string;
  app: string;
  support: { app: string; method: string; auto: boolean; redirects?: string[] };
  client?: string;
  url?: string;
}

interface SSOData {
  enabled: boolean;
  issuer: string;
  suggestions: string[];
  discovery?: string;
  clients: SSOClient[];
  domains: { id: string; host: string; kind: string; app?: string; sso?: boolean }[] | null;
  groups: string[] | null;
  proxy: boolean;
  apps: SSOAppView[];
  keys?: { id: string; state: string; created: string; active?: string; retired?: string }[];
}

export interface ManualSettings {
  clientId: string;
  issuer: string;
  discovery: string;
  internalDiscovery: string;
  authorization: string;
  token: string;
  userinfo: string;
  jwks: string;
  logout: string;
  redirectUris: string[];
  scopes: string;
  pkce: boolean;
}

export function Copyable({ label, value }: { label: string; value: string }) {
  const { toast } = useUI();
  return (
    <Field label={label}>
      <div className="flex items-center gap-2">
        <code className="mono min-w-0 flex-1 break-all rounded-lg bg-panel-2 px-2.5 py-1.5 text-[12.5px]">{value}</code>
        <Button size="sm" variant="ghost" title={tr("Copy")} icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(value); toast(tr("Copied")); }} />
      </div>
    </Field>
  );
}

const ssoKey = ["sso"];

export function useSSO() {
  return useQuery({ queryKey: ssoKey, queryFn: () => api.get<SSOData>("/api/sso") });
}

// What to type in each app StrataOS does not configure itself (from the
// apps' own documentation).
function AppSteps({ app, s, secret }: { app: string; s: ManualSettings; secret?: string }) {
  const sec = secret || tr("(the secret shown when it was created; make a new one if you lost it)");
  const pre = (t: string) => <pre className="mono overflow-x-auto whitespace-pre-wrap rounded-xl bg-panel-2 p-3 text-[12px]">{t}</pre>;
  switch (app) {
    case "nextcloud":
      return <div className="space-y-2 text-[13px]">
        <p>{tr("In a terminal of the NAS, install the OpenID Connect app of Nextcloud and add the NAS as its provider (replace <container> by the name of the Nextcloud container, see Containers):")}</p>
        {pre(`docker exec -u www-data <container> php occ app:install user_oidc\ndocker exec -u www-data <container> php occ user_oidc:provider StrataOS --clientid="${s.clientId}" --clientsecret="${sec}" --discoveryuri="${s.discovery}" --scope="openid profile email groups"`)}
        <p className="text-muted">{tr("Nextcloud reads this address from its container: it must reach the NAS under that name with a certificate it trusts.")}</p>
      </div>;
    case "immich":
      return <div className="space-y-2 text-[13px]">
        <p>{tr("In Immich: Administration → Settings → Authentication settings → OAuth. Turn it on and enter:")}</p>
        {pre(`Issuer URL: ${s.discovery}\nClient ID: ${s.clientId}\nClient secret: ${sec}\nScope: openid email profile\nAuto register: on\nMobile redirect URI override: off (app.immich:///oauth-callback is registered)`)}
      </div>;
    case "gitea":
      return <div className="space-y-2 text-[13px]">
        <p>{tr("In Gitea: Site administration → Identity & access → Authentication sources → Add authentication source:")}</p>
        {pre(`Authentication type: OAuth2\nAuthentication name: StrataOS   (exactly: it is part of the return address)\nOAuth2 provider: OpenID Connect\nClient ID (key): ${s.clientId}\nClient secret: ${sec}\nOpenID Connect auto discovery URL: ${s.discovery}\nAdditional scopes: groups`)}
      </div>;
    case "mealie":
      return <div className="space-y-2 text-[13px]">
        <p>{tr("Add these lines to the environment of the Mealie service (Compose stacks → edit), then redeploy:")}</p>
        {pre(`- OIDC_AUTH_ENABLED=true\n- OIDC_PROVIDER_NAME=StrataOS\n- OIDC_CONFIGURATION_URL=${s.discovery}\n- OIDC_CLIENT_ID=${s.clientId}\n- OIDC_CLIENT_SECRET=${sec}\n- OIDC_REQUIRES_EMAIL_VERIFICATION=false\n- OIDC_GROUPS_CLAIM=groups`)}
        <p className="text-muted">{tr("Mealie links accounts by email address: give your NAS users an email address.")}</p>
      </div>;
  }
  return null;
}

/** Settings to type in an app, with the secret when it was just made. */
export function ManualSettingsModal({ client, settings, secret, app, onClose }: { client: string; settings?: ManualSettings; secret?: string; app?: string; onClose: () => void }) {
  const q = useQuery({ queryKey: ["sso-settings", client], queryFn: () => api.get<ManualSettings>(`/api/sso/clients/${encodeURIComponent(client)}/settings`), enabled: !settings });
  const s = settings ?? q.data;
  return (
    <Modal open onClose={onClose} title={tr("Settings for the app")} wide footer={<Button variant="primary" onClick={onClose}>{tr("Done")}</Button>}>
      {!s ? <><ErrorBox error={q.error} /><Spinner /></> : (
        <div className="space-y-3">
          {secret && <Notice tone="warn">{tr("Copy the client secret now: it will not be shown again.")}</Notice>}
          {app && <AppSteps app={app} s={s} secret={secret} />}
          <div className="grid gap-3 sm:grid-cols-2">
            <Copyable label={tr("Client ID")} value={s.clientId} />
            {secret && <Copyable label={tr("Client secret")} value={secret} />}
            <Copyable label={tr("Discovery address (issuer)")} value={s.discovery} />
            <Copyable label={tr("Discovery address for apps of this NAS")} value={s.internalDiscovery} />
            <Copyable label={tr("Authorization endpoint")} value={s.authorization} />
            <Copyable label={tr("Token endpoint")} value={s.token} />
            <Copyable label={tr("Userinfo endpoint")} value={s.userinfo} />
            <Copyable label={tr("Sign-out endpoint")} value={s.logout} />
            <Copyable label={tr("Scopes")} value={s.scopes} />
            <Copyable label={tr("Username claim")} value="preferred_username" />
          </div>
          <Field label={tr("Registered return addresses")}><ul className="mono text-[12.5px]">{s.redirectUris.map((u) => <li key={u}>{u}</li>)}</ul></Field>
          <p className="text-[12.5px] text-muted">{tr("PKCE (S256): {0}. Groups are in the \"groups\" claim; the email address is not marked as verified.", s.pkce ? tr("required") : tr("supported"))}</p>
          <p className="text-[12.5px] text-muted">{tr("The second address serves apps running in containers of this NAS: add extra_hosts \"host.docker.internal:host-gateway\" to their stack. The browser still goes to the first one.")}</p>
        </div>
      )}
    </Modal>
  );
}

interface ClientForm {
  name: string;
  redirectUris: string;
  postLogoutUris: string;
  public: boolean;
  requirePkce: boolean;
  trusted: boolean;
  require2fa: boolean;
  allUsers: boolean;
  users: string;
  groups: string[];
  disabled: boolean;
}

function ClientModal({ client, groups, onClose }: { client: SSOClient | null; groups: string[]; onClose: (made?: { id: string; secret?: string }) => void }) {
  const qc = useQueryClient();
  const fwd = client?.kind === "forward";
  const [f, setF] = useState<ClientForm>(() => ({
    name: client?.name ?? "", redirectUris: (client?.redirectUris ?? []).join("\n"), postLogoutUris: (client?.postLogoutUris ?? []).join("\n"),
    public: !!client?.public, requirePkce: client ? !!client.requirePkce : true, trusted: !!client?.trusted, require2fa: !!client?.require2fa,
    allUsers: client ? !!client.allUsers : true, users: (client?.users ?? []).join(", "), groups: client?.groups ?? [], disabled: !!client?.disabled,
  }));
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const lines = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
  const save = async () => {
    setBusy(true);
    setErr(null);
    const body = { ...f, redirectUris: lines(f.redirectUris), postLogoutUris: lines(f.postLogoutUris), users: lines(f.users) };
    try {
      if (client) {
        const r = await api.put<{ secret?: string } | null>(`/api/sso/clients/${encodeURIComponent(client.id)}`, body);
        onClose(r?.secret ? { id: client.id, secret: r.secret } : undefined);
      } else {
        const r = await api.post<{ client: SSOClient; secret: string }>("/api/sso/clients", body);
        onClose({ id: r.client.id, secret: r.secret });
      }
      qc.invalidateQueries({ queryKey: ssoKey });
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  const set = (p: Partial<ClientForm>) => setF({ ...f, ...p });
  return (
    <Modal open onClose={() => onClose()} title={client ? tr("Application {0}", client.name) : tr("Add an application")} wide footer={<>
      <Button onClick={() => onClose()}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={busy} onClick={save}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => set({ name: e.target.value })} /></Field>
        {!fwd && <>
          <Field label={tr("Return addresses (redirect URIs), one per line")} hint={tr("Compared exactly, character for character.")}>
            <textarea className="input mono min-h-20 w-full text-[12.5px]" value={f.redirectUris} onChange={(e) => set({ redirectUris: e.target.value })} placeholder="https://app.example.com/oauth/callback" />
          </Field>
          <Field label={tr("Addresses after sign-out (optional)")}>
            <textarea className="input mono min-h-14 w-full text-[12.5px]" value={f.postLogoutUris} onChange={(e) => set({ postLogoutUris: e.target.value })} />
          </Field>
          <div className="grid gap-2 sm:grid-cols-2">
            <Checkbox checked={f.public} onChange={(v) => set({ public: v })} label={tr("Public client (no secret: mobile or browser app)")} />
            <Checkbox checked={f.requirePkce || f.public} disabled={f.public} onChange={(v) => set({ requirePkce: v })} label={tr("Require PKCE")} />
          </div>
        </>}
        <div className="grid gap-2 sm:grid-cols-2">
          <Checkbox checked={f.trusted} onChange={(v) => set({ trusted: v })} label={tr("Trusted: no consent screen")} />
          <Checkbox checked={f.require2fa} onChange={(v) => set({ require2fa: v })} label={tr("Only accounts with two-factor authentication")} />
          <Checkbox checked={f.disabled} onChange={(v) => set({ disabled: v })} label={tr("Disabled")} />
        </div>
        <div className="space-y-2 rounded-xl border border-border p-4">
          <p className="font-medium">{tr("Who may use it")}</p>
          <Switch checked={f.allUsers} onChange={(v) => set({ allUsers: v })} label={tr("Every account of the NAS")} />
          {!f.allUsers && <>
            <Field label={tr("Groups")}>
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {groups.length === 0 && <span className="text-muted text-[12.5px]">{tr("No group yet (Users page).")}</span>}
                {groups.map((g) => <Checkbox key={g} checked={f.groups.includes(g)} label={g} onChange={(v) => set({ groups: v ? [...f.groups, g] : f.groups.filter((x) => x !== g) })} />)}
              </div>
            </Field>
            <Field label={tr("Users (separated by commas)")}><Input value={f.users} onChange={(e) => set({ users: e.target.value })} placeholder="alice, bob" /></Field>
          </>}
        </div>
        <ErrorBox error={err} />
      </div>
    </Modal>
  );
}

export default function SingleSignOn() {
  const qc = useQueryClient();
  const { toast, confirm } = useUI();
  const { track } = useJobs();
  const { data, error, isLoading } = useSSO();
  const [enabled, setEnabled] = useState(false);
  const [issuer, setIssuer] = useState("");
  const [editing, setEditing] = useState<SSOClient | null | "new">(null);
  const [shown, setShown] = useState<{ client: string; secret?: string; settings?: ManualSettings; app?: string } | null>(null);
  const [busy, setBusy] = useState("");
  useEffect(() => {
    if (data) {
      setEnabled(data.enabled);
      setIssuer(data.issuer || data.suggestions[0] || "");
    }
  }, [data]);
  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try { await fn(); } catch (e) { toast(srv((e as Error).message), "error"); } finally { setBusy(""); qc.invalidateQueries({ queryKey: ssoKey }); }
  };
  if (isLoading || !data) return <div><PageHeader title={tr("Single sign-on")} icon={<KeySquare size={20} />} /><ErrorBox error={error} /><Spinner /></div>;
  const appOf = (c: SSOClient) => data.apps.find((a) => a.client === c.id);
  const access = (c: SSOClient) => c.allUsers ? tr("Everybody") : [...(c.groups ?? []).map((g) => "@" + g), ...(c.users ?? [])].join(", ") || tr("Nobody");
  const oidcClients = data.clients.filter((c) => c.kind === "oidc");
  const fwdClients = data.clients.filter((c) => c.kind === "forward");
  const toggleApp = (a: SSOAppView, on: boolean) => run("app:" + a.stack, async () => {
    const r = await api.put<{ job?: unknown; settings?: ManualSettings; secret?: string; client?: string } | null>(`/api/sso/apps/${a.stack}`, { enabled: on });
    if (r?.job) track(r.job as never);
    if (r?.settings && r.client) setShown({ client: r.client, settings: r.settings, secret: r.secret, app: a.app });
    toast(on ? tr("{0} signs in with the NAS", a.stack) : tr("{0} no longer signs in with the NAS", a.stack));
  });
  return (
    <div className="space-y-5">
      <PageHeader title={tr("Single sign-on")} subtitle={tr("Your NAS account signs you in to your apps")} icon={<KeySquare size={20} />} />

      <Card title={tr("Sign-in with the NAS")}>
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("The NAS becomes the sign-in of your apps (OpenID Connect): users sign in once, with their second factor, and the apps never see their password. You choose who may use each app.")}</p>
          <Switch checked={enabled} onChange={setEnabled} label={tr("Turn on single sign-on")} />
          <Field label={tr("Address of the NAS for apps and browsers")} hint={tr("The same address everywhere, reachable by the users' browsers; HTTPS with a trusted certificate is best (an app domain for the StrataOS interface, or the NAS's own certificate).")}>
            <Input className="mono" list="sso-issuers" value={issuer} onChange={(e) => setIssuer(e.target.value)} placeholder="https://nas.example.com" />
            <datalist id="sso-issuers">{data.suggestions.map((s) => <option key={s} value={s} />)}</datalist>
          </Field>
          {issuer.startsWith("http://") && <Notice tone="warn">{tr("Over plain HTTP, sign-ins can be read on the network. Use HTTPS outside a test.")}</Notice>}
          {data.discovery && data.enabled && <Copyable label={tr("Discovery address")} value={data.discovery} />}
          <div className="flex justify-end">
            <Button variant="primary" loading={busy === "settings"} onClick={() => run("settings", async () => { await api.put("/api/sso", { enabled, issuer }); toast(tr("Saved")); })}>{tr("Save")}</Button>
          </div>
        </div>
      </Card>

      <Card title={tr("Installed apps")}>
        {data.apps.length === 0 ? <p className="text-[13px] text-muted">{tr("None of the installed apps signs in with OpenID Connect yet. Supported: Grafana and Paperless-ngx (configured automatically); Nextcloud, Immich, Gitea and Mealie (settings to copy). Any app published on an app domain can also be protected below.")}</p> : (
          <div className="divide-y divide-border">
            {data.apps.map((a) => (
              <div key={a.stack} className="flex flex-wrap items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{a.stack}</div>
                  <div className="text-[12px] text-muted">{a.support.auto ? tr("Configured automatically (the app restarts)") : tr("Settings to copy into the app")}{a.url ? " · " + a.url : ""}</div>
                </div>
                {a.client && !a.support.auto && <Button size="sm" variant="ghost" icon={<Settings2 size={14} />} onClick={() => setShown({ client: a.client!, app: a.app })}>{tr("Settings")}</Button>}
                <Switch checked={!!a.client} disabled={!data.enabled || busy === "app:" + a.stack} onChange={(v) => toggleApp(a, v)} label={tr("Sign in with StrataOS")} />
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card title={tr("Applications")} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => setEditing("new")}>{tr("Add an application")}</Button>}>
        {oidcClients.length === 0 ? <Empty icon={<KeySquare size={22} />} title={tr("No application")} text={tr("Add one for any app that supports OpenID Connect.")} /> : (
          <div className="divide-y divide-border">
            {oidcClients.map((c) => (
              <div key={c.id} className="flex flex-wrap items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 font-medium">{c.name}
                    {c.disabled && <Badge tone="warn">{tr("Disabled")}</Badge>}
                    {c.trusted && <Badge>{tr("Trusted")}</Badge>}
                    {c.require2fa && <Badge tone="accent">{tr("Two-factor")}</Badge>}
                    {appOf(c) && <Badge tone="ok">{appOf(c)!.support.auto ? tr("Configured by StrataOS") : tr("App Center")}</Badge>}
                  </div>
                  <div className="mono text-[12px] text-muted">{c.id}</div>
                  <div className="text-[12px] text-muted">{tr("Access: {0}", access(c))} · {tr("{0} open sessions", c.sessions)} · {tr("{0} users allowed it", c.grants)}</div>
                </div>
                <Button size="sm" variant="ghost" icon={<Settings2 size={14} />} title={tr("Settings for the app")} onClick={() => setShown({ client: c.id, app: c.catalog })} />
                <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={tr("Edit")} onClick={() => setEditing(c)} />
                {!c.public && <Button size="sm" variant="ghost" icon={<RefreshCcw size={14} />} title={tr("New secret")} loading={busy === "secret:" + c.id} onClick={async () => {
                  if (!(await confirm({ title: tr("Make a new secret for {0}?", c.name), text: c.app ? tr("StrataOS writes it in the app's configuration and restarts it.") : tr("The app stops signing in until you enter the new secret in it.") }))) return;
                  run("secret:" + c.id, async () => {
                    const r = await api.post<{ secret: string; job?: unknown; appError?: string }>(`/api/sso/clients/${encodeURIComponent(c.id)}/secret`);
                    if (r.job) track(r.job as never);
                    if (r.appError) toast(srv(r.appError), "error");
                    setShown({ client: c.id, secret: r.secret, app: c.catalog });
                  });
                }} />}
                <Button size="sm" variant="ghost" icon={<LogOut size={14} />} title={tr("End all sessions")} onClick={async () => {
                  if (!(await confirm({ title: tr("End every session of {0}?", c.name), text: tr("Its users must sign in again.") }))) return;
                  run("revoke:" + c.id, async () => { await api.post(`/api/sso/clients/${encodeURIComponent(c.id)}/revoke`); toast(tr("Sessions ended")); });
                }} />
                <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} title={tr("Delete")} onClick={async () => {
                  if (!(await confirm({ title: tr("Delete {0}?", c.name), text: tr("Its users can no longer sign in to it with the NAS."), danger: true }))) return;
                  run("del:" + c.id, async () => { await api.del(`/api/sso/clients/${encodeURIComponent(c.id)}`); });
                }} />
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card title={tr("Apps protected on their app domain")}>
        <p className="mb-3 text-[13px]">{tr("For apps without OpenID Connect: the reverse proxy asks for a NAS sign-in before letting anyone in, and passes the user to the app in the Remote-User, Remote-Groups, Remote-Email and Remote-Name headers. Copies of these headers sent by visitors are always removed.")}</p>
        {!data.proxy && <Notice tone="warn">{tr("The app domains are off: turn them on in Remote access.")}</Notice>}
        {(data.domains ?? []).length === 0 ? <p className="text-[13px] text-muted">{tr("No app domain yet (Remote access → App domains).")}</p> : (
          <div className="divide-y divide-border">
            {(data.domains ?? []).map((d) => {
              const c = fwdClients.find((x) => x.domain === d.id);
              return (
                <div key={d.id} className="flex flex-wrap items-center gap-3 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="mono font-medium">{d.host}</div>
                    {c && <div className="text-[12px] text-muted">{tr("Access: {0}", access(c))} · {tr("{0} open sessions", c.sessions)}</div>}
                  </div>
                  {c && <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={tr("Who may use it")} onClick={() => setEditing(c)} />}
                  {c && <Button size="sm" variant="ghost" icon={<LogOut size={14} />} title={tr("End all sessions")} onClick={() => run("revoke:" + c.id, async () => { await api.post(`/api/sso/clients/${encodeURIComponent(c.id)}/revoke`); toast(tr("Sessions ended")); })} />}
                  <Switch checked={!!d.sso} disabled={!data.enabled} onChange={(v) => run("dom:" + d.id, async () => { await api.put(`/api/sso/domains/${d.id}`, { enabled: v }); })} label={tr("Require a NAS sign-in")} />
                </div>
              );
            })}
          </div>
        )}
        <p className="mt-3 text-[12px] text-muted">{tr("An app that trusts these headers must only be reachable through the proxy: anyone who reaches its own port directly could send them. Keep its port closed to the network, or let it trust only the proxy.")}</p>
      </Card>

      <Card title={tr("Signing keys")} actions={<Button size="sm" icon={<RefreshCcw size={14} />} loading={busy === "keys"} onClick={async () => {
        if (!(await confirm({ title: tr("Sign with the next key now?"), text: tr("Apps already know the next key. The current one stays published for 14 days, so signed-in users are not affected.") }))) return;
        run("keys", async () => { await api.post("/api/sso/keys/rotate"); toast(tr("Key rotated")); });
      }}>{tr("Rotate now")}</Button>}>
        <p className="mb-2 text-[12.5px] text-muted">{tr("ID tokens are signed with RS256. The key changes by itself every 90 days.")}</p>
        <div className="space-y-1 text-[12.5px]">
          {(data.keys ?? []).map((k) => (
            <div key={k.id} className="flex flex-wrap items-center gap-2">
              <code className="mono">{k.id}</code>
              <Badge tone={k.state === "active" ? "ok" : "neutral"}>{k.state === "active" ? tr("Signing") : k.state === "next" ? tr("Next") : tr("Retired")}</Badge>
              <span className="text-muted">{tr("created {0}", dateTime(k.created))}</span>
            </div>
          ))}
        </div>
      </Card>

      {editing && <ClientModal client={editing === "new" ? null : editing} groups={data.groups ?? []} onClose={(made) => { setEditing(null); if (made?.secret) setShown({ client: made.id, secret: made.secret }); }} />}
      {shown && <ManualSettingsModal client={shown.client} settings={shown.settings} secret={shown.secret} app={shown.app} onClose={() => setShown(null)} />}
    </div>
  );
}
