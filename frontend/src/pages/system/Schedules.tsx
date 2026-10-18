import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, AppWindow, Bug, CalendarClock, Camera, CheckCircle2, Edit3, History, Play, Plus, ShieldCheck, Sparkles, Trash2, XCircle } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Schedule, ScheduleTarget } from "../../lib/types";
import { ago, cx, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";

type Targets = { snapshot: ScheduleTarget[]; scrub: ScheduleTarget[]; disks: ScheduleTarget[] };

const typeInfo = {
  snapshot: { label: tr("Snapshot"), icon: <Camera size={15} />, tone: "accent" as const },
  scrub: { label: tr("Verification"), icon: <ShieldCheck size={15} />, tone: "ok" as const },
  smart: { label: tr("SMART test"), icon: <Activity size={15} />, tone: "warn" as const },
  appupdate: { label: tr("App updates"), icon: <AppWindow size={15} />, tone: "accent" as const },
  antivirus: { label: tr("Antivirus scan"), icon: <Bug size={15} />, tone: "danger" as const },
};

const days = ["dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"];

/* ---------- Schedule <-> cron ---------- */

export type Freq = "hourly" | "daily" | "weekly" | "monthly" | "custom";
export interface Plan { freq: Freq; minute: number; hour: number; weekday: number; monthday: number; every: number; cron: string }

export function toCron(p: Plan): string {
  switch (p.freq) {
    case "hourly": return p.every > 1 ? `${p.minute} */${p.every} * * *` : `${p.minute} * * * *`;
    case "daily": return `${p.minute} ${p.hour} * * *`;
    case "weekly": return `${p.minute} ${p.hour} * * ${p.weekday}`;
    case "monthly": return `${p.minute} ${p.hour} ${p.monthday} * *`;
    default: return p.cron;
  }
}

export function fromCron(cron: string): Plan {
  const base: Plan = { freq: "custom", minute: 0, hour: 2, weekday: 0, monthday: 1, every: 1, cron };
  const f = cron.trim().split(/\s+/);
  if (f.length !== 5) return base;
  const [mi, h, dom, mon, dow] = f;
  const n = (s: string) => /^\d+$/.test(s);
  if (!n(mi) || mon !== "*") return base;
  const m = +mi;
  if (h === "*" && dom === "*" && dow === "*") return { ...base, freq: "hourly", minute: m };
  const ev = h.match(/^\*\/(\d+)$/);
  if (ev && dom === "*" && dow === "*") return { ...base, freq: "hourly", minute: m, every: +ev[1] };
  if (!n(h)) return base;
  if (dom === "*" && dow === "*") return { ...base, freq: "daily", minute: m, hour: +h };
  if (dom === "*" && n(dow)) return { ...base, freq: "weekly", minute: m, hour: +h, weekday: +dow % 7 };
  if (n(dom) && dow === "*") return { ...base, freq: "monthly", minute: m, hour: +h, monthday: +dom };
  return base;
}

const hm = (h: number, m: number) => `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;

export function describeCron(cron: string): string {
  const p = fromCron(cron);
  switch (p.freq) {
    case "hourly": return p.every > 1 ? tr("Every {0} h at :{1}", p.every, String(p.minute).padStart(2, "0")) : tr("Every hour at :{0}", String(p.minute).padStart(2, "0"));
    case "daily": return tr("Every day at {0}", hm(p.hour, p.minute));
    case "weekly": return tr("Every {0} at {1}", days[p.weekday], hm(p.hour, p.minute));
    case "monthly": return tr("On day {0} of every month at {1}", p.monthday, hm(p.hour, p.minute));
    default: return tr("cron \"{0}\"", cron);
  }
}

export function ScheduleEditor({ plan, onChange }: { plan: Plan; onChange: (p: Plan) => void }) {
  const cron = toCron(plan);
  const preview = useQuery({ queryKey: ["cron-preview", cron], queryFn: () => api.get<string[]>(`/api/schedules/preview${q({ cron })}`), retry: false });
  const set = (k: Partial<Plan>) => onChange({ ...plan, ...k });
  const time = (
    <Field label={tr("Time")}>
      <Input type="time" value={hm(plan.hour, plan.minute)} onChange={(e) => { const [h, m] = e.target.value.split(":").map(Number); if (!isNaN(h)) set({ hour: h, minute: m }); }} />
    </Field>
  );
  return (
    <div className="space-y-3 rounded-xl border border-line p-4">
      <div className="flex flex-wrap gap-1">
        {(["hourly", "daily", "weekly", "monthly", "custom"] as Freq[]).map((f) => (
          <button key={f} type="button" onClick={() => set(f === "custom" ? { freq: f, cron } : { freq: f })} className={cx("rounded-lg px-3 py-1.5 text-[12.5px]", plan.freq === f ? "bg-accent text-white" : "bg-panel-2 text-muted hover:text-fg")}>
            {{ hourly: "Horaire", daily: "Quotidien", weekly: "Hebdomadaire", monthly: "Mensuel", custom: "Cron" }[f]}
          </button>
        ))}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {plan.freq === "hourly" && (
          <>
            <Field label={tr("Every (hours)")}><Input type="number" min={1} max={23} value={plan.every} onChange={(e) => set({ every: Math.max(1, +e.target.value) })} /></Field>
            <Field label={tr("Every minute")}><Input type="number" min={0} max={59} value={plan.minute} onChange={(e) => set({ minute: Math.min(59, Math.max(0, +e.target.value)) })} /></Field>
          </>
        )}
        {plan.freq === "daily" && time}
        {plan.freq === "weekly" && (
          <>
            <Field label={tr("Day")}><Select value={plan.weekday} onChange={(e) => set({ weekday: +e.target.value })}>{days.map((d, i) => <option key={d} value={i}>{d}</option>)}</Select></Field>
            {time}
          </>
        )}
        {plan.freq === "monthly" && (
          <>
            <Field label={tr("Day of the month")} hint={tr("1 to 28 to fall on every month")}><Input type="number" min={1} max={31} value={plan.monthday} onChange={(e) => set({ monthday: Math.min(31, Math.max(1, +e.target.value)) })} /></Field>
            {time}
          </>
        )}
        {plan.freq === "custom" && (
          <Field label={tr("Cron expression")} className="sm:col-span-2" hint={tr("minute hour day month weekday — e.g. 0 */6 * * * (every 6 h), 30 1 * * 1-5")}><Input className="mono" value={plan.cron} onChange={(e) => set({ cron: e.target.value })} /></Field>
        )}
      </div>
      <div className="text-[12px] text-muted">
        {preview.error ? <span className="text-danger">{(preview.error as Error).message}</span> : preview.data?.length ? <>{tr("Next runs:")}{" "}{preview.data.slice(0, 3).map((d) => dateTime(d)).join(" · ")}</> : null}
      </div>
    </div>
  );
}

/* ---------- Task editor ---------- */

const blank: Schedule = { id: "", name: "", type: "snapshot", cron: "0 0 * * *", enabled: true, targetKind: "zfs", target: "", keepCount: 14, keepDays: 0, smartTest: "short", history: [] };

function TaskModal({ initial, targets, onClose }: { initial: Schedule | null; targets: Targets; onClose: () => void }) {
  const editing = !!initial;
  const [s, setS] = useState<Schedule>(initial ?? blank);
  const [plan, setPlan] = useState<Plan>(fromCron((initial ?? blank).cron));
  const [named, setNamed] = useState(editing);
  const set = (k: Partial<Schedule>) => setS((p) => ({ ...p, ...k }));
  const stacks = useQuery({ queryKey: ["stacks"], queryFn: () => api.get<{ name: string }[]>("/api/docker/stacks"), enabled: s.type === "appupdate" });
  const stackOptions: ScheduleTarget[] = [{ kind: "stacks", value: "all", label: tr("All running applications") }, ...(stacks.data ?? []).map((x) => ({ kind: "stacks" as const, value: x.name, label: x.name }))];
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<{ name: string; path: string }[]>("/api/shares"), enabled: s.type === "antivirus" });
  const pathOptions: ScheduleTarget[] = [{ kind: "path", value: "all", label: tr("All storage") }, ...(shares.data ?? []).map((x) => ({ kind: "path" as const, value: x.path, label: tr("Share {0}", x.name) }))];
  const options = s.type === "snapshot" ? targets.snapshot : s.type === "scrub" ? targets.scrub : s.type === "appupdate" ? stackOptions : s.type === "antivirus" ? pathOptions : [];
  const current = options.find((o) => o.kind === s.targetKind && o.value === s.target);
  const selectedDisks = s.target === "all" ? [] : s.target.split(/[\s,]+/).filter(Boolean);

  // Pick a valid default target when the type changes.
  useEffect(() => {
    if (s.type === "smart") {
      if (s.targetKind !== "disks") set({ targetKind: "disks", target: "all" });
    } else if (!options.some((o) => o.kind === s.targetKind && o.value === s.target) && options[0]) {
      set({ targetKind: options[0].kind, target: options[0].value, subvolume: "" });
    }
  }, [s.type]);

  // Suggest a name until the user types one.
  useEffect(() => {
    if (named) return;
    const what = s.type === "smart" ? (s.smartTest === "long" ? "long" : "court") : current?.label ?? "";
    set({ name: `${typeInfo[s.type].label} ${what}`.trim() });
  }, [s.type, s.target, s.smartTest, named]);

  const payload = { ...s, cron: toCron(plan) };
  const save = useAction(() => (editing ? api.put(`/api/schedules/${s.id}`, payload) : api.post("/api/schedules", payload)), { success: editing ? tr("Task updated") : tr("Task created"), onDone: onClose });

  return (
    <Modal open onClose={onClose} title={editing ? tr("Edit \"{0}\"", s.name) : tr("New scheduled task")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!s.name || (s.type !== "smart" && !s.target)} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
          {(Object.keys(typeInfo) as Schedule["type"][]).map((t) => (
            <button key={t} type="button" disabled={editing} onClick={() => set({ type: t })} className={cx("flex items-center gap-2 rounded-xl border px-3 py-2.5 text-left transition disabled:opacity-60", s.type === t ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
              {typeInfo[t].icon}
              <div>
                <div className="font-medium text-[13px]">{typeInfo[t].label}</div>
                <div className="text-muted text-[11.5px]">{{ snapshot: tr("ZFS or Btrfs, with retention"), scrub: tr("RAID, ZFS pool, Btrfs"), smart: tr("Disk self-test"), appupdate: tr("New Docker images, data kept"), antivirus: tr("ClamAV, with quarantine") }[t]}</div>
              </div>
            </button>
          ))}
        </div>

        <Field label={tr("Name")}><Input value={s.name} onChange={(e) => { setNamed(true); set({ name: e.target.value }); }} /></Field>

        {s.type !== "smart" ? (
          options.length === 0 ? (
            <Notice tone="warn">{s.type === "snapshot" ? tr("No ZFS dataset or Btrfs volume: snapshots require one of the two.") : s.type === "appupdate" ? tr("No application installed.") : tr("No RAID array, ZFS pool or Btrfs volume to check.")}</Notice>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Target")} className={s.targetKind === "btrfs" && s.type === "snapshot" ? "" : "sm:col-span-2"}>
                <Select value={`${s.targetKind}|${s.target}`} onChange={(e) => { const [k, ...v] = e.target.value.split("|"); set({ targetKind: k as Schedule["targetKind"], target: v.join("|"), subvolume: "" }); }}>
                  {options.map((o) => <option key={`${o.kind}|${o.value}`} value={`${o.kind}|${o.value}`}>{o.label}</option>)}
                </Select>
              </Field>
              {s.type === "snapshot" && s.targetKind === "btrfs" && (
                <Field label="Sous-volume">
                  <Select value={s.subvolume ?? ""} onChange={(e) => set({ subvolume: e.target.value })}>
                    <option value="">{tr("Entire volume")}</option>
                    {current?.subvolumes?.map((sv) => <option key={sv} value={sv}>{sv}</option>)}
                  </Select>
                </Field>
              )}
            </div>
          )
        ) : (
          <div className="space-y-3">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Test type")}>
                <Select value={s.smartTest} onChange={(e) => set({ smartTest: e.target.value as "short" | "long" })}>
                  <option value="short">{tr("Short (~2 min)")}</option>
                  <option value="long">{tr("Long (full read, several hours)")}</option>
                </Select>
              </Field>
              <div className="pt-7"><Switch checked={s.target === "all"} onChange={(v) => set({ target: v ? "all" : targets.disks[0]?.value ?? "" })} label={tr("All disks")} /></div>
            </div>
            {s.target !== "all" && (
              <div className="grid gap-2 sm:grid-cols-2">
                {targets.disks.map((d) => (
                  <Checkbox key={d.value} checked={selectedDisks.includes(d.value)} label={d.label} onChange={(v) => set({ target: (v ? [...selectedDisks, d.value] : selectedDisks.filter((x) => x !== d.value)).join(" ") })} />
                ))}
              </div>
            )}
          </div>
        )}

        {s.type === "antivirus" && (
          <Field label={tr("Infected files")}>
            <Select value={s.avAction ?? "quarantine"} onChange={(e) => set({ avAction: e.target.value as "report" | "quarantine" })}>
              <option value="quarantine">{tr("Quarantine (can be restored from Antivirus)")}</option>
              <option value="report">{tr("Report only")}</option>
            </Select>
          </Field>
        )}
        {s.type === "snapshot" && (
          <div className="space-y-3 rounded-xl border border-line p-4">
            <div className="font-medium text-[13px]">{tr("Retention")}</div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Keep the last N")} hint={tr("0 = no limit on count")}><Input type="number" min={0} value={s.keepCount ?? 0} onChange={(e) => set({ keepCount: Math.max(0, +e.target.value) })} /></Field>
              <Field label={tr("Keep for (days)")} hint={tr("0 = no limit on age")}><Input type="number" min={0} value={s.keepDays ?? 0} onChange={(e) => set({ keepDays: Math.max(0, +e.target.value) })} /></Field>
            </div>
            <Field label={tr("Lock each snapshot for (days)")} hint={tr("Ransomware protection: no one can delete a locked snapshot before its date, not even an administrator. 0 = no lock.")}><Input type="number" min={0} max={3650} value={s.lockDays ?? 0} onChange={(e) => set({ lockDays: Math.max(0, +e.target.value) })} /></Field>
            {s.targetKind === "zfs" && <Switch checked={!!s.recursive} onChange={(v) => set({ recursive: v })} label={tr("Include child datasets")} hint={tr("Atomic recursive snapshot (zfs snapshot -r)")} />}
            <div className="text-[12px] text-muted">{tr("Only snapshots created by this task are deleted; the most recent one is always kept. Manual snapshots are never touched.")}</div>
          </div>
        )}

        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Schedule")}</div>
          <ScheduleEditor plan={plan} onChange={setPlan} />
        </div>
        <Switch checked={s.enabled} onChange={(v) => set({ enabled: v })} label={tr("Task active")} />
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function HistoryModal({ s, onClose }: { s: Schedule; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={tr("History — {0}", s.name)} wide>
      {s.history.length === 0 ? <Empty icon={<History />} title={tr("No runs")} /> : (
        <div className="space-y-2">
          {s.history.map((h, i) => (
            <div key={i} className="flex items-start gap-3 rounded-xl bg-panel-2 px-4 py-3">
              {h.status === "success" ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={17} className="mt-0.5 shrink-0 text-danger" />}
              <div className="min-w-0 flex-1">
                <div className="text-[13px]">{srv(h.message)}</div>
                <div className="text-muted text-[11.5px]">{dateTime(h.started)} · {h.duration < 1 ? "<1" : h.duration.toFixed(0)} s{h.manual ? tr(" · started manually") : ""}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

function targetLabel(s: Schedule, targets?: Targets) {
  if (s.type === "smart") return s.target === "all" ? tr("All disks") : s.target;
  if (s.type === "appupdate") return s.target === "all" ? tr("All running applications") : s.target;
  if (s.type === "antivirus") return s.target === "all" ? tr("All storage") : s.target;
  const list = s.type === "snapshot" ? targets?.snapshot : targets?.scrub;
  const t = list?.find((o) => o.kind === s.targetKind && o.value === s.target);
  return (t?.label ?? s.target) + (s.subvolume ? ` / ${s.subvolume}` : "");
}

export default function Schedules() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<Schedule | null | undefined>(undefined);
  const [hist, setHist] = useState<Schedule | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["schedules"], queryFn: () => api.get<Schedule[]>("/api/schedules"), refetchInterval: 10000 });
  const targets = useQuery({ queryKey: ["schedule-targets"], queryFn: () => api.get<Targets>("/api/schedules/targets") });
  const toggle = useAction((s: Schedule) => api.put(`/api/schedules/${s.id}`, { ...s, enabled: !s.enabled }));
  const run = useAction((id: string) => api.post(`/api/schedules/${id}/run`));
  const del = useAction((id: string) => api.del(`/api/schedules/${id}`), { success: tr("Task deleted") });
  const defaults = useAction(() => api.post<{ created: number }>("/api/schedules/defaults"), { success: (r) => (r.created ? tr("{0} recommended task(s) added", r.created) : tr("The recommended plan is already in place")) });

  return (
    <div>
      <PageHeader title={tr("Scheduled tasks")} subtitle={tr("Automatic snapshots, integrity checks and SMART tests")} icon={<CalendarClock size={20} />} actions={<>
        <Button icon={<Sparkles size={15} />} loading={defaults.isPending} onClick={() => defaults.mutate()}>{tr("Recommended plan")}</Button>
        <Button variant="primary" icon={<Plus size={15} />} disabled={!targets.data} onClick={() => setEdit(null)}>{tr("New task")}</Button>
      </>} />
      <ErrorBox error={error || targets.error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card>
          <Empty icon={<CalendarClock />} title={tr("No scheduled tasks")} text={tr("The recommended plan creates a monthly scrub per array and per pool, daily snapshots kept for 14 days, a short SMART test every week and a long test every month.")}
            action={<div className="flex gap-2"><Button variant="primary" icon={<Sparkles size={15} />} onClick={() => defaults.mutate()}>{tr("Create the recommended plan")}</Button><Button onClick={() => setEdit(null)}>{tr("Custom task")}</Button></div>} />
        </Card>
      ) : (
        <Card pad={false}>
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Task")}</th><th>{tr("Target")}</th><th>{tr("Schedule")}</th><th>{tr("Last run")}</th><th>{tr("Next")}</th><th>{tr("Active")}</th><th /></tr></thead>
              <tbody>
                {data.map((s) => {
                  const last = s.history[0];
                  const ti = typeInfo[s.type];
                  return (
                    <tr key={s.id} className={s.enabled ? "" : "opacity-60"}>
                      <td>
                        <div className="flex items-center gap-2.5">
                          <span className={cx("flex h-8 w-8 shrink-0 items-center justify-center rounded-lg", { accent: "bg-accent-soft text-accent", ok: "bg-ok/10 text-ok", warn: "bg-warn/10 text-warn", danger: "bg-danger/10 text-danger" }[ti.tone])}>{ti.icon}</span>
                          <div>
                            <div className="whitespace-nowrap font-medium">{s.name}</div>
                            <div className="whitespace-nowrap text-muted text-[11.5px]">{ti.label}{s.type === "snapshot" && ` · garder ${[s.keepCount ? tr("Last {0}", s.keepCount) : "", s.keepDays ? tr("{0} d", s.keepDays) : ""].filter(Boolean).join(" / ")}`}{s.type === "smart" && ` · ${s.smartTest === "long" ? "long" : "court"}`}</div>
                          </div>
                        </div>
                      </td>
                      <td className="whitespace-nowrap text-[12.5px]">{targetLabel(s, targets.data)}</td>
                      <td className="whitespace-nowrap text-[12.5px]">{describeCron(s.cron)}</td>
                      <td className="whitespace-nowrap text-[12.5px]">
                        {s.running ? <Badge tone="accent" dot>{tr("In progress")}</Badge> : last ? (
                          <button onClick={() => setHist(s)} className="flex items-center gap-1.5 text-left hover:underline" title={srv(last.message)}>
                            {last.status === "success" ? <CheckCircle2 size={14} className="text-ok" /> : <XCircle size={14} className="text-danger" />}
                            {ago(last.started)}
                          </button>
                        ) : <span className="text-muted">{tr("Never")}</span>}
                      </td>
                      <td className="whitespace-nowrap text-[12.5px] text-muted">{s.enabled && s.nextRun ? dateTime(s.nextRun) : "—"}</td>
                      <td><Switch checked={s.enabled} onChange={() => toggle.mutate(s)} /></td>
                      <td className="text-right whitespace-nowrap">
                        <IconButton title={tr("Run now")} disabled={s.running} onClick={() => run.mutate(s.id)}><Play size={15} /></IconButton>
                        <IconButton title={tr("History")} onClick={() => setHist(s)}><History size={15} /></IconButton>
                        <IconButton title={tr("Edit")} disabled={!targets.data} onClick={() => setEdit(s)}><Edit3 size={15} /></IconButton>
                        <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete \"{0}\"?", s.name), text: tr("The task will no longer run. Snapshots already created are kept."), danger: true })) del.mutate(s.id); }}><Trash2 size={15} /></IconButton>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <p className="mt-4 text-[12px] text-muted">{tr("Tasks run at the NAS local time. A run missed while the NAS is off is not made up. Failures trigger a notification (Settings → Notifications).")}</p>
      {edit !== undefined && targets.data && <TaskModal initial={edit} targets={targets.data} onClose={() => setEdit(undefined)} />}
      {hist && <HistoryModal s={data?.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
    </div>
  );
}
