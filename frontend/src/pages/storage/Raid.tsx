import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { RaidArray } from "../../lib/types";
import { bytes, cx } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Progress, Spinner, useUI } from "../../components/ui";
import { DiskPicker, useCandidates } from "../../components/DiskPicker";

const levels = [
  { id: "1", name: "RAID 1", min: 2, desc: "Miroir : chaque disque contient une copie complète. Supporte n-1 pannes.", cap: (_n: number, s: number) => s },
  { id: "5", name: "RAID 5", min: 3, desc: "Parité distribuée. Supporte 1 panne de disque.", cap: (n: number, s: number) => (n - 1) * s },
  { id: "6", name: "RAID 6", min: 4, desc: "Double parité. Supporte 2 pannes simultanées.", cap: (n: number, s: number) => (n - 2) * s },
  { id: "10", name: "RAID 10", min: 2, desc: "Miroirs agrégés : performances et redondance.", cap: (n: number, s: number) => (n * s) / 2 },
  { id: "0", name: "RAID 0", min: 2, desc: "Agrégation sans redondance : rapide, mais une panne = perte totale.", cap: (n: number, s: number) => n * s },
  { id: "linear", name: "JBOD", min: 1, desc: "Concaténation des disques, sans redondance.", cap: (n: number, s: number) => n * s },
  { id: "4", name: "RAID 4", min: 3, desc: "Parité sur un disque dédié.", cap: (n: number, s: number) => (n - 1) * s },
];

function CreateModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("data");
  const [level, setLevel] = useState("1");
  const [devices, setDevices] = useState<string[]>([]);
  const [spares, setSpares] = useState<string[]>([]);
  const { list } = useCandidates(false);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/raid", { name, level, devices, spares }), { onDone: onClose });
  const lv = levels.find((l) => l.id === level)!;
  const sizes = devices.map((d) => list.find((c) => c.path === d)?.size ?? 0);
  const minSize = sizes.length ? Math.min(...sizes) : 0;
  const capacity = devices.length >= lv.min ? lv.cap(devices.length, minSize) : 0;

  const submit = async () => {
    const ok = await confirm({ title: "Créer la grappe RAID ?", text: <>Les disques {devices.concat(spares).join(", ")} vont être <b>entièrement effacés</b>.</>, danger: true, confirm: "Créer" });
    if (ok) create.mutate();
  };
  return (
    <Modal open={open} onClose={onClose} title="Nouvelle grappe RAID (mdadm)" wide footer={<>
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" onClick={submit} loading={create.isPending} disabled={devices.length < lv.min || !name}>Créer</Button>
    </>}>
      <div className="space-y-5">
        <Field label="Nom de la grappe"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">Niveau RAID</div>
          <div className="grid gap-2 sm:grid-cols-2">
            {levels.map((l) => (
              <button key={l.id} type="button" onClick={() => setLevel(l.id)} className={cx("rounded-xl border px-3.5 py-2.5 text-left transition", level === l.id ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                <div className="flex items-center justify-between font-medium">{l.name} <span className="text-muted text-[11.5px] font-normal">min. {l.min} disque{l.min > 1 ? "s" : ""}</span></div>
                <div className="text-muted text-[12px]">{l.desc}</div>
              </button>
            ))}
          </div>
        </div>
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">Disques membres ({devices.length})</div>
          <DiskPicker value={devices} onChange={setDevices} exclude={spares} />
        </div>
        {level !== "0" && level !== "linear" && (
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">Disques de secours (optionnel)</div>
            <DiskPicker value={spares} onChange={setSpares} exclude={devices} />
          </div>
        )}
        {capacity > 0 && <Notice>Capacité utile estimée : <b>{bytes(capacity)}</b>. Créez ensuite un volume sur cette grappe pour y stocker des données.</Notice>}
        {(level === "0" || level === "linear") && <Notice tone="warn">Ce niveau n'offre aucune protection : la panne d'un seul disque entraîne la perte de toutes les données.</Notice>}
      </div>
    </Modal>
  );
}

function ArrayCard({ a }: { a: RaidArray }) {
  const { confirm } = useUI();
  const [addOpen, setAddOpen] = useState<null | "add" | "add-spare">(null);
  const [dev, setDev] = useState<string[]>([]);
  const act = useAction((p: { action: string; device?: string; count?: number }) => api.post(`/api/storage/raid/${a.label || a.name}/${p.action}`, { device: p.device ?? "", count: p.count ?? 0 }), { success: "Opération effectuée" });
  const del = useAction(() => api.del(`/api/storage/raid/${a.label || a.name}`), { success: "Grappe supprimée" });
  const spares = a.members.filter((m) => m.state.includes("spare")).length;

  const remove = async () => {
    if (await confirm({ title: `Supprimer ${a.label || a.name} ?`, text: "La grappe sera arrêtée et les métadonnées RAID effacées des disques. Les données seront perdues.", danger: true, typeToConfirm: a.label || a.name, confirm: "Supprimer" })) del.mutate();
  };

  return (
    <Card
      title={<span className="flex items-center gap-2.5">{a.label || a.name} <Badge>{a.level.toUpperCase()}</Badge> <Health value={a.health} /></span>}
      actions={<>
        <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => act.mutate({ action: a.syncAction ? "idle" : "check" })}>{a.syncAction?.startsWith("check") ? "Arrêter la vérification" : "Vérifier"}</Button>
        <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setAddOpen("add")}>Ajouter un disque</Button>
        <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={remove} />
      </>}
    >
      <div className="grid gap-4 sm:grid-cols-4">
        <div><div className="text-muted text-[12px]">Capacité</div><div className="font-semibold">{bytes(a.size)}</div></div>
        <div><div className="text-muted text-[12px]">Disques actifs</div><div className="font-semibold">{a.activeDevices}/{a.raidDevices}</div></div>
        <div><div className="text-muted text-[12px]">Secours / en panne</div><div className="font-semibold">{a.spareDevices} / <span className={a.failedDevices ? "text-danger" : ""}>{a.failedDevices}</span></div></div>
        <div><div className="text-muted text-[12px]">Périphérique</div><div className="mono">{a.device}</div></div>
      </div>
      {a.syncAction && (
        <div className="mt-4 rounded-xl bg-panel-2 p-3">
          <div className="mb-2 flex justify-between text-[12.5px]"><span className="capitalize">{a.syncAction}</span><span className="text-muted">{a.syncProgress?.toFixed(1)}% · reste {a.syncFinish} · {a.syncSpeed}</span></div>
          <Progress value={a.syncProgress ?? 0} tone="accent" />
        </div>
      )}
      <div className="mt-4 overflow-x-auto rounded-xl border border-line">
        <table className="table">
          <thead><tr><th>#</th><th>Disque</th><th>État</th><th /></tr></thead>
          <tbody>
            {a.members.map((m, i) => (
              <tr key={i}>
                <td className="mono">{m.raidDevice >= 0 ? m.raidDevice : "—"}</td>
                <td className="mono">{m.device || "—"}</td>
                <td><Badge tone={m.state.includes("faulty") || m.state.includes("removed") ? "danger" : m.state.includes("rebuild") ? "warn" : m.state.includes("spare") ? "accent" : "ok"}>{m.state}</Badge></td>
                <td className="text-right">
                  {m.device && (
                    <div className="flex justify-end gap-1">
                      {!m.state.includes("faulty") && <Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: "Marquer en panne ?", text: `${m.device} sera retiré du service ; la grappe passera en mode dégradé.`, danger: true })) act.mutate({ action: "fail", device: m.device }); }}>Marquer en panne</Button>}
                      <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: "Retirer le disque ?", text: `${m.device} sera retiré de la grappe et ses métadonnées RAID effacées.`, danger: true })) act.mutate({ action: "remove", device: m.device }); }}>Retirer</Button>
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {spares > 0 && ["raid5", "raid6", "raid1", "raid10", "raid4", "linear"].includes(a.level) && (
        <div className="mt-3 flex items-center justify-between gap-3 rounded-xl border border-dashed border-line px-4 py-3 text-[13px]">
          <span>{spares} disque(s) de secours disponible(s) : vous pouvez agrandir la grappe.</span>
          <Button size="sm" onClick={async () => { if (await confirm({ title: "Agrandir la grappe ?", text: `La grappe passera à ${a.raidDevices + spares} disques actifs. Le remodelage peut durer plusieurs heures ; redimensionnez ensuite le volume.` })) act.mutate({ action: "grow", count: a.raidDevices + spares }); }}>Agrandir</Button>
        </div>
      )}
      <Modal open={!!addOpen} onClose={() => setAddOpen(null)} title="Ajouter un disque" footer={<>
        <Button onClick={() => setAddOpen(null)}>Annuler</Button>
        <Button variant="primary" disabled={!dev.length} loading={act.isPending} onClick={() => { act.mutate({ action: "add", device: dev[0] }); setAddOpen(null); setDev([]); }}>Ajouter</Button>
      </>}>
        <div className="space-y-3">
          <Notice>Le disque sera ajouté comme secours. Si la grappe est dégradée, la reconstruction démarrera automatiquement.</Notice>
          <DiskPicker value={dev} onChange={setDev} single />
        </div>
      </Modal>
      {act.error && <div className="mt-3"><ErrorBox error={act.error} /></div>}
    </Card>
  );
}

export default function Raid() {
  const [open, setOpen] = useState(false);
  const { data, isLoading, error } = useQuery({ queryKey: ["raid"], queryFn: () => api.get<RaidArray[]>("/api/storage/raid"), refetchInterval: 5000 });
  return (
    <div>
      <PageHeader title="RAID logiciel" subtitle="Grappes mdadm : RAID 0, 1, 4, 5, 6, 10 et JBOD" icon={<Layers size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setOpen(true)}>Créer une grappe</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Layers />} title="Aucune grappe RAID" text="Regroupez plusieurs disques pour la redondance ou la performance. Pour ZFS ou Btrfs (RAID intégré), utilisez les pages ZFS ou Volumes." action={<Button variant="primary" onClick={() => setOpen(true)}>Créer une grappe</Button>} /></Card>
      ) : <div className="space-y-5">{data.map((a) => <ArrayCard key={a.name} a={a} />)}</div>}
      {open && <CreateModal open={open} onClose={() => setOpen(false)} />}
    </div>
  );
}
