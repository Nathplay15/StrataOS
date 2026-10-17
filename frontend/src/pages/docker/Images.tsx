import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Download, Eraser, HardDrive, Layers, Network, Plus, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { DockerImage } from "../../lib/types";
import { bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, PageHeader, Select, Spinner, Tabs, useUI } from "../../components/ui";
import { DockerGuard } from "./Containers";
import { tr } from "../../lib/i18n";

type DVolume = { Name: string; Driver: string; Mountpoint: string; CreatedAt: string; Labels: Record<string, string> | null };
type DNetwork = { Id: string; Name: string; Driver: string; Scope: string; IPAM: { Config: { Subnet?: string; Gateway?: string }[] | null }; Containers?: Record<string, unknown> };

function ImagesTab() {
  const { confirm } = useUI();
  const [pull, setPull] = useState<string | null>(null);
  const { data, isLoading } = useQuery({ queryKey: ["images"], queryFn: () => api.get<DockerImage[]>("/api/docker/images") });
  const doPull = useAction((img: string) => api.post("/api/docker/images/pull", { image: img }), { onDone: () => setPull(null) });
  const del = useAction((id: string) => api.del(`/api/docker/images/${encodeURIComponent(id)}`), { success: tr("Image supprimée") });
  return (
    <Card pad={false} title={tr("Images ({0})", data?.length ?? 0)} actions={<Button size="sm" variant="primary" icon={<Download size={14} />} onClick={() => setPull("")}>{tr("Télécharger une image")}</Button>}>
      {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<Layers />} title={tr("Aucune image")} /> : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>{tr("Dépôt : tag")}</th><th>ID</th><th>{tr("Taille")}</th><th>{tr("Créée")}</th><th>{tr("Utilisation")}</th><th /></tr></thead>
            <tbody>
              {data.map((i) => (
                <tr key={i.Id}>
                  <td className="mono">{i.RepoTags?.filter((t) => t !== "<none>:<none>").join(", ") || <span className="text-muted">{tr("<sans tag>")}</span>}</td>
                  <td className="mono text-muted">{i.Id.replace("sha256:", "").slice(0, 12)}</td>
                  <td>{bytes(i.Size)}</td>
                  <td className="text-muted text-[12.5px]">{dateTime(i.Created * 1000)}</td>
                  <td>{i.Containers > 0 ? <Badge tone="ok">{i.Containers} conteneur(s)</Badge> : i.Containers === 0 ? <Badge>{tr("Inutilisée")}</Badge> : null}</td>
                  <td className="text-right">
                    <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Supprimer l'image ?"), text: i.RepoTags?.join(", ") || i.Id, danger: true })) del.mutate(i.Id); }} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal open={pull !== null} onClose={() => setPull(null)} title={tr("Télécharger une image")} footer={<>
        <Button onClick={() => setPull(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!pull} loading={doPull.isPending} onClick={() => doPull.mutate(pull!)}>{tr("Télécharger")}</Button>
      </>}>
        <Field label={tr("Image")} hint={tr("ex. nginx:alpine, ghcr.io/home-assistant/home-assistant:stable")}><Input autoFocus className="mono" value={pull ?? ""} onChange={(e) => setPull(e.target.value)} /></Field>
      </Modal>
    </Card>
  );
}

function VolumesTab() {
  const { confirm } = useUI();
  const [name, setName] = useState<string | null>(null);
  const { data, isLoading } = useQuery({ queryKey: ["docker-volumes"], queryFn: () => api.get<{ Volumes: DVolume[] | null }>("/api/docker/volumes") });
  const create = useAction((n: string) => api.post("/api/docker/volumes", { name: n }), { success: tr("Volume créé"), onDone: () => setName(null) });
  const del = useAction((n: string) => api.del(`/api/docker/volumes/${encodeURIComponent(n)}`), { success: tr("Volume supprimé") });
  const vols = data?.Volumes ?? [];
  return (
    <Card pad={false} title={tr("Volumes Docker ({0})", vols.length)} actions={<Button size="sm" variant="primary" icon={<Plus size={14} />} onClick={() => setName("")}>{tr("Nouveau volume")}</Button>}>
      {isLoading ? <Spinner /> : !vols.length ? <Empty icon={<HardDrive />} title={tr("Aucun volume Docker")} /> : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>{tr("Nom")}</th><th>{tr("Pilote")}</th><th>{tr("Stack")}</th><th>{tr("Emplacement")}</th><th /></tr></thead>
            <tbody>
              {vols.map((v) => (
                <tr key={v.Name}>
                  <td className="mono max-w-[280px] truncate">{v.Name}</td><td>{v.Driver}</td>
                  <td>{v.Labels?.["com.docker.compose.project"] ?? "—"}</td>
                  <td className="mono max-w-[320px] truncate text-muted">{v.Mountpoint}</td>
                  <td className="text-right"><Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Supprimer le volume ?"), text: tr("Les données de {0} seront perdues.", v.Name), danger: true })) del.mutate(v.Name); }} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal open={name !== null} onClose={() => setName(null)} title={tr("Nouveau volume Docker")} footer={<>
        <Button onClick={() => setName(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!name} onClick={() => create.mutate(name!)}>{tr("Créer")}</Button>
      </>}>
        <Field label={tr("Nom")}><Input autoFocus value={name ?? ""} onChange={(e) => setName(e.target.value)} /></Field>
      </Modal>
    </Card>
  );
}

function NetworksTab() {
  const { confirm } = useUI();
  const [form, setForm] = useState<{ name: string; driver: string; subnet: string; gateway: string } | null>(null);
  const { data, isLoading } = useQuery({ queryKey: ["docker-networks"], queryFn: () => api.get<DNetwork[]>("/api/docker/networks") });
  const create = useAction(() => api.post("/api/docker/networks", form), { success: tr("Réseau créé"), onDone: () => setForm(null) });
  const del = useAction((id: string) => api.del(`/api/docker/networks/${id}`), { success: tr("Réseau supprimé") });
  const builtin = ["bridge", "host", "none"];
  return (
    <Card pad={false} title={tr("Réseaux ({0})", data?.length ?? 0)} actions={<Button size="sm" variant="primary" icon={<Plus size={14} />} onClick={() => setForm({ name: "", driver: "bridge", subnet: "", gateway: "" })}>{tr("Nouveau réseau")}</Button>}>
      {isLoading ? <Spinner /> : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>{tr("Nom")}</th><th>{tr("Pilote")}</th><th>{tr("Sous-réseau")}</th><th>{tr("Conteneurs")}</th><th /></tr></thead>
            <tbody>
              {data?.map((n) => (
                <tr key={n.Id}>
                  <td className="font-medium">{n.Name}</td><td>{n.Driver}</td>
                  <td className="mono text-muted">{n.IPAM?.Config?.map((c) => c.Subnet).filter(Boolean).join(", ") || "—"}</td>
                  <td>{Object.keys(n.Containers ?? {}).length || "—"}</td>
                  <td className="text-right">{!builtin.includes(n.Name) && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Supprimer {0} ?", n.Name), text: tr("Le réseau doit être inutilisé."), danger: true })) del.mutate(n.Id); }} />}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal open={!!form} onClose={() => setForm(null)} title={tr("Nouveau réseau")} footer={<>
        <Button onClick={() => setForm(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!form?.name} loading={create.isPending} onClick={() => create.mutate()}>{tr("Créer")}</Button>
      </>}>
        {form && (
          <div className="space-y-4">
            <Field label={tr("Nom")}><Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Field>
            <Field label={tr("Pilote")}><Select value={form.driver} onChange={(e) => setForm({ ...form, driver: e.target.value })}><option value="bridge">bridge</option><option value="macvlan">macvlan</option><option value="ipvlan">ipvlan</option></Select></Field>
            <Field label={tr("Sous-réseau (optionnel)")} hint={tr("ex. 172.30.0.0/16")}><Input className="mono" value={form.subnet} onChange={(e) => setForm({ ...form, subnet: e.target.value })} /></Field>
            <Field label={tr("Passerelle (optionnelle)")}><Input className="mono" value={form.gateway} onChange={(e) => setForm({ ...form, gateway: e.target.value })} /></Field>
          </div>
        )}
      </Modal>
    </Card>
  );
}

function PruneModal({ onClose }: { onClose: () => void }) {
  const [o, setO] = useState({ containers: true, images: true, volumes: false, networks: true, buildCache: true });
  const prune = useAction(() => api.post<Record<string, { SpaceReclaimed?: number }>>("/api/docker/prune", o), {
    success: (r) => tr("Espace libéré : {0}", bytes(Object.values(r ?? {}).reduce((s, x) => s + (x?.SpaceReclaimed ?? 0), 0))),
    onDone: onClose,
  });
  return (
    <Modal open onClose={onClose} title={tr("Nettoyer Docker")} footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="danger" loading={prune.isPending} onClick={() => prune.mutate()}>{tr("Nettoyer")}</Button>
    </>}>
      <div className="space-y-3">
        <Checkbox checked={o.containers} onChange={(v) => setO({ ...o, containers: v })} label={tr("Conteneurs arrêtés")} />
        <Checkbox checked={o.images} onChange={(v) => setO({ ...o, images: v })} label={tr("Images inutilisées")} />
        <Checkbox checked={o.networks} onChange={(v) => setO({ ...o, networks: v })} label={tr("Réseaux inutilisés")} />
        <Checkbox checked={o.buildCache} onChange={(v) => setO({ ...o, buildCache: v })} label={tr("Cache de construction")} />
        <Checkbox checked={o.volumes} onChange={(v) => setO({ ...o, volumes: v })} label={<span className="text-danger">{tr("Volumes inutilisés (données perdues)")}</span>} />
      </div>
    </Modal>
  );
}

export default function Images() {
  const [tab, setTab] = useState<"images" | "volumes" | "networks">("images");
  const [prune, setPrune] = useState(false);
  const info = useQuery({ queryKey: ["docker-info"], queryFn: () => api.get<{ available: boolean; info?: { ServerVersion: string; DockerRootDir: string; Driver: string }; df?: { LayersSize: number } }>("/api/docker/info") });
  const i = info.data?.info;
  return (
    <div>
      <PageHeader title={tr("Images, volumes et réseaux")} subtitle={i ? tr("Docker {0} · stockage {1} · {2} · {3} d'images", i.ServerVersion, i.Driver, i.DockerRootDir, bytes(info.data?.df?.LayersSize ?? 0)) : tr("Docker Engine")} icon={<Network size={20} />} actions={<Button icon={<Eraser size={15} />} onClick={() => setPrune(true)}>{tr("Nettoyer")}</Button>} />
      <DockerGuard>
        <Tabs value={tab} onChange={setTab} tabs={[{ id: "images", label: tr("Images") }, { id: "volumes", label: tr("Volumes") }, { id: "networks", label: tr("Réseaux") }]} />
        <ErrorBox error={info.error} />
        {tab === "images" ? <ImagesTab /> : tab === "volumes" ? <VolumesTab /> : <NetworksTab />}
      </DockerGuard>
      {prune && <PruneModal onClose={() => setPrune(false)} />}
    </div>
  );
}
