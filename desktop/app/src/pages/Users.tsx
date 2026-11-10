// Users and groups of the NAS: create, edit (role, groups, SSH and Docker
// rights, disabled, new password), reset two-factor, delete. Quotas,
// directory accounts and devices stay in the web interface.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { KeyRound, Pencil, Plus, ShieldCheck, Trash2, UserPlus } from "lucide-react";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Select, Spinner, Switch, Tabs, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import type { Group, User } from "@nas/lib/types";
import { api } from "../lib/nasapi";
import { useApp } from "../lib/state";
import { t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

type Form = { username: string; fullName: string; email: string; role: "admin" | "user"; password: string; groups: string[]; ssh: boolean; docker: boolean; disabled: boolean };

export function UsersPage() {
  const [tab, setTab] = useState<"users" | "groups">("users");
  return (
    <>
      <PageHeader title={t("Users and groups")} actions={<WebButton path="/users" title={t("Users")} />} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "users", label: t("Users") }, { id: "groups", label: t("Groups") }]} />
      <div className="mt-4">{tab === "users" ? <UserList /> : <GroupList />}</div>
    </>
  );
}

function UserList() {
  const { confirm } = useUI();
  const { info } = useApp();
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const [edit, setEdit] = useState<User | null | undefined>(undefined);
  const del = useAction((p: { name: string; home: boolean }) => api.del(`/api/users/${encodeURIComponent(p.name)}${p.home ? "?removeHome=1" : ""}`), { success: t("User deleted") });
  const reset2fa = useAction((n: string) => api.post(`/api/users/${encodeURIComponent(n)}/2fa/reset`), { success: t("Two-factor authentication reset") });
  return (
    <>
      <div className="mb-3 flex justify-end"><Button variant="primary" icon={<UserPlus size={15} />} onClick={() => setEdit(null)}>{t("New user")}</Button></div>
      <ErrorBox error={users.error} />
      <Card pad={false}>
        {users.isLoading ? <Spinner /> : !users.data?.length ? <Empty title={t("No users")} /> : (
          <table className="table">
            <thead><tr><th>{t("User")}</th><th>{t("Role")}</th><th>{t("Groups")}</th><th /></tr></thead>
            <tbody>
              {users.data.map((u) => (
                <tr key={u.username}>
                  <td>
                    <div className="flex items-center gap-2 font-medium">{u.username}{u.disabled && <Badge tone="warn">{t("Disabled")}</Badge>}{u.twoFactor && <ShieldCheck size={14} className="text-ok" />}</div>
                    <div className="text-[12px] text-muted">{u.fullName}{u.email ? ` · ${u.email}` : ""}</div>
                  </td>
                  <td>{u.role === "admin" ? <Badge tone="accent">{t("Administrator")}</Badge> : <Badge>{t("User")}</Badge>}</td>
                  <td className="text-[12.5px] text-muted">{u.groups.join(", ")}</td>
                  <td className="whitespace-nowrap text-right">
                    <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={t("Edit")} onClick={() => setEdit(u)} />
                    {u.twoFactor && <Button size="sm" variant="ghost" icon={<KeyRound size={14} />} title={t("Reset two-factor authentication")} onClick={async () => {
                      if (await confirm({ title: t("Reset two-factor authentication"), text: t("{0} signs in with the password only until a new code is set up.", u.username), danger: true })) reset2fa.mutate(u.username);
                    }} />}
                    {u.username !== info?.user?.username && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={t("Delete")} onClick={async () => {
                      if (await confirm({ title: t("Delete {0}?", u.username), text: t("The account is deleted; its home folder is kept."), danger: true, typeToConfirm: u.username })) del.mutate({ name: u.username, home: false });
                    }} />}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {edit !== undefined && <UserModal user={edit} groups={groups.data ?? []} onClose={() => setEdit(undefined)} />}
    </>
  );
}

function UserModal({ user, groups, onClose }: { user: User | null; groups: Group[]; onClose: () => void }) {
  const editing = !!user;
  const [f, setF] = useState<Form>(user ? { ...user, password: "" } : { username: "", fullName: "", email: "", role: "user", password: "", groups: [], ssh: false, docker: false, disabled: false });
  const save = useAction(() => (editing ? api.put(`/api/users/${encodeURIComponent(user!.username)}`, f) : api.post("/api/users", f)), { success: editing ? t("User updated") : t("User created"), onDone: onClose });
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => ({ ...p, [k]: v }));
  return (
    <Modal open onClose={onClose} title={editing ? t("Edit {0}", user!.username) : t("New user")} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!f.username || (!editing && f.password.length < 8) || (editing && f.password.length > 0 && f.password.length < 8)} onClick={() => save.mutate()}>{editing ? t("Save") : t("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("Username")}><Input value={f.username} disabled={editing} onChange={(e) => set("username", e.target.value.toLowerCase())} /></Field>
          <Field label={t("Full name")}><Input value={f.fullName} onChange={(e) => set("fullName", e.target.value)} /></Field>
          <Field label="E-mail"><Input type="email" value={f.email} onChange={(e) => set("email", e.target.value)} /></Field>
          <Field label={t("Role")}>
            <Select value={f.role} onChange={(e) => set("role", e.target.value as Form["role"])}>
              <option value="user">{t("User")}</option>
              <option value="admin">{t("Administrator")}</option>
            </Select>
          </Field>
          <Field label={editing ? t("New password (optional)") : t("Password")} hint={t("At least 8 characters")}><Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => set("password", e.target.value)} /></Field>
        </div>
        {groups.some((g) => !g.system) && (
          <Field label={t("Groups")}>
            <div className="flex flex-wrap gap-3">{groups.filter((g) => !g.system).map((g) => (
              <Checkbox key={g.name} label={g.name} checked={f.groups.includes(g.name)} onChange={(v) => set("groups", v ? [...f.groups, g.name] : f.groups.filter((x) => x !== g.name))} />
            ))}</div>
          </Field>
        )}
        <div className="grid gap-2 sm:grid-cols-3">
          <Switch checked={f.ssh} onChange={(v) => set("ssh", v)} label={t("SSH access")} />
          <Switch checked={f.docker} onChange={(v) => set("docker", v)} label={t("Docker access")} />
          {editing && <Switch checked={f.disabled} onChange={(v) => set("disabled", v)} label={t("Disabled")} />}
        </div>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function GroupList() {
  const { confirm } = useUI();
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const [edit, setEdit] = useState<Group | null | undefined>(undefined);
  const del = useAction((n: string) => api.del(`/api/groups/${encodeURIComponent(n)}`), { success: t("Group deleted") });
  const list = (groups.data ?? []).filter((g) => !g.system);
  return (
    <>
      <div className="mb-3 flex justify-end"><Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{t("New group")}</Button></div>
      <ErrorBox error={groups.error} />
      <Card pad={false}>
        {groups.isLoading ? <Spinner /> : !list.length ? <Empty title={t("No groups")} /> : (
          <table className="table">
            <thead><tr><th>{t("Group")}</th><th>{t("Members")}</th><th /></tr></thead>
            <tbody>{list.map((g) => (
              <tr key={g.name}>
                <td className="font-medium">{g.name}</td>
                <td className="text-[12.5px] text-muted">{g.members.join(", ") || "—"}</td>
                <td className="whitespace-nowrap text-right">
                  <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={t("Edit")} onClick={() => setEdit(g)} />
                  <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={t("Delete")} onClick={async () => { if (await confirm({ title: t("Delete {0}?", g.name), text: t("Members keep their accounts; the rights given to the group are removed."), danger: true })) del.mutate(g.name); }} />
                </td>
              </tr>
            ))}</tbody>
          </table>
        )}
      </Card>
      {edit !== undefined && <GroupModal group={edit} users={users.data ?? []} onClose={() => setEdit(undefined)} />}
    </>
  );
}

function GroupModal({ group, users, onClose }: { group: Group | null; users: User[]; onClose: () => void }) {
  const [name, setName] = useState(group?.name ?? "");
  const [members, setMembers] = useState<string[]>(group?.members ?? []);
  const save = useAction(() => (group ? api.put(`/api/groups/${encodeURIComponent(group.name)}`, { members }) : api.post("/api/groups", { name, members })), { success: t("Group saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={group ? t("Edit {0}", group.name) : t("New group")} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!name.trim()} onClick={() => save.mutate()}>{t("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={t("Name")}><Input value={name} disabled={!!group} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <Field label={t("Members")}>
          <div className="flex flex-wrap gap-3">{users.map((u) => (
            <Checkbox key={u.username} label={u.username} checked={members.includes(u.username)} onChange={(v) => setMembers(v ? [...members, u.username] : members.filter((x) => x !== u.username))} />
          ))}</div>
        </Field>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}
