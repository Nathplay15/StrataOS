import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Download, Globe, Plug, Plus, QrCode, Smartphone, Trash2, Waypoints } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type Peer = { id: string; name: string; mode: "nas" | "lan" | "all"; ip: string; publicKey: string; createdAt: string };
type VPN = { enabled: boolean; port: number; subnet: string; endpoint: string; lan: string; publicKey: string; peers: Peer[] };
type DDNS = { provider: "" | "duckdns" | "cloudflare" | "dynv6" | "custom"; hostname: string; token?: string; zoneId?: string; url?: string; lastIp: string; lastUpdate: string; lastResult: string };
type Data = { installed: boolean; config: VPN; serverIp: string; status: Record<string, { handshake: number; endpoint: string; rx: number; tx: number }>; lanGuess: string; ddns: DDNS; ddnsHost: string };

const modes = () => ({
  nas: tr("Le NAS seulement"),
  lan: tr("Le NAS et le réseau local"),
  all: tr("Tout le trafic (comme à la maison)"),
});

function ConfigModal({ name, config, onClose }: { name: string; config: string; onClose: () => void }) {
  const [qr, setQr] = useState("");
  useEffect(() => { QRCode.toDataURL(config, { margin: 1, width: 260 }).then(setQr); }, [config]);
  const file = `${name.replace(/[^\p{L}\p{N}_-]+/gu, "-")}.conf`;
  return (
    <Modal open onClose={onClose} title={tr("Configuration de {0}", name)} footer={<><a href={URL.createObjectURL(new Blob([config], { type: "text/plain" }))} download={file}><Button icon={<Download size={15} />}>{tr("Télécharger {0}", file)}</Button></a><Button variant="primary" onClick={onClose}>{tr("Fermer")}</Button></>}>
      <div className="space-y-4">
        <div className="text-[13px]">{tr("Sur le téléphone : application WireGuard → + → Scanner un QR code. Sur un ordinateur : importez le fichier .conf.")}</div>
        {qr && <img src={qr} alt="" className="mx-auto rounded-lg bg-white p-2" />}
        <Notice tone="warn">{tr("Ce QR code et ce fichier donnent accès au NAS : ne les partagez pas.")}</Notice>
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
  const save = useAction(() => api.put("/api/vpn", c), { success: tr("VPN enregistré") });
  const addPeer = useAction(() => api.post<{ peer: Peer; config: string }>("/api/vpn/peers", add), { onDone: (r) => { setShow({ name: r.peer.name, config: r.config }); setAdd({ ...add, name: "" }); } });
  const showPeer = useAction((id: string) => api.get<{ name: string; config: string }>(`/api/vpn/peers/${id}/config`), { onDone: (r) => setShow(r) });
  const delPeer = useAction((id: string) => api.del(`/api/vpn/peers/${id}`), { success: tr("Appareil supprimé") });
  const saveDDNS = useAction(() => api.put<DDNS>("/api/ddns", d), { success: tr("DNS dynamique enregistré"), onDone: (r) => setD(r) });

  const header = <PageHeader title={tr("Accès à distance")} subtitle={tr("VPN WireGuard et DNS dynamique : accédez au NAS depuis l'extérieur sans exposer ses services")} icon={<Waypoints size={20} />} />;
  if (isLoading || !data || !c || !d) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  const m = modes();

  return (
    <div>
      {header}
      <ErrorBox error={save.error || addPeer.error || saveDDNS.error} />
      <div className="space-y-5">
        <Card title={<span className="flex items-center gap-2"><Waypoints size={16} className="text-accent" />{tr("Serveur VPN WireGuard")}</span>} actions={data.installed && <Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Enregistrer")}</Button>}>
          {!data.installed ? (
            <div className="flex flex-wrap items-center justify-between gap-3"><div className="text-muted text-[13px]">{tr("WireGuard n'est pas installé.")}</div><Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button></div>
          ) : (
            <div className="space-y-4">
              <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Activer le serveur VPN")} hint={tr("Redirigez le port UDP {0} de votre box vers le NAS.", c.port)} />
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Adresse publique")} hint={tr("Nom (DNS dynamique) ou adresse IP de votre box")}><Input className="mono" value={c.endpoint} onChange={(e) => setC({ ...c, endpoint: e.target.value.trim() })} placeholder={data.ddns.lastIp || "nas.duckdns.org"} /></Field>
                <Field label={tr("Port UDP")}><Input type="number" value={c.port} onChange={(e) => setC({ ...c, port: +e.target.value })} /></Field>
                <Field label={tr("Réseau local accessible")} hint={tr("Pour les appareils « NAS et réseau local »")}><Input className="mono" value={c.lan} onChange={(e) => setC({ ...c, lan: e.target.value.trim() })} placeholder="192.168.1.0/24" /></Field>
                <Field label={tr("Réseau du VPN")} hint={tr("Adresse du NAS dans le VPN : {0}", data.serverIp)}><Input className="mono" value={c.subnet} onChange={(e) => setC({ ...c, subnet: e.target.value.trim() })} /></Field>
              </div>
            </div>
          )}
        </Card>

        {data.installed && data.config.publicKey && (
          <Card title={<span className="flex items-center gap-2"><Smartphone size={16} className="text-accent" />{tr("Appareils")}</span>}>
            <div className="space-y-4">
              <div className="flex flex-wrap items-end gap-3">
                <Field label={tr("Nom de l'appareil")} className="min-w-48 flex-1"><Input value={add.name} onChange={(e) => setAdd({ ...add, name: e.target.value })} placeholder={tr("Téléphone de Marie")} /></Field>
                <Field label={tr("Accès")}><Select value={add.mode} onChange={(e) => setAdd({ ...add, mode: e.target.value as Peer["mode"] })}>{(Object.keys(m) as Peer["mode"][]).map((k) => <option key={k} value={k}>{m[k]}</option>)}</Select></Field>
                <Button variant="primary" icon={<Plus size={15} />} disabled={!add.name} loading={addPeer.isPending} onClick={() => addPeer.mutate()}>{tr("Ajouter")}</Button>
              </div>
              {data.config.peers.length === 0 ? <Empty icon={<Smartphone />} title={tr("Aucun appareil")} text={tr("Ajoutez un appareil puis scannez son QR code avec l'application WireGuard.")} /> : (
                <div className="divide-y divide-line rounded-xl border border-line">
                  {data.config.peers.map((p) => {
                    const st = data.status[p.publicKey];
                    const online = st && st.handshake > 0 && Date.now() / 1000 - st.handshake < 180;
                    return (
                      <div key={p.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-[13px]">
                        <Badge tone={online ? "ok" : "neutral"} dot>{online ? tr("Connecté") : tr("Hors ligne")}</Badge>
                        <div className="min-w-0 flex-1">
                          <div className="font-medium">{p.name}</div>
                          <div className="text-muted text-[12px]">{m[p.mode]} · <span className="mono">{p.ip}</span>{st?.handshake ? ` · ${tr("vu {0}", ago(st.handshake * 1000))} · ↓${bytes(st.tx)} ↑${bytes(st.rx)}` : ""}</div>
                        </div>
                        <IconButton title={tr("QR code et configuration")} onClick={() => showPeer.mutate(p.id)}><QrCode size={15} /></IconButton>
                        <IconButton title={tr("Supprimer")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer {0} ?", p.name), text: tr("L'appareil ne pourra plus se connecter."), danger: true })) delPeer.mutate(p.id); }}><Trash2 size={15} /></IconButton>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          </Card>
        )}

        <Card title={<span className="flex items-center gap-2"><Globe size={16} className="text-accent" />{tr("DNS dynamique")}</span>} actions={<Button size="sm" variant="primary" loading={saveDDNS.isPending} onClick={() => saveDDNS.mutate()}>{tr("Enregistrer")}</Button>}>
          <div className="space-y-4">
            <div className="text-muted text-[13px]">{tr("Votre box change d'adresse publique ? Un nom gratuit la suit automatiquement (vérification toutes les 5 minutes).")}</div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Service")}>
                <Select value={d.provider} onChange={(e) => setD({ ...d, provider: e.target.value as DDNS["provider"] })}>
                  <option value="">{tr("Aucun")}</option>
                  <option value="duckdns">DuckDNS ({tr("gratuit")})</option>
                  <option value="dynv6">dynv6 ({tr("gratuit")})</option>
                  <option value="cloudflare">Cloudflare</option>
                  <option value="custom">{tr("URL personnalisée")}</option>
                </Select>
              </Field>
              {d.provider && d.provider !== "custom" && <Field label={d.provider === "duckdns" ? tr("Sous-domaine") : tr("Nom d'hôte")} hint={d.provider === "duckdns" ? "monnas → monnas.duckdns.org" : "nas.exemple.fr"}><Input value={d.hostname} onChange={(e) => setD({ ...d, hostname: e.target.value })} /></Field>}
              {d.provider && d.provider !== "custom" && <Field label={d.provider === "cloudflare" ? tr("Jeton d'API (droit Zone DNS : modification)") : tr("Jeton")}><Input type="password" autoComplete="off" value={d.token ?? ""} onChange={(e) => setD({ ...d, token: e.target.value })} /></Field>}
              {d.provider === "cloudflare" && <Field label={tr("Identifiant de zone")}><Input className="mono" value={d.zoneId ?? ""} onChange={(e) => setD({ ...d, zoneId: e.target.value.trim() })} /></Field>}
              {d.provider === "custom" && <Field label={tr("URL de mise à jour")} hint={tr("{ip} est remplacé par l'adresse publique")} className="sm:col-span-2"><Input className="mono" value={d.url ?? ""} onChange={(e) => setD({ ...d, url: e.target.value })} placeholder="https://…?ip={ip}" /></Field>}
            </div>
            {data.ddns.provider && (
              <div className="text-[13px]">
                {tr("Adresse publique")} : <span className="mono">{data.ddns.lastIp || "—"}</span>
                {data.ddns.lastUpdate && !data.ddns.lastUpdate.startsWith("0001") && <> · {tr("vérifiée le {0}", dateTime(data.ddns.lastUpdate))} · <span className={data.ddns.lastResult === "OK" ? "text-ok" : "text-danger"}>{srv(data.ddns.lastResult)}</span></>}
              </div>
            )}
          </div>
        </Card>
      </div>
      {show && <ConfigModal name={show.name} config={show.config} onClose={() => setShow(null)} />}
    </div>
  );
}
