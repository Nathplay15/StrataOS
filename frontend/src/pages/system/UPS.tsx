import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { BatteryCharging, BatteryLow, BatteryWarning, Plug, Search, Zap } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { duration } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Notice, PageHeader, Progress, Select, Spinner, Switch } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Config = {
  mode: "off" | "usb" | "netclient" | "simulation";
  driver?: string; port?: string; desc?: string;
  remoteHost?: string; remotePort?: number; remoteName?: string; remoteUser?: string; remotePassword?: string;
  share: boolean; shareUser?: string; sharePassword?: string;
  shutdownAfter: number;
};
type Status = { status: string; charge: number | null; runtime: number | null; load: number | null; input: number | null; model: string };
type UPSData = { installed: boolean; config: Config; status?: Status; error?: string };

function statusLabel(s: string): [string, "ok" | "warn" | "danger" | "neutral"] {
  if (s.includes("LB")) return [tr("Low battery"), "danger"];
  if (s.startsWith("OB")) return [tr("On battery"), "warn"];
  if (s.startsWith("OL")) return [s.includes("CHRG") ? tr("Mains (on charge)") : tr("On mains power"), "ok"];
  return [s || tr("Unknown"), "neutral"];
}

export default function UPSPage() {
  const { data, isLoading, error } = useQuery({ queryKey: ["ups"], queryFn: () => api.get<UPSData>("/api/ups"), refetchInterval: 5000 });
  const [c, setC] = useState<Config | null>(null);
  const [minutes, setMinutes] = useState(5);
  const detect = useQuery({ queryKey: ["ups-detect"], queryFn: () => api.get<{ driver: string; port: string; vendor: string; product: string }[]>("/api/ups/detect"), enabled: false });
  const install = useAction(() => api.post("/api/system/components/nut/install"));
  const save = useAction(() => api.put("/api/ups", c), { success: tr("UPS configuration applied") });
  const simulate = useAction((v: { status: string; charge: number }) => api.post("/api/ups/simulate", v), { success: tr("Simulated state changed") });
  useEffect(() => {
    if (data && !c) {
      setC(data.config);
      if (data.config.shutdownAfter > 0) setMinutes(Math.round(data.config.shutdownAfter / 60));
    }
  }, [data, c]);

  if (isLoading || !data || !c) return <div><PageHeader title={tr("UPS")} icon={<BatteryCharging size={20} />} /><ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) {
    return (
      <div>
        <PageHeader title={tr("UPS")} subtitle={tr("Gracefully shut down the NAS on power failure")} icon={<BatteryCharging size={20} />} />
        <Card><Empty icon={<Plug />} title={tr("Network UPS Tools is not installed")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card>
      </div>
    );
  }
  const set = (k: Partial<Config>) => setC({ ...c, ...k });
  const s = data.status;
  const [label, tone] = statusLabel(s?.status ?? "");

  return (
    <div>
      <PageHeader title={tr("UPS")} subtitle={tr("Gracefully shut down the NAS on power failure (NUT)")} icon={<BatteryCharging size={20} />} actions={<Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Apply")}</Button>} />
      <ErrorBox error={error || save.error} />
      <div className="space-y-5">
        {data.config.mode !== "off" && (
          <Card title={tr("Status")}>
            {!s ? <Notice tone="warn">{tr("UPS unreachable:")}{" "}{data.error}</Notice> : (
              <div className="grid gap-5 sm:grid-cols-4">
                <div className="space-y-1"><div className="text-muted text-[12px]">{tr("Power")}</div><Badge tone={tone} dot>{label}</Badge><div className="text-muted text-[12px]">{s.model}</div></div>
                <div><div className="mb-1 flex justify-between text-[12px]"><span className="text-muted">{tr("Battery")}</span><span>{s.charge ?? "—"} %</span></div><Progress value={s.charge ?? 0} tone={(s.charge ?? 0) < 30 ? "danger" : (s.charge ?? 0) < 60 ? "warn" : "ok"} /></div>
                <div><div className="text-muted text-[12px]">{tr("Estimated runtime")}</div><div className="font-semibold">{s.runtime ? duration(s.runtime) : "—"}</div></div>
                <div><div className="text-muted text-[12px]">{tr("Load / input voltage")}</div><div className="font-semibold">{s.load ?? "—"} % · {s.input ?? "—"} V</div></div>
              </div>
            )}
          </Card>
        )}

        <Card title={tr("Configuration")}>
          <div className="space-y-5">
            <Field label={tr("Mode")}>
              <Select value={c.mode} onChange={(e) => set({ mode: e.target.value as Config["mode"] })}>
                <option value="off">{tr("Disabled")}</option>
                <option value="usb">{tr("UPS connected to this NAS (USB, serial, SNMP)")}</option>
                <option value="netclient">{tr("UPS managed by another NUT server (Synology, other NAS…)")}</option>
                <option value="simulation">{tr("Simulation (test, no UPS)")}</option>
              </Select>
            </Field>

            {c.mode === "usb" && (
              <div className="space-y-4 rounded-xl border border-line p-4">
                <div className="flex flex-wrap items-end gap-3">
                  <Field label={tr("Driver")} className="w-56">
                    <Select value={c.driver || "usbhid-ups"} onChange={(e) => set({ driver: e.target.value })}>
                      {["usbhid-ups", "nutdrv_qx", "blazer_usb", "bcmxcp_usb", "riello_usb", "tripplite_usb", "blazer_ser", "apcsmart", "snmp-ups"].map((d) => <option key={d}>{d}</option>)}
                    </Select>
                  </Field>
                  <Field label={tr("Port")} className="w-56" hint={tr("auto for USB")}><Input className="mono" value={c.port || "auto"} onChange={(e) => set({ port: e.target.value })} /></Field>
                  <Button icon={<Search size={15} />} loading={detect.isFetching} onClick={() => detect.refetch()}>{tr("Detect")}</Button>
                </div>
                {detect.data && (detect.data.length === 0 ? <Notice tone="warn">{tr("No USB UPS detected. Check the USB cable.")}</Notice> : (
                  <div className="space-y-1">
                    {detect.data.map((d, i) => (
                      <button key={i} onClick={() => set({ driver: d.driver, port: d.port })} className="flex w-full justify-between rounded-lg bg-panel-2 px-3 py-2 text-left text-[13px] hover:bg-accent-soft">
                        <span><b>{d.vendor} {d.product}</b></span><span className="mono text-muted">{d.driver} · {d.port}</span>
                      </button>
                    ))}
                  </div>
                ))}
                <Switch checked={c.share} onChange={(v) => set({ share: v })} label={tr("Share the UPS on the network")} hint={tr("Other machines (PC, another NAS) can monitor this UPS and shut down as well")} />
                {c.share && (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={tr("User for clients")}><Input value={c.shareUser ?? "monuser"} onChange={(e) => set({ shareUser: e.target.value })} /></Field>
                    <Field label={tr("Password")}><Input type="password" value={c.sharePassword ?? ""} onChange={(e) => set({ sharePassword: e.target.value })} /></Field>
                  </div>
                )}
              </div>
            )}

            {c.mode === "netclient" && (
              <div className="grid gap-4 rounded-xl border border-line p-4 sm:grid-cols-2">
                <Field label={tr("NUT server")}><Input value={c.remoteHost ?? ""} onChange={(e) => set({ remoteHost: e.target.value })} placeholder="192.168.1.10" /></Field>
                <Field label={tr("UPS name")} hint={tr("\"ups\" for a Synology")}><Input value={c.remoteName ?? "ups"} onChange={(e) => set({ remoteName: e.target.value })} /></Field>
                <Field label={tr("User")} hint={tr("Synology: myuser")}><Input value={c.remoteUser ?? ""} onChange={(e) => set({ remoteUser: e.target.value })} /></Field>
                <Field label={tr("Password")} hint={tr("Synology: secret")}><Input type="password" value={c.remotePassword ?? ""} onChange={(e) => set({ remotePassword: e.target.value })} /></Field>
              </div>
            )}

            {c.mode === "simulation" && (
              <Notice>{tr("Test mode: a fake UPS is simulated to verify alerts and automatic shutdown.")}{" "}<b>{tr("The shutdown is real")}</b> {" "}{tr("if the timeout is reached.")}</Notice>
            )}

            {c.mode !== "off" && (
              <div className="space-y-3 rounded-xl border border-line p-4">
                <div className="font-medium text-[13px]">{tr("Automatic NAS shutdown")}</div>
                <Switch checked={c.shutdownAfter === 0} onChange={(v) => set({ shutdownAfter: v ? 0 : minutes * 60 })} label={tr("Only when the battery is low")} hint={tr("The NAS stays on as long as the UPS can sustain it")} />
                {c.shutdownAfter > 0 && (
                  <Field label={tr("After a power outage of (minutes)")} className="w-56"><Input type="number" min={1} value={minutes} onChange={(e) => { const m = Math.max(1, +e.target.value); setMinutes(m); set({ shutdownAfter: m * 60 }); }} /></Field>
                )}
              </div>
            )}
          </div>
        </Card>

        {data.config.mode === "simulation" && (
          <Card title={tr("Simulate")}>
            <div className="flex flex-wrap gap-2">
              <Button icon={<Zap size={15} />} onClick={() => simulate.mutate({ status: "OL", charge: 100 })}>{tr("Normal power")}</Button>
              <Button icon={<BatteryWarning size={15} />} onClick={() => simulate.mutate({ status: "OB", charge: 80 })}>{tr("Power failure")}</Button>
              <Button variant="danger" icon={<BatteryLow size={15} />} onClick={() => simulate.mutate({ status: "OB LB", charge: 8 })}>{tr("Low battery (shutdown)")}</Button>
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}
