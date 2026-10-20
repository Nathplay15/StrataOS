import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Download, Globe, Plug, Plus, QrCode, Smartphone, Trash2, Waypoints } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";
import RemoteExtras from "./RemoteExtras";

type Peer = { id: string; name: string; mode: "nas" | "lan" | "all"; ip: string; publicKey: string; createdAt: string };
type VPN = { enabled: boolean; port: number; subnet: string; endpoint: string; lan: string; publicKey: string; peers: Peer[] };
type DDNS = { provider: "" | "duckdns" | "cloudflare" | "dynv6" | "custom"; hostname: string; token?: string; zoneId?: string; url?: string; lastIp: string; lastUpdate: string; lastResult: string };
type Data = { installed: boolean; config: VPN; serverIp: string; status: Record<string, { handshake: number; endpoint: string; rx: number; tx: number }>; lanGuess: string; ddns: DDNS; ddnsHost: string };

const modes = () => ({
  nas: tr("The NAS only"),
  lan: tr("The NAS and the local network"),
  all: tr("All traffic (as at home)"),
});

function ConfigModal({ name, config, onClose }: { name: string; config: string; onClose: () => void }) {
  const [qr, setQr] = useState("");
  useEffect(() => { QRCode.toDataURL(config, { margin: 1, width: 260 }).then(setQr); }, [config]);
  const file = `${name.replace(/[^\p{L}\p{N}_-]+/gu, "-")}.conf`;
  return (
    <Modal open onClose={onClose} title={tr("{0} configuration", name)} footer={<><a href={URL.createObjectURL(new Blob([config], { type: "text/plain" }))} download={file}><Button icon={<Download size={15} />}>{tr("Download {0}", file)}</Button></a><Button variant="primary" onClick={onClose}>{tr("Close")}</Button></>}>
      <div className="space-y-4">
        <div className="text-[13px]">{tr("On a phone: WireGuard app → + → Scan a QR code. On a computer: import the .conf file.")}</div>
        {qr && <img src={qr} alt="" className="mx-auto rounded-lg bg-white p-2" />}
        <Notice tone="warn">{tr("This QR code and file grant access to the NAS: do not share them.")}</Notice>
      </div>
    </Modal>
  );
}

export default function RemoteAccess() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["vpn"], queryFn: () => api.get<Data>("/api/vpn"), refetchInterval: 5000 });
  const install = useAction(() => api.post("/api/system/components/vpn/install"));
  const [c, setC] = useState<VPN | null>(null);
  const [d, setD] = useState<DDNS | null>(null);
  const [add, setAdd] = useState({ name: "", mode: "lan" as Peer["mode"] });
  const [show, setShow] = useState<{ name: string; config: string } | null>(null);
  useEffect(() => {
    if (data && !c) setC({ ...data.config, lan: data.config.lan || data.lanGuess, endpoint: data.config.endpoint || data.ddnsHost });
    if (data && !d) setD(data.ddns);
  }, [data, c, d]);
  const save = useAction(() => api.put("/api/vpn", c), { success: tr("VPN saved") });
  const addPeer = useAction(() => api.post<{ peer: Peer; config: string }>("/api/vpn/peers", add), { onDone: (r) => { setShow({ name: r.peer.name, config: r.config }); setAdd({ ...add, name: "" }); } });
  const showPeer = useAction((id: string) => api.get<{ name: string; config: string }>(`/api/vpn/peers/${id}/config`), { onDone: (r) => setShow(r) });
  const delPeer = useAction((id: string) => api.del(`/api/vpn/peers/${id}`), { success: tr("Device deleted") });
  const saveDDNS = useAction(() => api.put<DDNS>("/api/ddns", d), { success: tr("Dynamic DNS saved"), onDone: (r) => setD(r) });

  const header = <PageHeader title={tr("Remote access")} subtitle={tr("WireGuard VPN and dynamic DNS: access the NAS from outside without exposing its services")} icon={<Waypoints size={20} />} />;
  if (isLoading || !data || !c || !d) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  const m = modes();

  return (
    <div>
      {header}
      <ErrorBox error={save.error || addPeer.error || saveDDNS.error} />
      <div className="space-y-5">
        <Card title={<span className="flex items-center gap-2"><Waypoints size={16} className="text-accent" />{tr("WireGuard VPN server")}</span>} actions={data.installed && <Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
          {!data.installed ? (
            <div className="flex flex-wrap items-center justify-between gap-3"><div className="text-muted text-[13px]">{tr("WireGuard is not installed.")}</div><Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button></div>
          ) : (
            <div className="space-y-4">
              <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Enable VPN server")} hint={tr("Forward UDP port {0} from your router to the NAS.", c.port)} />
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Public address")} hint={tr("Name (dynamic DNS) or IP address of your router")}><Input className="mono" value={c.endpoint} onChange={(e) => setC({ ...c, endpoint: e.target.value.trim() })} placeholder={data.ddns.lastIp || "nas.duckdns.org"} /></Field>
                <Field label={tr("UDP port")}><Input type="number" value={c.port} onChange={(e) => setC({ ...c, port: +e.target.value })} /></Field>
                <Field label={tr("Local network reachable")} hint={tr("For \"NAS and local network\" devices")}><Input className="mono" value={c.lan} onChange={(e) => setC({ ...c, lan: e.target.value.trim() })} placeholder="192.168.1.0/24" /></Field>
                <Field label={tr("VPN network")} hint={tr("NAS address in the VPN: {0}", data.serverIp)}><Input className="mono" value={c.subnet} onChange={(e) => setC({ ...c, subnet: e.target.value.trim() })} /></Field>
              </div>
            </div>
          )}
        </Card>

        {data.installed && data.config.publicKey && (
          <Card title={<span className="flex items-center gap-2"><Smartphone size={16} className="text-accent" />{tr("Devices")}</span>}>
            <div className="space-y-4">
              <div className="flex flex-wrap items-end gap-3">
                <Field label={tr("Device name")} className="min-w-48 flex-1"><Input value={add.name} onChange={(e) => setAdd({ ...add, name: e.target.value })} placeholder={tr("Marie's phone")} /></Field>
                <Field label={tr("Access")}><Select value={add.mode} onChange={(e) => setAdd({ ...add, mode: e.target.value as Peer["mode"] })}>{(Object.keys(m) as Peer["mode"][]).map((k) => <option key={k} value={k}>{m[k]}</option>)}</Select></Field>
                <Button variant="primary" icon={<Plus size={15} />} disabled={!add.name} loading={addPeer.isPending} onClick={() => addPeer.mutate()}>{tr("Add")}</Button>
              </div>
              {data.config.peers.length === 0 ? <Empty icon={<Smartphone />} title={tr("No devices")} text={tr("Add a device, then scan its QR code with the WireGuard app.")} /> : (
                <div className="divide-y divide-line rounded-xl border border-line">
                  {data.config.peers.map((p) => {
                    const st = data.status[p.publicKey];
                    const online = st && st.handshake > 0 && Date.now() / 1000 - st.handshake < 180;
                    return (
                      <div key={p.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]">
                        <Badge tone={online ? "ok" : "neutral"} dot>{online ? tr("Connected") : tr("Offline")}</Badge>
                        <div className="min-w-0 flex-1">
                          <div className="font-medium">{p.name}</div>
                          <div className="text-muted text-[12px]">{m[p.mode]} · <span className="mono">{p.ip}</span>{st?.handshake ? ` · ${tr("seen {0}", ago(st.handshake * 1000))} · ↓${bytes(st.tx)} ↑${bytes(st.rx)}` : ""}</div>
                        </div>
                        <IconButton title={tr("QR code and configuration")} onClick={() => showPeer.mutate(p.id)}><QrCode size={15} /></IconButton>
                        <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete {0}?", p.name), text: tr("The device will no longer be able to connect."), danger: true })) delPeer.mutate(p.id); }}><Trash2 size={15} /></IconButton>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          </Card>
        )}

        <Card title={<span className="flex items-center gap-2"><Globe size={16} className="text-accent" />{tr("Dynamic DNS")}</span>} actions={<Button size="sm" variant="primary" loading={saveDDNS.isPending} onClick={() => saveDDNS.mutate()}>{tr("Save")}</Button>}>
          <div className="space-y-4">
            <div className="text-muted text-[13px]">{tr("Does your router change public address? A free name follows it automatically (checked every 5 minutes).")}</div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Service")}>
                <Select value={d.provider} onChange={(e) => setD({ ...d, provider: e.target.value as DDNS["provider"] })}>
                  <option value="">{tr("None")}</option>
                  <option value="duckdns">DuckDNS ({tr("free")})</option>
                  <option value="dynv6">dynv6 ({tr("free")})</option>
                  <option value="cloudflare">Cloudflare</option>
                  <option value="custom">{tr("Custom URL")}</option>
                </Select>
              </Field>
              {d.provider && d.provider !== "custom" && <Field label={d.provider === "duckdns" ? tr("Subdomain") : tr("Hostname")} hint={d.provider === "duckdns" ? "monnas → monnas.duckdns.org" : "nas.exemple.fr"}><Input value={d.hostname} onChange={(e) => setD({ ...d, hostname: e.target.value })} /></Field>}
              {d.provider && d.provider !== "custom" && <Field label={d.provider === "cloudflare" ? tr("API token (Zone DNS: edit permission)") : tr("Token")}><Input type="password" autoComplete="off" value={d.token ?? ""} onChange={(e) => setD({ ...d, token: e.target.value })} /></Field>}
              {d.provider === "cloudflare" && <Field label={tr("Zone ID")}><Input className="mono" value={d.zoneId ?? ""} onChange={(e) => setD({ ...d, zoneId: e.target.value.trim() })} /></Field>}
              {d.provider === "custom" && <Field label={tr("Update URL")} hint={tr("{ip} is replaced by the public address")} className="sm:col-span-2"><Input className="mono" value={d.url ?? ""} onChange={(e) => setD({ ...d, url: e.target.value })} placeholder="https://…?ip={ip}" /></Field>}
            </div>
            {data.ddns.provider && (
              <div className="text-[13px]">
                {tr("Public address")} : <span className="mono">{data.ddns.lastIp || "—"}</span>
                {data.ddns.lastUpdate && !data.ddns.lastUpdate.startsWith("0001") && <> · {tr("checked on {0}", dateTime(data.ddns.lastUpdate))} · <span className={data.ddns.lastResult === "OK" ? "text-ok" : "text-danger"}>{srv(data.ddns.lastResult)}</span></>}
              </div>
            )}
          </div>
        </Card>

        <RemoteExtras />
      </div>
      {show && <ConfigModal name={show.name} config={show.config} onClose={() => setShow(null)} />}
    </div>
  );
}
