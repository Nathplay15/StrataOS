import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Expand, Plus, Server, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { VG } from "../../lib/types";
import { bytes, parseSize, pct } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";

function CreateVG({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("vg0");
  const [devices, setDevices] = useState<string[]>([]);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/lvm", { name, devices }), { success: "Groupe de volumes créé", onDone: onClose });
  return (
    <Modal open onClose={onClose} title="Nouveau groupe de volumes" wide footer={<>
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" disabled={!devices.length || !name} loading={create.isPending} onClick={async () => { if (await confirm({ title: "Créer le groupe ?", text: `${devices.join(", ")} seront effacés.`, danger: true })) create.mutate(); }}>Créer</Button>
    </>}>
      <div className="space-y-4">
        <Field label="Nom"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Notice>Les disques (ou grappes RAID) sélectionnés deviennent des volumes physiques mis en commun. Vous pourrez ensuite y découper des volumes logiques redimensionnables.</Notice>
        <DiskPicker value={devices} onChange={setDevices} includeVirtual />
      </div>
    </Modal>
  );
}

function CreateLV({ vg, onClose }: { vg: VG; onClose: () => void }) {
  const [name, setName] = useState("lv0");
  const [size, setSize] = useState("");
  const [type, setType] = useState("linear");
  const [stripes, setStripes] = useState(2);
  const create = useAction(() => api.post(`/api/storage/lvm/${vg.name}/lv`, { name, size: size ? parseSize(size) : 0, type, stripes }), { success: "Volume logique créé", onDone: onClose });
  return (
    <Modal open onClose={onClose} title={`Nouveau volume logique dans ${vg.name}`} footer={<>
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" disabled={!name || (!!size && isNaN(parseSize(size)))} loading={create.isPending} onClick={() => create.mutate()}>Créer</Button>
    </>}>
      <div className="space-y-4">
        <Field label="Nom"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Taille" hint={`ex. 100G — vide = tout l'espace libre (${bytes(vg.free)})`}><Input value={size} onChange={(e) => setSize(e.target.value)} /></Field>
        <Field label="Type">
          <Select value={type} onChange={(e) => setType(e.target.value)}>
            <option value="linear">Linéaire</option>
            <option value="striped">Agrégé (striped)</option>
            <option value="raid1">RAID 1 LVM</option>
            <option value="raid5">RAID 5 LVM</option>
            <option value="raid6">RAID 6 LVM</option>
            <option value="raid10">RAID 10 LVM</option>
          </Select>
        </Field>
        {type !== "linear" && type !== "raid1" && <Field label="Nombre de bandes"><Input type="number" min={2} value={stripes} onChange={(e) => setStripes(+e.target.value)} /></Field>}
        <Notice>Créez ensuite un volume (ext4, XFS ou Btrfs) sur ce volume logique depuis la page Volumes.</Notice>
      </div>
    </Modal>
  );
}

export default function Lvm() {
  const { confirm } = useUI();
  const [create, setCreate] = useState(false);
  const [newLV, setNewLV] = useState<VG | null>(null);
  const [extend, setExtend] = useState<VG | null>(null);
  const [extDev, setExtDev] = useState<string[]>([]);
  const [resize, setResize] = useState<{ vg: string; lv: string; size: string } | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["lvm"], queryFn: () => api.get<VG[]>("/api/storage/lvm") });
  const delVG = useAction((n: string) => api.del(`/api/storage/lvm/${n}`), { success: "Groupe supprimé" });
  const delLV = useAction((p: { vg: string; lv: string }) => api.del(`/api/storage/lvm/${p.vg}/lv/${p.lv}`), { success: "Volume logique supprimé" });
  const ext = useAction((p: { vg: string; devices: string[] }) => api.post(`/api/storage/lvm/${p.vg}/extend`, { devices: p.devices }), { success: "Groupe étendu" });
  const rs = useAction((p: { vg: string; lv: string; size: number }) => api.post(`/api/storage/lvm/${p.vg}/lv/${p.lv}/resize`, { size: p.size }), { success: "Volume agrandi (système de fichiers inclus)" });
  const snap = useAction((p: { vg: string; lv: string }) => api.post(`/api/storage/lvm/${p.vg}/lv/${p.lv}/snapshot`, { name: `${p.lv}-snap-${Date.now().toString(36)}`, size: 0 }), { success: "Snapshot créé" });

  return (
    <div>
      <PageHeader title="LVM" subtitle="Groupes de volumes et volumes logiques" icon={<Server size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>Créer un groupe</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Server />} title="Aucun groupe de volumes" text="LVM permet de regrouper des disques et de découper des volumes redimensionnables à chaud." action={<Button variant="primary" onClick={() => setCreate(true)}>Créer un groupe</Button>} /></Card>
      ) : (
        <div className="space-y-5">
          {data.map((vg) => (
            <Card key={vg.name} title={<span className="flex items-center gap-2">{vg.name} <Badge>{vg.pvCount} PV</Badge><Badge>{vg.lvCount} LV</Badge></span>} actions={<>
              <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setNewLV(vg)}>Volume logique</Button>
              <Button size="sm" variant="ghost" icon={<Expand size={14} />} onClick={() => setExtend(vg)}>Étendre</Button>
              <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: `Supprimer ${vg.name} ?`, text: "Le groupe doit être vide.", danger: true })) delVG.mutate(vg.name); }} />
            </>}>
              <Progress value={pct(vg.size - vg.free, vg.size)} />
              <div className="mt-1.5 mb-4 flex justify-between text-[12px] text-muted"><span>{bytes(vg.size - vg.free)} alloués</span><span>{bytes(vg.free)} libres sur {bytes(vg.size)}</span></div>
              <div className="mb-3 flex flex-wrap gap-1.5">{vg.pvs.map((p) => <Badge key={p.name}>{p.name} · {bytes(p.size)}</Badge>)}</div>
              {vg.lvs.length > 0 && (
                <div className="overflow-x-auto rounded-xl border border-line">
                  <table className="table">
                    <thead><tr><th>Volume logique</th><th>Taille</th><th>Type</th><th>Contenu</th><th /></tr></thead>
                    <tbody>
                      {vg.lvs.map((lv) => (
                        <tr key={lv.name}>
                          <td><span className="mono">{lv.name}</span>{lv.snapshot && <span className="ml-2"><Badge tone="accent">snapshot de {lv.origin}</Badge></span>}</td>
                          <td>{bytes(lv.size)}</td><td>{lv.type}</td><td>{lv.fsType || <span className="text-muted">vide</span>}</td>
                          <td className="text-right whitespace-nowrap">
                            <Button size="sm" variant="ghost" onClick={() => setResize({ vg: vg.name, lv: lv.name, size: "" })}>Agrandir</Button>
                            {!lv.snapshot && <Button size="sm" variant="ghost" icon={<Camera size={13} />} onClick={() => snap.mutate({ vg: vg.name, lv: lv.name })} />}
                            <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={async () => { if (await confirm({ title: `Supprimer ${lv.name} ?`, text: "Les données du volume logique seront perdues.", danger: true, typeToConfirm: lv.name })) delLV.mutate({ vg: vg.name, lv: lv.name }); }} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          ))}
        </div>
      )}
      {create && <CreateVG onClose={() => setCreate(false)} />}
      {newLV && <CreateLV vg={newLV} onClose={() => setNewLV(null)} />}
      <Modal open={!!extend} onClose={() => setExtend(null)} title={`Étendre ${extend?.name}`} footer={<>
        <Button onClick={() => setExtend(null)}>Annuler</Button>
        <Button variant="primary" disabled={!extDev.length} onClick={() => { ext.mutate({ vg: extend!.name, devices: extDev }); setExtend(null); setExtDev([]); }}>Ajouter</Button>
      </>}>
        <DiskPicker value={extDev} onChange={setExtDev} includeVirtual />
      </Modal>
      <Modal open={!!resize} onClose={() => setResize(null)} title={`Agrandir ${resize?.lv}`} footer={<>
        <Button onClick={() => setResize(null)}>Annuler</Button>
        <Button variant="primary" onClick={() => { rs.mutate({ vg: resize!.vg, lv: resize!.lv, size: resize!.size ? parseSize(resize!.size) : 0 }); setResize(null); }}>Agrandir</Button>
      </>}>
        {resize && <Field label="Nouvelle taille totale" hint="ex. 200G — vide = utiliser tout l'espace libre du groupe. Le système de fichiers est agrandi automatiquement."><Input value={resize.size} onChange={(e) => setResize({ ...resize, size: e.target.value })} /></Field>}
      </Modal>
    </div>
  );
}
