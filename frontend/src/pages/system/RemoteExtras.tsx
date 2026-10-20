// Remote access without open ports (Tailscale, Cloudflare Tunnel) and app
// domains served by the built-in reverse proxy: sections of the Remote access page.
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cloud, ExternalLink, Globe2, KeyRound, LogOut, Network, Pencil, Plug, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, Switch, Tabs, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

/* ---------- Tailscale ---------- */

type TSPeer = { name: string; dnsName: string; ips: string[] | null; os: string; online: boolean; exitNode?: boolean };
type TSStatus = { installed: boolean; state: string; loggedIn: boolean; authUrl?: string; name?: string; dnsName?: string; ips?: string[]; tailnet?: string; magicDns?: boolean; peers: TSPeer[]; online: number; health?: string[]; error?: string };
type TSConfig = { advertiseLan: boolean; routes: string; exitNode: boolean; acceptRoutes: boolean; ssh: boolean };
type TSData = { installed: boolean; status: TSStatus; config: TSConfig; lanGuess: string };

function Tailscale() {
  const { confirm } = useUI();
  const { data, error } = useQuery({ queryKey: ["remote-tailscale"], queryFn: () => api.get<TSData>("/api/remote/tailscale"), refetchInterval: 5000 });
  const [c, setC] = useState<TSConfig | null>(null);
  const [key, setKey] = useState("");
  const [url, setUrl] = useState("");
  useEffect(() => { if (data && !c) setC({ ...data.config, routes: data.config.routes ?? "" }); }, [data, c]);
  const install = useAction(() => api.post("/api/system/components/tailscale/install"));
  const save = useAction(() => api.put("/api/remote/tailscale", c), { success: tr("Tailscale options saved") });
  const login = useAction((authKey: string) => api.post<{ authUrl: string }>("/api/remote/tailscale/login", { authKey }), { onDone: (r) => { setKey(""); setUrl(r.authUrl); } });
  const logout = useAction(() => api.post("/api/remote/tailscale/logout"), { success: tr("Signed out of Tailscale") });
  if (!data || !c) return <><ErrorBox error={error} /><Spinner /></>;
  const st = data.status;
  if (!data.installed) {
    return (
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="text-[13px] text-muted">{tr("Tailscale connects your devices in a private network, without opening any port on the router. It is not installed.")}</div>
          <Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>
        </div>
      </Card>
    );
  }
  const authUrl = url || st.authUrl || "";
  return (
    <div className="space-y-5">
      <ErrorBox error={save.error || login.error || logout.error} />
      <Card title={<span className="flex items-center gap-2"><Network size={16} className="text-accent" />{tr("Tailscale")}</span>}
        actions={st.loggedIn && <Button size="sm" icon={<LogOut size={14} />} loading={logout.isPending} onClick={async () => { if (await confirm({ title: tr("Sign out of Tailscale?"), text: tr("The devices of your tailnet will no longer reach the NAS."), danger: true })) logout.mutate(); }}>{tr("Sign out")}</Button>}>
        {st.loggedIn ? (
          <div className="space-y-3 text-[13px]">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={st.state === "Running" ? "ok" : "warn"} dot>{st.state === "Running" ? tr("Connected") : st.state}</Badge>
              {st.tailnet && <span className="text-muted">{tr("Tailnet: {0}", st.tailnet)}</span>}
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              <div><div className="text-muted text-[12px]">{tr("Machine name")}</div><div className="font-medium">{st.name}</div></div>
              <div><div className="text-muted text-[12px]">{tr("Tailnet addresses")}</div><div className="mono">{(st.ips ?? []).join(", ")}</div></div>
              <div><div className="text-muted text-[12px]">{tr("MagicDNS name")}</div><div className="mono">{st.magicDns ? st.dnsName : tr("MagicDNS is off")}</div></div>
            </div>
            {(st.health ?? []).map((h) => <Notice key={h} tone="warn">{h}</Notice>)}
          </div>
        ) : (
          <div className="space-y-4">
            <div className="text-[13px] text-muted">{tr("The NAS is not signed in to Tailscale ({0}).", st.state)}{st.error ? ` ${st.error}` : ""}</div>
            {authUrl ? (
              <Notice>
                <div className="space-y-2">
                  <div>{tr("Open this address, sign in to Tailscale and approve the NAS. This page updates by itself once it is done.")}</div>
                  <a className="mono inline-flex items-center gap-1 break-all text-accent underline" href={authUrl} target="_blank" rel="noreferrer noopener">{authUrl} <ExternalLink size={13} /></a>
                </div>
              </Notice>
            ) : (
              <Button variant="primary" icon={<ExternalLink size={15} />} loading={login.isPending && !key} onClick={() => login.mutate("")}>{tr("Sign in with a browser")}</Button>
            )}
            <div className="flex flex-wrap items-end gap-3">
              <Field label={tr("Or an auth key")} hint={tr("Tailscale admin console → Settings → Keys")} className="min-w-64 flex-1"><Input type="password" autoComplete="off" className="mono" value={key} onChange={(e) => setKey(e.target.value.trim())} placeholder="tskey-auth-…" /></Field>
              <Button icon={<KeyRound size={15} />} disabled={!key} loading={login.isPending && !!key} onClick={() => login.mutate(key)}>{tr("Sign in with the key")}</Button>
            </div>
          </div>
        )}
      </Card>

      <Card title={tr("Options")} actions={<Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
        <div className="space-y-4">
          <Switch checked={c.advertiseLan} onChange={(v) => setC({ ...c, advertiseLan: v })} label={tr("Share the local network (subnet routes)")} hint={tr("Your devices reach the whole local network through the NAS. Approve the routes in the Tailscale admin console.")} />
          {c.advertiseLan && <Field label={tr("Networks")} hint={tr("Comma-separated; empty: {0}", data.lanGuess || "—")}><Input className="mono" value={c.routes} onChange={(e) => setC({ ...c, routes: e.target.value })} placeholder={data.lanGuess || "192.168.1.0/24"} /></Field>}
          <Switch checked={c.exitNode} onChange={(v) => setC({ ...c, exitNode: v })} label={tr("Offer the NAS as an exit node")} hint={tr("Devices can send all their Internet traffic through your home connection.")} />
          <Switch checked={c.acceptRoutes} onChange={(v) => setC({ ...c, acceptRoutes: v })} label={tr("Accept the routes of other machines")} />
          <Switch checked={c.ssh} onChange={(v) => setC({ ...c, ssh: v })} label={tr("Tailscale SSH")} hint={tr("Lets the tailnet open SSH sessions according to its access rules, without the system's own SSH sign-in. Off by default.")} />
        </div>
      </Card>

      {st.loggedIn && (
        <Card title={tr("Machines of the tailnet ({0} online)", st.online)}>
          {st.peers.length === 0 ? <Empty icon={<Network />} title={tr("No other machine")} text={tr("Install Tailscale on your phone or computer and sign in with the same account.")} /> : (
            <div className="divide-y divide-line rounded-xl border border-line">
              {st.peers.map((p) => (
                <div key={p.dnsName || p.name} className="flex flex-wrap items-center gap-3 px-4 py-2.5 text-[13px]">
                  <Badge tone={p.online ? "ok" : "neutral"} dot>{p.online ? tr("Online") : tr("Offline")}</Badge>
                  <div className="min-w-0 flex-1"><span className="font-medium">{p.name}</span> <span className="text-muted">· {p.os}{p.exitNode ? ` · ${tr("exit node in use")}` : ""}</span></div>
                  <span className="mono text-muted">{(p.ips ?? [])[0]}</span>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}
    </div>
  );
}

/* ---------- Cloudflare Tunnel ---------- */

type TunnelData = {
  config: { enabled: boolean; token?: string; tunnelId?: string };
  status: { installed: boolean; configured: boolean; service: string; connected: boolean; connections: number; connectorId?: string; tunnelId?: string };
  proxyEnabled: boolean;
};

function CloudflareTunnel() {
  const { data, error } = useQuery({ queryKey: ["remote-tunnel"], queryFn: () => api.get<TunnelData>("/api/remote/tunnel"), refetchInterval: 5000 });
  const [c, setC] = useState<{ enabled: boolean; token: string } | null>(null);
  useEffect(() => { if (data && !c) setC({ enabled: data.config.enabled, token: data.config.token ?? "" }); }, [data, c]);
  const install = useAction(() => api.post("/api/system/components/tunnel/install"));
  const save = useAction(() => api.put("/api/remote/tunnel", c), { success: tr("Cloudflare Tunnel saved") });
  if (!data || !c) return <><ErrorBox error={error} /><Spinner /></>;
  const st = data.status;
  if (!st.installed) {
    return (
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="text-[13px] text-muted">{tr("Cloudflare Tunnel publishes web services under your domain through Cloudflare, without opening any port. cloudflared is not installed.")}</div>
          <Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>
        </div>
      </Card>
    );
  }
  return (
    <div className="space-y-5">
      <ErrorBox error={save.error} />
      <Card title={<span className="flex items-center gap-2"><Cloud size={16} className="text-accent" />{tr("Cloudflare Tunnel")}</span>} actions={<Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2 text-[13px]">
            {!st.configured ? <Badge>{tr("Not configured")}</Badge> : st.connected ? <Badge tone="ok" dot>{tr("Connected ({0} connections)", st.connections)}</Badge> : <Badge tone="warn" dot>{tr("Not connected ({0})", st.service)}</Badge>}
            {st.tunnelId && <span className="text-muted">{tr("Tunnel")} <span className="mono">{st.tunnelId}</span></span>}
            {st.connectorId && <span className="text-muted">· {tr("Connector")} <span className="mono">{st.connectorId}</span></span>}
          </div>
          <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Run the tunnel")} />
          <Field label={tr("Tunnel token")} hint={tr("You can paste the whole install command: only the token is kept.")}><Input type="password" autoComplete="off" className="mono" value={c.token} onChange={(e) => setC({ ...c, token: e.target.value })} placeholder="eyJhIjoi…" /></Field>
        </div>
      </Card>
      <Card title={tr("How to set it up")}>
        <ol className="list-decimal space-y-1.5 pl-5 text-[13px]">
          <li>{tr("In the Cloudflare dashboard: Zero Trust → Networks → Tunnels → Create a tunnel (type Cloudflared).")}</li>
          <li>{tr("Copy the token of the install command, paste it above, turn the tunnel on and save.")}</li>
          <li>{tr("In the tunnel, add a public hostname for each service and point it to a local address of the NAS:")}</li>
        </ol>
        <div className="mt-3 space-y-1.5 text-[13px]">
          <div><span className="mono rounded bg-panel-2 px-1.5 py-0.5">http://localhost:7070</span> — {tr("the StrataOS interface")}</div>
          <div><span className="mono rounded bg-panel-2 px-1.5 py-0.5">http://localhost:&lt;port&gt;</span> — {tr("an app (its port is shown in the App Center)")}</div>
          <div><span className="mono rounded bg-panel-2 px-1.5 py-0.5">https://localhost:443</span> — {tr("the app domains of the NAS (enable \"No TLS Verify\" in the tunnel, and use the same host names)")}</div>
        </div>
        <Notice tone="warn">{tr("Everything you publish this way is reachable from the whole Internet: keep sign-in and two-factor authentication on. StrataOS sees the real address of the visitors (blocked addresses and countries apply).")}</Notice>
      </Card>
    </div>
  );
}

/* ---------- App domains ---------- */

type Domain = {
  id: string; host: string; kind: "app" | "url" | "ui"; app?: string; target?: string; lanOnly?: boolean; created: string;
  certError?: string; certTried?: string; cert: { source: "letsencrypt" | "self-signed" | "none"; issuer?: string; notAfter?: string };
  address?: string; problem?: string; localName?: boolean; obtaining?: boolean;
};
type AppRef = { stack: string; name: string; address: string; status: string };
type DomainsData = { enabled: boolean; running: boolean; email: string; domains: Domain[]; apps: AppRef[]; dnsChallenge: boolean; publicIp: string };
type Edit = { id?: string; host: string; kind: Domain["kind"]; app: string; target: string; lanOnly: boolean };

function DomainModal({ edit, apps, onClose }: { edit: Edit; apps: AppRef[]; onClose: () => void }) {
  const [d, setD] = useState(edit);
  const save = useAction(() => (d.id ? api.put(`/api/remote/domains/${d.id}`, d) : api.post("/api/remote/domains", d)), { success: tr("Domain saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={d.id ? tr("Edit {0}", edit.host) : tr("Publish an app")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={save.isPending} disabled={!d.host} onClick={() => save.mutate()}>{tr("Save")}</Button></>}>
      <div className="space-y-4">
        <ErrorBox error={save.error} />
        <Field label={tr("Host name")} hint={tr("Its DNS record must point to the public address of your router (ports 80 and 443 forwarded to the NAS).")}><Input className="mono" value={d.host} onChange={(e) => setD({ ...d, host: e.target.value.trim() })} placeholder="jellyfin.example.com" /></Field>
        <Field label={tr("Service")}>
          <Select value={d.kind} onChange={(e) => setD({ ...d, kind: e.target.value as Edit["kind"] })}>
            <option value="app">{tr("An installed app")}</option>
            <option value="url">{tr("An address on the NAS or the local network")}</option>
            <option value="ui">{tr("StrataOS interface")}</option>
          </Select>
        </Field>
        {d.kind === "app" && (
          <Field label={tr("App")}>
            <Select value={d.app} onChange={(e) => setD({ ...d, app: e.target.value })}>
              <option value="">—</option>
              {apps.map((a) => <option key={a.stack} value={a.stack}>{a.name} ({a.address})</option>)}
            </Select>
          </Field>
        )}
        {d.kind === "url" && <Field label={tr("Address")} hint={tr("http:// or https:// (self-signed certificates accepted), on this NAS or the local network")}><Input className="mono" value={d.target} onChange={(e) => setD({ ...d, target: e.target.value.trim() })} placeholder="http://192.168.1.20:8080" /></Field>}
        <Switch checked={d.lanOnly} onChange={(v) => setD({ ...d, lanOnly: v })} label={tr("Local network only")} hint={tr("Visitors from the Internet are refused (local, VPN and Tailscale addresses are allowed).")} />
        <Notice>{tr("\"Require StrataOS sign-in\" is not offered: it would mean sharing the interface's session cookie with every name of the parent domain, so that any published app (or another site of the domain) could read or reuse an administrator session. Use \"Local network only\", the VPN or Tailscale for apps without their own sign-in.")}</Notice>
      </div>
    </Modal>
  );
}

function certBadge(d: Domain) {
  if (d.obtaining) return <Badge tone="accent" dot>{tr("Requesting a certificate…")}</Badge>;
  if (d.cert.source === "letsencrypt") return <Badge tone="ok" dot>{tr("Let's Encrypt until {0}", d.cert.notAfter ? dateTime(d.cert.notAfter) : "—")}</Badge>;
  if (d.localName) return <Badge tone="neutral" dot>{tr("Self-signed (local name)")}</Badge>;
  return <Badge tone="warn" dot>{tr("Self-signed for now")}</Badge>;
}

function AppDomains() {
  const { confirm } = useUI();
  const { data, error } = useQuery({ queryKey: ["remote-domains"], queryFn: () => api.get<DomainsData>("/api/remote/domains"), refetchInterval: 5000 });
  const [email, setEmail] = useState<string | null>(null);
  const [edit, setEdit] = useState<Edit | null>(null);
  useEffect(() => { if (data && email === null) setEmail(data.email); }, [data, email]);
  const settings = useAction((enabled: boolean) => api.put("/api/remote/domains/settings", { enabled, email }), { success: tr("App domains saved") });
  const del = useAction((id: string) => api.del(`/api/remote/domains/${id}`), { success: tr("Domain removed") });
  const cert = useAction((id: string) => api.post(`/api/remote/domains/${id}/certificate`));
  if (!data || email === null) return <><ErrorBox error={error} /><Spinner /></>;
  return (
    <div className="space-y-5">
      <ErrorBox error={settings.error || del.error || cert.error} />
      <Card title={<span className="flex items-center gap-2"><Globe2 size={16} className="text-accent" />{tr("App domains")}</span>} actions={<Button size="sm" variant="primary" loading={settings.isPending} onClick={() => settings.mutate(data.enabled)}>{tr("Save")}</Button>}>
        <div className="space-y-4">
          <div className="text-[13px] text-muted">{tr("Give your apps clean addresses (https://jellyfin.example.com): the NAS answers on ports 80 and 443, with a free Let's Encrypt certificate per name, renewed automatically.")}</div>
          <Switch checked={data.enabled} onChange={(v) => settings.mutate(v)} label={tr("Serve the app domains on ports 80 and 443")} hint={data.enabled && !data.running ? <span className="text-danger">{tr("Enabled, but not listening: ports 80 or 443 are used by another program.")}</span> : tr("Refused if another program (such as Nginx Proxy Manager) already uses these ports.")} />
          <Field label={tr("Email for Let's Encrypt (optional)")} hint={tr("Expiry warnings from Let's Encrypt")}><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
          <div className="text-[12.5px] text-muted">
            {data.dnsChallenge ? tr("Names in your Cloudflare zone are validated through DNS (dynamic DNS token): port 80 does not need to be reachable for them.") : tr("Certificates are validated over port 80: forward ports 80 and 443 of your router to the NAS. With a Cloudflare token in the dynamic DNS settings, names of that zone are validated through DNS instead.")}
            {data.publicIp && ` ${tr("Public address: {0}", data.publicIp)}`}
          </div>
        </div>
      </Card>
      <Card title={tr("Published names")} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => setEdit({ host: "", kind: "app", app: data.apps[0]?.stack ?? "", target: "", lanOnly: false })}>{tr("Add")}</Button>}>
        {data.domains.length === 0 ? <Empty icon={<Globe2 />} title={tr("No published name")} text={tr("Add a host name and choose the app it opens.")} /> : (
          <div className="divide-y divide-line rounded-xl border border-line">
            {data.domains.map((d) => (
              <div key={d.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <a className="mono font-medium hover:underline" href={`https://${d.host}`} target="_blank" rel="noreferrer noopener">{d.host}</a>
                    {certBadge(d)}
                    {d.lanOnly && <Badge tone="accent">{tr("Local network only")}</Badge>}
                  </div>
                  <div className="text-muted text-[12px]">
                    → {d.kind === "ui" ? tr("StrataOS interface") : d.kind === "app" ? `${data.apps.find((a) => a.stack === d.app)?.name ?? d.app}${d.address ? ` (${d.address})` : ""}` : d.target}
                    {d.problem && <span className="text-danger"> · {srv(d.problem)}</span>}
                  </div>
                  {d.certError && d.cert.source !== "letsencrypt" && <div className="text-danger text-[12px] break-words">{tr("Certificate not obtained ({0}): {1}", d.certTried ? dateTime(d.certTried) : "", srv(d.certError))}</div>}
                </div>
                {!d.localName && data.running && <IconButton title={tr("Request the certificate again")} onClick={() => cert.mutate(d.id)}><RefreshCw size={15} /></IconButton>}
                <IconButton title={tr("Edit")} onClick={() => setEdit({ id: d.id, host: d.host, kind: d.kind, app: d.app ?? "", target: d.target ?? "", lanOnly: !!d.lanOnly })}><Pencil size={15} /></IconButton>
                <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Stop publishing {0}?", d.host), text: tr("The name and its certificate are removed."), danger: true })) del.mutate(d.id); }}><Trash2 size={15} /></IconButton>
              </div>
            ))}
          </div>
        )}
        <div className="mt-3 flex items-start gap-2 text-[12.5px] text-muted"><ShieldCheck size={15} className="mt-0.5 shrink-0" />{tr("Plain HTTP is redirected to HTTPS; WebSockets work; apps receive the X-Forwarded-For, X-Forwarded-Proto and X-Forwarded-Host headers. Blocked addresses and countries (Address blocking) apply.")}</div>
      </Card>
      {edit && <DomainModal edit={edit} apps={data.apps} onClose={() => setEdit(null)} />}
    </div>
  );
}

/* ---------- Sections ---------- */

type Tab = "tailscale" | "tunnel" | "domains";

export default function RemoteExtras() {
  const [tab, setTab] = useState<Tab>(() => {
    try { return (localStorage.getItem("strata.remoteTab") as Tab) || "tailscale"; } catch { return "tailscale"; }
  });
  const pick = (t: Tab) => { setTab(t); try { localStorage.setItem("strata.remoteTab", t); } catch { /* private mode */ } };
  return (
    <div className="pt-2">
      <h2 className="mb-3 text-[15px] font-semibold">{tr("Other ways in")}</h2>
      <Tabs<Tab> value={tab} onChange={pick} tabs={[{ id: "tailscale", label: tr("Tailscale") }, { id: "tunnel", label: tr("Cloudflare Tunnel") }, { id: "domains", label: tr("App domains") }]} />
      {tab === "tailscale" && <Tailscale />}
      {tab === "tunnel" && <CloudflareTunnel />}
      {tab === "domains" && <AppDomains />}
    </div>
  );
}
