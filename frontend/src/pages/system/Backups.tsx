import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArchiveRestore, ArrowLeftRight, Cloud, Download, Copy, DatabaseBackup, Edit3, FolderSync, History, Layers, Play, Plus, RefreshCw, RotateCcw, Trash2, Vault as VaultIcon } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { BackupTask } from "../../lib/types";
import { ago, bytes, cx, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { ScheduleEditor, describeCron, fromCron, toCron, type Plan } from "./Schedules";
import { tr, srv } from "../../lib/i18n";
import { certChanged, TLSTrustField, trustOf } from "../../components/TLSTrust";
import { TimeMachineCard } from "./TimeMachine";
import { BackupVMPicker } from "./BackupVMs";
import { ProtectionCard, RestoreTestBadge } from "./Protection";
import { RestoreWizard } from "../../components/RestoreWizard";
import { DatastoresCard, useDatastores } from "./Datastores";
import { RunIcon, runTone, StopJobButton } from "../../components/StopJob";
import { FolderPicker } from "../../components/FolderPicker";

type Sources = { folders: { path: string; label: string }[]; datasets: { path: string; label: string }[] };

const kinds = {
  rsync: { label: tr("File copy"), sub: tr("Mirror files (rsync) to a disk, a volume or an SSH server"), icon: <FolderSync size={18} /> },
  pull: { label: tr("Back up another NAS"), sub: tr("Fetch folders from another NAS or server (rsync, SSH, SMB), with versions"), icon: <Download size={18} /> },
  restic: { label: tr("Versioned backup"), sub: tr("Encrypted and deduplicated (restic): local, SFTP, S3, B2, MinIO…"), icon: <Cloud size={18} /> },
  zfs: { label: tr("ZFS replication"), sub: tr("Snapshots sent to or fetched from another pool, local or remote"), icon: <Layers size={18} /> },
  sync: { label: tr("Two-way sync"), sub: tr("Keep a folder identical on two NAS, changes on both sides (SFTP)"), icon: <ArrowLeftRight size={18} /> },
};
type CardKind = keyof typeof kinds;
const cardOf = (b: BackupTask): CardKind => (b.kind === "rsync" && b.sourceType ? "pull" : b.kind);

function srcLabel(b: BackupTask) {
  if (b.kind === "zfs" && b.sourceType) return `${b.user}@${b.host}:${b.sources[0] ?? ""}`;
  switch (b.kind === "rsync" ? b.sourceType : "") {
    case "ssh": return b.sources.map((s) => `${b.user}@${b.host}:${s}`).join(", ");
    case "rsyncd": return b.sources.map((s) => `rsync://${b.host}/${s}`).join(", ");
    case "smb": return b.sources.map((s) => `//${b.host}/${s}`).join(", ");
  }
  return b.sources.join(", ");
}

const blank: BackupTask = { id: "", name: "", kind: "rsync", sources: [], destType: "local", destPath: "", port: 22, cron: "0 1 * * *", enabled: true, history: [], keepDaily: 7, keepWeekly: 4, keepMonthly: 12, keepLast: 7 };

function destLabel(b: BackupTask, stores?: { id: string; name: string }[]) {
  switch (b.destType) {
    case "datastore": return tr("Repository {0}", stores?.find((d) => d.id === b.datastore)?.name ?? "?");
    case "local": return b.destPath;
    case "ssh": case "sftp": return `${b.user}@${b.host}:${b.destPath}`;
    case "s3": return tr("S3 {0}/{1}", b.s3Endpoint?.replace(/^https?:\/\//, ""), b.destPath);
    case "rest": return b.destPath;
    case "strataos": return tr("Vault of {0}", (b.vaultUrl ?? "").replace(/^https:\/\//, "").split(/[:/]/)[0]);
  }
}

function SSHKey() {
  const { data } = useQuery({ queryKey: ["backup-key"], queryFn: () => api.get<{ publicKey: string }>("/api/backups/ssh-key") });
  const { toast } = useUI();
  return (
    <Notice>
      <div className="space-y-2">
        <div>{tr("Add this public key to")}{" "}<span className="mono">~/.ssh/authorized_keys</span> {" "}{tr("of the remote account (another StrataOS NAS, a Synology, a Linux server…):")}</div>
        <div className="flex items-center gap-2">
          <code className="mono min-w-0 flex-1 truncate rounded-lg bg-panel px-2.5 py-1.5">{data?.publicKey ?? "…"}</code>
          <IconButton title={tr("Copy")} onClick={() => { if (data) { navigator.clipboard?.writeText(data.publicKey); toast(tr("Key copied")); } }}><Copy size={15} /></IconButton>
        </div>
      </div>
    </Notice>
  );
}

/** SSH authentication of a task: the NAS's key (recommended) or a password,
 *  for servers that cannot be given a key. */
function SSHAuth({ b, set }: { b: BackupTask; set: (k: Partial<BackupTask>) => void }) {
  const [usePassword, setUsePassword] = useState(!!b.sshPassword);
  return (
    <div className="space-y-3">
      <Field label={tr("Authentication")}>
        <Select value={usePassword ? "password" : "key"} onChange={(e) => { const p = e.target.value === "password"; setUsePassword(p); if (!p) set({ sshPassword: "" }); }}>
          <option value="key">{tr("Key of this NAS (recommended)")}</option>
          <option value="password">{tr("Password")}</option>
        </Select>
      </Field>
      {usePassword ? (
        <>
          <Field label={tr("SSH password")} hint={tr("Stored on this NAS and never shown again. Use a key when the server accepts one: it cannot be guessed or reused elsewhere.")}>
            <Input type="password" autoComplete="new-password" value={b.sshPassword ?? ""} onChange={(e) => set({ sshPassword: e.target.value })} />
          </Field>
        </>
      ) : <SSHKey />}
    </div>
  );
}

function VaultPair({ b, set }: { b: BackupTask; set: (k: Partial<BackupTask>) => void }) {
  const [host, setHost] = useState("");
  const [code, setCode] = useState("");
  const pair = useAction(() => api.post<{ vaultUrl: string; vaultToken: string; vaultCert: string; appendOnly: boolean }>("/api/backups/vault-pair", { host, code }), {
    success: tr("Paired with the vault"),
    onDone: (r) => set({ ...r, destPath: r.vaultUrl }),
  });
  return (
    <div className="space-y-3">
      {b.vaultUrl && <Notice>{tr("Paired with {0}", b.vaultUrl.replace(/^https:\/\//, "").split("/")[0])}{b.appendOnly ? ` · ${tr("append-only vault: older versions are never deleted")}` : ""}</Notice>}
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label={tr("Vault NAS address")} className="sm:col-span-2"><Input value={host} onChange={(e) => setHost(e.target.value.trim())} placeholder="192.168.1.30" /></Field>
        <Field label={tr("Pairing code")} hint={tr("Shown by the vault")}><Input className="mono uppercase" value={code} onChange={(e) => setCode(e.target.value)} placeholder="ABCDE-12345" /></Field>
      </div>
      <div className="flex justify-end"><Button loading={pair.isPending} disabled={!host || !code} onClick={() => pair.mutate()}>{b.vaultUrl ? tr("Pair again") : tr("Pair")}</Button></div>
      <ErrorBox error={pair.error} />
    </div>
  );
}

type VaultT = { id: string; name: string; path: string; appendOnly: boolean; shared?: boolean; created: string; clients: { id: string; name: string; created: string; lastSeen?: string; size: number }[] };

function VaultsCard({ sources }: { sources?: Sources }) {
  const { confirm, toast } = useUI();
  const { data } = useQuery({ queryKey: ["vaults"], queryFn: () => api.get<VaultT[]>("/api/vaults") });
  const [form, setForm] = useState<{ name: string; path: string; appendOnly: boolean; shared: boolean } | null>(null);
  const [code, setCode] = useState<{ vault: string; code: string } | null>(null);
  const create = useAction(() => api.post("/api/vaults", form), { success: tr("Vault created"), onDone: () => setForm(null) });
  const mkCode = useAction((id: string) => api.post<{ code: string }>(`/api/vaults/${id}/code`), { onDone: (r) => setCode({ vault: "", code: r.code }) });
  const revoke = useAction((v: { id: string; cid: string }) => api.del(`/api/vaults/${v.id}/clients/${v.cid}`), { success: tr("Access removed (backups are kept)") });
  const del = useAction((id: string) => api.del(`/api/vaults/${id}`), { success: tr("Vault deleted (data is kept)") });
  return (
    <Card className="mt-6" title={<span className="flex items-center gap-2"><VaultIcon size={16} />{tr("Backup vaults")}</span>} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => setForm({ name: "", path: "", appendOnly: true, shared: false })}>{tr("New vault")}</Button>}>
      <p className="mb-3 text-muted text-[12.5px]">{tr("Receive encrypted backups from other StrataOS NAS here (“Vault of another StrataOS NAS” type on the NAS to back up).")}</p>
      {!data?.length ? <div className="text-muted text-[13px]">{tr("No vaults.")}</div> : data.map((v) => (
        <div key={v.id} className="mb-3 rounded-xl border border-line p-3">
          <div className="flex flex-wrap items-center gap-2">
            <div className="font-medium">{v.name}</div><span className="mono text-muted text-[12px]">{v.path}</span>
            {v.appendOnly && <Badge tone="ok">{tr("Append-only")}</Badge>}
            {v.shared && <Badge tone="accent">{tr("Shared by the paired NAS")}</Badge>}
            <div className="flex-1" />
            <Button size="sm" loading={mkCode.isPending && mkCode.variables === v.id} onClick={() => mkCode.mutate(v.id)}>{tr("Pairing code")}</Button>
            <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete the vault {0}?", v.name), text: tr("Paired NAS will no longer be able to send backups to it. The data remains in the folder."), danger: true })) del.mutate(v.id); }}><Trash2 size={15} /></IconButton>
          </div>
          {v.clients.length > 0 && (
            <table className="table mt-2">
              <tbody>
                {v.clients.map((c) => (
                  <tr key={c.id}>
                    <td>{c.name}</td><td className="text-muted">{v.shared ? "" : bytes(c.size)}</td>
                    <td className="text-muted text-[12px]">{c.lastSeen ? tr("Last upload {0}", ago(c.lastSeen)) : tr("Never")}</td>
                    <td className="text-right"><Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: tr("Remove access for {0}?", c.name), text: tr("This NAS will no longer be able to send backups. Those already received are kept."), danger: true })) revoke.mutate({ id: v.id, cid: c.id }); }}>{tr("Remove")}</Button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      ))}
      <Modal open={!!form} onClose={() => setForm(null)} title={tr("New backup vault")} footer={<><Button onClick={() => setForm(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={create.isPending} disabled={!form?.name || !form.path} onClick={() => create.mutate()}>{tr("Create")}</Button></>}>
        {form && (
          <div className="space-y-4">
            <Field label={tr("Name")}><Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder={tr("e.g. Office backups")} /></Field>
            <Field label={tr("Folder")}>
              <FolderPicker value={form.path} onChange={(v) => setForm({ ...form, path: v })} suffix="coffre" label={tr("Folder")} quick={sources?.folders.filter((f) => f.label.startsWith("Volume") || f.label.startsWith("ZFS"))} />
            </Field>
            <Switch checked={form.appendOnly} onChange={(v) => setForm({ ...form, appendOnly: v })} label={tr("Append-only (anti-ransomware)")} hint={tr("A paired NAS can add backups but never delete any: even if compromised, it cannot destroy its copies. Older versions accumulate.")} />
            <Switch checked={form.shared} onChange={(v) => setForm({ ...form, shared: v })} label={tr("One repository for all the paired NAS")} hint={tr("For a backup repository shared by several NAS: data they have in common (same VMs, same files) is stored once. They must use the same repository password, and each one can read the others' backups.")} />
            <ErrorBox error={create.error} />
          </div>
        )}
      </Modal>
      <Modal open={!!code} onClose={() => setCode(null)} title={tr("Pairing code")} footer={<Button onClick={() => setCode(null)}>{tr("Close")}</Button>}>
        {code && <div className="space-y-3 text-[13px]"><p className="text-muted">{tr("Enter this code on the NAS to back up, in a backup of type “Vault of another StrataOS NAS”. It is valid for 15 minutes, one time only.")}</p><div className="flex items-center gap-2"><code className="mono rounded-lg bg-panel-2 px-3 py-2 text-[18px] tracking-widest">{code.code}</code><IconButton title={tr("Copy")} onClick={() => { navigator.clipboard?.writeText(code.code); toast(tr("Code copied")); }}><Copy size={15} /></IconButton></div></div>}
      </Modal>
    </Card>
  );
}

const pullProtos = {
  ssh: { label: tr("rsync over SSH"), port: 22, hint: tr("Absolute folders of the other NAS, one per line, e.g. /volume1/photos") },
  rsyncd: { label: tr("rsync daemon (rsync://)"), port: 873, hint: tr("Modules, optionally with a sub-folder, one per line, e.g. photos or photos/2024 (Synology, QNAP and TrueNAS offer an rsync server)") },
  smb: { label: tr("SMB share (Windows, any NAS)"), port: 445, hint: tr("Shares, optionally with a sub-folder, one per line, e.g. Photos or home/alice. Mounted read-only during the copy.") },
};

function RemoteFields({ b, set, userLabel, userOptional }: { b: BackupTask; set: (k: Partial<BackupTask>) => void; userLabel?: string; userOptional?: boolean }) {
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      <Field label={tr("Server")} className="sm:col-span-2"><Input value={b.host ?? ""} onChange={(e) => set({ host: e.target.value.trim() })} placeholder={tr("nas2.local or 192.168.1.20")} /></Field>
      <Field label={tr("Port")}><Input type="number" value={b.port ?? 22} onChange={(e) => set({ port: +e.target.value })} /></Field>
      <Field label={userLabel ?? tr("User")} hint={userOptional ? tr("Empty for anonymous access") : undefined}><Input value={b.user ?? ""} onChange={(e) => set({ user: e.target.value.trim() })} /></Field>
    </div>
  );
}

function VersionsFields({ b, set, always }: { b: BackupTask; set: (k: Partial<BackupTask>) => void; always?: boolean }) {
  return (
    <div className="space-y-3">
      {!always && <Switch checked={!!b.versions} onChange={(v) => set({ versions: v, keepLast: v && !b.keepLast && !b.keepDays ? 14 : b.keepLast })} label={tr("Keep versions (snapshots)")} hint={tr("After each run, a read-only snapshot of the destination is taken when it is on Btrfs or ZFS: earlier states stay recoverable even if files were deleted or encrypted at the source.")} />}
      {(always || b.versions) && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={tr("Snapshots kept")} hint={tr("0: no limit by number")}><Input type="number" min={0} value={b.keepLast ?? 0} onChange={(e) => set({ keepLast: Math.max(0, +e.target.value) })} /></Field>
          <Field label={tr("Days kept")} hint={tr("0: no limit by age")}><Input type="number" min={0} value={b.keepDays ?? 0} onChange={(e) => set({ keepDays: Math.max(0, +e.target.value) })} /></Field>
        </div>
      )}
    </div>
  );
}

function LocalFolder({ value, onChange, sources, suffix }: { value: string; onChange: (v: string) => void; sources: Sources; suffix: string }) {
  return <FolderPicker value={value} onChange={onChange} suffix={suffix} label={tr("Destination folder")} quick={sources.folders.filter((f) => f.label.startsWith("Volume") || f.label.startsWith("ZFS"))} />;
}

function Editor({ initial, preset, sources, onClose }: { initial: BackupTask | null; preset?: Partial<BackupTask>; sources: Sources; onClose: () => void }) {
  const editing = !!initial;
  const [b, setB] = useState<BackupTask>(initial ?? { ...blank, ...preset });
  const [scheduled, setScheduled] = useState(!!(initial ?? blank).cron);
  const [plan, setPlan] = useState<Plan>(fromCron((initial ?? blank).cron || "0 1 * * *"));
  const [excludes, setExcludes] = useState((initial?.excludes ?? []).join("\n"));
  const [remoteSources, setRemoteSources] = useState(initial?.sourceType && initial.kind === "rsync" ? initial.sources.join("\n") : "");
  const set = (k: Partial<BackupTask>) => setB((p) => ({ ...p, ...k }));
  const card = cardOf(b);
  const pull = !!b.sourceType;
  const payload = () => ({
    ...b,
    sources: card === "pull" ? remoteSources.split("\n").map((x) => x.trim()).filter(Boolean) : b.sources,
    cron: scheduled ? toCron(plan) : "",
    excludes: excludes.split("\n").map((x) => x.trim()).filter(Boolean),
  });
  const save = useAction(() => (editing ? api.put(`/api/backups/${b.id}`, payload()) : api.post("/api/backups", payload())), { success: editing ? tr("Backup updated") : tr("Backup created"), onDone: onClose });
  const test = useAction(() => api.post<{ message: string }>("/api/backups/test", payload()), { success: (r) => srv(r.message) });
  const remote = !pull && (b.destType === "ssh" || b.destType === "sftp");
  const stores = useDatastores();
  const destTypes = b.kind === "restic" ? [["datastore", tr("Backup repository (VMs as deduplicated images)")], ["local", tr("Local folder / USB disk")], ["sftp", tr("SFTP server (SSH)")], ["s3", tr("S3 / B2 / MinIO")], ["rest", tr("restic REST server")], ["strataos", tr("Vault of another StrataOS NAS")]] : [["local", b.kind === "zfs" ? tr("Local pool") : tr("Local folder / USB disk")], ["ssh", tr("Remote server (SSH)")]];
  const pickKind = (k: CardKind) => {
    const reset = { sources: [], destPath: "", sourceType: "" as const, versions: false };
    if (k === "pull") set({ ...reset, kind: "rsync", sourceType: "ssh", destType: "local", port: 22, versions: true, keepLast: 14, keepDays: 0 });
    else if (k === "sync") set({ ...reset, kind: "sync", destType: "sftp", port: 22, keepLast: 7, keepDays: 0 });
    else set({ ...reset, kind: k, destType: "local", keepLast: 7 });
    setRemoteSources("");
  };
  const hasDest = card === "sync" || b.destType === "strataos" ? true : b.destType === "datastore" ? !!b.datastore : !!b.destPath;
  const hasSources = card === "pull" ? !!remoteSources.trim() : b.sources.length + (b.vms?.length ?? 0) + (b.containers?.length ?? 0) > 0;

  return (
    <Modal open onClose={onClose} title={editing ? tr("Edit \"{0}\"", b.name) : tr("New backup")} wide footer={<>
      <Button className="mr-auto" loading={test.isPending} onClick={() => test.mutate()}>{pull || card === "sync" ? tr("Test connection") : tr("Test destination")}</Button>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!b.name || !hasSources || !hasDest || (card === "sync" && !b.destPath)} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-2 sm:grid-cols-3">
          {(Object.keys(kinds) as CardKind[]).map((k) => (
            <button key={k} type="button" disabled={editing} onClick={() => pickKind(k)} className={cx("flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-left transition disabled:opacity-60", card === k ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
              <span className="mt-0.5 text-accent">{kinds[k].icon}</span>
              <span><span className="block text-[13px] font-medium">{kinds[k].label}</span><span className="block text-muted text-[11.5px]">{kinds[k].sub}</span></span>
            </button>
          ))}
        </div>
        <Field label={tr("Name")}><Input value={b.name} onChange={(e) => set({ name: e.target.value })} placeholder={card === "pull" ? tr("e.g. Office NAS to this NAS") : tr("e.g. Photos to USB disk")} /></Field>

        {card === "pull" && (
          <div className="space-y-4 rounded-xl border border-line p-4">
            <div className="font-medium text-[13px]">{tr("Source: the other NAS")}</div>
            <Field label={tr("Protocol")}>
              <Select value={b.sourceType} disabled={editing} onChange={(e) => { const st = e.target.value as keyof typeof pullProtos; set({ sourceType: st, port: pullProtos[st].port, remotePassword: "", domain: "" }); }}>
                {(Object.keys(pullProtos) as (keyof typeof pullProtos)[]).map((p) => <option key={p} value={p}>{pullProtos[p].label}</option>)}
              </Select>
            </Field>
            <RemoteFields b={b} set={set} userLabel={b.sourceType === "ssh" ? tr("SSH user") : tr("User")} userOptional={b.sourceType !== "ssh"} />
            {b.sourceType !== "ssh" && (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Password")} hint={tr("Stored on this NAS and never shown again; handed to rsync or mount.cifs through a file readable by root only.")}><Input type="password" autoComplete="new-password" value={b.remotePassword ?? ""} onChange={(e) => set({ remotePassword: e.target.value })} /></Field>
                {b.sourceType === "smb" && <Field label={tr("Domain or workgroup")} hint={tr("Optional")}><Input value={b.domain ?? ""} onChange={(e) => set({ domain: e.target.value.trim() })} placeholder="WORKGROUP" /></Field>}
              </div>
            )}
            <Field label={b.sourceType === "rsyncd" ? tr("Modules") : b.sourceType === "smb" ? tr("Shares") : tr("Remote folders")} hint={pullProtos[(b.sourceType || "ssh") as keyof typeof pullProtos].hint}>
              <textarea className="input mono h-20" value={remoteSources} onChange={(e) => setRemoteSources(e.target.value)} />
            </Field>
            {b.sourceType === "ssh" && <SSHAuth b={b} set={set} />}
          </div>
        )}

        {card === "sync" && (
          <div className="space-y-4">
            <Notice tone="warn">{tr("Deletions propagate both ways: a file deleted on one NAS is deleted on the other at the next run. A snapshot of the local folder is taken before each run (Btrfs or ZFS volume), so a mistake can be recovered from Snapshots / Previous versions.")}</Notice>
            <Field label={tr("Folder of this NAS")}>
              <FolderPicker value={b.sources[0] ?? ""} onChange={(v) => set({ sources: v ? [v] : [] })} label={tr("Folder of this NAS")} quick={sources.folders} quickLabel={tr("Choose…")} />
            </Field>
            <div className="space-y-4 rounded-xl border border-line p-4">
              <div className="font-medium text-[13px]">{tr("The other NAS (SFTP)")}</div>
              <RemoteFields b={b} set={set} userLabel={tr("SSH user")} />
              <Field label={tr("Remote folder")} hint={tr("Absolute path, e.g. /volume1/shared. On another StrataOS NAS, an account without SSH access sees the volumes at the root: /main/Shared")}><Input className="mono" value={b.destPath} onChange={(e) => set({ destPath: e.target.value })} /></Field>
              <SSHAuth b={b} set={set} />
            </div>
            <p className="text-muted text-[12px]">{tr("The first run merges both folders without deleting anything. A file changed on both sides is kept twice (both versions, renamed). Two-way sync works on files, on any file system.")}</p>
          </div>
        )}
        {card !== "pull" && card !== "sync" && b.kind !== "zfs" && <BackupVMPicker b={b} set={set} />}

        {card !== "pull" && card !== "sync" && (
          <div>
            {b.kind === "zfs" && (
              <Field label={tr("Direction")} className="mb-3">
                <Select value={b.sourceType ? "pull" : "push"} disabled={editing} onChange={(e) => set(e.target.value === "pull" ? { sourceType: "ssh", destType: "local", sources: [], port: 22 } : { sourceType: "", sources: [] })}>
                  <option value="push">{tr("Send a dataset of this NAS (push)")}</option>
                  <option value="pull">{tr("Fetch a dataset from a remote ZFS host (pull)")}</option>
                </Select>
              </Field>
            )}
            {b.kind === "zfs" && <p className="mb-3 text-muted text-[12px]">{tr("ZFS can only replicate one way: the copy is a read-only image of the source. To keep two folders identical with changes on both sides, use a Two-way sync (it works on files).")}</p>}
            {b.kind === "zfs" && pull ? (
              <div className="space-y-4 rounded-xl border border-line p-4">
                <div className="font-medium text-[13px]">{tr("Source: remote ZFS host (StrataOS or any ZFS server)")}</div>
                <RemoteFields b={b} set={set} userLabel={tr("SSH user")} />
                <Field label={tr("Remote dataset")} hint={tr("e.g. tank/photos")}><Input className="mono" value={b.sources[0] ?? ""} onChange={(e) => set({ sources: e.target.value.trim() ? [e.target.value.trim()] : [] })} /></Field>
                <Notice>{tr("This NAS connects to the source; the source holds no access to this NAS, so even compromised it cannot delete its backups. The remote account needs root or: zfs allow <user> snapshot,send,hold,destroy,mount <dataset>")}</Notice>
                <SSHAuth b={b} set={set} />
              </div>
            ) : (
              <>
                <div className="mb-2 text-[12.5px] font-medium text-muted">{b.kind === "zfs" ? tr("Dataset to replicate") : tr("Folders to back up")}</div>
                {b.kind === "zfs" ? (
                  sources.datasets.length === 0 ? <Notice tone="warn">{tr("No ZFS datasets.")}</Notice> : (
                    <Select value={b.sources[0] ?? ""} onChange={(e) => set({ sources: e.target.value ? [e.target.value] : [] })}>
                      <option value="">{tr("Choose…")}</option>
                      {sources.datasets.map((d) => <option key={d.path} value={d.path}>{d.label}</option>)}
                    </Select>
                  )
                ) : (
                  <div className="grid gap-2 sm:grid-cols-2">
                    {sources.folders.map((f) => (
                      <Checkbox key={f.path} checked={b.sources.includes(f.path)} onChange={(v) => set({ sources: v ? [...b.sources, f.path] : b.sources.filter((x) => x !== f.path) })} label={<span><span className="text-[13px]">{srv(f.label)}</span> <span className="mono text-muted text-[11px]">{f.path}</span></span>} />
                    ))}
                  </div>
                )}
              </>
            )}
          </div>
        )}

        {card !== "sync" && (
          <div className="space-y-4 rounded-xl border border-line p-4">
            <div className="font-medium text-[13px]">{tr("Destination")}{pull ? ` ${tr("(this NAS)")}` : ""}</div>
            {!pull && (
              <Field label={tr("Type")}>
                <Select value={b.destType} onChange={(e) => set({ destType: e.target.value as BackupTask["destType"], destPath: "" })}>
                  {destTypes.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                </Select>
              </Field>
            )}
            {remote && <RemoteFields b={b} set={set} />}
            {b.destType === "datastore" && (
              <Field label={tr("Repository")} hint={tr("Backup → Backup repositories. Its location, password and integrity checks are set there.")}>
                <Select value={b.datastore ?? ""} onChange={(e) => set({ datastore: e.target.value })}>
                  <option value="">{stores.data?.length ? tr("Choose…") : tr("No repositories yet: create one below this list first")}</option>
                  {stores.data?.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
                </Select>
              </Field>
            )}
            {b.destType === "datastore" && ((b.vms?.length ?? 0) > 0 || (b.containers?.length ?? 0) > 0) && <Notice>{tr("VM disks and containers are stored as images cut into blocks: blocks already in the repository (other VMs, earlier runs, other NAS) are not stored again. A VM disk backup can be opened in Restore… to bring back single files.")}</Notice>}
            {b.destType === "strataos" && <VaultPair b={b} set={set} />}
            {b.destType === "s3" && (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Endpoint")} className="sm:col-span-2" hint={tr("e.g. https://s3.eu-west-3.amazonaws.com, https://s3.us-west-002.backblazeb2.com, http://minio:9000")}><Input value={b.s3Endpoint ?? ""} onChange={(e) => set({ s3Endpoint: e.target.value })} /></Field>
                <Field label={tr("Access key")}><Input value={b.accessKey ?? ""} onChange={(e) => set({ accessKey: e.target.value })} /></Field>
                <Field label={tr("Secret key")}><Input type="password" value={b.secretKey ?? ""} onChange={(e) => set({ secretKey: e.target.value })} /></Field>
              </div>
            )}
            {b.destType !== "strataos" && b.destType !== "datastore" && <Field
              label={b.kind === "zfs" ? tr("Destination dataset") : b.destType === "s3" ? tr("Bucket / folder") : b.destType === "rest" ? tr("REST server URL") : b.destType === "local" ? tr("Destination folder") : tr("Remote path")}
              hint={b.kind === "zfs" ? tr("e.g. backup/media (created automatically)") : pull ? tr("Each source is copied into a sub-folder. A new folder on Btrfs or ZFS becomes its own sub-volume or dataset, so its snapshots hold only this backup.") : b.destType === "local" ? tr("Inside a volume, e.g. /srv/strata/usb/backups (a USB disk is imported from Storage Manager)") : undefined}
            >
              {b.destType === "local" && b.kind !== "zfs" ? <LocalFolder value={b.destPath} onChange={(v) => set({ destPath: v })} sources={sources} suffix={pull ? "other-nas" : "sauvegardes"} /> : <Input className="mono" value={b.destPath} onChange={(e) => set({ destPath: e.target.value })} />}
            </Field>}
            {b.destType === "local" && !pull && <Switch checked={!!b.offsite} onChange={(v) => set({ offsite: v })} label={tr("Kept off-site")} hint={tr("A USB disk you take away from the NAS between backups: it then counts as the off-site copy of the 3-2-1 rule.")} />}
            {(b.destType === "s3" || b.destType === "rest") && <TLSTrustField address={(b.destType === "s3" ? b.s3Endpoint : b.destPath) ?? ""} value={trustOf(b)} onChange={(v) => set(v)} />}
            {remote && <SSHAuth b={b} set={set} />}
          </div>
        )}

        {b.kind === "rsync" && (
          <div className="space-y-3">
            <Switch checked={!!b.delete} onChange={(v) => set({ delete: v })} label={tr("Exact mirror")} hint={pull ? tr("Deletes from the copy the files deleted on the other NAS (snapshots still keep them). Without this option, nothing is ever erased from the copy.") : tr("Deletes from the destination the files deleted at the source. Without this option, nothing is ever erased from the copy.")} />
            <Field label={tr("Exclusions (one per line)")} hint={tr("e.g. *.tmp, Thumbs.db, cache/")}><textarea className="input mono h-20" value={excludes} onChange={(e) => setExcludes(e.target.value)} /></Field>
            <Field label={tr("Bandwidth limit")} hint={tr("e.g. 10M (per second); empty: no limit")}><Input className="!w-40" value={b.bwLimit ?? ""} onChange={(e) => set({ bwLimit: e.target.value.trim() })} placeholder={tr("None")} /></Field>
            {pull && <VersionsFields b={b} set={set} />}
          </div>
        )}
        {card === "sync" && (
          <div className="space-y-3">
            <Field label={tr("Exclusions (one per line)")} hint={tr("e.g. *.tmp, Thumbs.db, cache/**")}><textarea className="input mono h-16" value={excludes} onChange={(e) => setExcludes(e.target.value)} /></Field>
            <Field label={tr("Bandwidth limit")} hint={tr("e.g. 10M (per second); empty: no limit")}><Input className="!w-40" value={b.bwLimit ?? ""} onChange={(e) => set({ bwLimit: e.target.value.trim() })} placeholder={tr("None")} /></Field>
            <div className="text-[12.5px] font-medium text-muted">{tr("Snapshots of the local folder before each run")}</div>
            <VersionsFields b={b} set={set} always />
          </div>
        )}
        {b.kind === "restic" && (
          <div className="space-y-3 rounded-xl border border-line p-4">
            {b.destType !== "datastore" && <Field label={tr("Repository password")} hint={tr("Encrypts all data. Without it, the backup is unrecoverable: keep it outside the NAS.")}><Input type="password" value={b.password ?? ""} onChange={(e) => set({ password: e.target.value })} /></Field>}
            <div className="text-[12.5px] font-medium text-muted">{tr("Keep")}</div>
            <div className="grid grid-cols-3 gap-3 sm:grid-cols-6">
              <Field label={tr("Latest")}><Input type="number" min={0} value={b.keepLast ?? 0} onChange={(e) => set({ keepLast: +e.target.value })} /></Field>
              <Field label={tr("Hourly")}><Input type="number" min={0} value={b.keepHourly ?? 0} onChange={(e) => set({ keepHourly: +e.target.value })} /></Field>
              <Field label={tr("Daily")}><Input type="number" min={0} value={b.keepDaily ?? 0} onChange={(e) => set({ keepDaily: +e.target.value })} /></Field>
              <Field label={tr("Weekly")}><Input type="number" min={0} value={b.keepWeekly ?? 0} onChange={(e) => set({ keepWeekly: +e.target.value })} /></Field>
              <Field label={tr("Monthly")}><Input type="number" min={0} value={b.keepMonthly ?? 0} onChange={(e) => set({ keepMonthly: +e.target.value })} /></Field>
              <Field label={tr("Yearly")}><Input type="number" min={0} value={b.keepYearly ?? 0} onChange={(e) => set({ keepYearly: +e.target.value })} /></Field>
            </div>
            {b.destType === "datastore" && <p className="text-muted text-[12px]">{tr("Each VM, container and the folders keep their own versions by these rules; the data no version uses any more is removed by the repository's daily cleanup.")}</p>}
            <Field label={tr("Exclusions (one per line)")}><textarea className="input mono h-16" value={excludes} onChange={(e) => setExcludes(e.target.value)} /></Field>
          </div>
        )}
        {b.kind === "zfs" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Replication points kept")}><Input type="number" min={1} value={b.keepLast ?? 7} onChange={(e) => set({ keepLast: Math.max(1, +e.target.value) })} /></Field>
            <div className="pt-6"><Switch checked={!!b.recursive} onChange={(v) => set({ recursive: v })} label={tr("Include child datasets")} /></div>
          </div>
        )}

        <div className="space-y-3">
          <Switch checked={scheduled} onChange={setScheduled} label={tr("Run automatically")} hint={tr("Otherwise, only on demand")} />
          {scheduled && <ScheduleEditor plan={plan} onChange={setPlan} />}
        </div>
        <Switch checked={b.enabled} onChange={(v) => set({ enabled: v })} label={tr("Backup active")} />
        <ErrorBox error={save.error || test.error} />
      </div>
    </Modal>
  );
}

export function HistoryModal({ b, onClose }: { b: Pick<BackupTask, "name" | "history">; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={tr("History — {0}", b.name)} wide>
      {b.history.length === 0 ? <Empty icon={<History />} title={tr("Never run")} /> : (
        <div className="space-y-2">
          {b.history.map((h, i) => (
            <div key={i} className="flex items-start gap-3 rounded-xl bg-panel-2 px-4 py-3">
              <RunIcon status={h.status} size={17} />
              <div className="min-w-0"><div className="text-[13px] break-words">{srv(h.message)}</div><div className="text-muted text-[11.5px]">{dateTime(h.started)} · {Math.max(1, Math.round(h.duration))} s{h.manual ? tr(" · manual") : ""}</div></div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

export default function Backups() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<BackupTask | null | undefined>(undefined);
  const [restore, setRestore] = useState<{ source?: string } | null>(null);
  const [preset, setPreset] = useState<Partial<BackupTask> | undefined>(undefined);
  const [hist, setHist] = useState<BackupTask | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["backups"], queryFn: () => api.get<BackupTask[]>("/api/backups"), refetchInterval: 8000 });
  const sources = useQuery({ queryKey: ["backup-sources"], queryFn: () => api.get<Sources>("/api/backups/sources") });
  const run = useAction((id: string) => api.post(`/api/backups/${id}/run`));
  const resync = useAction((id: string) => api.post(`/api/backups/${id}/resync`));
  const toggle = useAction((b: BackupTask) => api.put(`/api/backups/${b.id}`, { ...b, enabled: !b.enabled }));
  const del = useAction((id: string) => api.del(`/api/backups/${id}`), { success: tr("Backup deleted (backed-up data is kept)") });
  const stores = useDatastores();

  return (
    <div>
      <PageHeader title={tr("Backup")} subtitle={tr("Copies, versioned backups, replication and sync, to or from a disk, another NAS or the cloud")} icon={<DatabaseBackup size={20} />} actions={<><Button icon={<ArchiveRestore size={15} />} onClick={() => setRestore({})}>{tr("Restore…")}</Button><Button variant="primary" icon={<Plus size={15} />} disabled={!sources.data} onClick={() => { setPreset(undefined); setEdit(null); }}>{tr("New backup")}</Button></>} />
      <ProtectionCard sources={sources.data?.folders.filter((f) => f.label.startsWith("Share")).map((f) => f.path) ?? []} onPreset={(b) => { setPreset(b); setEdit(null); }} />
      <ErrorBox error={error || sources.error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<DatabaseBackup />} title={tr("No backups")} text={tr("RAID protects against a disk failure, not against deletion, ransomware or fire. Keep at least one copy elsewhere: USB disk, another NAS or the cloud.")} action={<Button variant="primary" disabled={!sources.data} onClick={() => setEdit(null)}>{tr("Create a backup")}</Button>} /></Card>
      ) : (
        <div className="@container"><div className="grid gap-4 @3xl:grid-cols-2">
          {data.map((b) => {
            const last = b.history[0];
            return (
              <Card key={b.id} pad={false} className={b.enabled ? "" : "opacity-70"}>
                <div className="p-5">
                  <div className="flex items-start gap-3">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent">{kinds[cardOf(b)].icon}</div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 font-semibold">{b.name}{b.running && <Badge tone="accent" dot>{tr("In progress")}</Badge>}</div>
                      <div className="text-muted text-[12px]">{kinds[cardOf(b)].label}{b.kind === "zfs" && b.sourceType ? ` · ${tr("pull")}` : ""}</div>
                    </div>
                    <Switch aria-label={tr("Enabled")} checked={b.enabled} onChange={() => toggle.mutate(b)} />
                  </div>
                  <div className="mt-3 space-y-1 text-[12.5px]">
                    <div className="truncate"><span className="text-muted">{tr("From")}{" "}</span><span className="mono">{srcLabel(b)}</span></div>
                    <div className="truncate"><span className="text-muted">{b.kind === "sync" ? tr("With") : tr("To")}{" "}</span><span className="mono">{destLabel(b, stores.data)}</span></div>
                    <div className="text-muted">{b.cron ? describeCron(b.cron) : tr("On demand")}{b.enabled && b.nextRun ? tr(" · next {0}", dateTime(b.nextRun)) : ""}</div>
                  </div>
                  {last && (
                    <button onClick={() => setHist(b)} className={cx("mt-3 flex w-full items-start gap-2 rounded-lg px-3 py-2 text-left text-[12.5px]", runTone(last.status))}>
                      <RunIcon status={last.status} />
                      <span className="min-w-0 break-words">{ago(last.started)} — {srv(last.message)}</span>
                    </button>
                  )}
                  <RestoreTestBadge t={b.restoreTest} />
                </div>
                <div className="flex flex-wrap justify-end gap-1 border-t border-line bg-panel-2/50 px-3 py-2">
                  {b.running && b.jobId ? <StopJobButton id={b.jobId} /> : <Button size="sm" variant="ghost" icon={<Play size={14} />} disabled={b.running} onClick={() => run.mutate(b.id)}>{tr("Run")}</Button>}
                  {(b.kind === "restic" || (b.kind === "rsync" && !b.sourceType && b.destType === "local")) && <Button size="sm" variant="ghost" icon={<RotateCcw size={14} />} onClick={() => setRestore({ source: `backup:${b.id}` })}>{tr("Restore")}</Button>}
                  {b.kind === "sync" && b.synced && <Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} disabled={b.running} onClick={async () => { if (await confirm({ title: tr("Resynchronise \"{0}\"?", b.name), text: tr("Both folders are merged again, as on the first run: nothing is deleted, and files deleted on one side since the last run come back.") })) resync.mutate(b.id); }}>{tr("Resync")}</Button>}
                  <IconButton title={tr("History")} onClick={() => setHist(b)}><History size={15} /></IconButton>
                  {!b.running && last?.status === "failed" && certChanged(last.message) && (b.destType === "s3" || b.destType === "rest") && <Button size="sm" variant="ghost" disabled={!sources.data} onClick={() => setEdit(b)}>{tr("Review the certificate")}</Button>}
                  <IconButton title={tr("Edit")} disabled={!sources.data} onClick={() => setEdit(b)}><Edit3 size={15} /></IconButton>
                  <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete \"{0}\"?", b.name), text: tr("The task will no longer run. Data already backed up is not deleted."), danger: true })) del.mutate(b.id); }}><Trash2 size={15} /></IconButton>
                </div>
              </Card>
            );
          })}
        </div></div>
      )}
      <DatastoresCard volumes={sources.data?.folders.filter((f) => f.label.startsWith("Volume") || f.label.startsWith("ZFS")) ?? []} />
      <TimeMachineCard volumes={sources.data?.folders.filter((f) => f.label.startsWith("Volume") || f.label.startsWith("ZFS")) ?? []} />
      <VaultsCard sources={sources.data} />
      {edit !== undefined && sources.data && <Editor initial={edit} preset={edit ? undefined : preset} sources={sources.data} onClose={() => setEdit(undefined)} />}
      {restore && <RestoreWizard initialSource={restore.source} onClose={() => setRestore(null)} />}
      {hist && <HistoryModal b={data?.find((x) => x.id === hist.id) ?? hist} onClose={() => setHist(null)} />}
    </div>
  );
}
