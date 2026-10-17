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
  scrub: { label: tr("Vérification"), icon: <ShieldCheck size={15} />, tone: "ok" as const },
  smart: { label: tr("Test SMART"), icon: <Activity size={15} />, tone: "warn" as const },
  appupdate: { label: tr("Mise à jour des applications"), icon: <AppWindow size={15} />, tone: "accent" as const },
  antivirus: { label: tr("Analyse antivirus"), icon: <Bug size={15} />, tone: "danger" as const },
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
    case "hourly": return p.every > 1 ? tr("Toutes les {0} h à :{1}", p.every, String(p.minute).padStart(2, "0")) : tr("Toutes les heures à :{0}", String(p.minute).padStart(2, "0"));
    case "daily": return tr("Tous les jours à {0}", hm(p.hour, p.minute));
    case "weekly": return tr("Chaque {0} à {1}", days[p.weekday], hm(p.hour, p.minute));
    case "monthly": return tr("Le {0} de chaque mois à {1}", p.monthday, hm(p.hour, p.minute));
    default: return tr("cron « {0} »", cron);
  }
}

export function ScheduleEditor({ plan, onChange }: { plan: Plan; onChange: (p: Plan) => void }) {
  const cron = toCron(plan);
  const preview = useQuery({ queryKey: ["cron-preview", cron], queryFn: () => api.get<string[]>(`/api/schedules/preview${q({ cron })}`), retry: false });
  const set = (k: Partial<Plan>) => onChange({ ...plan, ...k });
  const time = (
    <Field label={tr("Heure")}>
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
            <Field label={tr("Toutes les (heures)")}><Input type="number" min={1} max={23} value={plan.every} onChange={(e) => set({ every: Math.max(1, +e.target.value) })} /></Field>
            <Field label={tr("À la minute")}><Input type="number" min={0} max={59} value={plan.minute} onChange={(e) => set({ minute: Math.min(59, Math.max(0, +e.target.value)) })} /></Field>
          </>
        )}
        {plan.freq === "daily" && time}
        {plan.freq === "weekly" && (
          <>
            <Field label={tr("Jour")}><Select value={plan.weekday} onChange={(e) => set({ weekday: +e.target.value })}>{days.map((d, i) => <option key={d} value={i}>{d}</option>)}</Select></Field>
            {time}
          </>
        )}
        {plan.freq === "monthly" && (
          <>
            <Field label={tr("Jour du mois")} hint={tr("1 à 28 pour tomber chaque mois")}><Input type="number" min={1} max={31} value={plan.monthday} onChange={(e) => set({ monthday: Math.min(31, Math.max(1, +e.target.value)) })} /></Field>
            {time}
          </>
        )}
        {plan.freq === "custom" && (
          <Field label={tr("Expression cron")} className="sm:col-span-2" hint={tr("minute heure jour mois jour-semaine — ex. 0 */6 * * * (toutes les 6 h), 30 1 * * 1-5")}><Input className="mono" value={plan.cron} onChange={(e) => set({ cron: e.target.value })} /></Field>
        )}
      </div>
      <div className="text-[12px] text-muted">
        {preview.error ? <span className="text-danger">{(preview.error as Error).message}</span> : preview.data?.length ? <>{tr("Prochaines exécutions :")}{" "}{preview.data.slice(0, 3).map((d) => dateTime(d)).join(" · ")}</> : null}
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
  const stackOptions: ScheduleTarget[] = [{ kind: "stacks", value: "all", label: tr("Toutes les applications en marche") }, ...(stacks.data ?? []).map((x) => ({ kind: "stacks" as const, value: x.name, label: x.name }))];
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<{ name: string; path: string }[]>("/api/shares"), enabled: s.type === "antivirus" });
  const pathOptions: ScheduleTarget[] = [{ kind: "path", value: "all", label: tr("Tout le stockage") }, ...(shares.data ?? []).map((x) => ({ kind: "path" as const, value: x.path, label: tr("Partage {0}", x.name) }))];
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
  const save = useAction(() => (editing ? api.put(`/api/schedules/${s.id}`, payload) : api.post("/api/schedules", payload)), { success: editing ? tr("Tâche mise à jour") : tr("Tâche créée"), onDone: onClose });

  return (
    <Modal open onClose={onClose} title={editing ? tr("Modifier « {0} »", s.name) : tr("Nouvelle tâche planifiée")} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!s.name || (s.type !== "smart" && !s.target)} onClick={() => save.mutate()}>{editing ? tr("Enregistrer") : tr("Créer")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
          {(Object.keys(typeInfo) as Schedule["type"][]).map((t) => (
            <button key={t} type="button" disabled={editing} onClick={() => set({ type: t })} className={cx("flex items-center gap-2 rounded-xl border px-3 py-2.5 text-left transition disabled:opacity-60", s.type === t ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
              {typeInfo[t].icon}
              <div>
                <div className="font-medium text-[13px]">{typeInfo[t].label}</div>
                <div className="text-muted text-[11.5px]">{{ snapshot: tr("ZFS ou Btrfs, avec rétention"), scrub: tr("RAID, pool ZFS, Btrfs"), smart: tr("Autodiagnostic des disques"), appupdate: tr("Nouvelles images Docker, sans perte de données"), antivirus: tr("ClamAV, avec mise en quarantaine") }[t]}</div>
              </div>
            </button>
          ))}
        </div>

        <Field label={tr("Nom")}><Input value={s.name} onChange={(e) => { setNamed(true); set({ name: e.target.value }); }} /></Field>

        {s.type !== "smart" ? (
          options.length === 0 ? (
            <Notice tone="warn">{s.type === "snapshot" ? tr("Aucun dataset ZFS ni volume Btrfs : les snapshots nécessitent l'un des deux.") : s.type === "appupdate" ? tr("Aucune application installée.") : tr("Aucune grappe RAID, pool ZFS ou volume Btrfs à vérifier.")}</Notice>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Cible")} className={s.targetKind === "btrfs" && s.type === "snapshot" ? "" : "sm:col-span-2"}>
                <Select value={`${s.targetKind}|${s.target}`} onChange={(e) => { const [k, ...v] = e.target.value.split("|"); set({ targetKind: k as Schedule["targetKind"], target: v.join("|"), subvolume: "" }); }}>
                  {options.map((o) => <option key={`${o.kind}|${o.value}`} value={`${o.kind}|${o.value}`}>{o.label}</option>)}
                </Select>
              </Field>
              {s.type === "snapshot" && s.targetKind === "btrfs" && (
                <Field label="Sous-volume">
                  <Select value={s.subvolume ?? ""} onChange={(e) => set({ subvolume: e.target.value })}>
                    <option value="">{tr("Volume entier")}</option>
                    {current?.subvolumes?.map((sv) => <option key={sv} value={sv}>{sv}</option>)}
                  </Select>
                </Field>
              )}
            </div>
          )
        ) : (
          <div className="space-y-3">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Type de test")}>
                <Select value={s.smartTest} onChange={(e) => set({ smartTest: e.target.value as "short" | "long" })}>
                  <option value="short">{tr("Court (~2 min)")}</option>
                  <option value="long">{tr("Long (lecture complète, plusieurs heures)")}</option>
                </Select>
              </Field>
              <div className="pt-7"><Switch checked={s.target === "all"} onChange={(v) => set({ target: v ? "all" : targets.disks[0]?.value ?? "" })} label={tr("Tous les disques")} /></div>
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
          <Field label={tr("Fichiers infectés")}>
            <Select value={s.avAction ?? "quarantine"} onChange={(e) => set({ avAction: e.target.value as "report" | "quarantine" })}>
              <option value="quarantine">{tr("Mettre en quarantaine (restaurables depuis Antivirus)")}</option>
              <option value="report">{tr("Signaler seulement")}</option>
            </Select>
          </Field>
        )}
        {s.type === "snapshot" && (
          <div className="space-y-3 rounded-xl border border-line p-4">
            <div className="font-medium text-[13px]">{tr("Rétention")}</div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Garder les N derniers")} hint={tr("0 = pas de limite en nombre")}><Input type="number" min={0} value={s.keepCount ?? 0} onChange={(e) => set({ keepCount: Math.max(0, +e.target.value) })} /></Field>
              <Field label={tr("Garder pendant (jours)")} hint={tr("0 = pas de limite en âge")}><Input type="number" min={0} value={s.keepDays ?? 0} onChange={(e) => set({ keepDays: Math.max(0, +e.target.value) })} /></Field>
            </div>
            {s.targetKind === "zfs" && <Switch checked={!!s.recursive} onChange={(v) => set({ recursive: v })} label={tr("Inclure les datasets enfants")} hint={tr("Snapshot récursif atomique (zfs snapshot -r)")} />}
            <div className="text-[12px] text-muted">{tr("Seuls les snapshots créés par cette tâche sont supprimés ; le plus récent est toujours conservé. Les snapshots manuels ne sont jamais touchés.")}</div>
          </div>
        )}

        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Planification")}</div>
          <ScheduleEditor plan={plan} onChange={setPlan} />
        </div>
        <Switch checked={s.enabled} onChange={(v) => set({ enabled: v })} label={tr("Tâche active")} />
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function HistoryModal({ s, onClose }: { s: Schedule; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={tr("Historique — {0}", s.name)} wide>
      {s.history.length === 0 ? <Empty icon={<History />} title={tr("Aucune exécution")} /> : (
        <div className="space-y-2">
          {s.history.map((h, i) => (
            <div key={i} className="flex items-start gap-3 rounded-xl bg-panel-2 px-4 py-3">
              {h.status === "success" ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={17} className="mt-0.5 shrink-0 text-danger" />}
              <div className="min-w-0 flex-1">
                <div className="text-[13px]">{srv(h.message)}</div>
                <div className="text-muted text-[11.5px]">{dateTime(h.started)} · {h.duration < 1 ? "<1" : h.duration.toFixed(0)} s{h.manual ? tr(" · lancée manuellement") : ""}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

function targetLabel(s: Schedule, targets?: Targets) {
  if (s.type === "smart") return s.target === "all" ? tr("Tous les disques") : s.target;
  if (s.type === "appupdate") return s.target === "all" ? tr("Toutes les applications en marche") : s.target;
  if (s.type === "antivirus") return s.target === "all" ? tr("Tout le stockage") : s.target;
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
  const del = useAction((id: string) => api.del(`/api/schedules/${id}`), { success: tr("Tâche supprimée") });
  const defaults = useAction(() => api.post<{ created: number }>("/api/schedules/defaults"), { success: (r) => (r.created ? tr("{0} tâche(s) recommandée(s) ajoutée(s)", r.created) : tr("Le plan recommandé est déjà en place")) });

  return (
    <div>
      <PageHeader title={tr("Tâches planifiées")} subtitle={tr("Snapshots automatiques, vérifications d'intégrité et tests SMART")} icon={<CalendarClock size={20} />} actions={<>
        <Button icon={<Sparkles size={15} />} loading={defaults.isPending} onClick={() => defaults.mutate()}>{tr("Plan recommandé")}</Button>
        <Button variant="primary" icon={<Plus size={15} />} disabled={!targets.data} onClick={() => setEdit(null)}>{tr("Nouvelle tâche")}</Button>
      </>} />
      <ErrorBox error={error || targets.error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card>
          <Empty icon={<CalendarClock />} title={tr("Aucune tâche planifiée")} text={tr("Le plan recommandé crée un scrub mensuel par grappe et par pool, des snapshots quotidiens conservés 14 jours, un test SMART court chaque semaine et un test long chaque mois.")}
            action={<div className="flex gap-2"><Button variant="primary" icon={<Sparkles size={15} />} onClick={() => defaults.mutate()}>{tr("Créer le plan recommandé")}</Button><Button onClick={() => setEdit(null)}>{tr("Tâche personnalisée")}</Button></div>} />
        </Card>
      ) : (
        <Card pad={false}>
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Tâche")}</th><th>{tr("Cible")}</th><th>{tr("Planification")}</th><th>{tr("Dernière exécution")}</th><th>{tr("Prochaine")}</th><th>{tr("Active")}</th><th /></tr></thead>
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
                            <div className="whitespace-nowrap text-muted text-[11.5px]">{ti.label}{s.type === "snapshot" && ` · garder ${[s.keepCount ? tr("{0} derniers", s.keepCount) : "", s.keepDays ? tr("{0} j", s.keepDays) : ""].filter(Boolean).join(" / ")}`}{s.type === "smart" && ` · ${s.smartTest === "long" ? "long" : "court"}`}</div>
                          </div>
                        </div>
                      </td>
                      <td className="whitespace-nowrap text-[12.5px]">{targetLabel(s, targets.data)}</td>
                      <td className="whitespace-nowrap text-[12.5px]">{describeCron(s.cron)}</td>
                      <td className="whitespace-nowrap text-[12.5px]">
                        {s.running ? <Badge tone="accent" dot>{tr("En cours")}</Badge> : last ? (
                          <button onClick={() => setHist(s)} className="flex items-center gap-1.5 text-left hover:underline" title={srv(last.message)}>
                            {last.status === "success" ? <CheckCircle2 size={14} className="text-ok" /> : <XCircle size={14} className="text-danger" />}
                            {ago(last.started)}
                          </button>
                        ) : <span className="text-muted">{tr("Jamais")}</span>}
                      </td>
                      <td className="whitespace-nowrap text-[12.5px] text-muted">{s.enabled && s.nextRun ? dateTime(s.nextRun) : "—"}</td>
                      <td><Switch checked={s.enabled} onChange={() => toggle.mutate(s)} /></td>
                      <td className="text-right whitespace-nowrap">
                        <IconButton title={tr("Exécuter maintenant")} disabled={s.running} onClick={() => run.mutate(s.id)}><Play size={15} /></IconButton>
                        <IconButton title={tr("Historique")} onClick={() => setHist(s)}><History size={15} /></IconButton>
                        <IconButton title={tr("Modifier")} disabled={!targets.data} onClick={() => setEdit(s)}><Edit3 size={15} /></IconButton>
                        <IconButton title={tr("Supprimer")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer « {0} » ?", s.name), text: tr("La tâche ne s'exécutera plus. Les snapshots déjà créés sont conservés."), danger: true })) del.mutate(s.id); }}><Trash2 size={15} /></IconButton>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <p className="mt-4 text-[12px] text-muted">{tr("Les tâches s'exécutent à l'heure locale du NAS. Une exécution manquée pendant que le NAS est éteint n'est pas rattrapée. Les échecs déclenchent une notification (Paramètres → Notifications).")}</p>
      {edit !== undefined && targets.data && <TaskModal initial={edit} targets={targets.data} onClose={() => setEdit(undefined)} />}
      {hist && <HistoryModal s={data?.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
    </div>
  );
}
