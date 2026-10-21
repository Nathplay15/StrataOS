import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Building2, Download, Globe2, Link2, Pencil, Plus, PlugZap, QrCode, RefreshCw, ShieldCheck, Smartphone, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, Switch, Tabs, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

export type Mode = "nas" | "lan" | "all";
export type Peer = {
  id: string; name: string; mode: Mode; ip: string; publicKey: string; createdAt: string; disabled: boolean;
  owner?: string; kind?: "" | "site"; services?: string[]; expires?: string; expired?: boolean;
  subnets?: string[]; endpoint?: string; keepalive?: number;
};
export type Link = { id: string; name: string; iface: string; address: string; endpoint: string; subnets: string[]; listenPort?: number; disabled?: boolean; createdAt: string };
export type PeerStatus = { handshake: number; endpoint: string; rx: number; tx: number };
export type WGConfig = { enabled: boolean; subnet: string; lan: string; endpoint: string; defaultMode?: Mode; userDevices?: boolean; peers: Peer[]; links: Link[] };
export type WGData = { config: WGConfig; status: Record<string, PeerStatus>; linkStatus: Record<string, PeerStatus>; inactive: Record<string, "expired" | "disabled" | "user">; users: { username: string; disabled: boolean }[]; services: Record<string, string[]> };

export const modes = () => ({
  nas: tr("The NAS only"),
  lan: tr("The NAS and the local network"),
  all: tr("All traffic (as at home)"),
});

const serviceNames = () => ({ smb: tr("File sharing (SMB)"), web: tr("Web interface"), nfs: "NFS", ssh: "SSH", dns: "DNS" });

const online = (st?: PeerStatus) => !!st && st.handshake > 0 && Date.now() / 1000 - st.handshake < 180;
const expiryDay = (v?: string) => (v && !v.startsWith("0001") ? new Date(new Date(v).getTime() - 1000).toLocaleDateString("sv-SE") : "");

export function ConfigModal({ name, config, qr = true, onClose }: { name: string; config: string; qr?: boolean; onClose: () => void }) {
  const [img, setImg] = useState("");
  useEffect(() => { if (qr) QRCode.toDataURL(config, { margin: 1, width: 260 }).then(setImg); }, [config, qr]);
  const file = `${name.replace(/[^\p{L}\p{N}_-]+/gu, "-")}.conf`;
  return (
    <Modal open onClose={onClose} title={tr("{0} configuration", name)} footer={<><a href={URL.createObjectURL(new Blob([config], { type: "text/plain" }))} download={file}><Button icon={<Download size={15} />}>{tr("Download {0}", file)}</Button></a><Button variant="primary" onClick={onClose}>{tr("Close")}</Button></>}>
      <div className="space-y-4">
        {qr ? <div className="text-[13px]">{tr("On a phone: WireGuard app → + → Scan a QR code. On a computer: import the .conf file.")}</div> : <pre className="mono max-h-72 overflow-auto rounded-lg bg-panel-2 p-3 text-[11.5px] whitespace-pre-wrap break-all">{config}</pre>}
        {img && <img src={img} alt="" className="mx-auto rounded-lg bg-white p-2" />}
        <Notice tone="warn">{tr("This QR code and file grant access to the NAS: do not share them.")}</Notice>
      </div>
    </Modal>
  );
}

function PeerLine({ p, st, why, children }: { p: Peer; st?: PeerStatus; why?: string; children: React.ReactNode }) {
  const m = modes();
  const svc = serviceNames() as Record<string, string>;
  return (
    <div className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]">
      {why ? <Badge tone={why === "expired" ? "warn" : "neutral"}>{why === "expired" ? tr("Expired") : why === "user" ? tr("User disabled") : tr("Disabled")}</Badge> : <Badge tone={online(st) ? "ok" : "neutral"} dot>{online(st) ? tr("Connected") : tr("Offline")}</Badge>}
      <div className="min-w-0 flex-1">
        <div className="font-medium">{p.name}{p.owner && <span className="text-muted font-normal"> · {p.owner}</span>}</div>
        <div className="text-muted text-[12px]">
          {p.kind === "site" ? (p.subnets ?? []).join(", ") : m[p.mode]} · <span className="mono">{p.ip}</span>
          {p.services?.length ? ` · ${tr("only {0}", p.services.map((s) => svc[s] ?? s).join(", "))}` : ""}
          {expiryDay(p.expires) ? ` · ${tr("until {0}", expiryDay(p.expires))}` : ""}
          {st?.handshake ? ` · ${tr("seen {0}", ago(st.handshake * 1000))} · ↓${bytes(st.tx)} ↑${bytes(st.rx)}` : ""}
          {st?.endpoint ? <> · <span className="mono">{st.endpoint}</span></> : ""}
        </div>
      </div>
      {children}
    </div>
  );
}

type Form = { id?: string; kind: "" | "site"; name: string; owner: string; mode: Mode; services: string[]; ports: string; expires: string; disabled: boolean; subnets: string; endpoint: string; keepalive: number };

function PeerModal({ form, data, onClose, onCreated }: { form: Form; data: WGData; onClose: () => void; onCreated: (r: { peer: Peer; config: string }) => void }) {
  const [f, setF] = useState(form);
  const svc = serviceNames() as Record<string, string>;
  const m = modes();
  const body = () => ({
    name: f.name, owner: f.owner, kind: f.kind, mode: f.mode, disabled: f.disabled, expires: f.expires, keepalive: f.keepalive, endpoint: f.endpoint.trim(),
    services: [...f.services, ...f.ports.split(/[\s,]+/).filter(Boolean)],
    subnets: f.subnets.split(/[\s,]+/).filter(Boolean),
  });
  const save = useAction(() => (f.id ? api.put(`/api/vpn/peers/${f.id}`, body()) : api.post<{ peer: Peer; config: string }>("/api/vpn/peers", body())), {
    success: tr("Saved"),
    onDone: (r) => { if (!f.id && r && typeof r === "object") onCreated(r as { peer: Peer; config: string }); onClose(); },
  });
  const site = f.kind === "site";
  return (
    <Modal open onClose={onClose} title={f.id ? tr("Edit {0}", form.name) : site ? tr("New site") : tr("New device")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={save.isPending} disabled={!f.name} onClick={() => save.mutate()}>{tr("Save")}</Button></>}>
      <div className="space-y-4">
        <ErrorBox error={save.error} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={site ? tr("Site name") : tr("Device name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder={site ? tr("Office") : tr("Marie's phone")} /></Field>
          {!site && <Field label={tr("User")}><Select value={f.owner} onChange={(e) => setF({ ...f, owner: e.target.value })}><option value="">{tr("Administrators")}</option>{data.users.map((u) => <option key={u.username} value={u.username}>{u.username}</option>)}</Select></Field>}
          {!site && <Field label={tr("Access")}><Select value={f.mode} onChange={(e) => setF({ ...f, mode: e.target.value as Mode })}>{(Object.keys(m) as Mode[]).map((k) => <option key={k} value={k}>{m[k]}</option>)}</Select></Field>}
          {site && <Field label={tr("Networks of the site")} hint={tr("Separated by commas, e.g. 192.168.2.0/24")}><Input className="mono" value={f.subnets} onChange={(e) => setF({ ...f, subnets: e.target.value })} /></Field>}
          {site && <Field label={tr("Address of the site (optional)")} hint={tr("host:port, when the NAS should connect to it")}><Input className="mono" value={f.endpoint} onChange={(e) => setF({ ...f, endpoint: e.target.value })} placeholder="office.example.org:51820" /></Field>}
          {site && <Field label={tr("Keepalive (seconds)")}><Input type="number" value={f.keepalive} onChange={(e) => setF({ ...f, keepalive: +e.target.value })} /></Field>}
          <Field label={tr("Expires on")} hint={tr("Disabled automatically after this day (empty: never)")}><Input type="date" value={f.expires} onChange={(e) => setF({ ...f, expires: e.target.value })} /></Field>
        </div>
        <Field label={tr("Restrict to")} hint={tr("Nothing checked and no port: no restriction")}>
          <div className="flex flex-wrap gap-x-5 gap-y-2 pt-1">
            {Object.keys(data.services).map((k) => <Checkbox key={k} checked={f.services.includes(k)} onChange={(v) => setF({ ...f, services: v ? [...f.services, k] : f.services.filter((x) => x !== k) })} label={svc[k] ?? k} />)}
          </div>
        </Field>
        <Field label={tr("Other ports")} hint={tr("e.g. tcp/8080, udp/5000-5010")}><Input className="mono" value={f.ports} onChange={(e) => setF({ ...f, ports: e.target.value })} /></Field>
        {f.id && <Switch checked={!f.disabled} onChange={(v) => setF({ ...f, disabled: !v })} label={tr("Enabled")} />}
      </div>
    </Modal>
  );
}

const toForm = (p: Peer, services: Record<string, string[]>): Form => ({
  id: p.id, kind: p.kind ?? "", name: p.name, owner: p.owner ?? "", mode: p.mode, disabled: p.disabled,
  services: (p.services ?? []).filter((s) => s in services), ports: (p.services ?? []).filter((s) => !(s in services)).join(", "),
  expires: expiryDay(p.expires), subnets: (p.subnets ?? []).join(", "), endpoint: p.endpoint ?? "", keepalive: p.keepalive ?? 25,
});

function Devices({ data, onShow }: { data: WGData; onShow: (s: { name: string; config: string; qr?: boolean }) => void }) {
  const { confirm } = useUI();
  const [owner, setOwner] = useState("*");
  const [edit, setEdit] = useState<Form | null>(null);
  const show = useAction((id: string) => api.get<{ name: string; config: string }>(`/api/vpn/peers/${id}/config`), { onDone: (r) => onShow(r) });
  const del = useAction((id: string) => api.del(`/api/vpn/peers/${id}`), { success: tr("Device deleted") });
  const peers = data.config.peers.filter((p) => p.kind !== "site" && (owner === "*" || (p.owner ?? "") === owner));
  const blank: Form = { kind: "", name: "", owner: "", mode: data.config.defaultMode ?? "lan", services: [], ports: "", expires: "", disabled: false, subnets: "", endpoint: "", keepalive: 25 };
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <Field label={tr("User")}><Select value={owner} onChange={(e) => setOwner(e.target.value)}><option value="*">{tr("All")}</option><option value="">{tr("Administrators")}</option>{data.users.map((u) => <option key={u.username} value={u.username}>{u.username}</option>)}</Select></Field>
        <div className="flex-1" />
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit({ ...blank, owner: owner === "*" ? "" : owner })}>{tr("Add a device")}</Button>
      </div>
      {peers.length === 0 ? <Empty icon={<Smartphone />} title={tr("No devices")} text={tr("Add a device, then scan its QR code with the WireGuard app.")} /> : (
        <div className="divide-y divide-line rounded-xl border border-line">
          {peers.map((p) => (
            <PeerLine key={p.id} p={p} st={data.status[p.publicKey]} why={data.inactive[p.id]}>
              <IconButton title={tr("QR code and configuration")} onClick={() => show.mutate(p.id)}><QrCode size={15} /></IconButton>
              <IconButton title={tr("Edit")} onClick={() => setEdit(toForm(p, data.services))}><Pencil size={15} /></IconButton>
              <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete {0}?", p.name), text: tr("The device will no longer be able to connect."), danger: true })) del.mutate(p.id); }}><Trash2 size={15} /></IconButton>
            </PeerLine>
          ))}
        </div>
      )}
      {edit && <PeerModal form={edit} data={data} onClose={() => setEdit(null)} onCreated={(r) => onShow({ name: r.peer.name, config: r.config })} />}
    </div>
  );
}

function Sites({ data, onShow }: { data: WGData; onShow: (s: { name: string; config: string; qr?: boolean }) => void }) {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<Form | null>(null);
  const [imp, setImp] = useState<{ code: string; name: string } | null>(null);
  const show = useAction(({ id, format }: { id: string; format: string }) => api.get<{ name: string; config: string }>(`/api/vpn/peers/${id}/config?format=${format}`), { onDone: (r) => onShow({ ...r, qr: false }) });
  const del = useAction((id: string) => api.del(`/api/vpn/peers/${id}`), { success: tr("Site deleted") });
  const delLink = useAction((id: string) => api.del(`/api/vpn/links/${id}`), { success: tr("Site deleted") });
  const toggleLink = useAction((l: Link) => api.put(`/api/vpn/links/${l.id}`, { disabled: !l.disabled }));
  const doImport = useAction(() => api.post("/api/vpn/links", imp), { success: tr("Site imported"), onDone: () => setImp(null) });
  const sites = data.config.peers.filter((p) => p.kind === "site");
  const blank: Form = { kind: "site", name: "", owner: "", mode: "lan", services: [], ports: "", expires: "", disabled: false, subnets: "", endpoint: "", keepalive: 25 };
  return (
    <div className="space-y-4">
      <div className="text-muted text-[13px]">{tr("Connect a whole remote network (another office, a second home) to the local network of the NAS. Its devices reach each other through the tunnel.")}</div>
      <div className="flex flex-wrap justify-end gap-2">
        <Button icon={<Link2 size={15} />} onClick={() => setImp({ code: "", name: "" })}>{tr("Import a StrataOS site code")}</Button>
        <Button variant="primary" icon={<Plus size={15} />} disabled={!data.config.enabled} onClick={() => setEdit(blank)}>{tr("Add a site")}</Button>
      </div>
      {sites.length + data.config.links.length === 0 ? <Empty icon={<Building2 />} title={tr("No sites")} text={tr("Add a site, then set up its router with the generated configuration, or give its code to another StrataOS.")} /> : (
        <div className="divide-y divide-line rounded-xl border border-line">
          {sites.map((p) => (
            <PeerLine key={p.id} p={p} st={data.status[p.publicKey]} why={data.inactive[p.id]}>
              <Button size="sm" onClick={() => show.mutate({ id: p.id, format: "router" })}>{tr("Router configuration")}</Button>
              <Button size="sm" onClick={() => show.mutate({ id: p.id, format: "strataos" })}>{tr("StrataOS code")}</Button>
              <IconButton title={tr("Edit")} onClick={() => setEdit(toForm(p, data.services))}><Pencil size={15} /></IconButton>
              <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete {0}?", p.name), text: tr("The tunnel to this site will be cut."), danger: true })) del.mutate(p.id); }}><Trash2 size={15} /></IconButton>
            </PeerLine>
          ))}
          {data.config.links.map((l) => {
            const st = data.linkStatus[l.id];
            return (
              <div key={l.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]">
                {l.disabled ? <Badge>{tr("Disabled")}</Badge> : <Badge tone={online(st) ? "ok" : "neutral"} dot>{online(st) ? tr("Connected") : tr("Offline")}</Badge>}
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{l.name} <span className="text-muted font-normal">· {tr("imported from another StrataOS")}</span></div>
                  <div className="text-muted text-[12px]">{l.subnets.join(", ")} · <span className="mono">{l.address}</span>{l.endpoint ? <> · <span className="mono">{l.endpoint}</span></> : ""}{st?.handshake ? ` · ${tr("seen {0}", ago(st.handshake * 1000))} · ↓${bytes(st.rx)} ↑${bytes(st.tx)}` : ""}</div>
                </div>
                <Switch checked={!l.disabled} onChange={() => toggleLink.mutate(l)} />
                <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete {0}?", l.name), text: tr("The tunnel to this site will be cut."), danger: true })) delLink.mutate(l.id); }}><Trash2 size={15} /></IconButton>
              </div>
            );
          })}
        </div>
      )}
      {edit && <PeerModal form={edit} data={data} onClose={() => setEdit(null)} onCreated={(r) => onShow({ name: r.peer.name, config: r.config, qr: false })} />}
      {imp && (
        <Modal open onClose={() => setImp(null)} title={tr("Import a StrataOS site code")} footer={<><Button onClick={() => setImp(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={doImport.isPending} disabled={!imp.code.trim()} onClick={() => doImport.mutate()}>{tr("Import")}</Button></>}>
          <div className="space-y-4">
            <ErrorBox error={doImport.error} />
            <div className="text-[13px]">{tr("On the other StrataOS: Remote access → Sites → Add a site (with the networks of this NAS), then StrataOS code.")}</div>
            <Field label={tr("Site code")}><textarea className="input mono h-28 w-full text-[12px]" value={imp.code} onChange={(e) => setImp({ ...imp, code: e.target.value })} placeholder="strataos-site:…" /></Field>
            <Field label={tr("Name (optional)")}><Input value={imp.name} onChange={(e) => setImp({ ...imp, name: e.target.value })} /></Field>
          </div>
        </Modal>
      )}
    </div>
  );
}

type Client = { enabled: boolean; name?: string; addresses?: string[]; dns?: string[]; endpoint?: string; peerPublicKey?: string; scope: "apps" | "all"; apps?: string[]; appsSubnet: string; checkUrl?: string };
type ClientData = { installed: boolean; config: Client; status: PeerStatus | null; routing: boolean; containers: string[]; docker: boolean; defaultCheckUrl: string };

function Outgoing() {
  const { confirm, toast } = useUI();
  const { data, error } = useQuery({ queryKey: ["vpn-client"], queryFn: () => api.get<ClientData>("/api/vpn/client"), refetchInterval: 5000 });
  const [c, setC] = useState<Client | null>(null);
  const [conf, setConf] = useState("");
  const [warn, setWarn] = useState<string[]>([]);
  const [ks, setKs] = useState<{ tunnelRoute: string; downRoute: string; blocked: boolean } | null>(null);
  useEffect(() => { if (data && !c) setC(data.config); }, [data, c]);
  const save = useAction(() => api.put<{ warnings: string[] }>("/api/vpn/client", { ...c, conf }), { success: tr("Outgoing tunnel saved"), onDone: (r) => { setWarn(r.warnings); setConf(""); } });
  const remove = useAction(() => api.del("/api/vpn/client"), { success: tr("Outgoing tunnel removed"), onDone: () => setC(null) });
  const check = useAction(() => api.post<{ publicIp: string }>("/api/vpn/client/check"), { onDone: (r) => toast(tr("Public address through the tunnel: {0}", r.publicIp)) });
  const kill = useAction(() => api.post<{ tunnelRoute: string; downRoute: string; blocked: boolean }>("/api/vpn/client/killswitch"), { onDone: setKs });
  const reconnect = useAction(() => api.post("/api/vpn/client/reconnect"), { success: tr("Tunnel restarted") });
  if (!data || !c) return <><ErrorBox error={error} /><Spinner /></>;
  const st = data.status;
  const has = !!data.config.peerPublicKey;
  const readFile = (file?: File) => file?.text().then(setConf);
  return (
    <div className="space-y-4">
      <div className="text-muted text-[13px]">{tr("The NAS connects to a VPN provider (or another WireGuard server) with the configuration file it gives you.")}</div>
      <ErrorBox error={save.error} />
      {has && (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border border-line px-4 py-3 text-[13px]">
          <Badge tone={data.config.enabled ? (online(st ?? undefined) ? "ok" : "warn") : "neutral"} dot>{!data.config.enabled ? tr("Off") : online(st ?? undefined) ? tr("Connected") : tr("No handshake")}</Badge>
          <div className="min-w-0 flex-1">
            <div className="font-medium">{data.config.name || data.config.endpoint}</div>
            <div className="text-muted text-[12px]"><span className="mono">{data.config.endpoint}</span> · <span className="mono">{(data.config.addresses ?? []).join(", ")}</span>{st?.handshake ? ` · ${tr("seen {0}", ago(st.handshake * 1000))} · ↓${bytes(st.rx)} ↑${bytes(st.tx)}` : ""}{data.config.enabled && !data.routing ? ` · ${tr("routes missing")}` : ""}</div>
          </div>
          {data.config.enabled && <Button size="sm" icon={<Globe2 size={14} />} loading={check.isPending} onClick={() => check.mutate()}>{tr("Check public address")}</Button>}
          {data.config.enabled && data.config.scope === "apps" && <Button size="sm" icon={<ShieldCheck size={14} />} loading={kill.isPending} onClick={() => kill.mutate()}>{tr("Test kill switch")}</Button>}
          {data.config.enabled && <IconButton title={tr("Reconnect")} onClick={() => reconnect.mutate()}><RefreshCw size={15} /></IconButton>}
          <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Remove the outgoing tunnel?"), text: tr("The routed apps get their normal Internet access back."), danger: true })) remove.mutate(); }}><Trash2 size={15} /></IconButton>
        </div>
      )}
      {ks && <Notice tone={ks.blocked ? "info" : "warn"}>{ks.blocked ? tr("Kill switch OK: with the tunnel down the routed apps have no way out.") : tr("Kill switch FAILED: the routed apps could reach the Internet without the tunnel.")} <span className="mono text-[11.5px]">{ks.downRoute}</span></Notice>}
      <Field label={has ? tr("Replace the configuration file") : tr("Configuration file of the provider (.conf)")}>
        <div className="space-y-2">
          <input type="file" accept=".conf,text/plain" onChange={(e) => readFile(e.target.files?.[0])} className="text-[13px]" />
          <textarea className="input mono h-28 w-full text-[12px]" value={conf} onChange={(e) => setConf(e.target.value)} placeholder={"[Interface]\nPrivateKey = …\nAddress = …\n\n[Peer]\n…"} />
        </div>
      </Field>
      {warn.length > 0 && <Notice tone="warn">{warn.join(" · ")}</Notice>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={tr("Name")}><Input value={c.name ?? ""} onChange={(e) => setC({ ...c, name: e.target.value })} placeholder="Mullvad, Proton VPN…" /></Field>
        <Field label={tr("What goes through the tunnel")}>
          <Select value={c.scope} onChange={(e) => setC({ ...c, scope: e.target.value as Client["scope"] })}>
            <option value="apps">{tr("Selected Docker apps only (recommended)")}</option>
            <option value="all">{tr("The whole NAS")}</option>
          </Select>
        </Field>
      </div>
      {c.scope === "all" && <Notice tone="warn">{tr("All the traffic of the NAS will go through the provider. The local network and the WireGuard server stay reachable, but other remote access (port forwarding, Tailscale, Cloudflare Tunnel) may stop working.")}</Notice>}
      {c.scope === "apps" && (
        <Field label={tr("Apps routed through the tunnel")} hint={tr("They reach the Internet only through the tunnel (kill switch). On this Docker version they leave their other networks.")}>
          {!data.docker ? <div className="text-muted text-[13px]">{tr("Docker is not installed.")}</div> : (
            <div className="flex flex-wrap gap-x-5 gap-y-2 pt-1">
              {data.containers.length === 0 && <span className="text-muted text-[13px]">{tr("No containers")}</span>}
              {data.containers.map((n) => <Checkbox key={n} checked={(c.apps ?? []).includes(n)} onChange={(v) => setC({ ...c, apps: v ? [...(c.apps ?? []), n] : (c.apps ?? []).filter((x) => x !== n) })} label={n} />)}
            </div>
          )}
        </Field>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        {c.scope === "apps" && <Field label={tr("Network of the routed apps")}><Input className="mono" value={c.appsSubnet} onChange={(e) => setC({ ...c, appsSubnet: e.target.value.trim() })} /></Field>}
        <Field label={tr("Address check URL")} hint={tr("Returns the public address as text")}><Input className="mono" value={c.checkUrl ?? ""} onChange={(e) => setC({ ...c, checkUrl: e.target.value.trim() })} placeholder={data.defaultCheckUrl} /></Field>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Enable the outgoing tunnel")} disabled={!data.installed} />
        <Button variant="primary" icon={<PlugZap size={15} />} loading={save.isPending} disabled={!has && !conf.trim()} onClick={() => save.mutate()}>{tr("Save")}</Button>
      </div>
    </div>
  );
}

/** Devices, sites and outgoing tunnel of the WireGuard section. */
export function WireGuardTabs({ data }: { data: WGData }) {
  const [tab, setTab] = useState<"devices" | "sites" | "out">("devices");
  const [show, setShow] = useState<{ name: string; config: string; qr?: boolean } | null>(null);
  return (
    <Card>
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "devices", label: tr("Devices") }, { id: "sites", label: tr("Sites") }, { id: "out", label: tr("Outgoing tunnel") }]} />
      {tab === "devices" && <Devices data={data} onShow={setShow} />}
      {tab === "sites" && <Sites data={data} onShow={setShow} />}
      {tab === "out" && <Outgoing />}
      {show && <ConfigModal name={show.name} config={show.config} qr={show.qr !== false} onClose={() => setShow(null)} />}
    </Card>
  );
}

/** "My VPN devices": the devices of the signed-in user. */
export function MyVpnDevices() {
  const { confirm } = useUI();
  const { data, error } = useQuery({ queryKey: ["vpn-my"], queryFn: () => api.get<{ allowed: boolean; devices: Peer[]; status: Record<string, PeerStatus>; inactive: Record<string, string>; max: number }>("/api/vpn/my"), refetchInterval: 5000 });
  const [name, setName] = useState("");
  const [show, setShow] = useState<{ name: string; config: string } | null>(null);
  const add = useAction(() => api.post<{ peer: Peer; config: string }>("/api/vpn/my/devices", { name }), { onDone: (r) => { setShow({ name: r.peer.name, config: r.config }); setName(""); } });
  const get = useAction((id: string) => api.get<{ name: string; config: string }>(`/api/vpn/my/devices/${id}/config`), { onDone: setShow });
  const del = useAction((id: string) => api.del(`/api/vpn/my/devices/${id}`), { success: tr("Device revoked") });
  if (!data) return <><ErrorBox error={error} /><Spinner /></>;
  return (
    <Card title={<span className="flex items-center gap-2"><Smartphone size={16} className="text-accent" />{tr("My VPN devices")}</span>}>
      <div className="space-y-4">
        <ErrorBox error={add.error} />
        {data.allowed ? (
          <div className="flex flex-wrap items-end gap-3">
            <Field label={tr("Device name")} className="min-w-48 flex-1"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder={tr("My phone")} /></Field>
            <Button variant="primary" icon={<Plus size={15} />} disabled={!name || data.devices.length >= data.max} loading={add.isPending} onClick={() => add.mutate()}>{tr("Add")}</Button>
          </div>
        ) : <Notice>{tr("The administrator does not let users add VPN devices.")}</Notice>}
        {data.devices.length === 0 ? <Empty icon={<Smartphone />} title={tr("No devices")} text={tr("Add a device, then scan its QR code with the WireGuard app.")} /> : (
          <div className="divide-y divide-line rounded-xl border border-line">
            {data.devices.map((p) => (
              <PeerLine key={p.id} p={p} st={data.status[p.publicKey]} why={data.inactive[p.id]}>
                <IconButton title={tr("QR code and configuration")} onClick={() => get.mutate(p.id)}><QrCode size={15} /></IconButton>
                <IconButton title={tr("Revoke")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Revoke {0}?", p.name), text: tr("The device will no longer be able to connect."), danger: true })) del.mutate(p.id); }}><Trash2 size={15} /></IconButton>
              </PeerLine>
            ))}
          </div>
        )}
        {data.devices.some((p) => p.expires && !p.expires.startsWith("0001")) && <div className="text-muted text-[12px]">{tr("Devices with an end date are disabled automatically after it ({0}).", data.devices.filter((p) => expiryDay(p.expires)).map((p) => `${p.name}: ${dateTime(p.expires)}`).join(", "))}</div>}
      </div>
      {show && <ConfigModal name={show.name} config={show.config} onClose={() => setShow(null)} />}
    </Card>
  );
}
