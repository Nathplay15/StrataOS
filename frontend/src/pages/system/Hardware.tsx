import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cpu, Fan, Lightbulb, Plus, Thermometer, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Badge, Button, Card, ErrorBox, Input, Notice, PageHeader, Select, Spinner } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type Point = { temp: number; pwm: number };
type PWM = { id: string; chip: string; label: string; value: number; enable: number; rpm: number; controllable: boolean };
type LED = { name: string; on: boolean; trigger: string; disk: boolean; mode: string };
type Data = {
  dmi: { vendor: string; product: string; board: string; cpu: string };
  pwms: PWM[]; sensors: { chip: string; label: string; rpm: number }[];
  cpuTemp: number | null; diskTemps: { device: string; temp: number }[];
  profile: string; cpuCurve: Point[] | null; diskCurve: Point[] | null; presets: Record<string, [Point[], Point[]]>;
  active: boolean; percent: number; fallback: boolean; error: string; notice: "" | "none" | "terramaster"; leds: LED[];
};

const profiles = () => [
  { id: "bios", label: tr("BIOS (automatic)"), hint: tr("The firmware drives the fans; StrataOS changes nothing.") },
  { id: "quiet", label: tr("Quiet"), hint: tr("Low speed, lets the disks run warmer (up to about 45 °C).") },
  { id: "balanced", label: tr("Balanced"), hint: tr("Keeps disks around 40 °C.") },
  { id: "cool", label: tr("Cool"), hint: tr("Keeps everything as cool as possible, louder.") },
  { id: "custom", label: tr("Custom curve"), hint: tr("Your own temperature → speed points.") },
];

function CurveEditor({ title, value, onChange }: { title: string; value: Point[]; onChange: (v: Point[]) => void }) {
  const set = (i: number, p: Partial<Point>) => onChange(value.map((x, j) => (j === i ? { ...x, ...p } : x)));
  return (
    <div>
      <div className="mb-2 text-[13px] font-semibold">{title}</div>
      <div className="space-y-1.5">
        {value.map((p, i) => (
          <div key={i} className="flex items-center gap-2 text-[12.5px]">
            <Input type="number" className="w-20" value={p.temp} min={0} max={100} onChange={(e) => set(i, { temp: +e.target.value })} /><span className="text-muted">°C →</span>
            <Input type="number" className="w-20" value={p.pwm} min={15} max={100} onChange={(e) => set(i, { pwm: +e.target.value })} /><span className="text-muted">%</span>
            <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} disabled={value.length <= 2} onClick={() => onChange(value.filter((_, j) => j !== i))} />
          </div>
        ))}
        {value.length < 8 && <Button size="sm" icon={<Plus size={14} />} onClick={() => { const l = value[value.length - 1]; onChange([...value, { temp: Math.min(100, (l?.temp ?? 30) + 5), pwm: 100 }]); }}>{tr("Add a point")}</Button>}
      </div>
    </div>
  );
}

export default function Hardware() {
  const { data, isLoading, error } = useQuery({ queryKey: ["hardware"], queryFn: () => api.get<Data>("/api/hardware"), refetchInterval: 5000 });
  const [profile, setProfile] = useState<string | null>(null);
  const [cpu, setCpu] = useState<Point[]>([]);
  const [disk, setDisk] = useState<Point[]>([]);
  useEffect(() => {
    if (data && profile === null) {
      setProfile(data.profile);
      setCpu(data.cpuCurve ?? data.presets.balanced[0]);
      setDisk(data.diskCurve ?? data.presets.balanced[1]);
    }
  }, [data, profile]);
  const save = useAction(() => api.put("/api/hardware/fans", { profile, cpuCurve: cpu, diskCurve: disk }), { success: tr("Fan settings applied") });
  const led = useAction((v: { name: string; mode: string }) => api.put(`/api/hardware/leds/${encodeURIComponent(v.name)}`, { mode: v.mode }), { success: tr("LED updated") });

  const header = <PageHeader title={tr("Hardware")} subtitle={tr("Fans, temperatures and front-panel lights")} icon={<Fan size={20} />} />;
  if (isLoading || !data || profile === null) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  const canDrive = data.pwms.some((p) => p.controllable);
  const hint = profiles().find((p) => p.id === profile)?.hint;

  return (
    <div>
      {header}
      <ErrorBox error={save.error} />
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Thermometer size={16} />{tr("Temperatures")}</span>}>
          <div className="space-y-2 text-[13px]">
            <div className="flex justify-between"><span className="flex items-center gap-2"><Cpu size={14} />{tr("Processor")}</span><b>{data.cpuTemp != null ? `${data.cpuTemp.toFixed(0)} °C` : "—"}</b></div>
            {data.diskTemps.map((d) => <div key={d.device} className="flex justify-between"><span className="mono">{d.device}</span><b>{d.temp} °C</b></div>)}
            {data.diskTemps.length === 0 && <div className="text-muted text-[12px]">{tr("No disk temperature yet (read every 5 minutes from awake disks).")}</div>}
            <div className="border-t border-line pt-2 text-muted text-[12px]">{[data.dmi.vendor, data.dmi.product].filter(Boolean).join(" ")}{data.dmi.cpu && ` · ${data.dmi.cpu}`}</div>
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Fan size={16} />{tr("Fans")}</span>}>
          <div className="space-y-1.5 text-[13px]">
            {data.pwms.map((p) => (
              <div key={p.id} className="flex items-center gap-2">
                <span className="flex-1">{p.label} <span className="mono text-muted text-[11.5px]">{p.chip} · {p.id}</span></span>
                <span>{p.rpm >= 0 ? tr("{0} rpm", p.rpm) : "—"}</span>
                <span className="w-12 text-right text-muted">{Math.round((p.value * 100) / 255)} %</span>
                {!p.controllable && <Badge>{tr("Read only")}</Badge>}
              </div>
            ))}
            {data.sensors.map((f, i) => (
              <div key={i} className="flex items-center gap-2"><span className="flex-1">{f.label} <span className="mono text-muted text-[11.5px]">{f.chip}</span></span><span>{tr("{0} rpm", f.rpm)}</span></div>
            ))}
            {data.pwms.length === 0 && data.sensors.length === 0 && <div className="text-muted">{tr("No fan sensor detected.")}</div>}
          </div>
        </Card>

        <Card className="lg:col-span-2" title={<span className="flex items-center gap-2"><Fan size={16} />{tr("Fan control")}</span>}
          actions={canDrive && <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
          {data.notice === "terramaster" ? (
            <Notice tone="warn">{tr("This TerraMaster NAS uses an ITE IT8613E controller that the standard Linux it87 driver does not support. Fan control needs the out-of-tree it87 driver (github.com/frankcrawford/it87), which StrataOS does not install. Until then the BIOS keeps driving the fans, which is safe.")}</Notice>
          ) : data.notice === "none" ? (
            <Notice>{tr("No controllable fans on this hardware: the BIOS drives them. (Virtual machines and many NAS boards expose no fan output to Linux.)")}</Notice>
          ) : (
            <div className="space-y-4">
              <div className="flex flex-wrap items-center gap-3">
                <Select value={profile} onChange={(e) => setProfile(e.target.value)} className="max-w-xs">
                  {profiles().map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
                </Select>
                <span className="text-muted text-[12.5px]">{hint}</span>
              </div>
              {data.active && <div className="flex flex-wrap gap-2 text-[12.5px]">
                <Badge tone="accent" dot>{tr("Driven by StrataOS: {0} %", Math.round(data.percent))}</Badge>
                {data.fallback && <Badge tone="warn" dot>{tr("No temperature readable: full speed")}</Badge>}
              </div>}
              {data.error && <Notice tone="warn">{srv(data.error)}</Notice>}
              {profile === "custom" && (
                <div className="grid gap-5 sm:grid-cols-2">
                  <CurveEditor title={tr("Processor")} value={cpu} onChange={setCpu} />
                  <CurveEditor title={tr("Disks")} value={disk} onChange={setDisk} />
                </div>
              )}
              {profile !== "bios" && profile !== "custom" && data.presets[profile] && (
                <div className="grid gap-3 text-[12px] text-muted sm:grid-cols-2">
                  <div>{tr("Processor")}: {data.presets[profile][0].map((p) => `${p.temp} °C → ${p.pwm} %`).join(", ")}</div>
                  <div>{tr("Disks")}: {data.presets[profile][1].map((p) => `${p.temp} °C → ${p.pwm} %`).join(", ")}</div>
                </div>
              )}
              <p className="text-muted text-[12px]">{tr("Fans follow the hotter of the processor and the disks, with 3 °C of hysteresis. If no temperature can be read they run at full speed. Choosing BIOS again, or stopping StrataOS, gives the fans back to the firmware.")}</p>
            </div>
          )}
        </Card>

        <Card className="lg:col-span-2" title={<span className="flex items-center gap-2"><Lightbulb size={16} />{tr("Front-panel lights")}</span>}>
          {data.leds.length === 0 ? <div className="text-muted text-[13px]">{tr("Not supported on this hardware: the kernel exposes no LED.")}</div> : (
            <div className="space-y-2">
              {data.leds.map((l) => (
                <div key={l.name} className="flex flex-wrap items-center gap-3 text-[13px]">
                  <span className="mono flex-1">{l.name}</span>
                  <Badge tone={l.on ? "ok" : "neutral"} dot>{l.on ? tr("On") : tr("Off")}</Badge>
                  <Select value={l.mode} className="w-48" onChange={(e) => led.mutate({ name: l.name, mode: e.target.value })}>
                    <option value="">{tr("Unchanged ({0})", l.trigger || "none")}</option>
                    <option value="on">{tr("On")}</option>
                    <option value="off">{tr("Off")}</option>
                    {l.disk && <option value="disk">{tr("Disk activity")}</option>}
                  </Select>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}
