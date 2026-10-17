import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cpu, HardDrive, Leaf, Moon, Plus, Power, Trash2, Zap } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, ErrorBox, Field, Input, Notice, PageHeader, Select, Spinner, Switch } from "../../components/ui";
import { locale, tr } from "../../lib/i18n";

type Schedule = { enabled: boolean; days: number[]; shutdown: string; wake: string };
type Device = { name: string; mac: string };
type Config = { spindownMin: number; governor?: string; schedule: Schedule; wol?: string[]; devices: Device[] };
type NIC = { name: string; mac: string; supported: boolean; enabled: boolean };
type Data = {
  config: Config; hdparm: boolean; disks: { path: string; model: string; size: number; state: string }[];
  governor: string; governors: string[]; rtcWake: boolean; nics: NIC[] | null;
  nextShutdown?: string; nextWake?: string; watts?: number; upsLoad?: number;
};

const govLabel = (g: string) => ({
  powersave: tr("Économie d'énergie"), schedutil: tr("Équilibré (selon la charge)"), ondemand: tr("À la demande"),
  conservative: tr("Progressif"), performance: tr("Performances maximales"), userspace: tr("Manuel"),
}[g] ?? g);

export default function Energy() {
  const { data, isLoading, error } = useQuery({ queryKey: ["energy"], queryFn: () => api.get<Data>("/api/energy"), refetchInterval: 10000 });
  const [c, setC] = useState<Config | null>(null);
  const [dev, setDev] = useState<Device>({ name: "", mac: "" });
  useEffect(() => { if (data && !c) setC({ ...data.config, devices: data.config.devices ?? [], schedule: { ...data.config.schedule, days: data.config.schedule?.days ?? [] } }); }, [data, c]);
  const save = useAction(() => api.put<{ warning?: string } | null>("/api/energy", c), { success: (r) => r?.warning ?? tr("Réglages d'énergie appliqués") });
  const standby = useAction(() => api.post<{ warning?: string } | null>("/api/energy/standby"), { success: (r) => r?.warning ?? tr("Disques mis en veille") });
  const wake = useAction((mac: string) => api.post("/api/energy/wake", { mac }), { success: tr("Paquet de réveil envoyé") });

  const header = <PageHeader title={tr("Énergie")} subtitle={tr("Veille des disques, processeur, arrêt programmé et réveil par le réseau")} icon={<Leaf size={20} />} actions={c && <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Enregistrer")}</Button>} />;
  if (isLoading || !data || !c) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;

  const asleep = data.disks.filter((d) => d.state === "standby").length;
  const days = Array.from({ length: 7 }, (_, i) => (i + 1) % 7); // Monday first
  const dayName = (d: number) => new Intl.DateTimeFormat(locale, { weekday: "short" }).format(new Date(2026, 0, 4 + d));
  const set = (s: Partial<Schedule>) => setC({ ...c, schedule: { ...c.schedule, ...s } });

  return (
    <div>
      {header}
      <ErrorBox error={save.error} />
      <div className="mb-5 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card><div className="flex items-center gap-3"><Zap size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Consommation")}</div><div className="text-[18px] font-semibold">{data.watts ? `${Math.round(data.watts)} W` : "—"}</div>{!data.watts && <div className="text-muted text-[11.5px]">{tr("Mesurée par un onduleur compatible")}</div>}</div></div></Card>
        <Card><div className="flex items-center gap-3"><Moon size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Disques en veille")}</div><div className="text-[18px] font-semibold">{data.disks.length ? `${asleep} / ${data.disks.length}` : "—"}</div></div></div></Card>
        <Card><div className="flex items-center gap-3"><Cpu size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Processeur")}</div><div className="text-[15px] font-semibold">{data.governor ? govLabel(data.governor) : "—"}</div></div></div></Card>
        <Card><div className="flex items-center gap-3"><Power size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Prochain arrêt")}</div><div className="text-[14px] font-semibold">{data.nextShutdown ? dateTime(data.nextShutdown) : "—"}</div>{data.nextWake && <div className="text-muted text-[11.5px]">{tr("Redémarrage {0}", dateTime(data.nextWake))}</div>}</div></div></Card>
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><HardDrive size={16} />{tr("Veille des disques")}</span>} actions={data.disks.length > 0 && data.hdparm && <Button size="sm" loading={standby.isPending} onClick={() => standby.mutate()}>{tr("Mettre en veille maintenant")}</Button>}>
          <div className="space-y-4">
            <Field label={tr("Mettre en veille les disques durs inactifs après")} hint={tr("Les SSD et les disques USB ne sont pas concernés. Un disque se réveille en quelques secondes au premier accès.")}>
              <Select value={c.spindownMin} onChange={(e) => setC({ ...c, spindownMin: +e.target.value })}>
                <option value={0}>{tr("Jamais")}</option>
                {[10, 20, 30, 60, 120, 240].map((m) => <option key={m} value={m}>{m < 60 ? tr("{0} minutes", m) : tr("{0} heure(s)", m / 60)}</option>)}
              </Select>
            </Field>
            {data.disks.length === 0 ? <div className="text-muted text-[13px]">{tr("Aucun disque dur rotatif détecté.")}</div> : (
              <div className="space-y-1.5">
                {data.disks.map((d) => (
                  <div key={d.path} className="flex items-center gap-3 text-[13px]">
                    <span className="mono w-20">{d.path}</span><span className="flex-1 truncate text-muted">{d.model} · {bytes(d.size)}</span>
                    <Badge tone={d.state === "standby" ? "ok" : d.state === "active" ? "accent" : "neutral"} dot>{d.state === "standby" ? tr("En veille") : d.state === "active" ? tr("Actif") : tr("Inconnu")}</Badge>
                  </div>
                ))}
              </div>
            )}
            <p className="text-muted text-[12px]">{tr("Les contrôles SMART périodiques ne réveillent pas les disques en veille.")}</p>
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Cpu size={16} />{tr("Processeur")}</span>}>
          {!data.governors?.length ? <div className="text-muted text-[13px]">{tr("Cette machine ne permet pas de régler la fréquence du processeur (fréquent dans une machine virtuelle).")}</div> : (
            <Field label={tr("Politique de fréquence")} hint={tr("« Équilibré » convient à la plupart des NAS : la fréquence monte seulement quand c'est nécessaire.")}>
              <Select value={c.governor || data.governor} onChange={(e) => setC({ ...c, governor: e.target.value })}>
                {(data.governors ?? []).map((g) => <option key={g} value={g}>{govLabel(g)} ({g})</option>)}
              </Select>
            </Field>
          )}
        </Card>

        <Card title={<span className="flex items-center gap-2"><Power size={16} />{tr("Arrêt et démarrage programmés")}</span>}>
          <div className="space-y-4">
            <Switch checked={c.schedule.enabled} onChange={(v) => set({ enabled: v, shutdown: c.schedule.shutdown || "23:30", days: c.schedule.days?.length ? c.schedule.days : [0, 1, 2, 3, 4, 5, 6] })} label={tr("Éteindre le NAS automatiquement")} hint={tr("Une sauvegarde ou une mise à jour en cours repousse l'arrêt de 10 minutes.")} />
            {c.schedule.enabled && <>
              <div className="flex flex-wrap gap-1.5">
                {days.map((d) => {
                  const on = c.schedule.days.includes(d);
                  return <button key={d} onClick={() => set({ days: on ? c.schedule.days.filter((x) => x !== d) : [...c.schedule.days, d] })} className={`rounded-lg border px-2.5 py-1 text-[12.5px] capitalize ${on ? "border-accent bg-accent-soft" : "border-line text-muted"}`}>{dayName(d)}</button>;
                })}
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Heure d'arrêt")}><Input type="time" value={c.schedule.shutdown} onChange={(e) => set({ shutdown: e.target.value })} /></Field>
                <Field label={tr("Heure de redémarrage")} hint={tr("Vide : le NAS reste éteint")}><Input type="time" value={c.schedule.wake} disabled={!data.rtcWake} onChange={(e) => set({ wake: e.target.value })} /></Field>
              </div>
              {!data.rtcWake && <Notice tone="warn">{tr("L'horloge de cette machine ne permet pas le réveil automatique : programmez le démarrage dans le BIOS ou réveillez le NAS par le réseau.")}</Notice>}
            </>}
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Zap size={16} />{tr("Réveil par le réseau (Wake-on-LAN)")}</span>}>
          <div className="space-y-4">
            <div className="space-y-2">
              <div className="text-muted text-[12.5px]">{tr("Permettre de rallumer ce NAS à distance :")}</div>
              {(data.nics ?? []).map((n) => (
                <Switch key={n.name} disabled={!n.supported} checked={(c.wol ?? []).includes(n.name)} onChange={(v) => setC({ ...c, wol: v ? [...(c.wol ?? []), n.name] : (c.wol ?? []).filter((x) => x !== n.name) })}
                  label={<span><span className="mono">{n.name}</span> <span className="mono text-muted text-[12px]">{n.mac}</span></span>} hint={n.supported ? undefined : tr("Non pris en charge par cette carte réseau")} />
              ))}
            </div>
            <div className="border-t border-line pt-4">
              <div className="mb-2 text-muted text-[12.5px]">{tr("Appareils que ce NAS peut réveiller (serveur de sauvegarde, PC…) :")}</div>
              {c.devices.map((d, i) => (
                <div key={i} className="mb-1.5 flex items-center gap-2 text-[13px]">
                  <span className="flex-1 truncate">{d.name}</span><span className="mono text-muted text-[12px]">{d.mac}</span>
                  <Button size="sm" loading={wake.isPending && wake.variables === d.mac} onClick={() => wake.mutate(d.mac)}>{tr("Réveiller")}</Button>
                  <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={() => setC({ ...c, devices: c.devices.filter((_, j) => j !== i) })} />
                </div>
              ))}
              <div className="mt-2 flex gap-2">
                <Input placeholder={tr("Nom")} value={dev.name} onChange={(e) => setDev({ ...dev, name: e.target.value })} />
                <Input className="mono" placeholder="aa:bb:cc:dd:ee:ff" value={dev.mac} onChange={(e) => setDev({ ...dev, mac: e.target.value.trim() })} />
                <Button icon={<Plus size={15} />} disabled={!dev.name || !/^([0-9a-f]{2}[:-]){5}[0-9a-f]{2}$/i.test(dev.mac)} onClick={() => { setC({ ...c, devices: [...c.devices, dev] }); setDev({ name: "", mac: "" }); }} />
              </div>
              <p className="mt-2 text-muted text-[12px]">{tr("Pensez à enregistrer après avoir ajouté un appareil.")}</p>
            </div>
          </div>
        </Card>
      </div>
    </div>
  );
}
