import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Boxes, CheckCircle2, Database, Edit3, Eraser, History, List, Plus, RefreshCw, RotateCcw, ShieldCheck, Trash2, XCircle } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, cx, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr, srv } from "../../lib/i18n";
import { StorageSelect } from "../vms/panels";
import { FolderPicker } from "../../components/FolderPicker";
import { StopJobButton } from "../../components/StopJob";
import type { LXCLocation } from "../vms/containers/types";

// Backup repositories (datastores): restic repositories that several tasks
// and NAS share; VMs and containers are stored there as deduplicated images.

export type VerifyRun = { started: string; duration: number; status: "success" | "failed"; subset: string; manual?: boolean; message: string; errors?: string[]; broken?: string[]; affected?: string[] };
export type Datastore = {
  id: string; name: string; destType: "local" | "sftp" | "s3" | "rest" | "strataos"; destPath: string;
  host?: string; user?: string; port?: number; s3Endpoint?: string; accessKey?: string; secretKey?: string; password?: string;
  vaultUrl?: string; vaultToken?: string; vaultCert?: string; appendOnly?: boolean; offsite?: boolean; created?: string;
  verify: { off?: boolean; cron?: string; subsets?: number }; pruneCron?: string; lastPrune?: string;
  verifies?: VerifyRun[];
  stats?: { updated: string; snapshots: number; logical: number; unique: number; stored: number; dedup: number; ratio: number };
  tasks?: { id: string; name: string }[]; running?: string; jobId?: string; nextVerify?: string; nextPrune?: string; lastVerified?: string; subsets?: number; media?: string; isOffsite?: boolean;
};
type Part = { name: string; id: string; logical: number; added: number };
type Item = { kind: "folders" | "vm" | "ct"; host: string; task: string; name: string; time: string; id: string; parts: Part[]; logical: number; added: number; complete: boolean };

const blank: Datastore = { id: "", name: "", destType: "local", destPath: "", port: 22, password: "", verify: {} };

const verifyPlans = [
  ["40 3 * * *", tr("Every day")],
  ["40 3 * * 0", tr("Every week (Sunday)")],
  ["40 3 1 * *", tr("Every month (the 1st)")],
];

export function datastoreLabel(d?: Pick<Datastore, "destType" | "destPath" | "host" | "user" | "s3Endpoint" | "vaultUrl">) {
  if (!d) return "";
  switch (d.destType) {
    case "local": return d.destPath;
    case "sftp": return `${d.user}@${d.host}:${d.destPath}`;
    case "s3": return tr("S3 {0}/{1}", d.s3Endpoint?.replace(/^https?:\/\//, ""), d.destPath);
    case "rest": return d.destPath;
    case "strataos": return tr("Vault of {0}", (d.vaultUrl ?? "").replace(/^https:\/\//, "").split(/[:/]/)[0]);
  }
}

export function useDatastores() {
  return useQuery({ queryKey: ["datastores"], queryFn: () => api.get<Datastore[]>("/api/datastores"), refetchInterval: 15000 });
}

function Editor({ initial, volumes, onClose }: { initial: Datastore | null; volumes: { path: string; label: string }[]; onClose: () => void }) {
  const editing = !!initial;
  const [d, setD] = useState<Datastore>(initial ?? blank);
  const [host, setHost] = useState("");
  const [code, setCode] = useState("");
  const set = (k: Partial<Datastore>) => setD((p) => ({ ...p, ...k }));
  const save = useAction(() => (editing ? api.put(`/api/datastores/${d.id}`, d) : api.post("/api/datastores", d)), { success: editing ? tr("Repository updated") : tr("Repository ready"), onDone: onClose });
  const test = useAction(() => api.post<{ message: string }>("/api/datastores/test", d), { success: (r) => srv(r.message) });
  const pair = useAction(() => api.post<{ vaultUrl: string; vaultToken: string; vaultCert: string; appendOnly: boolean }>("/api/backups/vault-pair", { host, code }), {
    success: tr("Paired with the vault"),
    onDone: (r) => set({ ...r, destPath: r.vaultUrl }),
  });
  const remote = d.destType === "sftp";
  const ready = !!d.name && !!d.password && (d.destType === "strataos" ? !!d.vaultUrl : !!d.destPath);
  return (
    <Modal open onClose={onClose} wide title={editing ? tr("Edit \"{0}\"", d.name) : tr("New backup repository")} footer={<>
      <Button className="mr-auto" loading={test.isPending} disabled={!ready} onClick={() => test.mutate()}>{tr("Test")}</Button>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!ready} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <p className="text-muted text-[12.5px]">{tr("A repository that several backup tasks, and several NAS, write to. Identical data is stored once, whatever VM, container or NAS it comes from. To share it with another NAS, add it there with the same location and password.")}</p>
        <Field label={tr("Name")}><Input value={d.name} onChange={(e) => set({ name: e.target.value })} placeholder={tr("e.g. VM backups")} /></Field>
        <Field label={tr("Location")}>
          <Select value={d.destType} disabled={editing} onChange={(e) => set({ destType: e.target.value as Datastore["destType"], destPath: "" })}>
            <option value="local">{tr("Local folder / USB disk")}</option>
            <option value="sftp">{tr("SFTP server (SSH)")}</option>
            <option value="s3">{tr("S3 / B2 / MinIO")}</option>
            <option value="rest">{tr("restic REST server")}</option>
            <option value="strataos">{tr("Vault of another StrataOS NAS")}</option>
          </Select>
        </Field>
        {d.destType === "local" && (
          <Field label={tr("Folder")} hint={tr("Inside a volume, e.g. /srv/strata/usb/datastore")}>
            <FolderPicker value={d.destPath} onChange={(v) => set({ destPath: v })} suffix="datastore" label={tr("Folder")} quick={volumes} />
          </Field>
        )}
        {remote && (
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={tr("Server")} className="sm:col-span-2"><Input value={d.host ?? ""} onChange={(e) => set({ host: e.target.value.trim() })} /></Field>
            <Field label={tr("Port")}><Input type="number" value={d.port ?? 22} onChange={(e) => set({ port: +e.target.value })} /></Field>
            <Field label={tr("User")}><Input value={d.user ?? ""} onChange={(e) => set({ user: e.target.value.trim() })} /></Field>
            <Field label={tr("Remote path")} className="sm:col-span-2"><Input className="mono" value={d.destPath} onChange={(e) => set({ destPath: e.target.value })} /></Field>
          </div>
        )}
        {d.destType === "s3" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Endpoint")} className="sm:col-span-2"><Input value={d.s3Endpoint ?? ""} onChange={(e) => set({ s3Endpoint: e.target.value })} /></Field>
            <Field label={tr("Access key")}><Input value={d.accessKey ?? ""} onChange={(e) => set({ accessKey: e.target.value })} /></Field>
            <Field label={tr("Secret key")}><Input type="password" value={d.secretKey ?? ""} onChange={(e) => set({ secretKey: e.target.value })} /></Field>
            <Field label={tr("Bucket / folder")} className="sm:col-span-2"><Input className="mono" value={d.destPath} onChange={(e) => set({ destPath: e.target.value })} /></Field>
          </div>
        )}
        {d.destType === "rest" && <Field label={tr("REST server URL")}><Input className="mono" value={d.destPath} onChange={(e) => set({ destPath: e.target.value })} /></Field>}
        {d.destType === "strataos" && (
          <div className="space-y-3">
            {d.vaultUrl && <Notice>{tr("Paired with {0}", d.vaultUrl.replace(/^https:\/\//, "").split("/")[0])}{d.appendOnly ? ` · ${tr("append-only vault: older versions are never deleted")}` : ""}</Notice>}
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label={tr("Vault NAS address")} className="sm:col-span-2"><Input value={host} onChange={(e) => setHost(e.target.value.trim())} placeholder="192.168.1.30" /></Field>
              <Field label={tr("Pairing code")}><Input className="mono uppercase" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
            </div>
            <div className="flex justify-end"><Button loading={pair.isPending} disabled={!host || !code} onClick={() => pair.mutate()}>{d.vaultUrl ? tr("Pair again") : tr("Pair")}</Button></div>
            <p className="text-muted text-[12px]">{tr("For several NAS to share one repository there, create the vault as “shared”.")}</p>
          </div>
        )}
        <Field label={tr("Repository password")} hint={tr("Encrypts all data. Without it, the backup is unrecoverable: keep it outside the NAS.")}><Input type="password" value={d.password ?? ""} onChange={(e) => set({ password: e.target.value })} /></Field>
        {d.destType === "local" && <Switch checked={!!d.offsite} onChange={(v) => set({ offsite: v })} label={tr("Kept off-site")} hint={tr("A USB disk you take away from the NAS between backups: it then counts as the off-site copy of the 3-2-1 rule.")} />}
        <div className="space-y-3 rounded-xl border border-line p-4">
          <div className="font-medium text-[13px]">{tr("Integrity checks")}</div>
          <Switch checked={!d.verify.off} onChange={(v) => set({ verify: { ...d.verify, off: !v } })} label={tr("Check the repository on a schedule")} hint={tr("Reads the data back and checks every block against its checksum, without restoring anything. A failure is notified and shown in the security advisor.")} />
          {!d.verify.off && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={tr("When")}>
                <Select value={d.verify.cron || "40 3 * * 0"} onChange={(e) => set({ verify: { ...d.verify, cron: e.target.value } })}>
                  {verifyPlans.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                  {d.verify.cron && !verifyPlans.some(([v]) => v === d.verify.cron) && <option value={d.verify.cron}>{d.verify.cron}</option>}
                </Select>
              </Field>
              <Field label={tr("All the data is read over")} hint={tr("Each check reads one part of the data, in turn: 4 means all of it every 4 checks. 1: all of it each time.")}>
                <div className="flex items-center gap-2"><Input type="number" min={1} max={100} className="!w-24" value={d.verify.subsets || 4} onChange={(e) => set({ verify: { ...d.verify, subsets: Math.max(1, Math.min(100, +e.target.value)) } })} /><span className="text-muted text-[12.5px]">{tr("checks")}</span></div>
              </Field>
            </div>
          )}
        </div>
        <ErrorBox error={save.error || test.error || pair.error} />
      </div>
    </Modal>
  );
}

function VerifyHistory({ d, onClose }: { d: Datastore; onClose: () => void }) {
  const list = d.verifies ?? [];
  return (
    <Modal open onClose={onClose} wide title={tr("Integrity checks — {0}", d.name)}>
      {list.length === 0 ? <Empty icon={<ShieldCheck />} title={tr("Never checked")} /> : (
        <div className="space-y-2">
          {list.map((v, i) => (
            <div key={i} className={cx("rounded-xl px-4 py-3", v.status === "success" ? "bg-panel-2" : "bg-danger/10")}>
              <div className="flex items-start gap-3">
                {v.status === "success" ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={17} className="mt-0.5 shrink-0 text-danger" />}
                <div className="min-w-0 flex-1">
                  <div className="text-[13px]">{srv(v.message)}</div>
                  <div className="text-muted text-[11.5px]">{dateTime(v.started)} · {Math.max(1, Math.round(v.duration))} s · {v.subset === "all" ? tr("all the data read") : v.subset ? tr("data part {0} read", v.subset) : tr("structure only")}{v.manual ? tr(" · manual") : ""}</div>
                </div>
              </div>
              {!!v.errors?.length && <ul className="mono mt-2 max-h-32 list-disc space-y-0.5 overflow-y-auto pl-9 text-[11.5px]">{v.errors.map((e, j) => <li key={j} className="break-all">{e}</li>)}</ul>}
              {!!v.broken?.length && <div className="mt-2 pl-8 text-[12px]">{tr("Damaged pack files: {0}", v.broken.length)} <span className="mono text-muted">{v.broken.map((b) => b.slice(0, 12)).join(", ")}</span></div>}
              {!!v.affected?.length && <div className="mt-1 pl-8 text-[12px] text-danger">{tr("Snapshots affected: {0}", v.affected.length)} <span className="mono">{v.affected.slice(0, 20).map((a) => a.slice(0, 8)).join(", ")}</span></div>}
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

function RestoreGuest({ d, it, onClose }: { d: Datastore; it: Item; onClose: () => void }) {
  const [f, setF] = useState({ name: it.name, storage: "", replace: false, start: false });
  const locs = useQuery({ queryKey: ["lxc-storage"], queryFn: () => api.get<LXCLocation[]>("/api/lxc/storage"), enabled: it.kind === "ct" });
  const run = useAction(() => api.post(`/api/datastores/${d.id}/restore`, { snapshot: it.id, ...f }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Restore {0}", it.name)} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!f.name || (it.kind === "vm" && !f.storage)} loading={run.isPending} onClick={() => run.mutate()}>{tr("Restore")}</Button></>}>
      <div className="space-y-4">
        <div className="text-[12.5px] text-muted">{tr("Backup of {0} ({1}), from {2}", dateTime(it.time), bytes(it.logical), it.host)}</div>
        <Field label={tr("Name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        {it.kind === "vm" ? (
          <Field label={tr("Disk locations")}><StorageSelect value={f.storage} onChange={(v) => setF({ ...f, storage: v })} /></Field>
        ) : (
          <Field label={tr("Location")}>
            <Select value={f.storage} onChange={(e) => setF({ ...f, storage: e.target.value })}>
              <option value="">{tr("Where it was")}</option>
              {locs.data?.map((l) => <option key={l.path} value={l.path}>{l.label} ({l.path})</option>)}
            </Select>
          </Field>
        )}
        <Checkbox checked={f.replace} onChange={(v) => setF({ ...f, replace: v })} label={it.kind === "vm" ? tr("Replace the VM of that name (it must be shut down)") : tr("Replace the container of that name (it must be stopped)")} />
        {f.replace && <Notice tone="warn">{tr("The current one and its disks are deleted once the backup has been read back completely. If something fails after that, the restored files are kept on the volume.")}</Notice>}
        <Checkbox checked={f.start} onChange={(v) => setF({ ...f, start: v })} label={tr("Start it once restored")} />
        <ErrorBox error={run.error} />
      </div>
    </Modal>
  );
}

function Contents({ d, onClose }: { d: Datastore; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["datastore-contents", d.id], queryFn: () => api.get<{ items: Item[]; taskNames: Record<string, string>; host: string }>(`/api/datastores/${d.id}/contents`) });
  const [restore, setRestore] = useState<Item | null>(null);
  const kindLabel = { folders: tr("Folders"), vm: tr("VM"), ct: tr("Container") };
  return (
    <Modal open onClose={onClose} wide="xl" title={tr("Contents — {0}", d.name)}>
      <ErrorBox error={error} />
      {isLoading ? <Spinner label={tr("Reading the repository…")} /> : !data?.items.length ? <Empty icon={<Database />} title={tr("Empty repository")} text={tr("Run a backup task that uses it.")} /> : (
        <div className="max-h-[60vh] overflow-y-auto">
          <table className="table">
            <thead><tr><th>{tr("Backup")}</th><th>{tr("Date")}</th><th>{tr("NAS / task")}</th><th className="text-right">{tr("Size")}</th><th className="text-right">{tr("New data")}</th><th /></tr></thead>
            <tbody>
              {data.items.map((it) => (
                <tr key={it.id || it.parts[0]?.id}>
                  <td>
                    <div className="flex items-center gap-2"><Badge tone={it.kind === "folders" ? "neutral" : "accent"}>{kindLabel[it.kind]}</Badge><span className="truncate text-[13px]">{it.name}</span></div>
                    {it.kind !== "folders" && <div className="text-muted text-[11px]">{it.parts.map((p) => p.name === "config" ? tr("definition") : p.name.replace(/^disk-/, "")).join(" · ")}{!it.complete && <span className="text-warn"> · {tr("incomplete")}</span>}</div>}
                  </td>
                  <td className="whitespace-nowrap text-[12px]">{dateTime(it.time)}</td>
                  <td className="text-[12px]">{it.host}{it.host === data.host ? "" : ` (${tr("another NAS")})`}<div className="text-muted text-[11px]">{data.taskNames[it.task] ?? it.task}</div></td>
                  <td className="whitespace-nowrap text-right text-[12px]">{bytes(it.logical)}</td>
                  <td className="whitespace-nowrap text-right text-[12px] text-muted">{bytes(it.added)}</td>
                  <td className="text-right">{it.kind !== "folders" && it.complete && <Button size="sm" variant="ghost" icon={<RotateCcw size={13} />} onClick={() => setRestore(it)}>{tr("Restore")}</Button>}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="mt-2 text-muted text-[11.5px]">{tr("New data: what this backup added to the repository when it ran; the rest was already there (other runs, VMs or NAS). Folders are restored with Restore… (files) and VM disks can be opened there to bring back single files.")}</p>
        </div>
      )}
      {restore && <RestoreGuest d={d} it={restore} onClose={() => setRestore(null)} />}
    </Modal>
  );
}

function Ratio({ v }: { v?: number }) {
  if (!v) return <>—</>;
  return <>{`×${v >= 10 ? v.toFixed(0) : v.toFixed(2)}`}</>;
}

export function DatastoresCard({ volumes }: { volumes: { path: string; label: string }[] }) {
  const { confirm } = useUI();
  const { data } = useDatastores();
  const [edit, setEdit] = useState<Datastore | null | undefined>(undefined);
  const [hist, setHist] = useState<Datastore | null>(null);
  const [contents, setContents] = useState<Datastore | null>(null);
  const verify = useAction((v: { id: string; full: boolean }) => api.post(`/api/datastores/${v.id}/verify`, { full: v.full }));
  const prune = useAction((id: string) => api.post(`/api/datastores/${id}/prune`));
  const stats = useAction((id: string) => api.post(`/api/datastores/${id}/stats`));
  const del = useAction((id: string) => api.del(`/api/datastores/${id}`), { success: tr("Repository removed (its data is kept)") });
  return (
    <Card className="mt-6" title={<span className="flex items-center gap-2"><Boxes size={16} />{tr("Backup repositories")}</span>} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => setEdit(null)}>{tr("New repository")}</Button>}>
      <p className="mb-3 text-muted text-[12.5px]">{tr("Shared by several backup tasks (and NAS): VMs and containers are stored there as deduplicated disk images, identical blocks only once. Choose “Backup repository” as the destination of a versioned backup.")}</p>
      {!data?.length ? <div className="text-muted text-[13px]">{tr("No repositories.")}</div> : data.map((d) => {
        const last = d.verifies?.[0];
        return (
          <div key={d.id} className="mb-3 rounded-xl border border-line p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Database size={16} className="text-accent" />
              <div className="font-medium">{d.name}</div>
              <span className="mono truncate text-muted text-[12px]">{datastoreLabel(d)}</span>
              {d.isOffsite && <Badge tone="ok">{tr("Off-site")}</Badge>}
              {d.appendOnly && <Badge>{tr("Append-only")}</Badge>}
              {d.running && <Badge tone="accent" dot>{d.running === "verify" ? tr("Checking") : tr("Cleaning up")}</Badge>}
              {d.running && d.jobId && <StopJobButton id={d.jobId} />}
              <div className="flex-1" />
              <IconButton title={tr("Edit")} onClick={() => setEdit(d)}><Edit3 size={15} /></IconButton>
              <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Remove the repository {0}?", d.name), text: tr("Only its settings are removed from this NAS: the data stays where it is and can be added again."), danger: true })) del.mutate(d.id); }}><Trash2 size={15} /></IconButton>
            </div>
            <div className="mt-3 grid grid-cols-2 gap-3 text-[12.5px] sm:grid-cols-5">
              <div><div className="text-muted text-[11px]">{tr("Backed up")}</div><b>{d.stats ? bytes(d.stats.logical) : "—"}</b></div>
              <div><div className="text-muted text-[11px]">{tr("Stored")}</div><b>{d.stats ? bytes(d.stats.stored) : "—"}</b></div>
              <div title={tr("Size backed up divided by the size of the unique data")}><div className="text-muted text-[11px]">{tr("Deduplication")}</div><b><Ratio v={d.stats?.dedup} /></b></div>
              <div title={tr("Size backed up divided by the size stored (deduplication and compression)")}><div className="text-muted text-[11px]">{tr("Overall saving")}</div><b><Ratio v={d.stats?.ratio} /></b></div>
              <div><div className="text-muted text-[11px]">{tr("Snapshots")}</div><b>{d.stats?.snapshots ?? "—"}</b>{d.stats && <span className="text-muted text-[11px]"> · {ago(d.stats.updated)}</span>}</div>
            </div>
            <div className={cx("mt-3 flex items-start gap-2 rounded-lg px-3 py-2 text-[12.5px]", !last ? "bg-warn/10" : last.status === "success" ? "bg-ok/10" : "bg-danger/10 text-danger")}>
              {!last ? <AlertTriangle size={15} className="mt-0.5 shrink-0 text-warn" /> : last.status === "success" ? <ShieldCheck size={15} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={15} className="mt-0.5 shrink-0" />}
              <button className="min-w-0 flex-1 text-left" onClick={() => setHist(d)}>
                {!last ? tr("Never checked") : last.status === "success" ? tr("Checked {0}: no errors", ago(last.started)) : tr("Check failed {0}: {1}", ago(last.started), srv(last.message))}
                {last && last.status !== "success" && !!last.affected?.length && <span> · {tr("{0} snapshot(s) affected", last.affected.length)}</span>}
                <span className="block text-muted text-[11.5px]">{d.lastVerified ? tr("Last verified {0}", dateTime(d.lastVerified)) : ""}{d.verify.off ? ` ${tr("Scheduled checks are off.")}` : d.nextVerify ? ` · ${tr("next check {0}", dateTime(d.nextVerify))} (${tr("1/{0} of the data", d.subsets ?? 4)})` : ""}</span>
              </button>
            </div>
            {!!d.tasks?.length && <div className="mt-2 text-muted text-[12px]">{tr("Used by: {0}", d.tasks.map((t) => t.name).join(", "))}</div>}
            <div className="mt-2 flex flex-wrap justify-end gap-1">
              <Button size="sm" variant="ghost" icon={<List size={14} />} onClick={() => setContents(d)}>{tr("Contents")}</Button>
              <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} disabled={!!d.running} onClick={() => verify.mutate({ id: d.id, full: false })}>{tr("Verify now")}</Button>
              <Button size="sm" variant="ghost" disabled={!!d.running} onClick={async () => { if (await confirm({ title: tr("Read all the data of {0}?", d.name), text: tr("Every block is downloaded and checked: on a remote or cloud repository, this transfers all of it.") })) verify.mutate({ id: d.id, full: true }); }}>{tr("Full check")}</Button>
              {!d.appendOnly && <Button size="sm" variant="ghost" icon={<Eraser size={14} />} disabled={!!d.running} onClick={() => prune.mutate(d.id)}>{tr("Clean up")}</Button>}
              <IconButton title={tr("Measure the sizes again")} onClick={() => stats.mutate(d.id)}><RefreshCw size={15} /></IconButton>
              <IconButton title={tr("Checks")} onClick={() => setHist(d)}><History size={15} /></IconButton>
            </div>
          </div>
        );
      })}
      {edit !== undefined && <Editor initial={edit} volumes={volumes} onClose={() => setEdit(undefined)} />}
      {hist && <VerifyHistory d={data?.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
      {contents && <Contents d={contents} onClose={() => setContents(null)} />}
    </Card>
  );
}
