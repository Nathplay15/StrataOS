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
  if (s.includes("LB")) return [tr("Batterie faible"), "danger"];
  if (s.startsWith("OB")) return [tr("Sur batterie"), "warn"];
  if (s.startsWith("OL")) return [s.includes("CHRG") ? tr("Secteur (en charge)") : tr("Sur secteur"), "ok"];
  return [s || tr("Inconnu"), "neutral"];
}

export default function UPSPage() {
  const { data, isLoading, error } = useQuery({ queryKey: ["ups"], queryFn: () => api.get<UPSData>("/api/ups"), refetchInterval: 5000 });
  const [c, setC] = useState<Config | null>(null);
  const [minutes, setMinutes] = useState(5);
  const detect = useQuery({ queryKey: ["ups-detect"], queryFn: () => api.get<{ driver: string; port: string; vendor: string; product: string }[]>("/api/ups/detect"), enabled: false });
  const install = useAction(() => api.post("/api/system/components/nut/install"));
  const save = useAction(() => api.put("/api/ups", c), { success: tr("Configuration de l'onduleur appliquée") });
  const simulate = useAction((v: { status: string; charge: number }) => api.post("/api/ups/simulate", v), { success: tr("État simulé modifié") });
  useEffect(() => {
    if (data && !c) {
      setC(data.config);
      if (data.config.shutdownAfter > 0) setMinutes(Math.round(data.config.shutdownAfter / 60));
    }
  }, [data, c]);

  if (isLoading || !data || !c) return <div><PageHeader title={tr("Onduleur")} icon={<BatteryCharging size={20} />} /><ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) {
    return (
      <div>
        <PageHeader title={tr("Onduleur")} subtitle={tr("Arrêt propre du NAS en cas de coupure de courant")} icon={<BatteryCharging size={20} />} />
        <Card><Empty icon={<Plug />} title={tr("Network UPS Tools n'est pas installé")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button>} /></Card>
      </div>
    );
  }
  const set = (k: Partial<Config>) => setC({ ...c, ...k });
  const s = data.status;
  const [label, tone] = statusLabel(s?.status ?? "");

  return (
    <div>
      <PageHeader title={tr("Onduleur")} subtitle={tr("Arrêt propre du NAS en cas de coupure de courant (NUT)")} icon={<BatteryCharging size={20} />} actions={<Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Appliquer")}</Button>} />
      <ErrorBox error={error || save.error} />
      <div className="space-y-5">
        {data.config.mode !== "off" && (
          <Card title={tr("État")}>
            {!s ? <Notice tone="warn">{tr("Onduleur injoignable :")}{" "}{data.error}</Notice> : (
              <div className="grid gap-5 sm:grid-cols-4">
                <div className="space-y-1"><div className="text-muted text-[12px]">{tr("Alimentation")}</div><Badge tone={tone} dot>{label}</Badge><div className="text-muted text-[12px]">{s.model}</div></div>
                <div><div className="mb-1 flex justify-between text-[12px]"><span className="text-muted">{tr("Batterie")}</span><span>{s.charge ?? "—"} %</span></div><Progress value={s.charge ?? 0} tone={(s.charge ?? 0) < 30 ? "danger" : (s.charge ?? 0) < 60 ? "warn" : "ok"} /></div>
                <div><div className="text-muted text-[12px]">{tr("Autonomie estimée")}</div><div className="font-semibold">{s.runtime ? duration(s.runtime) : "—"}</div></div>
                <div><div className="text-muted text-[12px]">{tr("Charge / tension d'entrée")}</div><div className="font-semibold">{s.load ?? "—"} % · {s.input ?? "—"} V</div></div>
              </div>
            )}
          </Card>
        )}

        <Card title={tr("Configuration")}>
          <div className="space-y-5">
            <Field label={tr("Mode")}>
              <Select value={c.mode} onChange={(e) => set({ mode: e.target.value as Config["mode"] })}>
                <option value="off">{tr("Désactivé")}</option>
                <option value="usb">{tr("Onduleur branché sur ce NAS (USB, série, SNMP)")}</option>
                <option value="netclient">{tr("Onduleur géré par un autre serveur NUT (Synology, autre NAS…)")}</option>
                <option value="simulation">{tr("Simulation (test, sans onduleur)")}</option>
              </Select>
            </Field>

            {c.mode === "usb" && (
              <div className="space-y-4 rounded-xl border border-line p-4">
                <div className="flex flex-wrap items-end gap-3">
                  <Field label={tr("Pilote")} className="w-56">
                    <Select value={c.driver || "usbhid-ups"} onChange={(e) => set({ driver: e.target.value })}>
                      {["usbhid-ups", "nutdrv_qx", "blazer_usb", "bcmxcp_usb", "riello_usb", "tripplite_usb", "blazer_ser", "apcsmart", "snmp-ups"].map((d) => <option key={d}>{d}</option>)}
                    </Select>
                  </Field>
                  <Field label={tr("Port")} className="w-56" hint={tr("auto pour l'USB")}><Input className="mono" value={c.port || "auto"} onChange={(e) => set({ port: e.target.value })} /></Field>
                  <Button icon={<Search size={15} />} loading={detect.isFetching} onClick={() => detect.refetch()}>{tr("Détecter")}</Button>
                </div>
                {detect.data && (detect.data.length === 0 ? <Notice tone="warn">{tr("Aucun onduleur USB détecté. Vérifiez le câble USB.")}</Notice> : (
                  <div className="space-y-1">
                    {detect.data.map((d, i) => (
                      <button key={i} onClick={() => set({ driver: d.driver, port: d.port })} className="flex w-full justify-between rounded-lg bg-panel-2 px-3 py-2 text-left text-[13px] hover:bg-accent-soft">
                        <span><b>{d.vendor} {d.product}</b></span><span className="mono text-muted">{d.driver} · {d.port}</span>
                      </button>
                    ))}
                  </div>
                ))}
                <Switch checked={c.share} onChange={(v) => set({ share: v })} label={tr("Partager l'onduleur sur le réseau")} hint={tr("D'autres machines (PC, autre NAS) peuvent surveiller cet onduleur et s'arrêter elles aussi")} />
                {c.share && (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={tr("Utilisateur pour les clients")}><Input value={c.shareUser ?? "monuser"} onChange={(e) => set({ shareUser: e.target.value })} /></Field>
                    <Field label={tr("Mot de passe")}><Input type="password" value={c.sharePassword ?? ""} onChange={(e) => set({ sharePassword: e.target.value })} /></Field>
                  </div>
                )}
              </div>
            )}

            {c.mode === "netclient" && (
              <div className="grid gap-4 rounded-xl border border-line p-4 sm:grid-cols-2">
                <Field label={tr("Serveur NUT")}><Input value={c.remoteHost ?? ""} onChange={(e) => set({ remoteHost: e.target.value })} placeholder="192.168.1.10" /></Field>
                <Field label={tr("Nom de l'onduleur")} hint={tr("« ups » pour un Synology")}><Input value={c.remoteName ?? "ups"} onChange={(e) => set({ remoteName: e.target.value })} /></Field>
                <Field label={tr("Utilisateur")} hint={tr("Synology : monuser")}><Input value={c.remoteUser ?? ""} onChange={(e) => set({ remoteUser: e.target.value })} /></Field>
                <Field label={tr("Mot de passe")} hint={tr("Synology : secret")}><Input type="password" value={c.remotePassword ?? ""} onChange={(e) => set({ remotePassword: e.target.value })} /></Field>
              </div>
            )}

            {c.mode === "simulation" && (
              <Notice>{tr("Mode de test : un onduleur fictif est simulé pour vérifier les alertes et l'arrêt automatique.")}{" "}<b>{tr("L'arrêt est réel")}</b> {" "}{tr("si le délai est atteint.")}</Notice>
            )}

            {c.mode !== "off" && (
              <div className="space-y-3 rounded-xl border border-line p-4">
                <div className="font-medium text-[13px]">{tr("Arrêt automatique du NAS")}</div>
                <Switch checked={c.shutdownAfter === 0} onChange={(v) => set({ shutdownAfter: v ? 0 : minutes * 60 })} label={tr("Uniquement quand la batterie est faible")} hint={tr("Le NAS reste allumé tant que l'onduleur le peut")} />
                {c.shutdownAfter > 0 && (
                  <Field label={tr("Après une coupure de (minutes)")} className="w-56"><Input type="number" min={1} value={minutes} onChange={(e) => { const m = Math.max(1, +e.target.value); setMinutes(m); set({ shutdownAfter: m * 60 }); }} /></Field>
                )}
              </div>
            )}
          </div>
        </Card>

        {data.config.mode === "simulation" && (
          <Card title={tr("Simuler")}>
            <div className="flex flex-wrap gap-2">
              <Button icon={<Zap size={15} />} onClick={() => simulate.mutate({ status: "OL", charge: 100 })}>{tr("Courant normal")}</Button>
              <Button icon={<BatteryWarning size={15} />} onClick={() => simulate.mutate({ status: "OB", charge: 80 })}>{tr("Coupure de courant")}</Button>
              <Button variant="danger" icon={<BatteryLow size={15} />} onClick={() => simulate.mutate({ status: "OB LB", charge: 8 })}>{tr("Batterie faible (arrêt)")}</Button>
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}
