// Shared folders of the NAS: list, create on a volume, edit protocols and
// rights of users and groups, delete (the files stay). Encryption, quotas,
// NFS details and Time Machine options are set in the web interface.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Lock, Pencil, Plus, Share2, Trash2, Users as UsersIcon } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Select, Spinner, Switch, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { bytes } from "@nas/lib/format";
import type { Group, Perm, Share, User } from "@nas/lib/types";
import { api } from "../lib/nasapi";
import { t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

// Every user of the NAS, as the web interface names that group.
const ALL_USERS = "@strata";
const empty: Share = { name: "", path: "", description: "", smb: true, nfs: false, guest: false, browseable: true, recycleBin: true, timeMachine: false, permissions: {}, nfsClients: "", nfsOptions: "" };

export function SharesPage() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const [edit, setEdit] = useState<Share | null>(null);
  const del = useAction((n: string) => api.del(`/api/shares/${encodeURIComponent(n)}`), { success: t("Share deleted (files kept)") });
  return (
    <>
      <PageHeader title={t("Shared folders")} actions={<>
        <WebButton path="/shares" title={t("Shared folders")} />
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit({ ...empty })}>{t("New shared folder")}</Button>
      </>} />
      <ErrorBox error={error} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<Share2 />} title={t("No shared folders")} /> : (
          <table className="table">
            <thead><tr><th>{t("Name")}</th><th>{t("Access")}</th><th>{t("Used")}</th><th /></tr></thead>
            <tbody>
              {data.map((s) => (
                <tr key={s.name}>
                  <td>
                    <div className="flex items-center gap-2 font-medium">{s.encrypted && <Lock size={13} className={s.locked ? "text-warn" : "text-ok"} />}{s.name}</div>
                    <div className="mono text-[11.5px] text-muted">{s.path}</div>
                    {s.description && <div className="text-[12px] text-muted">{s.description}</div>}
                  </td>
                  <td>
                    <div className="flex flex-wrap gap-1">
                      {s.smb && <Badge>SMB</Badge>}{s.nfs && <Badge>NFS</Badge>}{s.guest && <Badge tone="warn">{t("Guest")}</Badge>}
                      {s.recycleBin && <Badge>{t("Recycle bin")}</Badge>}{s.timeMachine && <Badge>Time Machine</Badge>}{s.frozen && <Badge tone="danger">{t("Frozen")}</Badge>}
                    </div>
                  </td>
                  <td className="text-[12.5px] text-muted">{s.used !== undefined ? bytes(s.used) : "—"}{s.quotaGb ? ` / ${s.quotaGb} GB` : ""}</td>
                  <td className="whitespace-nowrap text-right">
                    <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={t("Edit")} onClick={() => setEdit(s)} />
                    <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={t("Delete")} onClick={async () => {
                      if (await confirm({ title: t("Delete the share {0}?", s.name), text: t("The network share is removed; the folder and its files stay on the volume."), danger: true, typeToConfirm: s.name })) del.mutate(s.name);
                    }} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {edit && <ShareModal initial={edit} onClose={() => setEdit(null)} />}
    </>
  );
}

function ShareModal({ initial, onClose }: { initial: Share; onClose: () => void }) {
  const editing = !!initial.name;
  const [s, setS] = useState<Share>(initial);
  const [base, setBase] = useState("");
  const locations = useQuery({ queryKey: ["locations"], queryFn: () => api.get<{ path: string; label: string; free: number }[]>("/api/storage/locations"), enabled: !editing });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const save = useAction(() => (editing ? api.put(`/api/shares/${encodeURIComponent(initial.name)}`, s) : api.post("/api/shares", s)), { success: editing ? t("Share updated") : t("Share created"), onDone: onClose });
  const loc = base || locations.data?.[0]?.path || "";
  const setName = (n: string) => setS((p) => ({ ...p, name: n, path: editing ? p.path : `${loc}/${n}` }));
  const setPerm = (who: string, p: Perm | "") => setS((x) => {
    const perms = { ...x.permissions };
    if (p) perms[who] = p;
    else delete perms[who];
    return { ...x, permissions: perms };
  });
  const who = [ALL_USERS, ...(groups.data ?? []).filter((g) => !g.system).map((g) => "@" + g.name), ...(users.data ?? []).map((u) => u.username)];
  return (
    <Modal open wide onClose={onClose} title={editing ? t("Edit {0}", initial.name) : t("New shared folder")} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!s.name.trim() || !s.path} onClick={() => save.mutate()}>{editing ? t("Save") : t("Create")}</Button>
    </>}>
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("Share name")}><Input value={s.name} onChange={(e) => setName(e.target.value)} /></Field>
          {editing ? <Field label={t("Folder")}><Input value={s.path} disabled className="mono" /></Field> : (
            <Field label={t("Volume")}>
              <Select value={loc} onChange={(e) => { setBase(e.target.value); setS((p) => ({ ...p, path: `${e.target.value}/${p.name}` })); }}>
                {(locations.data ?? []).map((l) => <option key={l.path} value={l.path}>{l.label} ({t("{0} free", bytes(l.free))})</option>)}
              </Select>
            </Field>
          )}
        </div>
        <Field label={t("Description")}><Input value={s.description} onChange={(e) => setS({ ...s, description: e.target.value })} /></Field>
        <div className="grid gap-2 sm:grid-cols-2">
          <Switch checked={s.smb} onChange={(v) => setS({ ...s, smb: v })} label={t("Windows / macOS sharing (SMB)")} />
          <Switch checked={s.nfs} onChange={(v) => setS({ ...s, nfs: v })} label={t("Linux sharing (NFS)")} />
          <Switch checked={s.recycleBin} onChange={(v) => setS({ ...s, recycleBin: v })} label={t("Recycle bin")} />
          <Switch checked={s.guest} onChange={(v) => setS({ ...s, guest: v })} label={t("Guest access (no password)")} />
        </div>
        <div>
          <div className="mb-1.5 flex items-center gap-2 text-[13px] font-medium"><UsersIcon size={15} />{t("Permissions")}</div>
          <div className="max-h-[260px] overflow-y-auto rounded-xl border border-line">
            {who.map((w) => (
              <div key={w} className="flex items-center gap-2 border-b border-line px-3 py-1.5 text-[13px] last:border-0">
                <span className="flex-1">{w === ALL_USERS ? t("All users") : w}</span>
                <Select value={s.permissions[w] ?? ""} onChange={(e) => setPerm(w, e.target.value as Perm | "")} className="!w-[150px]">
                  <option value="">{t("Not set")}</option>
                  <option value="rw">{t("Read and write")}</option>
                  <option value="ro">{t("Read only")}</option>
                  <option value="none">{t("No access")}</option>
                </Select>
              </div>
            ))}
          </div>
        </div>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}
