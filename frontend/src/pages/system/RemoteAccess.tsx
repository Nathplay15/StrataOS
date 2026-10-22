import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Globe, Plug, Waypoints } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { dateTime } from "../../lib/format";
import { Button, Card, ErrorBox, Field, Input, PageHeader, Select, Spinner, Switch } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";
import RemoteExtras from "./RemoteExtras";
import { modes, WireGuardTabs, type Mode, type WGConfig, type WGData } from "./WireGuard";

type VPN = WGConfig & { port: number; publicKey: string };
type DDNS = { provider: "" | "duckdns" | "cloudflare" | "dynv6" | "custom"; hostname: string; token?: string; zoneId?: string; url?: string; lastIp: string; lastUpdate: string; lastResult: string };
type Data = WGData & { installed: boolean; config: VPN; serverIp: string; lanGuess: string; ddns: DDNS; ddnsHost: string };

export default function RemoteAccess() {
  const { data, isLoading, error } = useQuery({ queryKey: ["vpn"], queryFn: () => api.get<Data>("/api/vpn"), refetchInterval: 5000 });
  const install = useAction(() => api.post("/api/system/components/vpn/install"));
  const [c, setC] = useState<VPN | null>(null);
  const [d, setD] = useState<DDNS | null>(null);
  useEffect(() => {
    if (data && !c) setC({ ...data.config, lan: data.config.lan || data.lanGuess, endpoint: data.config.endpoint || data.ddnsHost });
    if (data && !d) setD(data.ddns);
  }, [data, c, d]);
  const save = useAction(() => api.put("/api/vpn", c), { success: tr("VPN saved") });
  const saveDDNS = useAction(() => api.put<DDNS>("/api/ddns", d), { success: tr("Dynamic DNS saved"), onDone: (r) => setD(r) });

  const header = <PageHeader title={tr("Remote access")} subtitle={tr("WireGuard VPN and dynamic DNS: access the NAS from outside without exposing its services")} icon={<Waypoints size={20} />} />;
  if (isLoading || !data || !c || !d) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  const m = modes();

  return (
    <div>
      {header}
      <ErrorBox error={save.error || saveDDNS.error} />
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
                <Field label={tr("Warn before a device expires")} hint={tr("A notification to the administrators, once per device")}>
                  <Select value={c.expiryWarnDays ?? 0} onChange={(e) => setC({ ...c, expiryWarnDays: +e.target.value })}>
                    <option value={-1}>{tr("Never")}</option>
                    {[1, 3, 0, 14, 30].map((n) => <option key={n} value={n}>{n === 1 ? tr("1 day before") : tr("{0} days before", n || 7)}</option>)}
                  </Select>
                </Field>
                <Field label={tr("Default access of new devices")}><Select value={c.defaultMode ?? "lan"} onChange={(e) => setC({ ...c, defaultMode: e.target.value as Mode })}>{(Object.keys(m) as Mode[]).map((k) => <option key={k} value={k}>{m[k]}</option>)}</Select></Field>
              </div>
              <Switch checked={!!c.userDevices} onChange={(v) => setC({ ...c, userDevices: v })} label={tr("Users can add their own devices")} hint={tr("From \"My VPN devices\" in their account menu, with the default access")} />
            </div>
          )}
        </Card>

        {data.installed && data.config.publicKey && <WireGuardTabs data={data} />}

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
    </div>
  );
}
