import { useEffect, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Cpu, DatabaseBackup, HardDrive, MemoryStick, Pencil, Play, Plus, Trash2, Workflow, Zap } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Routine, RoutineAction } from "../../lib/types";
import { ago, cx } from "../../lib/format";
import { Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Trigger = Routine["trigger"];

const triggerInfo: Record<Trigger, { label: string; icon: ReactNode; desc: string }> = {
  cpu: { label: tr("CPU usage"), icon: <Cpu size={15} />, desc: tr("Average CPU over the last minute above a threshold") },
  mem: { label: tr("Memory usage"), icon: <MemoryStick size={15} />, desc: tr("Average memory over the last minute above a threshold") },
  "volume-full": { label: tr("Volume almost full"), icon: <HardDrive size={15} />, desc: tr("A volume fills beyond a percentage") },
  "disk-failed": { label: tr("Failing disk"), icon: <AlertTriangle size={15} />, desc: tr("A disk fails its SMART health check") },
  "disk-weak": { label: tr("Disk weakening"), icon: <HardDrive size={15} />, desc: tr("A disk shows warning signs (sectors, temperature)") },
  "backup-failed": { label: tr("Backup failed"), icon: <DatabaseBackup size={15} />, desc: tr("The last run of a backup task failed") },
};

function triggerLabel(rt: Routine): string {
  switch (rt.trigger) {
    case "cpu": return tr("CPU usage above {0} %", rt.value ?? 0);
    case "mem": return tr("Memory usage above {0} %", rt.value ?? 0);
    case "volume-full": return rt.target ? tr("Volume {0} above {1} %", rt.target, rt.value ?? 0) : tr("Any volume above {0} %", rt.value ?? 0);
    case "disk-failed": return rt.target ? tr("Disk {0} fails its health check", rt.target) : tr("A disk fails its health check");
    case "disk-weak": return rt.target ? tr("Disk {0} is getting weak", rt.target) : tr("A disk is getting weak");
    case "backup-failed": return rt.target ? tr("Backup task failed") : tr("Any backup task failed");
  }
}

function actionsSummary(actions: RoutineAction[]): string {
  return actions.map((a) => (a.kind === "webhook" ? tr("Webhook") : tr("Notification"))).join(", ") || "—";
}

const blank: Routine = { id: "", name: "", enabled: true, trigger: "volume-full", value: 90, target: "", actions: [{ kind: "notify", message: "" }], cooldown: 60 };

function RoutineModal({ initial, onClose }: { initial: Routine | null; onClose: () => void }) {
  const editing = !!initial;
  const [rt, setRt] = useState<Routine>(initial ?? blank);
  const [named, setNamed] = useState(editing);
  const set = (k: Partial<Routine>) => setRt((p) => ({ ...p, ...k }));
  const volumes = useQuery({ queryKey: ["overview"], queryFn: () => api.get<{ volumes: { name: string }[] }>("/api/overview"), enabled: rt.trigger === "volume-full" });
  const backups = useQuery({ queryKey: ["backups"], queryFn: () => api.get<{ id: string; name: string }[]>("/api/backups"), enabled: rt.trigger === "backup-failed" });

  useEffect(() => {
    if (named) return;
    const info = triggerInfo[rt.trigger];
    set({ name: info.label });
  }, [rt.trigger, named]);

  const setAction = (i: number, k: Partial<RoutineAction>) => set({ actions: rt.actions.map((a, j) => (j === i ? { ...a, ...k } : a)) });
  const payload = { ...rt, cooldown: rt.cooldown ?? 60 };
  const save = useAction(() => (editing ? api.put(`/api/routines/${rt.id}`, payload) : api.post("/api/routines", payload)), { success: editing ? tr("Routine updated") : tr("Routine created"), onDone: onClose });
  const test = useAction(() => api.post(`/api/routines/${rt.id}/fire`), { success: tr("Test run: the actions were executed") });

  return (
    <Modal open onClose={onClose} title={editing ? tr("Edit \"{0}\"", rt.name) : tr("New routine")} wide footer={<>
      {editing && <Button icon={<Play size={15} />} loading={test.isPending} onClick={() => test.mutate()}>{tr("Test now")}</Button>}
      <div className="flex-1" />
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!rt.name || !rt.actions.length} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="@container"><div className="grid gap-2 @xl:grid-cols-2 @3xl:grid-cols-3">
          {(Object.keys(triggerInfo) as Trigger[]).map((t) => (
            <button key={t} type="button" disabled={editing} onClick={() => set({ trigger: t, target: "" })} className={cx("flex items-center gap-2 rounded-xl border px-3 py-2.5 text-left transition disabled:opacity-60", rt.trigger === t ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
              {triggerInfo[t].icon}
              <div>
                <div className="font-medium text-[13px]">{triggerInfo[t].label}</div>
                <div className="text-muted text-[11.5px]">{triggerInfo[t].desc}</div>
              </div>
            </button>
          ))}
        </div></div>

        <Field label={tr("Name")}><Input value={rt.name} onChange={(e) => { setNamed(true); set({ name: e.target.value }); }} /></Field>

        {(rt.trigger === "cpu" || rt.trigger === "mem" || rt.trigger === "volume-full") && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Threshold (%)")} hint={tr("Checked every minute, on the average of the last minute for CPU and memory")}><Input type="number" min={1} max={99} value={rt.value ?? 90} onChange={(e) => set({ value: Math.min(99, Math.max(1, +e.target.value)) })} /></Field>
            {rt.trigger === "volume-full" && (
              <Field label={tr("Volume")} hint={tr("Leave on \"Any\" to watch every volume")}>
                <Select value={rt.target ?? ""} onChange={(e) => set({ target: e.target.value })}>
                  <option value="">{tr("Any")}</option>
                  {(volumes.data?.volumes ?? []).map((v) => <option key={v.name} value={v.name}>{v.name}</option>)}
                </Select>
              </Field>
            )}
          </div>
        )}
        {rt.trigger === "backup-failed" && (
          <Field label={tr("Backup task")} hint={tr("Leave on \"Any\" to watch every task")}>
            <Select value={rt.target ?? ""} onChange={(e) => set({ target: e.target.value })}>
              <option value="">{tr("Any")}</option>
              {(backups.data ?? []).map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </Select>
          </Field>
        )}

        <div>
          <div className="mb-2 flex items-center justify-between">
            <div className="text-[12.5px] font-medium text-muted">{tr("Then")}</div>
            <Button icon={<Plus size={14} />} onClick={() => set({ actions: [...rt.actions, { kind: "notify", message: "" }] })}>{tr("Add an action")}</Button>
          </div>
          <div className="space-y-2">
            {rt.actions.map((a, i) => (
              <div key={i} className="flex flex-wrap items-end gap-2 rounded-xl border border-line p-3">
                <Field label={tr("Action")} className="w-40">
                  <Select value={a.kind} onChange={(e) => setAction(i, { kind: e.target.value as RoutineAction["kind"] })}>
                    <option value="notify">{tr("Notification")}</option>
                    <option value="webhook">{tr("Webhook")}</option>
                  </Select>
                </Field>
                {a.kind === "notify" ? (
                  <Field label={tr("Message")} className="min-w-[240px] flex-1" hint={tr("{detail} = what fired the rule; empty = default text")}>
                    <Input value={a.message ?? ""} onChange={(e) => setAction(i, { message: e.target.value })} placeholder="{name}: {detail}" />
                  </Field>
                ) : (
                  <Field label="URL" className="min-w-[240px] flex-1" hint={tr("Receives a JSON body: title, message, host, routine, trigger")}>
                    <Input value={a.url ?? ""} onChange={(e) => setAction(i, { url: e.target.value })} placeholder="https://…" />
                  </Field>
                )}
                <IconButton title={tr("Remove")} className="hover:!text-danger" disabled={rt.actions.length === 1} onClick={() => set({ actions: rt.actions.filter((_, j) => j !== i) })}><Trash2 size={15} /></IconButton>
              </div>
            ))}
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("At most once every (minutes)")} hint={tr("0 = default (60 min)")}>
            <Input type="number" min={0} value={rt.cooldown ?? 60} onChange={(e) => set({ cooldown: Math.max(0, +e.target.value) })} />
          </Field>
          <div className="pt-7"><Switch checked={rt.enabled} onChange={(v) => set({ enabled: v })} label={tr("Routine active")} /></div>
        </div>
        <ErrorBox error={save.error || test.error} />
      </div>
    </Modal>
  );
}

export default function Routines() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<Routine | null | undefined>(undefined);
  const { data, isLoading, error } = useQuery({ queryKey: ["routines"], queryFn: () => api.get<Routine[]>("/api/routines"), refetchInterval: 10000 });
  const toggle = useAction((rt: Routine) => api.put(`/api/routines/${rt.id}`, { ...rt, enabled: !rt.enabled }));
  const fire = useAction((id: string) => api.post(`/api/routines/${id}/fire`), { success: tr("Test run: the actions were executed") });
  const del = useAction((id: string) => api.del(`/api/routines/${id}`), { success: tr("Routine deleted") });

  return (
    <div>
      <PageHeader title={tr("Routines")} subtitle={tr("If → then automations: react to what happens on the NAS")} icon={<Workflow size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{tr("New routine")}</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card>
          <Empty icon={<Workflow />} title={tr("No routines")} text={tr("A routine watches a condition — a full volume, a failing disk, a failed backup — and reacts: notification, webhook. Example: \"if a volume is more than 85 % full, notify me\".")}
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{tr("New routine")}</Button>} />
        </Card>
      ) : (
        <Card pad={false}>
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Routine")}</th><th>{tr("If")}</th><th>{tr("Then")}</th><th>{tr("Last run")}</th><th>{tr("Active")}</th><th /></tr></thead>
              <tbody>
                {data.map((rt) => (
                  <tr key={rt.id} className={rt.enabled ? "" : "opacity-60"}>
                    <td>
                      <div className="flex items-center gap-2.5">
                        <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent">{triggerInfo[rt.trigger].icon}</span>
                        <div>
                          <div className="whitespace-nowrap font-medium">{rt.name}</div>
                          <div className="whitespace-nowrap text-muted text-[11.5px]">{triggerInfo[rt.trigger].label}</div>
                        </div>
                      </div>
                    </td>
                    <td className="whitespace-nowrap text-[12.5px]">{triggerLabel(rt)}</td>
                    <td className="whitespace-nowrap text-[12.5px]">{actionsSummary(rt.actions)}</td>
                    <td className="whitespace-nowrap text-[12.5px] text-muted">{rt.lastFired ? ago(rt.lastFired) : tr("Never")}</td>
                    <td><Switch aria-label={tr("Active")} checked={rt.enabled} onChange={() => toggle.mutate(rt)} /></td>
                    <td className="text-right whitespace-nowrap">
                      <IconButton title={tr("Test now")} onClick={() => fire.mutate(rt.id)}><Zap size={15} /></IconButton>
                      <IconButton title={tr("Edit")} onClick={() => setEdit(rt)}><Pencil size={15} /></IconButton>
                      <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete \"{0}\"?", rt.name), text: tr("The routine will no longer run."), danger: true })) del.mutate(rt.id); }}><Trash2 size={15} /></IconButton>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <p className="mt-4 text-[12px] text-muted">{tr("Routines are evaluated every minute. Webhook actions receive a JSON body with the fields title, message, host, routine and trigger.")}</p>
      {edit !== undefined && <RoutineModal initial={edit} onClose={() => setEdit(undefined)} />}
    </div>
  );
}
