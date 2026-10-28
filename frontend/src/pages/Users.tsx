import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Edit3, FingerprintPattern, Plus, Shield, ShieldOff, Trash2, UserPlus, Users as UsersIcon } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Group, Me, User, UserQuota } from "../lib/types";
import { bytes, dateTime } from "../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, Tabs, useUI } from "../components/ui";
import { tr } from "../lib/i18n";
import { AdminDevices } from "../components/AdminDevices";

type Form = { source?: string; username: string; fullName: string; email: string; role: "admin" | "user"; password: string; groups: string[]; ssh: boolean; docker: boolean; disabled: boolean };

function UserModal({ user, groups, onClose }: { user: User | null; groups: Group[]; onClose: () => void }) {
  const editing = !!user;
  const [f, setF] = useState<Form>(user ? { ...user, password: "" } : { username: "", fullName: "", email: "", role: "user", password: "", groups: [], ssh: false, docker: false, disabled: false });
  const save = useAction(() => (editing ? api.put(`/api/users/${encodeURIComponent(user!.username)}`, f) : api.post("/api/users", f)), { success: editing ? tr("User updated") : tr("User created"), onDone: onClose });
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => ({ ...p, [k]: v }));
  return (
    <Modal open onClose={onClose} title={editing ? tr("Edit {0}", user!.username) : tr("New user")} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!f.username || (!editing && f.password.length < 8)} onClick={() => save.mutate()}>{editing ? tr("Save") : tr("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Username")}><Input value={f.username} disabled={editing} onChange={(e) => set("username", e.target.value.toLowerCase())} /></Field>
          <Field label={tr("Full name")}><Input value={f.fullName} onChange={(e) => set("fullName", e.target.value)} /></Field>
          <Field label="E-mail"><Input type="email" value={f.email} onChange={(e) => set("email", e.target.value)} /></Field>
          <Field label={tr("Role")}>
            <Select value={f.role} onChange={(e) => set("role", e.target.value as Form["role"])}>
              <option value="user">{tr("User")}</option>
              <option value="admin">{tr("Administrator")}</option>
            </Select>
          </Field>
        </div>
        {f.source ? <Notice>{tr("Directory account: its password and groups are managed in the directory (Active Directory / LDAP).")}</Notice> : <>
        <Field label={editing ? tr("New password (leave empty to keep unchanged)") : tr("Password")} hint={tr("Used for the web interface, SMB and SSH. 8 characters minimum.")}>
          <Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => set("password", e.target.value)} />
        </Field>
        {groups.length > 0 && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Groups")}</div>
            <div className="flex flex-wrap gap-3">
              {groups.map((g) => <Checkbox key={g.name} checked={f.groups.includes(g.name)} onChange={(v) => set("groups", v ? [...f.groups, g.name] : f.groups.filter((x) => x !== g.name))} label={g.name} />)}
            </div>
          </div>
        )}
        </>}
        {editing && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Disk space quotas")}</div>
            <QuotaEditor user={user!.username} />
          </div>
        )}
        <div className="space-y-3 rounded-xl border border-line p-4">
          {!f.source && <Switch checked={f.ssh} onChange={(v) => set("ssh", v)} label={tr("SSH access")} hint={tr("/bin/bash shell instead of nologin")} />}
          {!f.source && <Switch checked={f.docker} onChange={(v) => set("docker", v)} label={tr("Docker access")} hint={tr("Member of the docker group (root equivalent!)")} />}
          {editing && <Switch checked={f.disabled} onChange={(v) => set("disabled", v)} label={tr("Account disabled")} />}
        </div>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function QuotaEditor({ user }: { user: string }) {
  const { data, isLoading } = useQuery({ queryKey: ["user-quotas", user], queryFn: () => api.get<UserQuota[]>(`/api/users/${encodeURIComponent(user)}/quotas`) });
  const [edit, setEdit] = useState<Record<string, string>>({});
  const save = useAction((q: UserQuota) => api.put(`/api/users/${encodeURIComponent(user)}/quotas`, { kind: q.kind, target: q.target, limit: edit[q.kind + q.target] ? Math.round(parseFloat(edit[q.kind + q.target].replace(",", ".")) * 1024 ** 3) : 0 }), { success: tr("Quota saved") });
  if (isLoading) return <Spinner />;
  if (!data?.length) return <div className="text-muted text-[12.5px]">{tr("No volumes.")}</div>;
  return (
    <div className="divide-y divide-line rounded-xl border border-line">
      {data.map((q) => {
        const key = q.kind + q.target;
        return (
          <div key={key} className="flex flex-wrap items-center gap-3 px-3 py-2.5 text-[13px]">
            <div className="min-w-[140px] flex-1">
              <div className="font-medium">{q.name} <span className="text-muted text-[11.5px]">{q.kind}</span></div>
              {q.supported && q.enabled && <div className="text-muted text-[11.5px]">{bytes(q.used)} {" "}{tr("used")}{q.limit ? tr(" on {0}", bytes(q.limit)) : ""}</div>}
            </div>
            {!q.supported ? <span className="text-muted text-[12px]">{tr("No per-user quota on")}{" "}{q.kind}</span>
              : !q.enabled ? <span className="text-muted text-[12px]">{tr("Quotas disabled (Storage Manager → Volumes)")}</span>
              : (
                <div className="flex items-center gap-2">
                  <Input className="!w-24 !py-1" placeholder={q.limit ? String(+(q.limit / 1024 ** 3).toFixed(2)) : tr("unlimited")} value={edit[key] ?? ""} onChange={(e) => setEdit({ ...edit, [key]: e.target.value })} />
                  <span className="text-muted text-[12px]">{tr("GB")}</span>
                  <Button size="sm" loading={save.isPending} onClick={() => save.mutate(q)}>{edit[key] ? tr("Apply") : tr("Unlimited")}</Button>
                </div>
              )}
          </div>
        );
      })}
    </div>
  );
}

function GroupModal({ group, users, onClose }: { group: Group | null; users: User[]; onClose: () => void }) {
  const [name, setName] = useState(group?.name ?? "");
  const [members, setMembers] = useState<string[]>(group?.members ?? []);
  const save = useAction(() => (group ? api.put(`/api/groups/${group.name}`, { members }) : api.post("/api/groups", { name, members })), { success: tr("Group saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={group ? tr("Group {0}", group.name) : tr("New group")} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" disabled={!name} loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Name")}><Input value={name} disabled={!!group} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Members")}</div>
          <div className="grid gap-2 sm:grid-cols-2">
            {users.map((u) => <Checkbox key={u.username} checked={members.includes(u.username)} onChange={(v) => setMembers(v ? [...members, u.username] : members.filter((x) => x !== u.username))} label={u.username} />)}
          </div>
        </div>
      </div>
    </Modal>
  );
}

export default function Users({ me }: { me: Me }) {
  const { confirm } = useUI();
  const [tab, setTab] = useState<"users" | "groups" | "devices">("users");
  const [editUser, setEditUser] = useState<User | null | undefined>(undefined);
  const [editGroup, setEditGroup] = useState<Group | null | undefined>(undefined);
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const delUser = useAction((p: { name: string; home: boolean }) => api.del(`/api/users/${encodeURIComponent(p.name)}${p.home ? "?removeHome=1" : ""}`), { success: tr("User deleted") });
  const reset2fa = useAction((n: string) => api.post(`/api/users/${encodeURIComponent(n)}/2fa/reset`), { success: tr("Two-factor authentication reset") });
  const delPasskeys = useAction((n: string) => api.del(`/api/users/${encodeURIComponent(n)}/passkeys`), { success: tr("Passkeys removed") });
  const delGroup = useAction((n: string) => api.del(`/api/groups/${n}`), { success: tr("Group deleted") });
  const userGroups = (groups.data ?? []).filter((g) => !g.system);

  return (
    <div>
      <PageHeader title={tr("Users and groups")} subtitle={tr("NAS accounts (web interface, SMB, NFS, SSH)")} icon={<UsersIcon size={20} />} actions={tab === "devices" ? undefined : tab === "users"
        ? <Button variant="primary" icon={<UserPlus size={15} />} onClick={() => setEditUser(null)}>{tr("New user")}</Button>
        : <Button variant="primary" icon={<Plus size={15} />} onClick={() => setEditGroup(null)}>{tr("New group")}</Button>} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "users", label: tr("Users ({0})", users.data?.length ?? 0) }, { id: "groups", label: tr("Groups ({0})", userGroups.length) }, { id: "devices", label: tr("Devices") }]} />
      <ErrorBox error={users.error || groups.error} />
      {tab === "devices" ? <AdminDevices /> : tab === "users" ? (
        <Card pad={false}>
          {users.isLoading ? <Spinner /> : (
            <div className="overflow-x-auto">
              <table className="table">
                <thead><tr><th>{tr("User")}</th><th>{tr("Role")}</th><th>{tr("Groups")}</th><th>{tr("Access")}</th><th>{tr("Created on")}</th><th /></tr></thead>
                <tbody>
                  {users.data?.map((u) => (
                    <tr key={u.username} className={u.disabled ? "opacity-60" : ""}>
                      <td>
                        <div className="flex items-center gap-3">
                          <span className="flex h-8 w-8 items-center justify-center rounded-full bg-gradient-to-br from-accent to-accent-2 text-[12px] font-semibold text-white">{u.username[0].toUpperCase()}</span>
                          <div><div className="font-medium">{u.username}{u.username === me.username && <span className="text-muted"> (vous)</span>}</div><div className="text-muted text-[12px]">{u.fullName || u.email}</div></div>
                        </div>
                      </td>
                      <td>{u.role === "admin" ? <Badge tone="accent"><Shield size={11} /> {" "}{tr("Admin")}</Badge> : <Badge>{tr("User")}</Badge>}{u.disabled && <span className="ml-1"><Badge tone="danger">{tr("Disabled")}</Badge></span>}</td>
                      <td className="text-[12.5px]">{u.groups.join(", ") || <span className="text-muted">—</span>}</td>
                      <td className="space-x-1">{u.source && <Badge tone="accent">{u.source === "ad" ? "Active Directory" : "LDAP"}</Badge>}{u.ssh && <Badge>SSH</Badge>}{u.docker && <Badge>{tr("Docker")}</Badge>}<Badge>SMB</Badge>{u.twoFactor && <Badge tone="ok">2FA</Badge>}{!!u.passkeys && <Badge tone="ok">{u.passkeys === 1 ? tr("1 passkey") : tr("{0} passkeys", u.passkeys)}</Badge>}</td>
                      <td className="text-muted text-[12.5px]">{dateTime(u.createdAt)}</td>
                      <td className="text-right whitespace-nowrap">
                        <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEditUser(u)} />
                        {u.twoFactor && u.username !== me.username && <Button size="sm" variant="ghost" title={tr("Reset two-factor authentication")} icon={<ShieldOff size={14} />} onClick={async () => { if (await confirm({ title: tr("Reset two-factor authentication for {0}?", u.username), text: tr("Use if the user has lost their phone and backup codes. They will be able to sign in with their password alone and then set it up again."), danger: true })) reset2fa.mutate(u.username); }} />}
                        {!!u.passkeys && <Button size="sm" variant="ghost" title={tr("Remove passkeys")} icon={<FingerprintPattern size={14} />} onClick={async () => { if (await confirm({ title: tr("Remove the passkeys of {0}?", u.username), text: tr("Use if a device was lost or stolen. The user signs in with their password (and two-factor code) and can add passkeys again."), danger: true })) delPasskeys.mutate(u.username); }} />}
                        {u.username !== me.username && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => {
                          if (await confirm({ title: tr("Delete {0}?", u.username), text: tr("The Linux and Samba account will be deleted. Its home folder too."), danger: true, typeToConfirm: u.username })) delUser.mutate({ name: u.username, home: true });
                        }} />}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      ) : (
        <Card pad={false}>
          {groups.isLoading ? <Spinner /> : userGroups.length === 0 ? <Empty icon={<UsersIcon />} title={tr("No groups")} text={tr("Groups simplify permission management on shares.")} /> : (
            <table className="table">
              <thead><tr><th>{tr("Group")}</th><th>GID</th><th>{tr("Members")}</th><th /></tr></thead>
              <tbody>
                {userGroups.map((g) => (
                  <tr key={g.name}>
                    <td className="font-medium">{g.name}</td><td className="mono text-muted">{g.gid}</td><td className="text-[12.5px]">{g.members.join(", ") || "—"}</td>
                    <td className="text-right">
                      <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEditGroup(g)} />
                      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete group {0}?", g.name), text: tr("Their permissions on shares will be removed."), danger: true })) delGroup.mutate(g.name); }} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      )}
      {editUser !== undefined && <UserModal user={editUser} groups={userGroups} onClose={() => setEditUser(undefined)} />}
      {editGroup !== undefined && <GroupModal group={editGroup} users={users.data ?? []} onClose={() => setEditGroup(undefined)} />}
    </div>
  );
}
