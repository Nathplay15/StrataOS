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
type Rule = { id: string; enabled: boolean; action: "shutdown" | "restart" | "wake"; days: number[]; time: string };
type Guard = { users?: boolean; retryMin?: number; maxMin?: number; warnMin?: number };
type Config = { spindownMin: number; governor?: string; schedule: Schedule; wol?: string[]; devices: Device[]; rules: Rule[]; guard?: Guard };
type NIC = { name: string; mac: string; supported: boolean; enabled: boolean };
type Data = {
  config: Config; hdparm: boolean; disks: { path: string; model: string; size: number; state: string }[];
  governor: string; governors: string[]; rtcWake: boolean; nics: NIC[] | null;
  nextShutdown?: string; nextWake?: string; watts?: number; upsLoad?: number;
  nextAction?: { time: string; action: Rule["action"]; rule: string }; nextPowerOn?: string; rtcAlarm?: string;
  wolPersisted?: string[]; timezone?: string;
};

const actionLabel = (a: Rule["action"]) => ({ shutdown: tr("Shut down"), restart: tr("Restart"), wake: tr("Power on") }[a]);

const govLabel = (g: string) => ({
  powersave: tr("Power saving"), schedutil: tr("Balanced (depending on load)"), ondemand: tr("On demand"),
  conservative: tr("Gradual"), performance: tr("Maximum performance"), userspace: tr("Manual"),
}[g] ?? g);

export default function Energy() {
  const { data, isLoading, error } = useQuery({ queryKey: ["energy"], queryFn: () => api.get<Data>("/api/energy"), refetchInterval: 10000 });
  const [c, setC] = useState<Config | null>(null);
  const [dev, setDev] = useState<Device>({ name: "", mac: "" });
  useEffect(() => { if (data && !c) setC({ ...data.config, devices: data.config.devices ?? [], rules: (data.config.rules ?? []).map((r) => ({ ...r, days: r.days ?? [] })), guard: data.config.guard ?? {}, schedule: { ...data.config.schedule, days: data.config.schedule?.days ?? [] } }); }, [data, c]);
  const save = useAction(() => api.put<{ warning?: string } | null>("/api/energy", c), { success: (r) => r?.warning ?? tr("Power settings applied") });
  const standby = useAction(() => api.post<{ warning?: string } | null>("/api/energy/standby"), { success: (r) => r?.warning ?? tr("Disks spun down") });
  const wake = useAction((mac: string) => api.post("/api/energy/wake", { mac }), { success: tr("Wake packet sent") });

  const header = <PageHeader title={tr("Power")} subtitle={tr("Disk standby, CPU, scheduled shutdown and wake over the network")} icon={<Leaf size={20} />} actions={c && <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>} />;
  if (isLoading || !data || !c) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;

  const asleep = data.disks.filter((d) => d.state === "standby").length;
  const days = Array.from({ length: 7 }, (_, i) => (i + 1) % 7); // Monday first
  const dayName = (d: number) => new Intl.DateTimeFormat(locale, { weekday: "short" }).format(new Date(2026, 0, 4 + d));
  const setRule = (i: number, r: Partial<Rule>) => setC({ ...c, rules: c.rules.map((x, j) => (j === i ? { ...x, ...r } : x)) });
  const addRule = (action: Rule["action"]) => setC({ ...c, rules: [...c.rules, { id: `r${Date.now().toString(36)}`, enabled: true, action, days: [1, 2, 3, 4, 5], time: action === "wake" ? "07:00" : "23:30" }] });
  const guard = c.guard ?? {};
  const setGuard = (g: Partial<Guard>) => setC({ ...c, guard: { ...guard, ...g } });
  const next = data.nextAction;

  return (
    <div>
      {header}
      <ErrorBox error={save.error} />
      <div className="@container"><div className="mb-5 grid gap-4 @xl:grid-cols-2 @3xl:grid-cols-4">
        <Card><div className="flex items-center gap-3"><Zap size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Consumption")}</div><div className="text-[18px] font-semibold">{data.watts ? `${Math.round(data.watts)} W` : "—"}</div>{!data.watts && <div className="text-muted text-[11.5px]">{tr("Measured by a compatible UPS")}</div>}</div></div></Card>
        <Card><div className="flex items-center gap-3"><Moon size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Disks in standby")}</div><div className="text-[18px] font-semibold">{data.disks.length ? `${asleep} / ${data.disks.length}` : "—"}</div></div></div></Card>
        <Card><div className="flex items-center gap-3"><Cpu size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{tr("Processor")}</div><div className="text-[15px] font-semibold">{data.governor ? govLabel(data.governor) : "—"}</div></div></div></Card>
        <Card><div className="flex items-center gap-3"><Power size={20} className="text-accent" /><div><div className="text-muted text-[12px]">{next ? actionLabel(next.action) : tr("Next shutdown")}</div><div className="text-[14px] font-semibold">{next ? dateTime(next.time) : "—"}</div>{data.nextPowerOn && <div className="text-muted text-[11.5px]">{tr("Power-on {0}", dateTime(data.nextPowerOn))}</div>}</div></div></Card>
      </div></div>

      <div className="@container"><div className="grid gap-5 @3xl:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><HardDrive size={16} />{tr("Disk standby")}</span>} actions={data.disks.length > 0 && data.hdparm && <Button size="sm" loading={standby.isPending} onClick={() => standby.mutate()}>{tr("Spin down now")}</Button>}>
          <div className="space-y-4">
            <Field label={tr("Spin down inactive hard disks after")} hint={tr("SSDs and USB disks are not affected. A disk wakes up within a few seconds on first access.")}>
              <Select value={c.spindownMin} onChange={(e) => setC({ ...c, spindownMin: +e.target.value })}>
                <option value={0}>{tr("Never")}</option>
                {[10, 20, 30, 60, 120, 240].map((m) => <option key={m} value={m}>{m < 60 ? tr("{0} minutes", m) : tr("{0} hour(s)", m / 60)}</option>)}
              </Select>
            </Field>
            {data.disks.length === 0 ? <div className="text-muted text-[13px]">{tr("No spinning hard disk detected.")}</div> : (
              <div className="space-y-1.5">
                {data.disks.map((d) => (
                  <div key={d.path} className="flex items-center gap-3 text-[13px]">
                    <span className="mono w-20">{d.path}</span><span className="flex-1 truncate text-muted">{d.model} · {bytes(d.size)}</span>
                    <Badge tone={d.state === "standby" ? "ok" : d.state === "active" ? "accent" : "neutral"} dot>{d.state === "standby" ? tr("Standby") : d.state === "active" ? tr("Active") : tr("Unknown")}</Badge>
                  </div>
                ))}
              </div>
            )}
            <p className="text-muted text-[12px]">{tr("Periodic SMART checks do not wake disks in standby.")}</p>
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Cpu size={16} />{tr("Processor")}</span>}>
          {!data.governors?.length ? <div className="text-muted text-[13px]">{tr("This machine does not allow CPU frequency adjustment (common in a virtual machine).")}</div> : (
            <Field label={tr("Frequency policy")} hint={tr("\"Balanced\" suits most NAS: the frequency rises only when needed.")}>
              <Select value={c.governor || data.governor} onChange={(e) => setC({ ...c, governor: e.target.value })}>
                {(data.governors ?? []).map((g) => <option key={g} value={g}>{govLabel(g)} ({g})</option>)}
              </Select>
            </Field>
          )}
        </Card>

        <Card title={<span className="flex items-center gap-2"><Power size={16} />{tr("Scheduled shutdown and startup")}</span>}>
          <div className="space-y-4">
            {c.rules.length === 0 && <div className="text-muted text-[13px]">{tr("No scheduled power action.")}</div>}
            {c.rules.map((r, i) => (
              <div key={r.id} className="space-y-2 rounded-lg border border-line p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Switch checked={r.enabled} onChange={(v) => setRule(i, { enabled: v })} />
                  <Select className="!w-auto" value={r.action} onChange={(e) => setRule(i, { action: e.target.value as Rule["action"] })}>
                    {(["shutdown", "restart", "wake"] as const).map((a) => <option key={a} value={a} disabled={a === "wake" && !data.rtcWake}>{actionLabel(a)}</option>)}
                  </Select>
                  <Input className="!w-auto" type="time" value={r.time} onChange={(e) => setRule(i, { time: e.target.value })} />
                  <Button size="sm" variant="ghost" className="ml-auto" icon={<Trash2 size={14} />} title={tr("Delete")} onClick={() => setC({ ...c, rules: c.rules.filter((_, j) => j !== i) })} />
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {days.map((d) => {
                    const on = r.days.includes(d);
                    return <button key={d} onClick={() => setRule(i, { days: on ? r.days.filter((x) => x !== d) : [...r.days, d] })} className={`rounded-lg border px-2.5 py-1 text-[12.5px] capitalize ${on ? "border-accent bg-accent-soft" : "border-line text-muted"}`}>{dayName(d)}</button>;
                  })}
                </div>
              </div>
            ))}
            <div className="flex flex-wrap gap-2">
              <Button size="sm" icon={<Plus size={14} />} onClick={() => addRule("shutdown")}>{tr("Shutdown")}</Button>
              <Button size="sm" icon={<Plus size={14} />} onClick={() => addRule("restart")}>{tr("Restart")}</Button>
              <Button size="sm" icon={<Plus size={14} />} disabled={!data.rtcWake} onClick={() => addRule("wake")}>{tr("Power-on")}</Button>
            </div>
            {!data.rtcWake && <Notice tone="warn">{tr("This machine's clock does not support automatic wake-up: schedule startup in the BIOS or wake the NAS over the network.")}</Notice>}
            {data.rtcWake && data.rtcAlarm && <p className="text-muted text-[12px]">{tr("The clock of the motherboard will power the NAS on {0}, even after a shutdown by hand.", dateTime(data.rtcAlarm))}</p>}
            {data.timezone && <p className="text-muted text-[12px]">{tr("Times are in the time zone of the NAS ({0}); changes to and from daylight saving time are taken into account.", data.timezone)}</p>}
            {c.rules.some((r) => r.action !== "wake") && <div className="space-y-3 border-t border-line pt-4">
              <p className="text-muted text-[12.5px]">{tr("A running task (backup, update, RAID rebuild…) always postpones a shutdown or a restart.")}</p>
              <Switch checked={!!guard.users} onChange={(v) => setGuard({ users: v })} label={tr("Also wait while someone uses the NAS")} hint={tr("Files open over SMB, a shell session or a video being played.")} />
              <div className="grid gap-4 sm:grid-cols-3">
                <Field label={tr("Check again every")}><Select value={guard.retryMin || 10} onChange={(e) => setGuard({ retryMin: +e.target.value })}>{[5, 10, 15, 30].map((m) => <option key={m} value={m}>{tr("{0} minutes", m)}</option>)}</Select></Field>
                <Field label={tr("Give up after")}><Select value={guard.maxMin || 120} onChange={(e) => setGuard({ maxMin: +e.target.value })}>{[30, 60, 120, 240, 480].map((m) => <option key={m} value={m}>{m < 60 ? tr("{0} minutes", m) : tr("{0} hour(s)", m / 60)}</option>)}</Select></Field>
                <Field label={tr("Warn users")}><Select value={guard.warnMin || 10} onChange={(e) => setGuard({ warnMin: +e.target.value })}><option value={-1}>{tr("Never")}</option>{[5, 10, 15, 30].map((m) => <option key={m} value={m}>{tr("{0} minutes before", m)}</option>)}</Select></Field>
              </div>
            </div>}
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Zap size={16} />{tr("Wake over the network (Wake-on-LAN)")}</span>}>
          <div className="space-y-4">
            <div className="space-y-2">
              <div className="text-muted text-[12.5px]">{tr("Allow this NAS to be powered on remotely:")}</div>
              {(data.nics ?? []).map((n) => (
                <Switch key={n.name} disabled={!n.supported} checked={(c.wol ?? []).includes(n.name)} onChange={(v) => setC({ ...c, wol: v ? [...(c.wol ?? []), n.name] : (c.wol ?? []).filter((x) => x !== n.name) })}
                  label={<span><span className="mono">{n.name}</span> <span className="mono text-muted text-[12px]">{n.mac}</span></span>} hint={!n.supported ? tr("Not supported by this network card") : (data.wolPersisted ?? []).includes(n.name) ? tr("Kept on from the start of the NAS") : undefined} />
              ))}
            </div>
            <div className="border-t border-line pt-4">
              <div className="mb-2 text-muted text-[12.5px]">{tr("Devices this NAS can wake (backup server, PC…):")}</div>
              {c.devices.map((d, i) => (
                <div key={i} className="mb-1.5 flex items-center gap-2 text-[13px]">
                  <span className="flex-1 truncate">{d.name}</span><span className="mono text-muted text-[12px]">{d.mac}</span>
                  <Button size="sm" loading={wake.isPending && wake.variables === d.mac} onClick={() => wake.mutate(d.mac)}>{tr("Wake")}</Button>
                  <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={() => setC({ ...c, devices: c.devices.filter((_, j) => j !== i) })} />
                </div>
              ))}
              <div className="mt-2 flex gap-2">
                <Input placeholder={tr("Name")} value={dev.name} onChange={(e) => setDev({ ...dev, name: e.target.value })} />
                <Input className="mono" placeholder="aa:bb:cc:dd:ee:ff" value={dev.mac} onChange={(e) => setDev({ ...dev, mac: e.target.value.trim() })} />
                <Button icon={<Plus size={15} />} disabled={!dev.name || !/^([0-9a-f]{2}[:-]){5}[0-9a-f]{2}$/i.test(dev.mac)} onClick={() => { setC({ ...c, devices: [...c.devices, dev] }); setDev({ name: "", mac: "" }); }} />
              </div>
              <p className="mt-2 text-muted text-[12px]">{tr("Remember to save after adding a device.")}</p>
            </div>
          </div>
        </Card>
      </div></div>
    </div>
  );
}
