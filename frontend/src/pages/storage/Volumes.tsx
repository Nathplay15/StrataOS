import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Database, FolderPlus, Import, Plus, Plug, RefreshCw, ShieldCheck, Trash2, Lock } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { BlockDevice, BtrfsInfo, QuotaTarget, Volume } from "../../lib/types";
import { bytes, cx, pct, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Switch, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";
import { tr } from "../../lib/i18n";

const btrfsProfiles = [
  { id: "single", label: tr("Single (aucune redondance)"), min: 1 },
  { id: "raid0", label: tr("RAID 0 (agrégation)"), min: 2 },
  { id: "raid1", label: tr("RAID 1 (2 copies)"), min: 2 },
  { id: "raid1c3", label: tr("RAID 1C3 (3 copies)"), min: 3 },
  { id: "raid1c4", label: tr("RAID 1C4 (4 copies)"), min: 4 },
  { id: "raid10", label: tr("RAID 10"), min: 4 },
  { id: "raid5", label: tr("RAID 5 (expérimental)"), min: 2 },
  { id: "raid6", label: tr("RAID 6 (expérimental)"), min: 3 },
];

function CreateModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("volume1");
  const [fsType, setFsType] = useState("ext4");
  const [devices, setDevices] = useState<string[]>([]);
  const [dataProfile, setDataProfile] = useState("raid1");
  const [compression, setCompression] = useState(true);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/volumes", { name, fsType, devices, dataProfile: fsType === "btrfs" ? (devices.length > 1 ? dataProfile : "single") : "", compression }), { onDone: onClose });
  const multi = fsType === "btrfs";
  const submit = async () => {
    if (await confirm({ title: tr("Créer le volume ?"), text: <>{tr("Le contenu de")}{" "}{devices.join(", ")} sera <b>{tr("effacé")}</b> {" "}{tr("et formaté en")}{" "}{fsType}.</>, danger: true, confirm: tr("Formater et créer") })) create.mutate();
  };
  return (
    <Modal open onClose={onClose} title={tr("Nouveau volume")} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" onClick={submit} loading={create.isPending} disabled={!devices.length || !name}>{tr("Créer")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Nom")} hint={tr("Monté dans /srv/strata/{0}", name || "…")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label={tr("Système de fichiers")}>
            <Select value={fsType} onChange={(e) => { setFsType(e.target.value); setDevices([]); }}>
              <option value="ext4">{tr("ext4 — robuste et universel")}</option>
              <option value="xfs">{tr("XFS — très gros fichiers, hautes performances")}</option>
              <option value="btrfs">{tr("Btrfs — snapshots, compression, RAID intégré")}</option>
            </Select>
          </Field>
        </div>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{multi ? tr("Disques ou périphériques (Btrfs gère le RAID lui-même)") : tr("Périphérique (disque, grappe RAID ou volume logique LVM)")}</div>
          <DiskPicker value={devices} onChange={setDevices} includeVirtual single={!multi} />
        </div>
        {multi && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Profil des données")} hint={tr("Les métadonnées utilisent automatiquement un profil redondant.")}>
              <Select value={devices.length > 1 ? dataProfile : "single"} disabled={devices.length < 2} onChange={(e) => setDataProfile(e.target.value)}>
                {btrfsProfiles.filter((p) => p.min <= Math.max(devices.length, 1)).map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
              </Select>
            </Field>
            <div className="pt-6"><Switch checked={compression} onChange={setCompression} label={tr("Compression zstd")} hint={tr("Économise de l'espace, coût CPU minime")} /></div>
          </div>
        )}
        {multi && (dataProfile === "raid5" || dataProfile === "raid6") && devices.length > 1 && <Notice tone="warn">{tr("Le RAID 5/6 Btrfs reste déconseillé en production. Préférez RAID 1/10 Btrfs, mdadm ou ZFS.")}</Notice>}
      </div>
    </Modal>
  );
}

function ImportModal({ onClose }: { onClose: () => void }) {
  const { data } = useQuery({ queryKey: ["block"], queryFn: () => api.get<BlockDevice[]>("/api/storage/block") });
  const [sel, setSel] = useState("");
  const [name, setName] = useState("");
  const imp = useAction(() => api.post("/api/storage/volumes/import", { name, device: sel }), { success: tr("Volume importé"), onDone: onClose });
  const found: BlockDevice[] = [];
  const walk = (b: BlockDevice, sys: boolean) => {
    const isSys = sys || b.mountpoints.some((m) => ["/", "/boot", "/boot/efi", "[SWAP]"].includes(m));
    if (["ext4", "ext3", "ext2", "xfs", "btrfs", "exfat", "vfat", "ntfs"].includes(b.fsType) && b.mountpoints.length === 0 && !isSys) found.push(b);
    b.children?.forEach((c) => walk(c, isSys));
  };
  data?.forEach((d) => walk(d, d.children?.some((c) => c.mountpoints.includes("/")) ?? false));
  return (
    <Modal open onClose={onClose} title={tr("Importer un système de fichiers existant")} footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" disabled={!sel || !name} loading={imp.isPending} onClick={() => imp.mutate()}>{tr("Importer")}</Button>
    </>}>
      <div className="space-y-4">
        <Notice>{tr("Montez un disque contenant déjà des données (disque USB, ancien NAS…) sans le formater.")}</Notice>
        {found.length === 0 ? <div className="text-muted text-[13px]">{tr("Aucun système de fichiers non monté détecté.")}</div> : (
          <div className="space-y-2">
            {found.map((b) => (
              <button key={b.path} type="button" onClick={() => { setSel(b.path); setName((b.label || b.name).replace(/[^A-Za-z0-9_-]/g, "_")); }} className={cx("flex w-full justify-between rounded-xl border px-3 py-2.5 text-left", sel === b.path ? "border-accent bg-accent-soft" : "border-line bg-panel-2")}>
                <span><b className="mono">{b.path}</b> <span className="text-muted">{b.label}</span></span>
                <span className="text-muted">{b.fsType} · {bytes(b.size)}</span>
              </button>
            ))}
          </div>
        )}
        <Field label={tr("Nom du volume")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      </div>
    </Modal>
  );
}

function BtrfsPanel({ v }: { v: Volume }) {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["btrfs", v.id], queryFn: () => api.get<BtrfsInfo>(`/api/storage/volumes/${v.id}/btrfs`), refetchInterval: 10000 });
  const act = useAction((p: { action: string; body?: object }) => api.post(`/api/storage/volumes/${v.id}/btrfs/${p.action}`, p.body ?? {}), { success: tr("Opération effectuée") });
  const [sub, setSub] = useState("");
  const [addDev, setAddDev] = useState<string[] | null>(null);
  const [convert, setConvert] = useState<string | null>(null);
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data) return null;
  const subvols = data.subvolumes.filter((s) => !s.snapshot);
  const snaps = data.subvolumes.filter((s) => s.snapshot);
  return (
    <div className="mt-4 space-y-4 border-t border-line pt-4">
      <div className="flex flex-wrap gap-2">
        <Badge tone="accent">{tr("Données :")}{" "}{data.dataProfile}</Badge>
        <Badge tone="accent">{tr("Métadonnées :")}{" "}{data.metaProfile}</Badge>
        <Badge>{tr("Scrub :")}{" "}{data.scrub || "jamais"}</Badge>
        {data.balance && !data.balance.startsWith("No balance") && <Badge tone="warn">{data.balance}</Badge>}
        <div className="flex-1" />
        <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => act.mutate({ action: "scrub" })}>{tr("Scrub")}</Button>
        <Button size="sm" variant="ghost" icon={<Camera size={14} />} onClick={() => act.mutate({ action: "snapshot" })}>{tr("Snapshot")}</Button>
        <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setAddDev([])}>{tr("Ajouter un disque")}</Button>
        <Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} onClick={() => setConvert(data.dataProfile)}>{tr("Convertir le RAID")}</Button>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Disques (")}{data.devices.length})</div>
          <div className="space-y-1.5">
            {data.devices.map((d) => (
              <div key={d.id} className="flex items-center justify-between gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[12.5px]">
                <span className="mono">{d.path}</span>
                <span className="text-muted">{bytes(d.used)} / {bytes(d.size)}</span>
                {data.devices.length > 1 && <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Retirer ce disque ?"), text: tr("Les données seront d'abord déplacées vers les autres disques (peut être long)."), danger: true })) act.mutate({ action: "remove-device", body: { device: d.path } }); }}>{tr("Retirer")}</Button>}
              </div>
            ))}
          </div>
        </div>
        <div>
          <div className="mb-2 flex items-center justify-between text-[12.5px] font-medium text-muted">
            Sous-volumes
            <span className="flex gap-1.5">
              <Input className="!py-1 !text-[12px] w-32" placeholder="nouveau" value={sub} onChange={(e) => setSub(e.target.value)} />
              <Button size="sm" icon={<FolderPlus size={13} />} disabled={!sub} onClick={() => { act.mutate({ action: "subvolume", body: { name: sub } }); setSub(""); }}>{tr("Créer")}</Button>
            </span>
          </div>
          <div className="space-y-1.5">
            {subvols.length === 0 && <div className="text-muted text-[12.5px]">{tr("Aucun sous-volume")}</div>}
            {subvols.map((s) => (
              <div key={s.id} className="flex items-center justify-between rounded-lg bg-panel-2 px-3 py-1.5 text-[12.5px]">
                <span className="mono">{s.path}</span>
                <span className="flex gap-1">
                  <Button size="sm" variant="ghost" onClick={() => act.mutate({ action: "snapshot", body: { path: s.path } })}>{tr("Snapshot")}</Button>
                  <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer {0} ?", s.path), text: tr("Le sous-volume et son contenu seront supprimés."), danger: true, typeToConfirm: s.path })) act.mutate({ action: "delete-subvolume", body: { path: s.path } }); }}>{tr("Supprimer")}</Button>
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
      {snaps.length > 0 && (
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Snapshots (")}{snaps.length}{tr(") — accessibles en lecture seule dans .snapshots/")}</div>
          <div className="grid gap-1.5 md:grid-cols-2">
            {snaps.map((s) => (
              <div key={s.id} className="flex items-center justify-between rounded-lg bg-panel-2 px-3 py-1.5 text-[12.5px]">
                <span className="mono truncate">{s.path.replace(".snapshots/", "")}</span>
                {s.lockedUntil ? <span className="inline-flex items-center gap-1 text-[12px] text-muted" title={tr("Verrouillé : impossible à supprimer avant le {0}", dateTime(s.lockedUntil * 1000))}><Lock size={13} />{dateTime(s.lockedUntil * 1000)}</span> : <Button size="sm" variant="ghost" className="text-danger" onClick={() => act.mutate({ action: "delete-subvolume", body: { path: s.path } })}>{tr("Supprimer")}</Button>}
              </div>
            ))}
          </div>
        </div>
      )}
      <Modal open={addDev !== null} onClose={() => setAddDev(null)} title={tr("Ajouter un disque au volume Btrfs")} footer={<>
        <Button onClick={() => setAddDev(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!addDev?.length} onClick={() => { act.mutate({ action: "add-device", body: { device: addDev![0] } }); setAddDev(null); }}>{tr("Ajouter")}</Button>
      </>}>
        <div className="space-y-3">
          <Notice>{tr("Après l'ajout, lancez une conversion/équilibrage pour répartir les données.")}</Notice>
          <DiskPicker value={addDev ?? []} onChange={setAddDev} single />
        </div>
      </Modal>
      <Modal open={convert !== null} onClose={() => setConvert(null)} title={tr("Convertir le profil RAID")} footer={<>
        <Button onClick={() => setConvert(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" onClick={() => { act.mutate({ action: "balance", body: { dataProfile: convert, metaProfile: convert === "single" ? "dup" : ["raid5", "raid6", "raid0"].includes(convert!) ? "raid1" : convert } }); setConvert(null); }}>{tr("Lancer la conversion")}</Button>
      </>}>
        <div className="space-y-3">
          <Field label={tr("Nouveau profil des données")}>
            <Select value={convert ?? ""} onChange={(e) => setConvert(e.target.value)}>
              {btrfsProfiles.filter((p) => p.min <= data.devices.length).map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
            </Select>
          </Field>
          <Notice>{tr("La conversion s'effectue en ligne via un équilibrage complet ; elle peut durer plusieurs heures.")}</Notice>
        </div>
      </Modal>
    </div>
  );
}

export default function Volumes() {
  const { confirm } = useUI();
  const [create, setCreate] = useState(false);
  const [imp, setImp] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["volumes"], queryFn: () => api.get<Volume[]>("/api/storage/volumes"), refetchInterval: 10000 });
  const del = useAction((p: { id: string; wipe: boolean }) => api.del(`/api/storage/volumes/${p.id}${p.wipe ? "?wipe=1" : ""}`), { success: tr("Volume supprimé") });
  const mount = useAction((id: string) => api.post(`/api/storage/volumes/${id}/mount`), { success: tr("Volume monté") });
  const quotas = useQuery({ queryKey: ["quota-targets"], queryFn: () => api.get<QuotaTarget[]>("/api/quotas/targets") });
  const enableQuota = useAction((id: string) => api.post(`/api/storage/volumes/${id}/quota`));
  const quotaOf = (v: Volume) => quotas.data?.find((q) => q.target === v.id && q.kind === v.fsType);

  const remove = async (v: Volume) => {
    wipeRef.current = false;
    const ok = await confirm({
      title: tr("Supprimer le volume {0} ?", v.name),
      text: (
        <div className="space-y-3">
          <p>{tr("Le volume sera démonté et retiré de StrataOS.")}</p>
          <WipeToggle />
        </div>
      ),
      danger: true, typeToConfirm: v.name, confirm: tr("Supprimer"),
    });
    if (ok) del.mutate({ id: v.id, wipe: wipeRef.current });
  };

  return (
    <div>
      <PageHeader title={tr("Volumes")} subtitle={tr("Systèmes de fichiers montés dans /srv/strata")} icon={<Database size={20} />} actions={<>
        <Button icon={<Import size={15} />} onClick={() => setImp(true)}>{tr("Importer")}</Button>
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Créer un volume")}</Button>
      </>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Database />} title={tr("Aucun volume")} text={tr("Un volume est un système de fichiers (ext4, XFS ou Btrfs) créé sur un disque, une grappe RAID ou un volume LVM. Les pools ZFS sont gérés sur la page ZFS.")} action={<Button variant="primary" onClick={() => setCreate(true)}>{tr("Créer un volume")}</Button>} /></Card>
      ) : (
        <div className="space-y-4">
          {data.map((v) => (
            <Card key={v.id}>
              <div className="flex flex-wrap items-center gap-4">
                <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-accent-soft text-accent"><Database size={20} /></div>
                <div className="min-w-[180px] flex-1">
                  <div className="flex items-center gap-2 font-semibold">{v.name} <Badge>{v.fsType}</Badge>{v.profile && <Badge tone="accent">{v.profile}</Badge>} <Health value={v.health} /></div>
                  <div className="text-muted text-[12px]">{v.mountPoint} · {v.devices.join(", ")}</div>
                </div>
                <div className="w-full max-w-sm sm:w-72">
                  <Progress value={pct(v.usage.used, v.usage.total)} />
                  <div className="mt-1.5 flex justify-between text-[12px] text-muted"><span>{bytes(v.usage.used)} {" "}{tr("utilisés")}</span><span>{bytes(v.usage.total)}</span></div>
                </div>
                <div className="flex gap-1">
                  {quotaOf(v)?.supported && (quotaOf(v)!.enabled ? <span className="self-center"><Badge tone="ok">{tr("Quotas")}</Badge></span> : v.mounted && (
                    <Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: tr("Activer les quotas sur {0} ?", v.name), text: tr("Le volume est démonté quelques secondes : fermez les fichiers ouverts et arrêtez les applications qui l'utilisent. Les partages reprennent automatiquement."), confirm: tr("Activer") })) enableQuota.mutate(v.id); }}>{tr("Activer les quotas")}</Button>
                  ))}
                  {!v.mounted && <Button size="sm" icon={<Plug size={14} />} onClick={() => mount.mutate(v.id)}>{tr("Monter")}</Button>}
                  {v.fsType === "btrfs" && v.mounted && <Button size="sm" variant="ghost" onClick={() => setOpen(open === v.id ? null : v.id)}>{open === v.id ? tr("Masquer") : tr("Btrfs…")}</Button>}
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={() => remove(v)} />
                </div>
              </div>
              {v.shares.length > 0 && <div className="mt-3 text-[12px] text-muted">{tr("Partages :")}{" "}{v.shares.join(", ")}</div>}
              {open === v.id && <BtrfsPanel v={v} />}
            </Card>
          ))}
        </div>
      )}
      {create && <CreateModal onClose={() => setCreate(false)} />}
      {imp && <ImportModal onClose={() => setImp(false)} />}
      {del.error && <ErrorBox error={del.error} />}
    </div>
  );
}

// The confirm dialog content is rendered outside this component's state, so keep the wipe choice in a ref.
const wipeRef = { current: false };
function WipeToggle() {
  const [v, setV] = useState(false);
  return <Checkbox checked={v} onChange={(x) => { setV(x); wipeRef.current = x; }} label={tr("Effacer aussi les signatures du système de fichiers (irréversible)")} />;
}
