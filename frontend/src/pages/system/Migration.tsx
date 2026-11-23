import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownToLine, CheckCircle2, Edit3, History, Play, Plus, Trash2, XCircle } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Migration } from "../../lib/types";
import { ago, bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

// Migration assistant (roadmap 0.15): copy the data of another NAS (SMB, NFS
// or rsync daemon) into a shared folder of this NAS, once or on a schedule.

const kindLabels: Record<Migration["kind"], string> = { smb: tr("SMB share"), nfs: tr("NFS export"), rsync: tr("rsync daemon (module)") };

type Draft = {
  name: string; kind: Migration["kind"]; host: string; port: string; share: string; path: string;
  user: string; password: string; destShare: string; destSub: string; delete: boolean; cron: string; enabled: boolean;
};

const blank: Draft = { name: "", kind: "smb", host: "", port: "", share: "", path: "", user: "", password: "", destShare: "", destSub: "", delete: false, cron: "", enabled: true };

function draftOf(m?: Migration): Draft {
  if (!m) return blank;
  const [destShare, ...rest] = m.dest.replace(/^\//, "").split("/");
  return {
    name: m.name, kind: m.kind, host: m.host, port: m.port ? String(m.port) : "", share: m.share, path: m.path ?? "",
    user: m.user ?? "", password: "", destShare, destSub: rest.join("/"), delete: !!m.delete, cron: m.cron ?? "", enabled: m.enabled,
  };
}

function MigrationModal({ initial, shares, onClose }: { initial: Migration | null; shares: { name: string }[]; onClose: () => void }) {
  const editing = !!initial;
  const qc = useQueryClient();
  const [d, setD] = useState<Draft>(() => draftOf(initial ?? undefined));
  const up = (k: Partial<Draft>) => setD((p) => ({ ...p, ...k }));
  const dest = `/${d.destShare}${d.destSub ? "/" + d.destSub.replace(/^\/+/, "") : ""}`;
  const payload = {
    name: d.name, kind: d.kind, host: d.host, share: d.share, path: d.path,
    port: parseInt(d.port) || 0, user: d.user, password: d.password, dest,
    delete: d.delete, cron: d.cron, enabled: d.enabled,
  };
  const save = useAction(() => (editing ? api.put(`/api/migrations/${initial!.id}`, payload) : api.post("/api/migrations", payload)), {
    success: editing ? tr("Import updated") : tr("Import created"),
    onDone: () => { qc.invalidateQueries({ queryKey: ["migrations"] }); onClose(); },
  });
  return (
    <Modal open onClose={onClose} title={editing ? tr("Edit \"{0}\"", initial!.name) : tr("New import")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!d.name || !d.host || !d.share || !d.destShare} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <ErrorBox error={save.error} />
        <Field label={tr("Name")}><Input value={d.name} onChange={(e) => up({ name: e.target.value })} placeholder={tr("Import from another NAS")} /></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Source")}>
            <Select value={d.kind} onChange={(e) => up({ kind: e.target.value as Draft["kind"] })}>
              {(Object.keys(kindLabels) as Migration["kind"][]).map((k) => <option key={k} value={k}>{kindLabels[k]}</option>)}
            </Select>
          </Field>
          <Field label={tr("Host")}><Input value={d.host} onChange={(e) => up({ host: e.target.value })} placeholder="192.168.1.10" /></Field>
          {d.kind === "rsync" && <Field label={tr("Port")} hint={tr("873 by default")}><Input type="number" min={1} max={65535} value={d.port} onChange={(e) => up({ port: e.target.value })} /></Field>}
          <Field label={tr("Share, export or module")}><Input value={d.share} onChange={(e) => up({ share: e.target.value })} placeholder={d.kind === "rsync" ? "backup" : "data"} /></Field>
          <Field label={tr("Folder inside (optional)")}><Input value={d.path} onChange={(e) => up({ path: e.target.value })} placeholder="photos/2024" /></Field>
          <Field label={tr("User name")}><Input autoComplete="off" value={d.user} onChange={(e) => up({ user: e.target.value })} /></Field>
          <Field label={tr("Password")} hint={editing && initial?.password ? tr("Leave as is to keep the saved password.") : undefined}>
            <Input type="password" autoComplete="new-password" value={d.password} onChange={(e) => up({ password: e.target.value })} />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Shared folder")}><Select value={d.destShare} onChange={(e) => up({ destShare: e.target.value })}>
            <option value="">{tr("Choose a shared folder…")}</option>
            {shares.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
          </Select></Field>
          <Field label={tr("Folder in the share (optional)")}><Input value={d.destSub} onChange={(e) => up({ destSub: e.target.value })} placeholder="old-nas" /></Field>
        </div>
        <div className="text-[12.5px] text-muted">{tr("Destination")}: <span className="mono">{dest}</span></div>
        <Checkbox checked={d.delete} onChange={(v) => up({ delete: v })} label={tr("Delete files missing on the source (--delete)")} />
        <Field label={tr("Repeat on a schedule (cron, optional)")} hint={tr("minute hour day month weekday — e.g. 0 3 * * * (every night at 3)")}>
          <Input className="mono" value={d.cron} onChange={(e) => up({ cron: e.target.value })} placeholder="0 3 * * *" />
        </Field>
        <Switch checked={d.enabled} onChange={(v) => up({ enabled: v })} label={tr("Import active")} />
      </div>
    </Modal>
  );
}

function HistoryModal({ m, onClose }: { m: Migration; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={tr("History — {0}", m.name)} wide>
      {m.history.length === 0 ? <Empty icon={<History />} title={tr("No runs")} /> : (
        <div className="space-y-2">
          {m.history.map((h, i) => (
            <div key={i} className="flex items-start gap-3 rounded-xl bg-panel-2 px-4 py-3">
              {h.status === "success" ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={17} className="mt-0.5 shrink-0 text-danger" />}
              <div className="min-w-0 flex-1">
                <div className="text-[13px]">{srv(h.message)}{h.files ? ` · ${tr("{0} files", h.files)} · ${bytes(h.bytes ?? 0)}` : ""}</div>
                <div className="text-muted text-[11.5px]">{dateTime(h.started)} · {h.duration < 1 ? "<1" : h.duration.toFixed(0)} s{h.manual ? tr(" · started manually") : ""}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

export default function Migration() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<Migration | null | undefined>(undefined);
  const [hist, setHist] = useState<Migration | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["migrations"], queryFn: () => api.get<Migration[]>("/api/migrations"), refetchInterval: 10000 });
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<{ name: string; path: string }[]>("/api/shares") });
  const toggle = useAction((m: Migration) => api.put(`/api/migrations/${m.id}`, { ...m, password: m.password ?? "", enabled: !m.enabled }));
  const run = useAction((id: string) => api.post(`/api/migrations/${id}/run`));
  const del = useAction((id: string) => api.del(`/api/migrations/${id}`), { success: tr("Import deleted") });

  return (
    <div>
      <PageHeader title={tr("Migration")} subtitle={tr("Import from another NAS")} icon={<ArrowDownToLine size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} disabled={!shares.data} onClick={() => setEdit(null)}>{tr("New import")}</Button>} />
      <ErrorBox error={error || shares.error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card>
          <Empty icon={<ArrowDownToLine />} title={tr("No imports yet")} text={tr("Copy the data of another NAS (SMB, NFS or rsync) into a shared folder of this one.")}
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{tr("New import")}</Button>} />
        </Card>
      ) : (
        <Card pad={false}>
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Name")}</th><th>{tr("Source")}</th><th>{tr("Destination")}</th><th>{tr("Schedule")}</th><th>{tr("Last run")}</th><th>{tr("Next")}</th><th>{tr("Active")}</th><th /></tr></thead>
              <tbody>
                {data.map((m) => {
                  const last = m.history[0];
                  return (
                    <tr key={m.id} className={m.enabled ? "" : "opacity-60"}>
                      <td>
                        <div className="whitespace-nowrap font-medium">{m.name}</div>
                        <div className="whitespace-nowrap text-muted text-[11.5px]">{kindLabels[m.kind]}</div>
                      </td>
                      <td className="whitespace-nowrap text-[12.5px]"><span className="mono">{m.host}</span>{m.share ? ` / ${m.share}` : ""}{m.path ? ` / ${m.path}` : ""}</td>
                      <td className="whitespace-nowrap text-[12.5px] mono">{m.dest}</td>
                      <td className="whitespace-nowrap text-[12.5px]">{m.cron ? <span className="mono">{tr("cron \"{0}\"", m.cron)}</span> : <span className="text-muted">—</span>}</td>
                      <td className="whitespace-nowrap text-[12.5px]">
                        {m.running ? <Badge tone="accent" dot>{tr("In progress")}</Badge> : last ? (
                          <button onClick={() => setHist(m)} className="flex items-center gap-1.5 text-left hover:underline" title={srv(last.message)}>
                            {last.status === "success" ? <CheckCircle2 size={14} className="text-ok" /> : <XCircle size={14} className="text-danger" />}
                            {ago(last.started)}
                          </button>
                        ) : <span className="text-muted">{tr("Never")}</span>}
                      </td>
                      <td className="whitespace-nowrap text-[12.5px] text-muted">{m.enabled && m.nextRun ? dateTime(m.nextRun) : "—"}</td>
                      <td><Switch aria-label={tr("Active")} checked={m.enabled} onChange={() => toggle.mutate(m)} /></td>
                      <td className="text-right whitespace-nowrap">
                        <IconButton title={tr("Run now")} disabled={m.running} onClick={() => run.mutate(m.id)}><Play size={15} /></IconButton>
                        <IconButton title={tr("History")} onClick={() => setHist(m)}><History size={15} /></IconButton>
                        <IconButton title={tr("Edit")} disabled={!shares.data} onClick={() => setEdit(m)}><Edit3 size={15} /></IconButton>
                        <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete \"{0}\"?", m.name), text: tr("The import will no longer run. Copied data stays where it is."), danger: true })) del.mutate(m.id); }}><Trash2 size={15} /></IconButton>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      <p className="mt-4 text-[12px] text-muted">{tr("The copy is made with rsync by the NAS; SMB and NFS sources are mounted read-only while it runs. Failures trigger a notification.")}</p>
      {edit !== undefined && shares.data && <MigrationModal initial={edit} shares={shares.data} onClose={() => setEdit(undefined)} />}
      {hist && <HistoryModal m={data?.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
    </div>
  );
}
