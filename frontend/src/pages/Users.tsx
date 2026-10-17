import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Edit3, Plus, Shield, ShieldOff, Trash2, UserPlus, Users as UsersIcon } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Group, Me, User, UserQuota } from "../lib/types";
import { bytes, dateTime } from "../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, PageHeader, Select, Spinner, Switch, Tabs, useUI } from "../components/ui";

type Form = { username: string; fullName: string; email: string; role: "admin" | "user"; password: string; groups: string[]; ssh: boolean; docker: boolean; disabled: boolean };

function UserModal({ user, groups, onClose }: { user: User | null; groups: Group[]; onClose: () => void }) {
  const editing = !!user;
  const [f, setF] = useState<Form>(user ? { ...user, password: "" } : { username: "", fullName: "", email: "", role: "user", password: "", groups: [], ssh: false, docker: false, disabled: false });
  const save = useAction(() => (editing ? api.put(`/api/users/${user!.username}`, f) : api.post("/api/users", f)), { success: editing ? "Utilisateur mis à jour" : "Utilisateur créé", onDone: onClose });
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => ({ ...p, [k]: v }));
  return (
    <Modal open onClose={onClose} title={editing ? `Modifier ${user!.username}` : "Nouvel utilisateur"} footer={<>
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" loading={save.isPending} disabled={!f.username || (!editing && f.password.length < 8)} onClick={() => save.mutate()}>{editing ? "Enregistrer" : "Créer"}</Button>
    </>}>
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Nom d'utilisateur"><Input value={f.username} disabled={editing} onChange={(e) => set("username", e.target.value.toLowerCase())} /></Field>
          <Field label="Nom complet"><Input value={f.fullName} onChange={(e) => set("fullName", e.target.value)} /></Field>
          <Field label="E-mail"><Input type="email" value={f.email} onChange={(e) => set("email", e.target.value)} /></Field>
          <Field label="Rôle">
            <Select value={f.role} onChange={(e) => set("role", e.target.value as Form["role"])}>
              <option value="user">Utilisateur</option>
              <option value="admin">Administrateur</option>
            </Select>
          </Field>
        </div>
        <Field label={editing ? "Nouveau mot de passe (laisser vide pour ne pas changer)" : "Mot de passe"} hint="Utilisé pour l'interface web, SMB et SSH. 8 caractères minimum.">
          <Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => set("password", e.target.value)} />
        </Field>
        {groups.length > 0 && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">Groupes</div>
            <div className="flex flex-wrap gap-3">
              {groups.map((g) => <Checkbox key={g.name} checked={f.groups.includes(g.name)} onChange={(v) => set("groups", v ? [...f.groups, g.name] : f.groups.filter((x) => x !== g.name))} label={g.name} />)}
            </div>
          </div>
        )}
        {editing && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">Quotas d'espace disque</div>
            <QuotaEditor user={user!.username} />
          </div>
        )}
        <div className="space-y-3 rounded-xl border border-line p-4">
          <Switch checked={f.ssh} onChange={(v) => set("ssh", v)} label="Accès SSH" hint="Shell /bin/bash au lieu de nologin" />
          <Switch checked={f.docker} onChange={(v) => set("docker", v)} label="Accès Docker" hint="Membre du groupe docker (équivalent root !)" />
          {editing && <Switch checked={f.disabled} onChange={(v) => set("disabled", v)} label="Compte désactivé" />}
        </div>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function QuotaEditor({ user }: { user: string }) {
  const { data, isLoading } = useQuery({ queryKey: ["user-quotas", user], queryFn: () => api.get<UserQuota[]>(`/api/users/${user}/quotas`) });
  const [edit, setEdit] = useState<Record<string, string>>({});
  const save = useAction((q: UserQuota) => api.put(`/api/users/${user}/quotas`, { kind: q.kind, target: q.target, limit: edit[q.kind + q.target] ? Math.round(parseFloat(edit[q.kind + q.target].replace(",", ".")) * 1024 ** 3) : 0 }), { success: "Quota enregistré" });
  if (isLoading) return <Spinner />;
  if (!data?.length) return <div className="text-muted text-[12.5px]">Aucun volume.</div>;
  return (
    <div className="divide-y divide-line rounded-xl border border-line">
      {data.map((q) => {
        const key = q.kind + q.target;
        return (
          <div key={key} className="flex flex-wrap items-center gap-3 px-3 py-2.5 text-[13px]">
            <div className="min-w-[140px] flex-1">
              <div className="font-medium">{q.name} <span className="text-muted text-[11.5px]">{q.kind}</span></div>
              {q.supported && q.enabled && <div className="text-muted text-[11.5px]">{bytes(q.used)} utilisés{q.limit ? ` sur ${bytes(q.limit)}` : ""}</div>}
            </div>
            {!q.supported ? <span className="text-muted text-[12px]">Pas de quota par utilisateur sur {q.kind}</span>
              : !q.enabled ? <span className="text-muted text-[12px]">Quotas désactivés (Gestionnaire de stockage → Volumes)</span>
              : (
                <div className="flex items-center gap-2">
                  <Input className="!w-24 !py-1" placeholder={q.limit ? String(+(q.limit / 1024 ** 3).toFixed(2)) : "illimité"} value={edit[key] ?? ""} onChange={(e) => setEdit({ ...edit, [key]: e.target.value })} />
                  <span className="text-muted text-[12px]">Go</span>
                  <Button size="sm" loading={save.isPending} onClick={() => save.mutate(q)}>{edit[key] ? "Appliquer" : "Illimité"}</Button>
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
  const save = useAction(() => (group ? api.put(`/api/groups/${group.name}`, { members }) : api.post("/api/groups", { name, members })), { success: "Groupe enregistré", onDone: onClose });
  return (
    <Modal open onClose={onClose} title={group ? `Groupe ${group.name}` : "Nouveau groupe"} footer={<>
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" disabled={!name} loading={save.isPending} onClick={() => save.mutate()}>Enregistrer</Button>
    </>}>
      <div className="space-y-4">
        <Field label="Nom"><Input value={name} disabled={!!group} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">Membres</div>
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
  const [tab, setTab] = useState<"users" | "groups">("users");
  const [editUser, setEditUser] = useState<User | null | undefined>(undefined);
  const [editGroup, setEditGroup] = useState<Group | null | undefined>(undefined);
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const delUser = useAction((p: { name: string; home: boolean }) => api.del(`/api/users/${p.name}${p.home ? "?removeHome=1" : ""}`), { success: "Utilisateur supprimé" });
  const reset2fa = useAction((n: string) => api.post(`/api/users/${n}/2fa/reset`), { success: "Double authentification réinitialisée" });
  const delGroup = useAction((n: string) => api.del(`/api/groups/${n}`), { success: "Groupe supprimé" });
  const userGroups = (groups.data ?? []).filter((g) => !g.system);

  return (
    <div>
      <PageHeader title="Utilisateurs et groupes" subtitle="Comptes du NAS (interface web, SMB, NFS, SSH)" icon={<UsersIcon size={20} />} actions={tab === "users"
        ? <Button variant="primary" icon={<UserPlus size={15} />} onClick={() => setEditUser(null)}>Nouvel utilisateur</Button>
        : <Button variant="primary" icon={<Plus size={15} />} onClick={() => setEditGroup(null)}>Nouveau groupe</Button>} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "users", label: `Utilisateurs (${users.data?.length ?? 0})` }, { id: "groups", label: `Groupes (${userGroups.length})` }]} />
      <ErrorBox error={users.error || groups.error} />
      {tab === "users" ? (
        <Card pad={false}>
          {users.isLoading ? <Spinner /> : (
            <div className="overflow-x-auto">
              <table className="table">
                <thead><tr><th>Utilisateur</th><th>Rôle</th><th>Groupes</th><th>Accès</th><th>Créé le</th><th /></tr></thead>
                <tbody>
                  {users.data?.map((u) => (
                    <tr key={u.username} className={u.disabled ? "opacity-60" : ""}>
                      <td>
                        <div className="flex items-center gap-3">
                          <span className="flex h-8 w-8 items-center justify-center rounded-full bg-gradient-to-br from-accent to-accent-2 text-[12px] font-semibold text-white">{u.username[0].toUpperCase()}</span>
                          <div><div className="font-medium">{u.username}{u.username === me.username && <span className="text-muted"> (vous)</span>}</div><div className="text-muted text-[12px]">{u.fullName || u.email}</div></div>
                        </div>
                      </td>
                      <td>{u.role === "admin" ? <Badge tone="accent"><Shield size={11} /> Admin</Badge> : <Badge>Utilisateur</Badge>}{u.disabled && <span className="ml-1"><Badge tone="danger">Désactivé</Badge></span>}</td>
                      <td className="text-[12.5px]">{u.groups.join(", ") || <span className="text-muted">—</span>}</td>
                      <td className="space-x-1">{u.ssh && <Badge>SSH</Badge>}{u.docker && <Badge>Docker</Badge>}<Badge>SMB</Badge>{u.twoFactor && <Badge tone="ok">2FA</Badge>}</td>
                      <td className="text-muted text-[12.5px]">{dateTime(u.createdAt)}</td>
                      <td className="text-right whitespace-nowrap">
                        <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEditUser(u)} />
                        {u.twoFactor && u.username !== me.username && <Button size="sm" variant="ghost" title="Réinitialiser la double authentification" icon={<ShieldOff size={14} />} onClick={async () => { if (await confirm({ title: `Réinitialiser la double authentification de ${u.username} ?`, text: "À utiliser si l'utilisateur a perdu son téléphone et ses codes de secours. Il pourra se connecter avec son seul mot de passe puis la reconfigurer.", danger: true })) reset2fa.mutate(u.username); }} />}
                        {u.username !== me.username && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => {
                          if (await confirm({ title: `Supprimer ${u.username} ?`, text: "Le compte Linux et Samba sera supprimé. Son dossier personnel aussi.", danger: true, typeToConfirm: u.username })) delUser.mutate({ name: u.username, home: true });
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
          {groups.isLoading ? <Spinner /> : userGroups.length === 0 ? <Empty icon={<UsersIcon />} title="Aucun groupe" text="Les groupes simplifient la gestion des permissions sur les partages." /> : (
            <table className="table">
              <thead><tr><th>Groupe</th><th>GID</th><th>Membres</th><th /></tr></thead>
              <tbody>
                {userGroups.map((g) => (
                  <tr key={g.name}>
                    <td className="font-medium">{g.name}</td><td className="mono text-muted">{g.gid}</td><td className="text-[12.5px]">{g.members.join(", ") || "—"}</td>
                    <td className="text-right">
                      <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEditGroup(g)} />
                      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: `Supprimer le groupe ${g.name} ?`, text: "Ses permissions sur les partages seront retirées.", danger: true })) delGroup.mutate(g.name); }} />
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
