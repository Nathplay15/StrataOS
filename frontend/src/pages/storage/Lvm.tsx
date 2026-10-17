import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Expand, Plus, Server, Trash2, Zap } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { VG } from "../../lib/types";
import { bytes, parseSize, pct } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";
import { tr } from "../../lib/i18n";

function CreateVG({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("vg0");
  const [devices, setDevices] = useState<string[]>([]);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/lvm", { name, devices }), { success: tr("Groupe de volumes créé"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Nouveau groupe de volumes")} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" disabled={!devices.length || !name} loading={create.isPending} onClick={async () => { if (await confirm({ title: tr("Créer le groupe ?"), text: tr("{0} seront effacés.", devices.join(", ")), danger: true })) create.mutate(); }}>{tr("Créer")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Nom")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Notice>{tr("Les disques (ou grappes RAID) sélectionnés deviennent des volumes physiques mis en commun. Vous pourrez ensuite y découper des volumes logiques redimensionnables.")}</Notice>
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
  const create = useAction(() => api.post(`/api/storage/lvm/${vg.name}/lv`, { name, size: size ? parseSize(size) : 0, type, stripes }), { success: tr("Volume logique créé"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Nouveau volume logique dans {0}", vg.name)} footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" disabled={!name || (!!size && isNaN(parseSize(size)))} loading={create.isPending} onClick={() => create.mutate()}>{tr("Créer")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tr("Nom")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label={tr("Taille")} hint={tr("ex. 100G — vide = tout l'espace libre ({0})", bytes(vg.free))}><Input value={size} onChange={(e) => setSize(e.target.value)} /></Field>
        <Field label={tr("Type")}>
          <Select value={type} onChange={(e) => setType(e.target.value)}>
            <option value="linear">{tr("Linéaire")}</option>
            <option value="striped">{tr("Agrégé (striped)")}</option>
            <option value="raid1">{tr("RAID 1 LVM")}</option>
            <option value="raid5">{tr("RAID 5 LVM")}</option>
            <option value="raid6">{tr("RAID 6 LVM")}</option>
            <option value="raid10">{tr("RAID 10 LVM")}</option>
          </Select>
        </Field>
        {type !== "linear" && type !== "raid1" && <Field label={tr("Nombre de bandes")}><Input type="number" min={2} value={stripes} onChange={(e) => setStripes(+e.target.value)} /></Field>}
        <Notice>{tr("Créez ensuite un volume (ext4, XFS ou Btrfs) sur ce volume logique depuis la page Volumes.")}</Notice>
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
  const [cache, setCache] = useState<{ vg: string; lv: string; dev: string[]; mode: string } | null>(null);
  const addCache = useAction((c: { vg: string; lv: string; device: string; mode: string }) => api.post(`/api/storage/lvm/${c.vg}/lv/${c.lv}/cache`, { device: c.device, mode: c.mode }));
  const delCache = useAction((c: { vg: string; lv: string }) => api.del(`/api/storage/lvm/${c.vg}/lv/${c.lv}/cache`));
  const { data, isLoading, error } = useQuery({ queryKey: ["lvm"], queryFn: () => api.get<VG[]>("/api/storage/lvm") });
  const delVG = useAction((n: string) => api.del(`/api/storage/lvm/${n}`), { success: tr("Groupe supprimé") });
  const delLV = useAction((p: { vg: string; lv: string }) => api.del(`/api/storage/lvm/${p.vg}/lv/${p.lv}`), { success: tr("Volume logique supprimé") });
  const ext = useAction((p: { vg: string; devices: string[] }) => api.post(`/api/storage/lvm/${p.vg}/extend`, { devices: p.devices }), { success: tr("Groupe étendu") });
  const rs = useAction((p: { vg: string; lv: string; size: number }) => api.post(`/api/storage/lvm/${p.vg}/lv/${p.lv}/resize`, { size: p.size }), { success: tr("Volume agrandi (système de fichiers inclus)") });
  const snap = useAction((p: { vg: string; lv: string }) => api.post(`/api/storage/lvm/${p.vg}/lv/${p.lv}/snapshot`, { name: `${p.lv}-snap-${Date.now().toString(36)}`, size: 0 }), { success: tr("Snapshot créé") });

  return (
    <div>
      <PageHeader title="LVM" subtitle={tr("Groupes de volumes et volumes logiques")} icon={<Server size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Créer un groupe")}</Button>} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? (
        <Card><Empty icon={<Server />} title={tr("Aucun groupe de volumes")} text={tr("LVM permet de regrouper des disques et de découper des volumes redimensionnables à chaud.")} action={<Button variant="primary" onClick={() => setCreate(true)}>{tr("Créer un groupe")}</Button>} /></Card>
      ) : (
        <div className="space-y-5">
          {data.map((vg) => (
            <Card key={vg.name} title={<span className="flex items-center gap-2">{vg.name} <Badge>{vg.pvCount} PV</Badge><Badge>{vg.lvCount} LV</Badge></span>} actions={<>
              <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setNewLV(vg)}>{tr("Volume logique")}</Button>
              <Button size="sm" variant="ghost" icon={<Expand size={14} />} onClick={() => setExtend(vg)}>{tr("Étendre")}</Button>
              <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Supprimer {0} ?", vg.name), text: tr("Le groupe doit être vide."), danger: true })) delVG.mutate(vg.name); }} />
            </>}>
              <Progress value={pct(vg.size - vg.free, vg.size)} />
              <div className="mt-1.5 mb-4 flex justify-between text-[12px] text-muted"><span>{bytes(vg.size - vg.free)} {" "}{tr("alloués")}</span><span>{bytes(vg.free)} {" "}{tr("libres sur")}{" "}{bytes(vg.size)}</span></div>
              <div className="mb-3 flex flex-wrap gap-1.5">{vg.pvs.map((p) => <Badge key={p.name}>{p.name} · {bytes(p.size)}</Badge>)}</div>
              {vg.lvs.length > 0 && (
                <div className="overflow-x-auto rounded-xl border border-line">
                  <table className="table">
                    <thead><tr><th>{tr("Volume logique")}</th><th>{tr("Taille")}</th><th>{tr("Type")}</th><th>{tr("Contenu")}</th><th /></tr></thead>
                    <tbody>
                      {vg.lvs.map((lv) => (
                        <tr key={lv.name}>
                          <td>
                            <span className="mono">{lv.name}</span>{lv.snapshot && <span className="ml-2"><Badge tone="accent">{tr("snapshot de")}{" "}{lv.origin}</Badge></span>}
                            {lv.cache && (
                              <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted">
                                <Badge tone="ok"><Zap size={11} className="mr-1 inline" />{tr("Cache SSD")} · {lv.cache.mode === "writeback" ? tr("écriture différée") : tr("écriture immédiate")}</Badge>
                                <span>{lv.cache.device}</span>
                                <span>{tr("{0} % rempli", lv.cache.usedPercent.toFixed(0))}</span>
                                {lv.cache.readHits + lv.cache.readMisses > 0 && <span>{tr("{0} % des lectures servies par le SSD", Math.round((lv.cache.readHits * 100) / (lv.cache.readHits + lv.cache.readMisses)))}</span>}
                              </div>
                            )}
                          </td>
                          <td>{bytes(lv.size)}</td><td>{lv.type}</td><td>{lv.fsType || <span className="text-muted">vide</span>}</td>
                          <td className="text-right whitespace-nowrap">
                            <Button size="sm" variant="ghost" onClick={() => setResize({ vg: vg.name, lv: lv.name, size: "" })}>{tr("Agrandir")}</Button>
                            {!lv.snapshot && <Button size="sm" variant="ghost" icon={<Camera size={13} />} onClick={() => snap.mutate({ vg: vg.name, lv: lv.name })} />}
                            {!lv.snapshot && (lv.cache
                              ? <Button size="sm" variant="ghost" onClick={async () => { if (await confirm({ title: tr("Retirer le cache SSD ?"), text: tr("Les données en attente sont d'abord copiées sur les disques ; le volume reste utilisable pendant l'opération.") })) delCache.mutate({ vg: vg.name, lv: lv.name }); }}>{tr("Retirer le cache")}</Button>
                              : <Button size="sm" variant="ghost" icon={<Zap size={13} />} onClick={() => setCache({ vg: vg.name, lv: lv.name, dev: [], mode: "writethrough" })}>{tr("Cache SSD")}</Button>)}
                            <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={async () => { if (await confirm({ title: tr("Supprimer {0} ?", lv.name), text: tr("Les données du volume logique seront perdues."), danger: true, typeToConfirm: lv.name })) delLV.mutate({ vg: vg.name, lv: lv.name }); }} />
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
      <Modal open={!!cache} onClose={() => setCache(null)} title={tr("Cache SSD pour {0}", cache?.lv)} wide footer={<>
        <Button onClick={() => setCache(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={cache?.dev.length !== 1} onClick={() => { addCache.mutate({ vg: cache!.vg, lv: cache!.lv, device: cache!.dev[0], mode: cache!.mode }); setCache(null); }}>{tr("Ajouter le cache")}</Button>
      </>}>
        {cache && (
          <div className="space-y-4">
            <p className="text-muted text-[13px]">{tr("Un SSD libre accélère le volume : les données souvent lues y sont gardées. Il s'ajoute et se retire sans arrêter le volume. Pour un volume sur une grappe RAID, créez d'abord un groupe LVM sur la grappe.")}</p>
            <DiskPicker value={cache.dev} onChange={(v) => setCache({ ...cache, dev: v.slice(-1) })} includeVirtual single />
            <Field label={tr("Mode")}>
              <Select value={cache.mode} onChange={(e) => setCache({ ...cache, mode: e.target.value })}>
                <option value="writethrough">{tr("Écriture immédiate (recommandé) : accélère les lectures, sans risque")}</option>
                <option value="writeback">{tr("Écriture différée : accélère aussi les écritures")}</option>
              </Select>
            </Field>
            {cache.mode === "writeback" && <Notice tone="warn">{tr("En écriture différée, les données pas encore recopiées sur les disques sont perdues si le SSD tombe en panne. Utilisez un SSD fiable, idéalement en miroir.")}</Notice>}
          </div>
        )}
      </Modal>
      {newLV && <CreateLV vg={newLV} onClose={() => setNewLV(null)} />}
      <Modal open={!!extend} onClose={() => setExtend(null)} title={tr("Étendre {0}", extend?.name)} footer={<>
        <Button onClick={() => setExtend(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!extDev.length} onClick={() => { ext.mutate({ vg: extend!.name, devices: extDev }); setExtend(null); setExtDev([]); }}>{tr("Ajouter")}</Button>
      </>}>
        <DiskPicker value={extDev} onChange={setExtDev} includeVirtual />
      </Modal>
      <Modal open={!!resize} onClose={() => setResize(null)} title={tr("Agrandir {0}", resize?.lv)} footer={<>
        <Button onClick={() => setResize(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" onClick={() => { rs.mutate({ vg: resize!.vg, lv: resize!.lv, size: resize!.size ? parseSize(resize!.size) : 0 }); setResize(null); }}>{tr("Agrandir")}</Button>
      </>}>
        {resize && <Field label={tr("Nouvelle taille totale")} hint={tr("ex. 200G — vide = utiliser tout l'espace libre du groupe. Le système de fichiers est agrandi automatiquement.")}><Input value={resize.size} onChange={(e) => setResize({ ...resize, size: e.target.value })} /></Field>}
      </Modal>
    </div>
  );
}
