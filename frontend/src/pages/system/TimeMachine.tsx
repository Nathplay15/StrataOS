import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Laptop, Plus } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Group, User } from "../../lib/types";
import { ago, bytes } from "../../lib/format";
import { Badge, Button, Card, Checkbox, ErrorBox, Field, Input, Modal, Notice, Select } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";

type TMBackup = { name: string; path: string; size: number; lastBackup: string; machineId?: string; model?: string };
type TMShare = { name: string; path: string; maxGb: number; permissions: Record<string, string>; locked: boolean; used: number; backups: TMBackup[] };

function SizeLimit({ s }: { s: TMShare }) {
  const [gb, setGb] = useState(String(s.maxGb || ""));
  const save = useAction(() => api.put(`/api/backups/timemachine/${encodeURIComponent(s.name)}`, { maxGb: +gb || 0 }), { success: tr("Size limit saved") });
  return (
    <div className="flex items-end gap-2">
      <Field label={tr("Size limit (GB)")} hint={tr("Empty: no limit. Time Machine deletes its oldest backups to stay below.")}><Input className="!w-32" type="number" min={0} value={gb} onChange={(e) => setGb(e.target.value)} placeholder={tr("None")} /></Field>
      <Button size="sm" loading={save.isPending} disabled={(+gb || 0) === (s.maxGb || 0)} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </div>
  );
}

function CreateModal({ volumes, onClose }: { volumes: { path: string; label: string }[]; onClose: () => void }) {
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const [f, setF] = useState({ name: "TimeMachine", path: volumes[0] ? `${volumes[0].path}/TimeMachine` : "", maxGb: "", users: [] as string[] });
  const create = useAction(() => api.post("/api/backups/timemachine", { ...f, maxGb: +f.maxGb || 0 }), { success: tr("Time Machine destination created"), onDone: onClose });
  const toggle = (who: string, on: boolean) => setF({ ...f, users: on ? [...f.users, who] : f.users.filter((x) => x !== who) });
  return (
    <Modal open onClose={onClose} title={tr("Create a Time Machine destination")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={create.isPending} disabled={!f.name || !f.path || !f.users.length} onClick={() => create.mutate()}>{tr("Create")}</Button></>}>
      <div className="space-y-4">
        <p className="text-muted text-[12.5px]">{tr("Creates the folder and an SMB share announced to Macs on the network (Bonjour): it then appears in System Settings → General → Time Machine.")}</p>
        <Field label={tr("Share name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <Field label={tr("Folder")}>
          <div className="flex gap-2">
            <Select className="!w-56 shrink-0" value="" onChange={(e) => e.target.value && setF({ ...f, path: `${e.target.value}/TimeMachine` })}>
              <option value="">{tr("Volume…")}</option>
              {volumes.map((v) => <option key={v.path} value={v.path}>{srv(v.label)}</option>)}
            </Select>
            <Input className="mono" value={f.path} onChange={(e) => setF({ ...f, path: e.target.value })} />
          </div>
        </Field>
        <Field label={tr("Size limit (GB)")} hint={tr("Empty: no limit. Time Machine deletes its oldest backups to stay below.")}><Input type="number" min={0} value={f.maxGb} onChange={(e) => setF({ ...f, maxGb: e.target.value })} placeholder={tr("None")} /></Field>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Who can back up here")}</div>
          <div className="grid max-h-48 gap-2 overflow-y-auto sm:grid-cols-2">
            {groups.data?.filter((g) => !g.system).map((g) => <Checkbox key={`@${g.name}`} checked={f.users.includes(`@${g.name}`)} onChange={(v) => toggle(`@${g.name}`, v)} label={tr("Group {0}", g.name)} />)}
            {users.data?.filter((u) => !u.disabled).map((u) => <Checkbox key={u.username} checked={f.users.includes(u.username)} onChange={(v) => toggle(u.username, v)} label={u.fullName ? `${u.fullName} (${u.username})` : u.username} />)}
          </div>
        </div>
        <ErrorBox error={create.error} />
      </div>
    </Modal>
  );
}

export function TimeMachineCard({ volumes }: { volumes: { path: string; label: string }[] }) {
  const { data, error } = useQuery({ queryKey: ["timemachine"], queryFn: () => api.get<TMShare[]>("/api/backups/timemachine") });
  const [creating, setCreating] = useState(false);
  return (
    <Card className="mt-6" title={<span className="flex items-center gap-2"><Laptop size={16} />{tr("Time Machine")}</span>} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => setCreating(true)}>{tr("Create a Time Machine destination")}</Button>}>
      <p className="mb-3 text-muted text-[12.5px]">{tr("Macs back up here over SMB. Shares with Time Machine enabled, and the Macs found on them:")}</p>
      <ErrorBox error={error} />
      {!data?.length ? <div className="text-muted text-[13px]">{tr("No Time Machine share. Create a destination, or enable “Time Machine target” on a share.")}</div> : data.map((s) => (
        <div key={s.name} className="mb-3 rounded-xl border border-line p-3">
          <div className="flex flex-wrap items-center gap-2">
            <div className="font-medium">{s.name}</div><span className="mono text-muted text-[12px]">{s.path}</span>
            {s.locked && <Badge tone="warn">{tr("Locked")}</Badge>}
            <div className="flex-1" />
            <span className="text-muted text-[12px]">{s.maxGb ? tr("{0} used of {1}", bytes(s.used), bytes(s.maxGb * 1e9)) : tr("{0} used", bytes(s.used))}</span>
          </div>
          {s.backups.length === 0 ? <div className="mt-2 text-muted text-[12.5px]">{tr("No Mac has backed up to this share yet.")}</div> : (
            <table className="table mt-2">
              <thead><tr><th>{tr("Mac")}</th><th>{tr("Size")}</th><th>{tr("Last backup")}</th></tr></thead>
              <tbody>
                {s.backups.map((b) => (
                  <tr key={b.path}>
                    <td><div>{b.name}</div>{b.model && <div className="text-muted text-[11.5px]">{b.model}</div>}</td>
                    <td className="text-muted">{bytes(b.size)}</td>
                    <td className="text-muted text-[12px]">{b.lastBackup && !b.lastBackup.startsWith("0001") ? ago(b.lastBackup) : tr("Never")}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <div className="mt-3"><SizeLimit key={s.maxGb} s={s} /></div>
        </div>
      ))}
      {data && data.length > 0 && <Notice>{tr("On the Mac: System Settings → General → Time Machine → Add Backup Disk, then sign in with a NAS account allowed on the share.")}</Notice>}
      {creating && <CreateModal volumes={volumes} onClose={() => setCreating(false)} />}
    </Card>
  );
}
