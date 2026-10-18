import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Edit3, Lock, Monitor, Plus, Share2, Trash2, Unlock, User as UserIcon, Users as UsersIcon } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Group, Perm, Share, User } from "../lib/types";
import { bytes, cx } from "../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Switch, Tabs, useUI } from "../components/ui";
import { tr } from "../lib/i18n";

const empty: Share = { name: "", path: "", description: "", smb: true, nfs: false, guest: false, browseable: true, recycleBin: true, timeMachine: false, permissions: {}, nfsClients: "", nfsOptions: "" };

/** Rights of domain (Active Directory / LDAP) users and groups, with a search box. */
function DomainPerms({ perms, local, setPerm }: { perms: Record<string, Perm>; local: Set<string>; setPerm: (who: string, p?: Perm) => void }) {
  const dir = useQuery({ queryKey: ["directory"], queryFn: () => api.get<{ joined: boolean; config: { type: string; workgroup: string } }>("/api/directory") });
  const [q, setQ] = useState("");
  const search = useQuery({ queryKey: ["directory-search", q], queryFn: () => api.get<{ users: string[]; groups: string[] }>(`/api/directory/search?q=${encodeURIComponent(q)}`), enabled: !!dir.data?.joined && q.length >= 2 });
  const extra = Object.keys(perms).filter((k) => !local.has(k));
  if (!dir.data?.joined && extra.length === 0) return null;
  const results = [...(search.data?.users ?? []), ...(search.data?.groups ?? []).map((g) => "@" + g)].filter((n) => !(n in perms)).slice(0, 12);
  return (
    <div className="mt-3 rounded-xl border border-line">
      <div className="border-b border-line px-4 py-2.5 text-[12.5px] font-medium text-muted">{tr("Directory accounts")}{" "}{dir.data?.joined ? `(${dir.data.config.type === "ad" ? dir.data.config.workgroup : "LDAP"})` : tr("(directory left)")}</div>
      {extra.map((who) => (
        <div key={who} className="flex items-center justify-between gap-3 px-4 py-2.5">
          <span className="flex items-center gap-2">{who.startsWith("@") ? <UsersIcon size={15} className="text-accent" /> : <UserIcon size={15} className="text-muted" />}{who}</span>
          <PermPicker value={perms[who]} onChange={(p) => setPerm(who, p)} />
        </div>
      ))}
      {dir.data?.joined && (
        <div className="space-y-2 px-4 py-3">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={tr("Search for a domain user or group…")} />
          {results.map((n) => (
            <button key={n} type="button" onClick={() => { setPerm(n, "ro"); setQ(""); }} className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-[13px] hover:bg-panel-2">
              {n.startsWith("@") ? <UsersIcon size={14} className="text-accent" /> : <UserIcon size={14} className="text-muted" />}{n}<span className="text-muted ml-auto text-[11.5px]">ajouter</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function PermPicker({ value, onChange }: { value?: Perm; onChange: (p?: Perm) => void }) {
  const opts: { v?: Perm; label: string; cls: string }[] = [
    { v: "rw", label: tr("Read/write"), cls: "bg-ok text-white" },
    { v: "ro", label: tr("Read"), cls: "bg-accent text-white" },
    { v: "none", label: tr("Denied"), cls: "bg-danger text-white" },
    { v: undefined, label: "—", cls: "bg-line text-fg" },
  ];
  return (
    <div className="inline-flex rounded-lg border border-line bg-panel-2 p-0.5">
      {opts.map((o) => (
        <button key={o.label} type="button" onClick={() => onChange(o.v)} className={cx("rounded-md px-2 py-1 text-[11.5px] transition", value === o.v ? o.cls : "text-muted hover:text-fg")}>{o.label}</button>
      ))}
    </div>
  );
}

function UnlockModal({ name, onClose }: { name: string; onClose: () => void }) {
  const [secret, setSecret] = useState("");
  const [recovery, setRecovery] = useState(false);
  const unlock = useAction(() => api.post(`/api/shares/${encodeURIComponent(name)}/unlock`, recovery ? { recoveryKey: secret } : { passphrase: secret }), { success: tr("Folder unlocked"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Unlock {0}", name)} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!secret} loading={unlock.isPending} onClick={() => unlock.mutate()}>{tr("Unlock")}</Button></>}>
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); if (secret) unlock.mutate(); }}>
        <Field label={recovery ? tr("Recovery key") : tr("Passphrase")}><Input autoFocus type={recovery ? "text" : "password"} className={recovery ? "mono" : ""} value={secret} onChange={(e) => setSecret(e.target.value)} /></Field>
        <button type="button" className="text-accent text-[12.5px]" onClick={() => { setRecovery(!recovery); setSecret(""); }}>{recovery ? tr("Use the passphrase") : tr("Forgot the passphrase? Use the recovery key")}</button>
        <ErrorBox error={unlock.error} />
      </form>
    </Modal>
  );
}

function ShareModal({ initial, onClose }: { initial: Share | null; onClose: () => void }) {
  const editing = !!initial?.name;
  const [s, setS] = useState<Share>(initial ?? empty);
  const locations = useQuery({ queryKey: ["locations"], queryFn: () => api.get<{ path: string; label: string; free: number }[]>("/api/storage/locations") });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const [base, setBase] = useState(() => (initial?.path ? "" : ""));
  const [pass, setPass] = useState({ a: "", b: "" });
  const [recovery, setRecovery] = useState("");
  const save = useAction(() => (editing ? api.put(`/api/shares/${encodeURIComponent(s.name)}`, s) : api.post<{ recoveryKey?: string }>("/api/shares", { ...s, passphrase: s.encrypted ? pass.a : undefined })), {
    success: editing ? tr("Share updated") : tr("Share created"),
    onDone: (r) => { const k = (r as { recoveryKey?: string } | null)?.recoveryKey; if (k) setRecovery(k); else onClose(); },
  });
  const passOK = !s.encrypted || editing || (pass.a.length >= 8 && pass.a === pass.b);
  if (recovery) {
    return (
      <Modal open onClose={onClose} title={tr("Recovery key of the encrypted folder")} footer={<Button variant="primary" onClick={onClose}>{tr("I have noted it")}</Button>}>
        <div className="space-y-3">
          <Notice tone="warn">{tr("Write down this key and keep it somewhere safe: it is the only way to open the folder if the passphrase is forgotten. It will never be shown again.")}</Notice>
          <div className="mono break-all rounded-lg bg-panel-2 p-3 text-[14px] tracking-wide">{recovery}</div>
          <Button size="sm" onClick={() => navigator.clipboard?.writeText(recovery)}>{tr("Copy")}</Button>
        </div>
      </Modal>
    );
  }
  const set = <K extends keyof Share>(k: K, v: Share[K]) => setS((p) => ({ ...p, [k]: v }));
  const setPerm = (who: string, p?: Perm) => setS((x) => { const perms = { ...x.permissions }; if (p) perms[who] = p; else delete perms[who]; return { ...x, permissions: perms }; });

  const pickBase = (b: string) => {
    setBase(b);
    if (b) set("path", `${b}/${s.name || "partage"}`);
  };

  return (
    <Modal open onClose={onClose} title={editing ? tr("Edit \"{0}\"", s.name) : tr("New shared folder")} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!s.name || !s.path || !passOK} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Share name")}><Input value={s.name} disabled={editing} onChange={(e) => { const n = e.target.value; setS((p) => ({ ...p, name: n, path: base ? `${base}/${n}` : p.path })); }} /></Field>
          <Field label={tr("Description")}><Input value={s.description} onChange={(e) => set("description", e.target.value)} /></Field>
        </div>
        {!editing && (
          <Field label={tr("Location")} hint={locations.data?.length === 0 ? tr("First create a volume or a ZFS pool.") : undefined}>
            <Select value={base} onChange={(e) => pickBase(e.target.value)}>
              <option value="">{tr("Choose a volume…")}</option>
              {locations.data?.map((l) => <option key={l.path} value={l.path}>{l.label} — {bytes(l.free)} libres</option>)}
            </Select>
          </Field>
        )}
        <Field label={tr("Folder path")} hint={tr("Created automatically if it does not exist.")}><Input className="mono" value={s.path} onChange={(e) => set("path", e.target.value)} /></Field>

        {!editing && (
          <div className="space-y-3 rounded-xl border border-line p-4">
            <Switch checked={!!s.encrypted} onChange={(v) => setS((p) => ({ ...p, encrypted: v, nfs: v ? false : p.nfs }))} label={tr("Encrypt this folder")} hint={tr("Files are stored encrypted; after each startup, the folder stays closed until an administrator enters the passphrase. Useful if the NAS or its disks are stolen.")} />
            {s.encrypted && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label={tr("Passphrase")} hint={tr("8 characters minimum, never stored on the NAS")}><Input type="password" autoComplete="new-password" value={pass.a} onChange={(e) => setPass({ ...pass, a: e.target.value })} /></Field>
                <Field label={tr("Confirmation")}><Input type="password" autoComplete="new-password" value={pass.b} onChange={(e) => setPass({ ...pass, b: e.target.value })} /></Field>
              </div>
            )}
          </div>
        )}
        <div className="grid gap-4 rounded-xl border border-line p-4 sm:grid-cols-2">
          <Switch checked={s.smb} onChange={(v) => set("smb", v)} label={tr("SMB / CIFS")} hint={tr("Windows, macOS, Linux")} />
          <Switch checked={s.nfs} disabled={!!s.encrypted} onChange={(v) => set("nfs", v)} label="NFS" hint={s.encrypted ? tr("Not available for an encrypted folder") : tr("Linux, hypervisors")} />
          <Switch checked={s.browseable} onChange={(v) => set("browseable", v)} label={tr("Visible on the network")} />
          <Switch checked={s.recycleBin} onChange={(v) => set("recycleBin", v)} label={tr("Network recycle bin")} hint={tr("Deleted files kept in .recycle")} />
          <Switch checked={s.timeMachine} onChange={(v) => set("timeMachine", v)} label={tr("Time Machine target")} hint={tr("macOS backup")} />
          <Switch checked={s.guest} onChange={(v) => set("guest", v)} label={tr("Guest access")} hint={tr("Read/write without password")} />
        </div>

        <div className="grid gap-4 rounded-xl border border-line p-4 sm:grid-cols-2">
          <Field label={tr("Quota (GB)")} hint={tr("0 = unlimited. Alert at 90% and 100%, measured every 30 minutes")}><Input type="number" min={0} step="1" value={s.quotaGb ?? 0} onChange={(e) => set("quotaGb", Math.max(0, +e.target.value))} /></Field>
          <div className="pt-6"><Switch checked={!!s.quotaBlock} disabled={!s.quotaGb} onChange={(v) => set("quotaBlock", v)} label={tr("Read-only when the quota is reached")} hint={tr("For all protocols, until space is freed")} /></div>
        </div>

        {s.nfs && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Allowed NFS clients")} hint={tr("e.g. 192.168.1.0/24 10.0.0.5 — empty = all")}><Input value={s.nfsClients} onChange={(e) => set("nfsClients", e.target.value)} /></Field>
            <Field label={tr("NFS options")} hint={tr("Default: rw,sync,no_subtree_check,root_squash")}><Input className="mono" value={s.nfsOptions} onChange={(e) => set("nfsOptions", e.target.value)} /></Field>
          </div>
        )}

        {!s.guest && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Permissions")}</div>
            {users.isLoading ? <Spinner /> : (
              <div className="divide-y divide-line rounded-xl border border-line">
                {users.data?.map((u) => (
                  <div key={u.username} className="flex items-center justify-between gap-3 px-4 py-2.5">
                    <span className="flex items-center gap-2"><UserIcon size={15} className="text-muted" />{u.username}{u.fullName && <span className="text-muted text-[12px]">{u.fullName}</span>}</span>
                    <PermPicker value={s.permissions[u.username]} onChange={(p) => setPerm(u.username, p)} />
                  </div>
                ))}
                {groups.data?.map((g) => (
                  <div key={g.name} className="flex items-center justify-between gap-3 px-4 py-2.5">
                    <span className="flex items-center gap-2"><UsersIcon size={15} className="text-accent" />@{g.name}<span className="text-muted text-[12px]">{tr("group ·")}{" "}{g.members.length} membre(s)</span></span>
                    <PermPicker value={s.permissions["@" + g.name]} onChange={(p) => setPerm("@" + g.name, p)} />
                  </div>
                ))}
              </div>
            )}
            <DomainPerms perms={s.permissions} local={new Set([...(users.data ?? []).map((u) => u.username), ...(groups.data ?? []).map((g) => "@" + g.name)])} setPerm={setPerm} />
            {Object.keys(s.permissions).length === 0 && <div className="mt-2"><Notice>{tr("Without explicit permission, all StrataOS users have read-only access.")}</Notice></div>}
          </div>
        )}
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

export default function Shares() {
  const { confirm } = useUI();
  const [tab, setTab] = useState<"shares" | "sessions">("shares");
  const [edit, setEdit] = useState<Share | null | undefined>(undefined);
  const [unlocking, setUnlocking] = useState<string | null>(null);
  const lock = useAction((n: string) => api.post(`/api/shares/${encodeURIComponent(n)}/lock`), { success: tr("Folder locked") });
  const { data, isLoading, error } = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const sessions = useQuery({ queryKey: ["smb-sessions"], queryFn: () => api.get<{ pid: string; username: string; machine: string; protocol: string }[]>("/api/shares/sessions"), enabled: tab === "sessions", refetchInterval: 5000 });
  const del = useAction((n: string) => api.del(`/api/shares/${encodeURIComponent(n)}`), { success: tr("Share deleted (files kept)") });
  const host = location.hostname;

  return (
    <div>
      <PageHeader title={tr("Shared folders")} subtitle={tr("SMB and NFS shares, access rights per user and group")} icon={<Share2 size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{tr("New share")}</Button>} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "shares", label: tr("Shares") }, { id: "sessions", label: tr("Active connections") }]} />
      <ErrorBox error={error} />
      {tab === "shares" ? (
        isLoading ? <Spinner /> : !data?.length ? (
          <Card><Empty icon={<Share2 />} title={tr("No shares")} text={tr("Create a shared folder to access it from your computers, phones or TVs.")} action={<Button variant="primary" onClick={() => setEdit(null)}>{tr("Create a share")}</Button>} /></Card>
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {data.map((s) => (
              <Card key={s.name} pad={false}>
                <div className="p-5">
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex min-w-0 items-center gap-3">
                      <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-accent-soft text-accent"><Share2 size={18} /></div>
                      <div className="min-w-0">
                        <div className="truncate font-semibold">{s.name}</div>
                        <div className="truncate text-muted text-[12px] mono">{s.path}</div>
                      </div>
                    </div>
                  </div>
                  {s.description && <p className="mt-3 text-[13px] text-muted">{s.description}</p>}
                  <div className="mt-3 flex flex-wrap gap-1.5">
                    {s.encrypted && <Badge tone={s.locked ? "warn" : "ok"}>{s.locked ? tr("Encrypted · locked") : tr("Encrypted · unlocked")}</Badge>}
                    {s.smb && <Badge tone="accent">SMB</Badge>}
                    {s.nfs && <Badge tone="accent">NFS</Badge>}
                    {s.guest && <Badge tone="warn">{tr("Guest")}</Badge>}
                    {s.recycleBin && <Badge>{tr("Recycle bin")}</Badge>}
                    {s.timeMachine && <Badge>{tr("Time Machine")}</Badge>}
                    {!s.browseable && <Badge>{tr("Hidden")}</Badge>}
                    {s.quotaFull && <Badge tone="danger">{tr("Quota reached: read-only")}</Badge>}
                    {s.frozen && <Badge tone="danger">{tr("Trap file touched: read-only")}</Badge>}
                  </div>
                  {!!s.quotaGb && (
                    <div className="mt-3">
                      <div className="mb-1 flex justify-between text-[12px]"><span className="text-muted">{tr("Quota")}</span><span>{bytes(s.used ?? 0)} / {s.quotaGb} Go</span></div>
                      <Progress value={Math.min(100, ((s.used ?? 0) / (s.quotaGb * 1e9)) * 100)} tone={(s.used ?? 0) >= s.quotaGb * 1e9 ? "danger" : (s.used ?? 0) >= s.quotaGb * 0.9e9 ? "warn" : "ok"} />
                    </div>
                  )}
                  <div className="mt-3 text-[12px] text-muted">
                    {Object.entries(s.permissions).length === 0 ? tr("Read-only for all users") : Object.entries(s.permissions).map(([k, v]) => `${k} (${v})`).join(", ")}
                  </div>
                  {s.smb && <div className="mono mt-3 rounded-lg bg-panel-2 px-2.5 py-1.5 text-[11.5px] text-muted">\\{host}\{s.name}</div>}
                </div>
                <div className="flex justify-end gap-1 border-t border-line bg-panel-2/50 px-4 py-2.5">
                  {s.encrypted && (s.locked
                    ? <Button size="sm" variant="primary" icon={<Unlock size={14} />} onClick={() => setUnlocking(s.name)}>{tr("Unlock")}</Button>
                    : <Button size="sm" variant="ghost" icon={<Lock size={14} />} onClick={async () => { if (await confirm({ title: tr("Lock {0}?", s.name), text: tr("Share connections are closed and files are unreadable until the next unlock.") })) lock.mutate(s.name); }}>{tr("Lock")}</Button>)}
                  <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEdit(s)}>{tr("Edit")}</Button>
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete shared folder {0}?", s.name), text: tr("The share will no longer be accessible. Files remain on the volume."), danger: true })) del.mutate(s.name); }} />
                </div>
              </Card>
            ))}
          </div>
        )
      ) : (
        <Card pad={false}>
          {sessions.isLoading ? <Spinner /> : !sessions.data?.length ? <Empty icon={<Monitor />} title={tr("No active SMB connections")} /> : (
            <table className="table">
              <thead><tr><th>{tr("User")}</th><th>{tr("Machine")}</th><th>{tr("Protocol")}</th><th>PID</th></tr></thead>
              <tbody>{sessions.data.map((x) => <tr key={x.pid}><td>{x.username}</td><td className="mono">{x.machine}</td><td>{x.protocol}</td><td className="mono">{x.pid}</td></tr>)}</tbody>
            </table>
          )}
        </Card>
      )}
      {edit !== undefined && <ShareModal initial={edit} onClose={() => setEdit(undefined)} />}
      {unlocking && <UnlockModal name={unlocking} onClose={() => setUnlocking(null)} />}
    </div>
  );
}
