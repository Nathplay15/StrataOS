import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Boxes, Camera, Download, FolderPlus, Plus, RotateCcw, ShieldCheck, Trash2, X } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { ZDataset, ZPool } from "../../lib/types";
import { bytes, cx, dateTime, parseSize, pct } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Health, Input, Modal, Notice, PageHeader, Progress, Select, Spinner, Switch, Tabs, useUI } from "../../components/ui";
import { DiskPicker } from "../../components/DiskPicker";

type VDev = { type: string; disks: string[] };
const vdevTypes = [
  { id: "mirror", label: "Miroir", min: 2, hint: "n-1 pannes tolérées par vdev" },
  { id: "raidz1", label: "RAIDZ1", min: 3, hint: "1 panne tolérée" },
  { id: "raidz2", label: "RAIDZ2", min: 4, hint: "2 pannes tolérées" },
  { id: "raidz3", label: "RAIDZ3", min: 5, hint: "3 pannes tolérées" },
  { id: "stripe", label: "Stripe", min: 1, hint: "aucune redondance" },
];

function CreatePool({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("tank");
  const [vdevs, setVdevs] = useState<VDev[]>([{ type: "mirror", disks: [] }]);
  const [cache, setCache] = useState<string[]>([]);
  const [log, setLog] = useState<string[]>([]);
  const [spares, setSpares] = useState<string[]>([]);
  const [compression, setCompression] = useState("lz4");
  const [encryption, setEncryption] = useState(false);
  const [passphrase, setPassphrase] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const { confirm } = useUI();
  const create = useAction(() => api.post("/api/storage/zfs", { name, vdevs, cache, log, spares, compression, encryption, passphrase }), { onDone: onClose });
  const used = (except: string[]) => [...vdevs.flatMap((v) => v.disks), ...cache, ...log, ...spares].filter((d) => !except.includes(d));
  const valid = name && vdevs.every((v) => v.disks.length >= (vdevTypes.find((t) => t.id === v.type)?.min ?? 1)) && (!encryption || passphrase.length >= 8);
  const submit = async () => {
    if (await confirm({ title: `Créer le pool ${name} ?`, text: <>Tous les disques sélectionnés seront <b>effacés</b>.</>, danger: true, confirm: "Créer le pool" })) create.mutate();
  };
  return (
    <Modal open onClose={onClose} title="Nouveau pool ZFS" wide footer={<>
      <Button onClick={onClose}>Annuler</Button>
      <Button variant="primary" onClick={submit} loading={create.isPending} disabled={!valid}>Créer</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Nom du pool" hint={`Monté dans /srv/strata/${name}`}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
          <Field label="Compression">
            <Select value={compression} onChange={(e) => setCompression(e.target.value)}>
              <option value="lz4">lz4 (recommandé)</option><option value="zstd">zstd</option><option value="gzip">gzip</option><option value="off">désactivée</option>
            </Select>
          </Field>
        </div>
        {vdevs.map((v, i) => {
          const t = vdevTypes.find((x) => x.id === v.type)!;
          return (
            <div key={i} className="rounded-xl border border-line p-4">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <span className="font-medium">vdev {i + 1}</span>
                <div className="flex flex-wrap gap-1">
                  {vdevTypes.map((x) => (
                    <button key={x.id} type="button" onClick={() => setVdevs(vdevs.map((y, k) => (k === i ? { ...y, type: x.id } : y)))} className={cx("rounded-lg px-2.5 py-1 text-[12px]", v.type === x.id ? "bg-accent text-white" : "bg-panel-2 text-muted hover:text-fg")}>{x.label}</button>
                  ))}
                </div>
                <span className="text-muted text-[12px]">min. {t.min} disques · {t.hint}</span>
                <div className="flex-1" />
                {vdevs.length > 1 && <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => setVdevs(vdevs.filter((_, k) => k !== i))} />}
              </div>
              <DiskPicker value={v.disks} onChange={(d) => setVdevs(vdevs.map((y, k) => (k === i ? { ...y, disks: d } : y)))} exclude={used(v.disks)} />
            </div>
          );
        })}
        <Button size="sm" icon={<Plus size={14} />} onClick={() => setVdevs([...vdevs, { type: vdevs[0].type, disks: [] }])}>Ajouter un vdev (agrégation)</Button>
        <Switch checked={encryption} onChange={setEncryption} label="Chiffrement natif AES-256-GCM" hint="La phrase de passe sera demandée après chaque redémarrage pour déverrouiller le pool." />
        {encryption && <Field label="Phrase de passe"><Input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} /></Field>}
        <Switch checked={advanced} onChange={setAdvanced} label="Périphériques spéciaux (cache L2ARC, journal SLOG, secours)" />
        {advanced && (
          <div className="space-y-4">
            <div><div className="mb-2 text-[12.5px] font-medium text-muted">Cache L2ARC (SSD)</div><DiskPicker value={cache} onChange={setCache} exclude={used(cache)} /></div>
            <div><div className="mb-2 text-[12.5px] font-medium text-muted">Journal SLOG (SSD rapide)</div><DiskPicker value={log} onChange={setLog} exclude={used(log)} /></div>
            <div><div className="mb-2 text-[12.5px] font-medium text-muted">Disques de secours</div><DiskPicker value={spares} onChange={setSpares} exclude={used(spares)} /></div>
          </div>
        )}
      </div>
    </Modal>
  );
}

function Datasets({ pool }: { pool: ZPool }) {
  const { confirm } = useUI();
  const [form, setForm] = useState<{ name: string; compression: string; quota: string } | null>(null);
  const create = useAction(() => api.post("/api/storage/zfs-datasets", { name: `${pool.name}/${form!.name}`, compression: form!.compression, quota: form!.quota ? parseSize(form!.quota) : 0 }), { success: "Dataset créé", onDone: () => setForm(null) });
  const destroy = useAction((n: string) => api.del(`/api/storage/zfs-datasets${q({ name: n, recursive: 1 })}`), { success: "Dataset supprimé" });
  const snap = useAction((n: string) => api.post("/api/storage/zfs-snapshots", { dataset: n }), { success: "Snapshot créé" });
  return (
    <div>
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[12.5px] font-medium text-muted">Datasets</div>
        <Button size="sm" icon={<FolderPlus size={14} />} onClick={() => setForm({ name: "", compression: "", quota: "" })}>Nouveau dataset</Button>
      </div>
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="table">
          <thead><tr><th>Nom</th><th>Utilisé</th><th>Disponible</th><th>Compression</th><th>Quota</th><th>Point de montage</th><th /></tr></thead>
          <tbody>
            {pool.datasets?.map((d) => (
              <tr key={d.name}>
                <td className="mono">{d.name}{d.type === "volume" && <Badge>zvol</Badge>}</td>
                <td>{bytes(d.used)}</td><td>{bytes(d.avail)}</td><td>{d.compression}</td><td>{d.quota ? bytes(d.quota) : "—"}</td>
                <td className="mono text-muted">{d.mountpoint}</td>
                <td className="text-right whitespace-nowrap">
                  <Button size="sm" variant="ghost" icon={<Camera size={13} />} onClick={() => snap.mutate(d.name)}>Snapshot</Button>
                  {d.name.includes("/") && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={async () => { if (await confirm({ title: `Détruire ${d.name} ?`, text: "Le dataset, ses enfants et ses snapshots seront détruits.", danger: true, typeToConfirm: d.name })) destroy.mutate(d.name); }} />}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Modal open={!!form} onClose={() => setForm(null)} title="Nouveau dataset" footer={<>
        <Button onClick={() => setForm(null)}>Annuler</Button>
        <Button variant="primary" disabled={!form?.name} loading={create.isPending} onClick={() => create.mutate()}>Créer</Button>
      </>}>
        {form && (
          <div className="space-y-4">
            <Field label="Nom" hint={`${pool.name}/${form.name || "…"}`}><Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Field>
            <Field label="Compression"><Select value={form.compression} onChange={(e) => setForm({ ...form, compression: e.target.value })}><option value="">Héritée</option><option value="lz4">lz4</option><option value="zstd">zstd</option><option value="off">désactivée</option></Select></Field>
            <Field label="Quota" hint="ex. 500G, 2T — vide pour aucun"><Input value={form.quota} onChange={(e) => setForm({ ...form, quota: e.target.value })} /></Field>
          </div>
        )}
      </Modal>
    </div>
  );
}

function PoolCard({ p }: { p: ZPool }) {
  const { confirm } = useUI();
  const [showStatus, setShowStatus] = useState(false);
  const [add, setAdd] = useState<string | null>(null);
  const [dev, setDev] = useState<string[]>([]);
  const [replace, setReplace] = useState<string | null>(null);
  const act = useAction((a: { action: string; device?: string; newDevice?: string }) => api.post(`/api/storage/zfs/${p.name}/${a.action}`, { device: a.device ?? "", newDevice: a.newDevice ?? "" }), { success: "Opération effectuée" });
  const destroy = useAction(() => api.del(`/api/storage/zfs/${p.name}`), { success: "Pool détruit" });
  return (
    <Card title={<span className="flex items-center gap-2.5"><Boxes size={17} className="text-accent" />{p.name} <Health value={p.health === "ONLINE" ? "healthy" : p.health.toLowerCase()} /></span>} actions={<>
      <Button size="sm" variant="ghost" icon={<ShieldCheck size={14} />} onClick={() => act.mutate({ action: "scrub" })}>Scrub</Button>
      <Select className="!w-auto !py-1 !text-[12.5px]" value="" onChange={(e) => { const v = e.target.value; if (v.startsWith("add-")) setAdd(v); else if (v) act.mutate({ action: v }); }}>
        <option value="">Plus…</option>
        <option value="add-spare">Ajouter un secours</option><option value="add-cache">Ajouter un cache</option><option value="add-log">Ajouter un journal</option>
        <option value="clear">Effacer les erreurs</option><option value="trim">TRIM</option><option value="scrub-stop">Arrêter le scrub</option><option value="export">Exporter</option>
      </Select>
      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: `Détruire le pool ${p.name} ?`, text: "Toutes les données du pool seront définitivement perdues.", danger: true, typeToConfirm: p.name })) destroy.mutate(); }} />
    </>}>
      <div className="grid gap-4 sm:grid-cols-4">
        <div><div className="text-muted text-[12px]">Taille</div><div className="font-semibold">{bytes(p.size)}</div></div>
        <div><div className="text-muted text-[12px]">Alloué</div><div className="font-semibold">{bytes(p.alloc)}</div></div>
        <div><div className="text-muted text-[12px]">Fragmentation</div><div className="font-semibold">{p.fragmentation}%</div></div>
        <div><div className="text-muted text-[12px]">Déduplication</div><div className="font-semibold">{p.dedup}</div></div>
      </div>
      <Progress className="mt-4" value={pct(p.alloc, p.size)} />
      {!!p.problems?.length && (
        <div className="mt-4 space-y-2">
          {p.problems.map((d) => (
            <div key={d.device} className="flex flex-wrap items-center gap-3 rounded-xl border border-danger/30 bg-danger/10 px-4 py-3 text-[13px]">
              <Badge tone="danger">{d.state}</Badge>
              <span className="mono min-w-0 flex-1 truncate">{d.was || d.device}</span>
              <Button size="sm" variant="primary" onClick={() => { setDev([]); setReplace(d.device); }}>Remplacer</Button>
            </div>
          ))}
        </div>
      )}
      <button className="mt-4 text-accent text-[12.5px]" onClick={() => setShowStatus(!showStatus)}>{showStatus ? "Masquer" : "Afficher"} la topologie et l'état détaillé</button>
      {showStatus && <pre className="mono mt-2 overflow-x-auto rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{p.status}</pre>}
      <div className="mt-5"><Datasets pool={p} /></div>
      <Modal open={!!replace} onClose={() => setReplace(null)} title="Remplacer un disque" footer={<>
        <Button onClick={() => setReplace(null)}>Annuler</Button>
        <Button variant="primary" disabled={!dev.length} loading={act.isPending} onClick={() => { act.mutate({ action: "replace", device: replace!, newDevice: dev[0] }); setReplace(null); setDev([]); }}>Remplacer et reconstruire</Button>
      </>}>
        <div className="space-y-3">
          <Notice>Le nouveau disque sera effacé, puis ZFS recopiera les données (resilver). Le pool reste utilisable pendant l'opération.</Notice>
          <DiskPicker value={dev} onChange={setDev} single />
        </div>
      </Modal>
      <Modal open={!!add} onClose={() => setAdd(null)} title="Ajouter un périphérique au pool" footer={<>
        <Button onClick={() => setAdd(null)}>Annuler</Button>
        <Button variant="primary" disabled={!dev.length} onClick={() => { act.mutate({ action: add!, device: dev[0] }); setAdd(null); setDev([]); }}>Ajouter</Button>
      </>}>
        <DiskPicker value={dev} onChange={setDev} single />
      </Modal>
    </Card>
  );
}

function Snapshots() {
  const { confirm } = useUI();
  const { data, isLoading } = useQuery({ queryKey: ["zfs-snapshots"], queryFn: () => api.get<ZDataset[]>("/api/storage/zfs-snapshots") });
  const destroy = useAction((n: string) => api.del(`/api/storage/zfs-datasets${q({ name: n })}`), { success: "Snapshot supprimé" });
  const rollback = useAction((n: string) => api.post("/api/storage/zfs-snapshots/rollback", { snapshot: n }), { success: "Restauration effectuée" });
  if (isLoading) return <Spinner />;
  if (!data?.length) return <Card><Empty icon={<Camera />} title="Aucun snapshot" text="Créez des snapshots depuis la liste des datasets. Ils sont instantanés et ne consomment que les différences." /></Card>;
  return (
    <Card pad={false}>
      <div className="overflow-x-auto">
        <table className="table">
          <thead><tr><th>Snapshot</th><th>Créé le</th><th>Utilisé</th><th>Référencé</th><th /></tr></thead>
          <tbody>
            {data.map((s) => (
              <tr key={s.name}>
                <td className="mono">{s.name}</td><td>{dateTime((s.creation ?? 0) * 1000)}</td><td>{bytes(s.used)}</td><td>{bytes(s.refer)}</td>
                <td className="text-right whitespace-nowrap">
                  <Button size="sm" variant="ghost" icon={<RotateCcw size={13} />} onClick={async () => { if (await confirm({ title: "Restaurer ce snapshot ?", text: "Le dataset reviendra à cet état. Les modifications et snapshots plus récents seront perdus.", danger: true, confirm: "Restaurer" })) rollback.mutate(s.name); }}>Restaurer</Button>
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} onClick={() => destroy.mutate(s.name)} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

export default function Zfs() {
  const [tab, setTab] = useState<"pools" | "snapshots">("pools");
  const [create, setCreate] = useState(false);
  const [imp, setImp] = useState(false);
  const { data, isLoading, error } = useQuery({ queryKey: ["zfs"], queryFn: () => api.get<{ available: boolean; broken?: boolean; pools: ZPool[] }>("/api/storage/zfs"), refetchInterval: 10000 });
  const importable = useQuery({ queryKey: ["zfs-importable"], queryFn: () => api.get<{ name: string; id: string; state: string }[]>("/api/storage/zfs/importable"), enabled: imp });
  const doImport = useAction((n: string) => api.post("/api/storage/zfs/import", { name: n }), { success: "Pool importé", onDone: () => setImp(false) });
  const install = useAction(() => api.post("/api/system/components/zfs/install"));

  if (isLoading) return <Spinner />;
  return (
    <div>
      <PageHeader title="ZFS" subtitle="Pools, datasets et snapshots OpenZFS" icon={<Boxes size={20} />} actions={data?.available && <>
        <Button icon={<Download size={15} />} onClick={() => setImp(true)}>Importer</Button>
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>Créer un pool</Button>
      </>} />
      <ErrorBox error={error} />
      {!data?.available ? (
        <Card>
          {data?.broken ? (
            <Empty icon={<Boxes />} title="Le module ZFS ne se charge pas" text="Les outils ZFS sont installés mais le module noyau manque, en général après une mise à jour du noyau. La réparation recompile le module ; vos pools seront ensuite réimportés." action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>Réparer ZFS</Button>} />
          ) : (
            <Empty icon={<Boxes />} title="ZFS n'est pas installé" text="OpenZFS est disponible dans la section contrib de Debian. L'installation compile le module noyau (DKMS) et peut prendre plusieurs minutes." action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>Installer ZFS</Button>} />
          )}
        </Card>
      ) : (
        <>
          <Tabs value={tab} onChange={setTab} tabs={[{ id: "pools", label: `Pools (${data.pools.length})` }, { id: "snapshots", label: "Snapshots" }]} />
          {tab === "pools" ? (
            !data.pools.length ? <Card><Empty icon={<Boxes />} title="Aucun pool ZFS" text="Un pool ZFS combine disques, redondance (miroir, RAIDZ), intégrité des données, compression et snapshots." action={<Button variant="primary" onClick={() => setCreate(true)}>Créer un pool</Button>} /></Card>
              : <div className="space-y-5">{data.pools.map((p) => <PoolCard key={p.name} p={p} />)}</div>
          ) : <Snapshots />}
        </>
      )}
      {create && <CreatePool onClose={() => setCreate(false)} />}
      <Modal open={imp} onClose={() => setImp(false)} title="Importer un pool existant">
        {importable.isLoading ? <Spinner /> : !importable.data?.length ? <Notice>Aucun pool importable détecté sur les disques.</Notice> : (
          <div className="space-y-2">
            {importable.data.map((p) => (
              <div key={p.id} className="flex items-center justify-between rounded-xl bg-panel-2 px-4 py-3">
                <div><b>{p.name}</b> <span className="text-muted text-[12px]">{p.id} · {p.state}</span></div>
                <Button size="sm" variant="primary" loading={doImport.isPending} onClick={() => doImport.mutate(p.name)}>Importer</Button>
              </div>
            ))}
          </div>
        )}
      </Modal>
    </div>
  );
}
