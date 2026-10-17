import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Box, ExternalLink, FileText, Pause, Play, Plus, RotateCw, Square, SquareTerminal, Trash2, X } from "lucide-react";
import { api, wsURL } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Container, ContainerStats } from "../../lib/types";
import { bytes, cx } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Health, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { Terminal } from "../../components/Terminal";
import { tr, locale } from "../../lib/i18n";

export function DockerGuard({ children }: { children: React.ReactNode }) {
  const info = useQuery({ queryKey: ["docker-info"], queryFn: () => api.get<{ available: boolean; error?: string }>("/api/docker/info"), staleTime: 30000 });
  const install = useAction(() => api.post("/api/system/components/docker/install"));
  if (info.isLoading) return <Spinner />;
  if (!info.data?.available)
    return (
      <Card>
        <Empty icon={<Box />} title={tr("Docker Engine n'est pas disponible")} text={info.data?.error ?? tr("Installez Docker Engine pour gérer des conteneurs et des applications.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer Docker")}</Button>} />
      </Card>
    );
  return <>{children}</>;
}

const name = (c: Container) => c.Names[0]?.replace(/^\//, "") ?? c.Id.slice(0, 12);

export function LogsModal({ id, title, onClose }: { id: string; title: string; onClose: () => void }) {
  const [lines, setLines] = useState<{ s: string; l: string }[]>([]);
  const [follow, setFollow] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setLines([]);
    const ws = new WebSocket(wsURL(`/api/docker/containers/${id}/logs?tail=500`));
    const buf: { s: string; l: string }[] = [];
    let t: number | undefined;
    ws.onmessage = (e) => {
      try {
        buf.push(JSON.parse(e.data));
      } catch {
        buf.push({ s: "stderr", l: String(e.data) });
      }
      if (!t) t = window.setTimeout(() => { t = undefined; setLines((p) => [...p, ...buf.splice(0)].slice(-3000)); }, 100);
    };
    return () => { ws.close(); if (t) clearTimeout(t); };
  }, [id]);
  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);
  return (
    <Modal open onClose={onClose} title={tr("Journaux — {0}", title)} wide="xl" footer={<Switch checked={follow} onChange={setFollow} label={tr("Défilement automatique")} />}>
      <div ref={box} className="mono h-[60vh] overflow-auto rounded-xl bg-[#0b0f1c] p-3 text-[12px] leading-relaxed text-[#cbd3e6]">
        {lines.length === 0 && <span className="text-[#5d6785]">{tr("En attente des journaux…")}</span>}
        {lines.map((l, i) => {
          const sp = l.l.indexOf(" ");
          const ts = sp > 0 && /^\d{4}-/.test(l.l) ? l.l.slice(0, sp) : "";
          return (
            <div key={i} className={cx("whitespace-pre-wrap break-all", l.s === "stderr" && "text-[#f6a5a5]")}>
              {ts && <span className="text-[#5d6785]">{new Date(ts).toLocaleTimeString(locale)} </span>}
              {ts ? l.l.slice(sp + 1) : l.l}
            </div>
          );
        })}
      </div>
    </Modal>
  );
}

function ShellModal({ id, title, onClose }: { id: string; title: string; onClose: () => void }) {
  const [shell, setShell] = useState("/bin/sh");
  return (
    <Modal open onClose={onClose} title={tr("Console — {0}", title)} wide="xl" footer={
      <Select className="!w-auto" value={shell} onChange={(e) => setShell(e.target.value)}>
        <option value="/bin/sh">/bin/sh</option><option value="/bin/bash">/bin/bash</option><option value="/bin/ash">/bin/ash</option>
      </Select>
    }>
      <Terminal key={shell} path={`/api/docker/containers/${id}/exec?shell=${encodeURIComponent(shell)}`} height="60vh" />
    </Modal>
  );
}

type KV = { k: string; v: string };
type PortRow = { host: string; container: string; protocol: string };
type VolRow = { source: string; target: string; readOnly: boolean };

function CreateModal({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState({ name: "", image: "", restart: "unless-stopped", network: "", privileged: false, memoryMb: 0, cpus: 0, command: "" });
  const [ports, setPorts] = useState<PortRow[]>([]);
  const [vols, setVols] = useState<VolRow[]>([]);
  const [env, setEnv] = useState<KV[]>([]);
  const networks = useQuery({ queryKey: ["docker-networks"], queryFn: () => api.get<{ Name: string }[]>("/api/docker/networks") });
  const create = useAction(() => api.post("/api/docker/containers", {
    ...f, start: true,
    command: f.command ? f.command.match(/(?:[^\s"]+|"[^"]*")+/g)?.map((s) => s.replace(/^"|"$/g, "")) : [],
    ports: ports.filter((p) => p.container).map((p) => ({ host: +p.host || 0, container: +p.container, protocol: p.protocol })),
    volumes: vols.filter((v) => v.source && v.target),
    env: Object.fromEntries(env.filter((e) => e.k).map((e) => [e.k, e.v])),
  }), { onDone: onClose });
  const set = (k: keyof typeof f, v: string | number | boolean) => setF((p) => ({ ...p, [k]: v }));
  return (
    <Modal open onClose={onClose} title={tr("Nouveau conteneur")} wide footer={<>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" disabled={!f.image} loading={create.isPending} onClick={() => create.mutate()}>{tr("Créer et démarrer")}</Button>
    </>}>
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Image")} hint={tr("ex. nginx:latest, ghcr.io/org/app:1.2")}><Input value={f.image} onChange={(e) => set("image", e.target.value)} /></Field>
          <Field label={tr("Nom (optionnel)")}><Input value={f.name} onChange={(e) => set("name", e.target.value)} /></Field>
          <Field label={tr("Redémarrage")}>
            <Select value={f.restart} onChange={(e) => set("restart", e.target.value)}>
              <option value="unless-stopped">{tr("Sauf arrêt manuel")}</option><option value="always">{tr("Toujours")}</option><option value="on-failure">{tr("En cas d'échec")}</option><option value="no">{tr("Jamais")}</option>
            </Select>
          </Field>
          <Field label={tr("Réseau")}>
            <Select value={f.network} onChange={(e) => set("network", e.target.value)}>
              <option value="">{tr("bridge (par défaut)")}</option>
              {networks.data?.filter((n) => n.Name !== "bridge").map((n) => <option key={n.Name} value={n.Name}>{n.Name}</option>)}
            </Select>
          </Field>
          <Field label={tr("Limite mémoire (Mo, 0 = aucune)")}><Input type="number" min={0} value={f.memoryMb} onChange={(e) => set("memoryMb", +e.target.value)} /></Field>
          <Field label={tr("Limite CPU (cœurs, 0 = aucune)")}><Input type="number" min={0} step={0.5} value={f.cpus} onChange={(e) => set("cpus", +e.target.value)} /></Field>
        </div>
        <Field label={tr("Commande (optionnelle)")}><Input className="mono" value={f.command} onChange={(e) => set("command", e.target.value)} /></Field>

        <Rows title={tr("Ports")} add={() => setPorts([...ports, { host: "", container: "", protocol: "tcp" }])}>
          {ports.map((p, i) => (
            <div key={i} className="flex gap-2">
              <Input placeholder={tr("Port hôte")} value={p.host} onChange={(e) => setPorts(ports.map((x, k) => (k === i ? { ...x, host: e.target.value } : x)))} />
              <Input placeholder={tr("Port conteneur")} value={p.container} onChange={(e) => setPorts(ports.map((x, k) => (k === i ? { ...x, container: e.target.value } : x)))} />
              <Select className="!w-28" value={p.protocol} onChange={(e) => setPorts(ports.map((x, k) => (k === i ? { ...x, protocol: e.target.value } : x)))}><option>tcp</option><option>udp</option></Select>
              <IconButton title={tr("Retirer")} onClick={() => setPorts(ports.filter((_, k) => k !== i))}><X size={15} /></IconButton>
            </div>
          ))}
        </Rows>
        <Rows title={tr("Volumes")} add={() => setVols([...vols, { source: "/srv/strata/", target: "", readOnly: false }])}>
          {vols.map((v, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input placeholder={tr("Chemin hôte ou volume")} value={v.source} onChange={(e) => setVols(vols.map((x, k) => (k === i ? { ...x, source: e.target.value } : x)))} />
              <Input placeholder={tr("Chemin conteneur")} value={v.target} onChange={(e) => setVols(vols.map((x, k) => (k === i ? { ...x, target: e.target.value } : x)))} />
              <Switch checked={v.readOnly} onChange={(ro) => setVols(vols.map((x, k) => (k === i ? { ...x, readOnly: ro } : x)))} label="RO" />
              <IconButton title={tr("Retirer")} onClick={() => setVols(vols.filter((_, k) => k !== i))}><X size={15} /></IconButton>
            </div>
          ))}
        </Rows>
        <Rows title={tr("Variables d'environnement")} add={() => setEnv([...env, { k: "", v: "" }])}>
          {env.map((e, i) => (
            <div key={i} className="flex gap-2">
              <Input className="mono" placeholder="NOM" value={e.k} onChange={(ev) => setEnv(env.map((x, k) => (k === i ? { ...x, k: ev.target.value } : x)))} />
              <Input className="mono" placeholder="valeur" value={e.v} onChange={(ev) => setEnv(env.map((x, k) => (k === i ? { ...x, v: ev.target.value } : x)))} />
              <IconButton title={tr("Retirer")} onClick={() => setEnv(env.filter((_, k) => k !== i))}><X size={15} /></IconButton>
            </div>
          ))}
        </Rows>
        <Switch checked={f.privileged} onChange={(v) => set("privileged", v)} label={tr("Mode privilégié")} hint={tr("Accès complet au matériel de l'hôte. À éviter sauf nécessité.")} />
      </div>
    </Modal>
  );
}

function Rows({ title, add, children }: { title: string; add: () => void; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-2 flex items-center justify-between text-[12.5px] font-medium text-muted">{title}<Button size="sm" variant="ghost" icon={<Plus size={13} />} onClick={add}>{tr("Ajouter")}</Button></div>
      <div className="space-y-2">{children}</div>
    </div>
  );
}

function Detail({ id, onClose }: { id: string; onClose: () => void }) {
  const { data, isLoading } = useQuery({ queryKey: ["container", id], queryFn: () => api.get<Record<string, unknown>>(`/api/docker/containers/${id}`) });
  return (
    <Modal open onClose={onClose} title={tr("Inspection du conteneur")} wide="xl">
      {isLoading ? <Spinner /> : <pre className="mono max-h-[65vh] overflow-auto rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{JSON.stringify(data, null, 2)}</pre>}
    </Modal>
  );
}

export default function Containers() {
  const { confirm } = useUI();
  const [logs, setLogs] = useState<Container | null>(null);
  const [shell, setShell] = useState<Container | null>(null);
  const [inspect, setInspect] = useState<string | null>(null);
  const [create, setCreate] = useState(false);
  const [filter, setFilter] = useState("");
  const { data, isLoading, error } = useQuery({ queryKey: ["containers"], queryFn: () => api.get<Container[]>("/api/docker/containers"), refetchInterval: 5000 });
  const stats = useQuery({ queryKey: ["container-stats"], queryFn: () => api.get<Record<string, ContainerStats>>("/api/docker/stats"), refetchInterval: 5000 });
  const act = useAction((p: { id: string; action: string }) => api.post(`/api/docker/containers/${p.id}/${p.action}`));
  const del = useAction((id: string) => api.del(`/api/docker/containers/${id}`), { success: tr("Conteneur supprimé") });
  const host = location.hostname;
  const list = (data ?? []).filter((c) => !filter || name(c).includes(filter) || c.Image.includes(filter));

  return (
    <div>
      <PageHeader title={tr("Conteneurs")} subtitle={tr("Gestion de Docker Engine")} icon={<Box size={20} />} actions={<>
        <Input className="!w-52" placeholder={tr("Filtrer…")} value={filter} onChange={(e) => setFilter(e.target.value)} />
        <Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Nouveau conteneur")}</Button>
      </>} />
      <DockerGuard>
        <ErrorBox error={error} />
        <Card pad={false}>
          {isLoading ? <Spinner /> : !list.length ? <Empty icon={<Box />} title={tr("Aucun conteneur")} text={tr("Installez une application depuis l'App Center ou créez un conteneur.")} /> : (
            <div className="overflow-x-auto">
              <table className="table">
                <thead><tr><th>{tr("Nom")}</th><th>{tr("État")}</th><th>{tr("Image")}</th><th>{tr("Ports")}</th><th>CPU</th><th>{tr("Mémoire")}</th><th /></tr></thead>
                <tbody>
                  {list.map((c) => {
                    const st = stats.data?.[c.Id];
                    const running = c.State === "running";
                    const stack = c.Labels["com.docker.compose.project"];
                    const ports = [...new Map(c.Ports.filter((p) => p.PublicPort).map((p) => [`${p.PublicPort}/${p.Type}`, p])).values()];
                    return (
                      <tr key={c.Id}>
                        <td>
                          <button className="whitespace-nowrap text-left font-medium hover:text-accent" onClick={() => setInspect(c.Id)}>{name(c)}</button>
                          <div className="min-w-[180px] text-muted text-[11.5px]">{c.Status}{stack && <> {" "}{tr("· stack")}{" "}<b>{stack}</b></>}</div>
                        </td>
                        <td><Health value={c.State} /></td>
                        <td className="mono max-w-[260px] truncate text-muted" title={c.Image}>{c.Image}</td>
                        <td>
                          <div className="flex flex-wrap gap-1">
                            {ports.map((p) => p.Type === "tcp" ? (
                              <a key={`${p.PublicPort}${p.Type}`} href={`http://${host}:${p.PublicPort}`} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 rounded-md bg-accent-soft px-1.5 py-0.5 text-[11.5px] text-accent hover:underline">{p.PublicPort}<ExternalLink size={10} /></a>
                            ) : <Badge key={`${p.PublicPort}${p.Type}`}>{p.PublicPort}/udp</Badge>)}
                          </div>
                        </td>
                        <td className="text-[12.5px]">{st ? `${st.cpuPercent.toFixed(1)} %` : "—"}</td>
                        <td className="text-[12.5px]">{st ? bytes(st.memUsage) : "—"}</td>
                        <td className="text-right whitespace-nowrap">
                          {running ? (
                            <>
                              <IconButton title={tr("Arrêter")} onClick={() => act.mutate({ id: c.Id, action: "stop" })}><Square size={15} /></IconButton>
                              <IconButton title={tr("Redémarrer")} onClick={() => act.mutate({ id: c.Id, action: "restart" })}><RotateCw size={15} /></IconButton>
                              <IconButton title={tr("Pause")} onClick={() => act.mutate({ id: c.Id, action: "pause" })}><Pause size={15} /></IconButton>
                              <IconButton title={tr("Console")} onClick={() => setShell(c)}><SquareTerminal size={15} /></IconButton>
                            </>
                          ) : c.State === "paused" ? (
                            <IconButton title={tr("Reprendre")} onClick={() => act.mutate({ id: c.Id, action: "unpause" })}><Play size={15} /></IconButton>
                          ) : (
                            <IconButton title={tr("Démarrer")} onClick={() => act.mutate({ id: c.Id, action: "start" })}><Play size={15} /></IconButton>
                          )}
                          <IconButton title={tr("Journaux")} onClick={() => setLogs(c)}><FileText size={15} /></IconButton>
                          <IconButton title={tr("Supprimer")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer {0} ?", name(c)), text: stack ? tr("Ce conteneur appartient à la stack « {0} » : il sera recréé au prochain déploiement.", stack) : tr("Le conteneur sera arrêté et supprimé. Ses volumes nommés sont conservés."), danger: true })) del.mutate(c.Id); }}><Trash2 size={15} /></IconButton>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>
        {act.isPending && <div className="mt-3"><Notice>{tr("Opération en cours…")}</Notice></div>}
      </DockerGuard>
      {logs && <LogsModal id={logs.Id} title={name(logs)} onClose={() => setLogs(null)} />}
      {shell && <ShellModal id={shell.Id} title={name(shell)} onClose={() => setShell(null)} />}
      {inspect && <Detail id={inspect} onClose={() => setInspect(null)} />}
      {create && <CreateModal onClose={() => setCreate(false)} />}
    </div>
  );
}
