import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Edit3, Monitor, Plus, Share2, Trash2, User as UserIcon, Users as UsersIcon } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Group, Perm, Share, User } from "../lib/types";
import { bytes, cx } from "../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, Tabs, useUI } from "../components/ui";
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
      <div className="border-b border-line px-4 py-2.5 text-[12.5px] font-medium text-muted">{tr("Comptes de l'annuaire")}{" "}{dir.data?.joined ? `(${dir.data.config.type === "ad" ? dir.data.config.workgroup : "LDAP"})` : tr("(annuaire quitté)")}</div>
      {extra.map((who) => (
        <div key={who} className="flex items-center justify-between gap-3 px-4 py-2.5">
          <span className="flex items-center gap-2">{who.startsWith("@") ? <UsersIcon size={15} className="text-accent" /> : <UserIcon size={15} className="text-muted" />}{who}</span>
          <PermPicker value={perms[who]} onChange={(p) => setPerm(who, p)} />
        </div>
      ))}
      {dir.data?.joined && (
        <div className="space-y-2 px-4 py-3">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={tr("Rechercher un utilisateur ou un groupe du domaine…")} />
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
    { v: "rw", label: tr("Lecture/écriture"), cls: "bg-ok text-white" },
    { v: "ro", label: tr("Lecture"), cls: "bg-accent text-white" },
    { v: "none", label: tr("Refusé"), cls: "bg-danger text-white" },
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

function ShareModal({ initial, onClose }: { initial: Share | null; onClose: () => void }) {
  const editing = !!initial?.name;
  const [s, setS] = useState<Share>(initial ?? empty);
  const locations = useQuery({ queryKey: ["locations"], queryFn: () => api.get<{ path: string; label: string; free: number }[]>("/api/storage/locations") });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const [base, setBase] = useState(() => (initial?.path ? "" : ""));
  const save = useAction(() => (editing ? api.put(`/api/shares/${encodeURIComponent(s.name)}`, s) : api.post("/api/shares", s)), { success: editing ? tr("Partage mis à jour") : tr("Partage créé"), onDone: onClose });
  const set = <K extends keyof Share>(k: K, v: Share[K]) => setS((p) => ({ ...p, [k]: v }));
  const setPerm = (who: string, p?: Perm) => setS((x) => { const perms = { ...x.permissions }; if (p) perms[who] = p; else delete perms[who]; return { ...x, permissions: perms }; });

  const pickBase = (b: string) => {
    setBase(b);
    if (b) set("path", `${b}/${s.name || "partage"}`);
  };

  return (
    <Modal open onClose={onClose} title={editing ? tr("Modifier « {0} »", s.name) : tr("Nouveau dossier partagé")} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!s.name || !s.path} onClick={() => save.mutate()}>{editing ? tr("Enregistrer") : tr("Créer")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Nom du partage")}><Input value={s.name} disabled={editing} onChange={(e) => { const n = e.target.value; setS((p) => ({ ...p, name: n, path: base ? `${base}/${n}` : p.path })); }} /></Field>
          <Field label={tr("Description")}><Input value={s.description} onChange={(e) => set("description", e.target.value)} /></Field>
        </div>
        {!editing && (
          <Field label={tr("Emplacement")} hint={locations.data?.length === 0 ? tr("Créez d'abord un volume ou un pool ZFS.") : undefined}>
            <Select value={base} onChange={(e) => pickBase(e.target.value)}>
              <option value="">{tr("Choisir un volume…")}</option>
              {locations.data?.map((l) => <option key={l.path} value={l.path}>{l.label} — {bytes(l.free)} libres</option>)}
            </Select>
          </Field>
        )}
        <Field label={tr("Chemin du dossier")} hint={tr("Créé automatiquement s'il n'existe pas.")}><Input className="mono" value={s.path} onChange={(e) => set("path", e.target.value)} /></Field>

        <div className="grid gap-4 rounded-xl border border-line p-4 sm:grid-cols-2">
          <Switch checked={s.smb} onChange={(v) => set("smb", v)} label={tr("SMB / CIFS")} hint={tr("Windows, macOS, Linux")} />
          <Switch checked={s.nfs} onChange={(v) => set("nfs", v)} label="NFS" hint={tr("Linux, hyperviseurs")} />
          <Switch checked={s.browseable} onChange={(v) => set("browseable", v)} label={tr("Visible sur le réseau")} />
          <Switch checked={s.recycleBin} onChange={(v) => set("recycleBin", v)} label={tr("Corbeille réseau")} hint={tr("Fichiers supprimés conservés dans .recycle")} />
          <Switch checked={s.timeMachine} onChange={(v) => set("timeMachine", v)} label={tr("Cible Time Machine")} hint={tr("Sauvegarde macOS")} />
          <Switch checked={s.guest} onChange={(v) => set("guest", v)} label={tr("Accès invité")} hint={tr("Lecture/écriture sans mot de passe")} />
        </div>

        {s.nfs && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Clients NFS autorisés")} hint={tr("ex. 192.168.1.0/24 10.0.0.5 — vide = tous")}><Input value={s.nfsClients} onChange={(e) => set("nfsClients", e.target.value)} /></Field>
            <Field label={tr("Options NFS")} hint={tr("Par défaut : rw,sync,no_subtree_check,root_squash")}><Input className="mono" value={s.nfsOptions} onChange={(e) => set("nfsOptions", e.target.value)} /></Field>
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
                    <span className="flex items-center gap-2"><UsersIcon size={15} className="text-accent" />@{g.name}<span className="text-muted text-[12px]">{tr("groupe ·")}{" "}{g.members.length} membre(s)</span></span>
                    <PermPicker value={s.permissions["@" + g.name]} onChange={(p) => setPerm("@" + g.name, p)} />
                  </div>
                ))}
              </div>
            )}
            <DomainPerms perms={s.permissions} local={new Set([...(users.data ?? []).map((u) => u.username), ...(groups.data ?? []).map((g) => "@" + g.name)])} setPerm={setPerm} />
            {Object.keys(s.permissions).length === 0 && <div className="mt-2"><Notice>{tr("Sans permission explicite, tous les utilisateurs StrataOS ont un accès en lecture seule.")}</Notice></div>}
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
  const { data, isLoading, error } = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const sessions = useQuery({ queryKey: ["smb-sessions"], queryFn: () => api.get<{ pid: string; username: string; machine: string; protocol: string }[]>("/api/shares/sessions"), enabled: tab === "sessions", refetchInterval: 5000 });
  const del = useAction((n: string) => api.del(`/api/shares/${encodeURIComponent(n)}`), { success: tr("Partage supprimé (fichiers conservés)") });
  const host = location.hostname;

  return (
    <div>
      <PageHeader title={tr("Dossiers partagés")} subtitle={tr("Partages SMB et NFS, droits d'accès par utilisateur et groupe")} icon={<Share2 size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{tr("Nouveau partage")}</Button>} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "shares", label: tr("Partages") }, { id: "sessions", label: tr("Connexions actives") }]} />
      <ErrorBox error={error} />
      {tab === "shares" ? (
        isLoading ? <Spinner /> : !data?.length ? (
          <Card><Empty icon={<Share2 />} title={tr("Aucun partage")} text={tr("Créez un dossier partagé pour y accéder depuis vos ordinateurs, téléphones ou TV.")} action={<Button variant="primary" onClick={() => setEdit(null)}>{tr("Créer un partage")}</Button>} /></Card>
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
                    {s.smb && <Badge tone="accent">SMB</Badge>}
                    {s.nfs && <Badge tone="accent">NFS</Badge>}
                    {s.guest && <Badge tone="warn">{tr("Invité")}</Badge>}
                    {s.recycleBin && <Badge>{tr("Corbeille")}</Badge>}
                    {s.timeMachine && <Badge>{tr("Time Machine")}</Badge>}
                    {!s.browseable && <Badge>{tr("Masqué")}</Badge>}
                  </div>
                  <div className="mt-3 text-[12px] text-muted">
                    {Object.entries(s.permissions).length === 0 ? tr("Lecture seule pour tous les utilisateurs") : Object.entries(s.permissions).map(([k, v]) => `${k} (${v})`).join(", ")}
                  </div>
                  {s.smb && <div className="mono mt-3 rounded-lg bg-panel-2 px-2.5 py-1.5 text-[11.5px] text-muted">\\{host}\{s.name}</div>}
                </div>
                <div className="flex justify-end gap-1 border-t border-line bg-panel-2/50 px-4 py-2.5">
                  <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEdit(s)}>{tr("Modifier")}</Button>
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Supprimer le partage {0} ?", s.name), text: tr("Le partage ne sera plus accessible. Les fichiers restent sur le volume."), danger: true })) del.mutate(s.name); }} />
                </div>
              </Card>
            ))}
          </div>
        )
      ) : (
        <Card pad={false}>
          {sessions.isLoading ? <Spinner /> : !sessions.data?.length ? <Empty icon={<Monitor />} title={tr("Aucune connexion SMB active")} /> : (
            <table className="table">
              <thead><tr><th>{tr("Utilisateur")}</th><th>{tr("Machine")}</th><th>{tr("Protocole")}</th><th>PID</th></tr></thead>
              <tbody>{sessions.data.map((x) => <tr key={x.pid}><td>{x.username}</td><td className="mono">{x.machine}</td><td>{x.protocol}</td><td className="mono">{x.pid}</td></tr>)}</tbody>
            </table>
          )}
        </Card>
      )}
      {edit !== undefined && <ShareModal initial={edit} onClose={() => setEdit(undefined)} />}
    </div>
  );
}
