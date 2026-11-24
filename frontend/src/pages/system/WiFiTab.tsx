import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, ScanSearch, Trash2, Wifi, WifiOff } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, Spinner, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";
import { TrialModal, type Config, type Pending } from "./Network";

type WiFiLink = { connected: boolean; ssid?: string; signalDbm?: number; freqMhz?: number; state: string };
type WiFiIface = { name: string; status: WiFiLink; config: { configured: boolean; ssid?: string; hidden?: boolean; enabled?: boolean; pskSet?: boolean } };
type WiFiData = { ifaces: WiFiIface[]; wpaInstalled: boolean; iwInstalled: boolean };
type ScanNet = { ssid: string; bssid: string; signalDbm: number; freqMhz?: number; security: string };
type NetData = { config: Config; pending: Pending | null };

const secTone = (s: string) => (s === "open" ? "neutral" : s === "WEP" ? "danger" : "ok") as "neutral" | "danger" | "ok";

/** WiFi tab of the Network page: scan, connect (WPA) and forget the wireless
 *  links of the NAS. A change goes through the same trial apply as any network
 *  configuration change. */
export function WiFiTab() {
  const qc = useQueryClient();
  const { confirm } = useUI();
  const wifi = useQuery({ queryKey: ["network-wifi"], queryFn: () => api.get<WiFiData>("/api/system/network/wifi"), refetchInterval: 5000 });
  const net = useQuery({ queryKey: ["network"], queryFn: () => api.get<NetData>("/api/system/network") });
  const [scanIface, setScanIface] = useState<string | null>(null);
  const [scanRes, setScanRes] = useState<ScanNet[] | null>(null);
  const [target, setTarget] = useState<{ iface: string; ssid: string; security: string } | null>(null);
  const [psk, setPsk] = useState("");
  const [trial, setTrial] = useState<Pending | null>(null);
  const scan = useAction((iface: string) => api.post<{ networks: ScanNet[] }>("/api/system/network/wifi/scan", { iface }), {
    onDone: (r) => setScanRes(r.networks),
  });
  const apply = useAction((cfg: Config) => api.put<Pending>("/api/system/network/config", { config: cfg, timeout: 120 }), {
    onDone: (p) => setTrial(p),
  });

  const openScan = (iface: string) => {
    setScanIface(iface);
    setScanRes(null);
    scan.mutate(iface);
  };

  const connect = async () => {
    if (!target || !net.data) return;
    const cfg = structuredClone(net.data.config);
    cfg.wifi = (cfg.wifi ?? []).filter((w) => w.name !== target.iface);
    cfg.wifi.push({ name: target.iface, ssid: target.ssid, psk: target.security === "open" ? "" : psk, enabled: true });
    if (!cfg.interfaces.some((i) => i.name === target.iface)) {
      cfg.interfaces = [...cfg.interfaces, { name: target.iface, mode: "dhcp" as const }].sort((a, b) => a.name.localeCompare(b.name));
    }
    setTarget(null);
    setPsk("");
    if (await confirm({ title: tr("Apply network configuration?"), text: tr("The WiFi change is applied on trial for 2 minutes, like any network change: if the NAS becomes unreachable, the previous configuration comes back."), confirm: tr("Apply on trial") })) {
      apply.mutate(cfg);
    }
  };

  const forget = async (name: string) => {
    if (!net.data) return;
    if (!(await confirm({ title: tr("Forget this network"), text: tr("The saved WiFi credentials of {0} are removed; the link will not connect automatically anymore.", name), danger: true }))) return;
    const cfg = structuredClone(net.data.config);
    cfg.wifi = (cfg.wifi ?? []).filter((w) => w.name !== name);
    apply.mutate(cfg);
  };

  if (wifi.isLoading || net.isLoading) return <Spinner />;
  const data = wifi.data;
  return (
    <div className="mt-5 space-y-5">
      <ErrorBox error={wifi.error || net.error || scan.error || apply.error} />
      {data && !data.wpaInstalled && (
        <Notice tone="warn">{tr("WiFi needs the wpasupplicant and iw packages on the NAS (apt install wpasupplicant iw).")}</Notice>
      )}
      {data && data.ifaces.length === 0 && (
        <Card title={<span className="flex items-center gap-2"><Wifi size={16} /> {tr("Wireless links")}</span>}>
          <Notice>{tr("No wireless interface detected on this NAS.")}</Notice>
        </Card>
      )}
      {data?.ifaces.map((f) => (
        <Card key={f.name} title={<span className="flex items-center gap-2"><Wifi size={16} /> <span className="mono">{f.name}</span></span>}
          actions={<>
            <Button size="sm" icon={<ScanSearch size={14} />} loading={scan.isPending && scanIface === f.name} disabled={!data.iwInstalled} onClick={() => openScan(f.name)}>{tr("Scan for networks")}</Button>
            {f.config.configured && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={() => forget(f.name)}>{tr("Forget this network")}</Button>}
          </>}>
          <div className="flex flex-wrap items-center gap-2 text-[13px]">
            {f.status.connected ? (
              <Badge tone="ok"><Wifi size={12} /> {tr("Connected to {0}", f.status.ssid || "")}</Badge>
            ) : (
              <Badge tone="neutral"><WifiOff size={12} /> {tr("Not connected")}</Badge>
            )}
            <Badge tone="neutral">{tr("State")}: {f.status.state}</Badge>
            {f.status.connected && f.status.signalDbm !== undefined && <Badge tone="neutral">{tr("Signal: {0} dBm", f.status.signalDbm)}</Badge>}
            {f.config.configured && <Badge tone={f.config.enabled ? "ok" : "neutral"}>{tr("Saved on this NAS")}{f.config.ssid ? `: ${f.config.ssid}` : ""}</Badge>}
          </div>
        </Card>
      ))}

      <Modal open={scanIface !== null} onClose={() => setScanIface(null)} title={tr("Networks in range") + (scanIface ? ` (${scanIface})` : "")} wide>
        {scan.error && scanRes === null ? <div className="p-2 text-[13px] text-danger">{String((scan.error as Error).message || scan.error)}</div> : scan.isPending || scanRes === null ? <div className="p-2 text-[13px] text-muted">{tr("Scanning…")}</div> : (
          scanRes.length === 0 ? <div className="p-2 text-[13px] text-muted">{tr("No network found.")}</div> : (
            <div className="divide-y divide-line">
              {scanRes.map((n) => (
                <div key={n.bssid + n.ssid} className="flex flex-wrap items-center gap-2 px-2 py-2.5 text-[13px]">
                  {n.security !== "open" && <Lock size={13} className="text-muted" />}
                  <span className="min-w-0 flex-1 truncate font-medium">{n.ssid}</span>
                  <Badge tone="neutral">{tr("Signal: {0} dBm", n.signalDbm)}</Badge>
                  <Badge tone={secTone(n.security)}>{n.security === "open" ? tr("Open") : n.security}</Badge>
                  <Button size="sm" variant="primary" onClick={() => { setTarget({ iface: scanIface!, ssid: n.ssid, security: n.security }); setScanIface(null); setScanRes(null); }}>{tr("Connect")}</Button>
                </div>
              ))}
            </div>
          )
        )}
      </Modal>

      <Modal open={target !== null} onClose={() => { setTarget(null); setPsk(""); }} title={target ? tr("Connect to {0}", target.ssid) : ""}>
        {target && (
          <div className="space-y-4 p-1">
            <div className="text-[13px] text-muted">{target.iface} · <Badge tone={secTone(target.security)}>{target.security === "open" ? tr("Open") : target.security}</Badge></div>
            {target.security !== "open" && (
              <Field label={tr("WiFi password (WPA, 8 to 63 characters):")}>
                <Input type="password" autoComplete="off" value={psk} onChange={(e) => setPsk(e.target.value)} />
              </Field>
            )}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={() => { setTarget(null); setPsk(""); }}>{tr("Cancel")}</Button>
              <Button variant="primary" disabled={target.security !== "open" && (psk.length < 8 || psk.length > 63)} loading={apply.isPending} onClick={connect}>{tr("Connect")}</Button>
            </div>
          </div>
        )}
      </Modal>

      {trial && <TrialModal pending={trial} newURLs={[]} onClose={() => { setTrial(null); qc.invalidateQueries({ queryKey: ["network"] }); qc.invalidateQueries({ queryKey: ["network-wifi"] }); }} onConfirmed={() => { qc.invalidateQueries({ queryKey: ["network"] }); qc.invalidateQueries({ queryKey: ["network-wifi"] }); }} />}
    </div>
  );
}
