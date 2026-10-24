import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Check, FileInput, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { bytes, cx, dateTime } from "../../../lib/format";
import type { Group, Share, User } from "../../../lib/types";
import { Button, Checkbox, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, Switch } from "../../../components/ui";
import { tr } from "../../../lib/i18n";
import type { LXCFeatures, LXCImage, LXCImportable, LXCLocation, LXCMount, LXCNetwork } from "./types";
import { backendLabel } from "./ContainerCard";

const nameRe = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

export function useLocations() {
  return useQuery({ queryKey: ["lxc-storage"], queryFn: () => api.get<LXCLocation[]>("/api/lxc/storage") });
}

export const backendHint: Record<string, string> = {
  btrfs: tr("Btrfs subvolume: instant snapshots and clones, size limit by quota."),
  zfs: tr("ZFS dataset: instant snapshots, size limit by quota. Restoring a snapshot removes the newer ones."),
  image: tr("Disk image (ext4 file): a hard size limit on any volume; snapshots copy the image."),
  dir: tr("Plain folder: simplest, but no size limit and snapshots are full copies."),
};

/** Network settings, shared by the wizard and the Network tab. */
export function NetworkFields({ n, set, bridges }: { n: LXCNetwork; set: (n: LXCNetwork) => void; bridges: string[] }) {
  const [dns, setDns] = useState((n.dns ?? []).join(" "));
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label={tr("Network")} className="sm:col-span-2">
        <Select value={n.mode === "bridge" ? `bridge:${n.bridge}` : n.mode} onChange={(e) => {
          const v = e.target.value;
          set(v.startsWith("bridge:") ? { ...n, mode: "bridge", bridge: v.slice(7) } : { ...n, mode: v as LXCNetwork["mode"], bridge: "" });
        }}>
          <option value="nat">{tr("NAT (private network behind the NAS)")}</option>
          {bridges.map((b) => <option key={b} value={`bridge:${b}`}>{tr("Bridge")}{" "}{b} {" "}{tr("(on the local network)")}</option>)}
          <option value="none">{tr("No network")}</option>
        </Select>
      </Field>
      {n.mode !== "none" && (
        <>
          <Field label={tr("Static address (optional)")} hint={tr("e.g. 192.168.1.50/24; empty: DHCP")}><Input value={n.address ?? ""} onChange={(e) => set({ ...n, address: e.target.value })} placeholder="DHCP" /></Field>
          <Field label={tr("Gateway")}><Input value={n.gateway ?? ""} disabled={!n.address} onChange={(e) => set({ ...n, gateway: e.target.value })} /></Field>
          <Field label={tr("DNS servers")}><Input value={dns} disabled={!n.address} placeholder="1.1.1.1 9.9.9.9" onChange={(e) => { setDns(e.target.value); set({ ...n, dns: e.target.value.split(/[\s,]+/).filter(Boolean) }); }} /></Field>
          <Field label={tr("MAC address")} hint={tr("Empty: generated")}><Input className="mono" value={n.mac} onChange={(e) => set({ ...n, mac: e.target.value })} placeholder="00:16:3e:…" /></Field>
        </>
      )}
      {bridges.length === 0 && n.mode !== "none" && <div className="sm:col-span-2"><Notice>{tr("For the container to appear directly on your local network, create a bridge in Control Panel → Network.")}</Notice></div>}
    </div>
  );
}

/** Shared folders mounted into the container, and the ids kept inside. */
export function MountFields({ mounts, set, mapUsers, setMapUsers, mapGroups, setMapGroups, privileged }: {
  mounts: LXCMount[]; set: (m: LXCMount[]) => void; mapUsers: string[]; setMapUsers: (v: string[]) => void; mapGroups: string[]; setMapGroups: (v: string[]) => void; privileged: boolean;
}) {
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const users = useQuery({ queryKey: ["users"], queryFn: () => api.get<User[]>("/api/users") });
  const groups = useQuery({ queryKey: ["groups"], queryFn: () => api.get<Group[]>("/api/groups") });
  const [add, setAdd] = useState("");
  const toggle = (list: string[], v: string, on: boolean) => (on ? [...list, v] : list.filter((x) => x !== v));
  return (
    <div className="space-y-4">
      <div className="space-y-2">
        {mounts.map((m, i) => (
          <div key={i} className="grid items-end gap-2 rounded-xl border border-line p-3 sm:grid-cols-[1fr_1.2fr_auto_auto]">
            <div className="text-[13px] font-medium">{m.share}<div className="text-muted mono text-[11px]">{shares.data?.find((s) => s.name === m.share)?.path}</div></div>
            <Field label={tr("Mount point in the container")}><Input className="mono" value={m.target} onChange={(e) => set(mounts.map((x, j) => (j === i ? { ...x, target: e.target.value } : x)))} /></Field>
            <Checkbox checked={!!m.readOnly} onChange={(v) => set(mounts.map((x, j) => (j === i ? { ...x, readOnly: v } : x)))} label={tr("Read-only")} />
            <IconButton title={tr("Remove")} onClick={() => set(mounts.filter((_, j) => j !== i))}><Trash2 size={15} /></IconButton>
          </div>
        ))}
        <div className="flex items-end gap-2">
          <Field label={tr("Add a shared folder")} className="flex-1">
            <Select value={add} onChange={(e) => setAdd(e.target.value)}>
              <option value="">{tr("Choose…")}</option>
              {shares.data?.filter((s) => !mounts.some((m) => m.share === s.name)).map((s) => <option key={s.name} value={s.name}>{s.name} ({s.path})</option>)}
            </Select>
          </Field>
          <Button icon={<Plus size={14} />} disabled={!add} onClick={() => { set([...mounts, { share: add, target: `/mnt/${add.toLowerCase().replace(/\s+/g, "-")}`, readOnly: false }]); setAdd(""); }}>{tr("Add")}</Button>
        </div>
      </div>
      {privileged ? (
        <Notice tone="warn">{tr("Privileged container: files keep the owners of the NAS, and the container's root can write everywhere in the mounted folders.")}</Notice>
      ) : (
        <div className="space-y-3 rounded-xl border border-line p-4">
          <div className="text-[13px] font-medium">{tr("Owners of the mounted files")}</div>
          <div className="text-muted text-[12.5px]">{tr("In an unprivileged container, user n is user 100000+n of the NAS: the files of the shared folders appear as \"nobody\" and the container's root cannot change them. Keep the number of the StrataOS accounts and groups chosen here: inside, create users with the same number (e.g. useradd -u 1001) to read and write their files.")}</div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <div className="mb-1.5 text-[12px] font-medium text-muted">{tr("Users")}</div>
              <div className="max-h-40 space-y-1 overflow-y-auto">
                {users.data?.map((u) => <Checkbox key={u.username} checked={mapUsers.includes(u.username)} onChange={(v) => setMapUsers(toggle(mapUsers, u.username, v))} label={u.username} />)}
              </div>
            </div>
            <div>
              <div className="mb-1.5 text-[12px] font-medium text-muted">{tr("Groups")}</div>
              <div className="max-h-40 space-y-1 overflow-y-auto">
                {groups.data?.filter((g) => !g.system).map((g) => <Checkbox key={g.name} checked={mapGroups.includes(g.name)} onChange={(v) => setMapGroups(toggle(mapGroups, g.name, v))} label={<span>{g.name} <span className="text-muted text-[11px]">({g.gid})</span></span>} />)}
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export function FeatureFields({ f, set, privileged }: { f: LXCFeatures; set: (f: LXCFeatures) => void; privileged: boolean }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <Switch checked={!!f.nesting} onChange={(v) => set({ ...f, nesting: v })} label={tr("Nesting")} hint={tr("Containers inside this one (Docker, Podman)")} />
      <Switch checked={!!f.keyctl} disabled={privileged} onChange={(v) => set({ ...f, keyctl: v })} label="keyctl" hint={tr("Needed by Docker in an unprivileged container")} />
      <Switch checked={!!f.fuse} onChange={(v) => set({ ...f, fuse: v })} label="FUSE" hint={tr("File systems in user space (sshfs, rclone mount…)")} />
      <Switch checked={!!f.tun} onChange={(v) => set({ ...f, tun: v })} label="TUN" hint={tr("VPN software (WireGuard in user space, OpenVPN, Tailscale)")} />
    </div>
  );
}

export function LocationFields({ location, backend, sizeGb, set, fixedSize }: { location: string; backend: string; sizeGb: number; set: (k: { location?: string; backend?: LXCLocation["backends"][number]; sizeGb?: number }) => void; fixedSize?: boolean }) {
  const locs = useLocations();
  const cur = locs.data?.find((l) => l.path === location);
  useEffect(() => {
    if (!location && locs.data?.length) set({ location: locs.data[0].path, backend: locs.data[0].backends[0] });
  }, [locs.data]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <>
      <Field label={tr("Volume")} hint={cur ? tr("{0} free", bytes(cur.free)) : undefined}>
        <Select value={location} onChange={(e) => { const l = locs.data?.find((x) => x.path === e.target.value); set({ location: e.target.value, backend: l?.backends[0] }); }}>
          {locs.data?.length === 0 && <option value="">{tr("No volume")}</option>}
          {locs.data?.map((l) => <option key={l.path} value={l.path}>{l.label} ({l.fsType})</option>)}
        </Select>
      </Field>
      <Field label={tr("Storage")} hint={backendHint[backend]}>
        <Select value={backend} onChange={(e) => set({ backend: e.target.value as LXCLocation["backends"][number] })}>
          {(cur?.backends ?? ["image", "dir"]).map((b) => <option key={b} value={b}>{backendLabel[b]}</option>)}
        </Select>
      </Field>
      {!fixedSize && <Field label={tr("Disk size (GB)")} hint={backend === "dir" ? tr("A folder has no size limit") : tr("Root file system limit")}><Input type="number" min={1} disabled={backend === "dir"} value={backend === "dir" ? "" : sizeGb} onChange={(e) => set({ sizeGb: +e.target.value })} /></Field>}
    </>
  );
}

type Step = "image" | "resources" | "network" | "options";
const steps: { id: Step; label: string }[] = [
  { id: "image", label: tr("Image") }, { id: "resources", label: tr("Resources") }, { id: "network", label: tr("Network") }, { id: "options", label: tr("Folders and options") },
];

/** Wizard of a new LXC container. */
export function CreateContainer({ bridges, onClose, onImport }: { bridges: string[]; onClose: () => void; onImport: () => void }) {
  const [step, setStep] = useState<Step>("image");
  const [refresh, setRefresh] = useState(0);
  const images = useQuery({ queryKey: ["lxc-templates", refresh], queryFn: () => api.get<{ images: LXCImage[]; updated: string; server: string }>(`/api/lxc/templates${refresh ? "?refresh=1" : ""}`) });
  const [search, setSearch] = useState("");
  const [all, setAll] = useState(false);
  const [f, setF] = useState({
    name: "", description: "", distro: "debian", release: "trixie", location: "", backend: "image" as LXCLocation["backends"][number], sizeGb: 8,
    cpus: 2, memoryMb: 1024, swapMb: 512, privileged: false, autostart: false, start: true, rootPassword: "",
  });
  const [net, setNet] = useState<LXCNetwork>({ mode: "nat", mac: "" });
  const [mounts, setMounts] = useState<LXCMount[]>([]);
  const [mapUsers, setMapUsers] = useState<string[]>([]);
  const [mapGroups, setMapGroups] = useState<string[]>([]);
  const [features, setFeatures] = useState<LXCFeatures>({});
  const set = (k: Partial<typeof f>) => setF({ ...f, ...k });
  const shown = useMemo(() => {
    const s = search.toLowerCase();
    return (images.data?.images ?? []).filter((i) => (all || i.recommended || s) && (!s || i.label.toLowerCase().includes(s) || i.distro.includes(s)));
  }, [images.data, search, all]);
  const create = useAction(() => api.post("/api/lxc", {
    name: f.name, description: f.description, image: { distro: f.distro, release: f.release }, privileged: f.privileged,
    location: f.location, backend: f.backend, sizeGb: f.backend === "dir" ? 0 : f.sizeGb, cpus: f.cpus, memoryMb: f.memoryMb, swapMb: f.swapMb,
    network: net, mounts, features, mapUsers: f.privileged ? [] : mapUsers, mapGroups: f.privileged ? [] : mapGroups,
    autostart: f.autostart, start: f.start, rootPassword: f.rootPassword,
  }), { onDone: onClose });
  const i = steps.findIndex((s) => s.id === step);
  const nameOk = nameRe.test(f.name);
  const canNext = step === "image" ? !!f.distro && !!f.release : step === "resources" ? nameOk && !!f.location : true;
  const chosen = images.data?.images.find((x) => x.distro === f.distro && x.release === f.release);

  return (
    <Modal open onClose={onClose} wide title={tr("New container (LXC)")} footer={
      <>
        {i === 0 ? <Button icon={<FileInput size={14} />} onClick={onImport}>{tr("Import a backup…")}</Button> : <Button onClick={() => setStep(steps[i - 1].id)}>{tr("Back")}</Button>}
        <div className="flex-1" />
        <Button onClick={onClose}>{tr("Cancel")}</Button>
        {i < steps.length - 1
          ? <Button variant="primary" disabled={!canNext} onClick={() => setStep(steps[i + 1].id)}>{tr("Next")}</Button>
          : <Button variant="primary" disabled={!nameOk || !f.location} loading={create.isPending} onClick={() => create.mutate()}>{f.start ? tr("Create and start") : tr("Create")}</Button>}
      </>
    }>
      <ErrorBox error={create.error} />
      <div className="mb-5 flex flex-wrap gap-2">
        {steps.map((s, k) => (
          <button key={s.id} type="button" onClick={() => (k < i || canNext) && setStep(s.id)} className={cx("flex items-center gap-1.5 rounded-full border px-3 py-1 text-[12.5px]", s.id === step ? "border-accent bg-accent-soft text-accent" : k < i ? "border-line text-fg" : "border-line text-muted")}>
            {k < i ? <Check size={13} /> : <span className="text-[11px]">{k + 1}</span>}{s.label}
          </button>
        ))}
      </div>

      {step === "image" && (
        <div className="space-y-3">
          <div className="flex items-center gap-2">
            <div className="relative flex-1"><Search size={14} className="text-muted absolute left-2.5 top-2.5" /><Input className="pl-8" value={search} onChange={(e) => setSearch(e.target.value)} placeholder={tr("Search a distribution (Alpine, Fedora, Rocky…)")} /></div>
            <IconButton title={tr("Refresh the list")} onClick={() => setRefresh((r) => r + 1)}><RefreshCw size={15} /></IconButton>
          </div>
          <ErrorBox error={images.error} />
          {images.isLoading ? <Spinner /> : (
            <div className="grid max-h-[46vh] gap-2 overflow-y-auto sm:grid-cols-2">
              {shown.map((img) => (
                <button key={`${img.distro}/${img.release}`} type="button" onClick={() => set({ distro: img.distro, release: img.release })}
                  className={cx("rounded-xl border px-3 py-2.5 text-left transition", f.distro === img.distro && f.release === img.release ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                  <span className="block text-[13px] font-medium">{img.label}</span>
                  <span className="text-muted block text-[11.5px]">{img.distro} {img.release} · {img.arch} · {tr("build {0}", img.build)}</span>
                </button>
              ))}
            </div>
          )}
          {!search && <Checkbox checked={all} onChange={setAll} label={tr("Show every distribution")} />}
          <div className="text-muted text-[12px]">{tr("Images from {0}, downloaded once and checked with their GPG signature. List updated {1}.", images.data?.server ?? "images.linuxcontainers.org", images.data ? dateTime(images.data.updated) : "…")}</div>
        </div>
      )}

      {step === "resources" && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Name")} hint={f.name && !nameOk ? tr("Lower-case letters, digits and hyphens (it is also the host name)") : tr("Also its host name")}><Input value={f.name} onChange={(e) => set({ name: e.target.value })} placeholder="web" autoFocus /></Field>
          <Field label={tr("Description")}><Input value={f.description} onChange={(e) => set({ description: e.target.value })} /></Field>
          <LocationFields location={f.location} backend={f.backend} sizeGb={f.sizeGb} set={(k) => set(k)} />
          <Field label={tr("CPU limit (cores)")} hint={tr("0: no limit")}><Input type="number" min={0} step={0.5} value={f.cpus} onChange={(e) => set({ cpus: +e.target.value })} /></Field>
          <Field label={tr("Memory (MiB)")} hint={tr("0: no limit")}><Input type="number" min={0} step={256} value={f.memoryMb} onChange={(e) => set({ memoryMb: +e.target.value })} /></Field>
          <Field label={tr("Swap (MiB)")} hint={tr("On top of the memory")}><Input type="number" min={0} step={256} disabled={!f.memoryMb} value={f.swapMb} onChange={(e) => set({ swapMb: +e.target.value })} /></Field>
        </div>
      )}

      {step === "network" && <NetworkFields n={net} set={setNet} bridges={bridges} />}

      {step === "options" && (
        <div className="space-y-5">
          <MountFields mounts={mounts} set={setMounts} mapUsers={mapUsers} setMapUsers={setMapUsers} mapGroups={mapGroups} setMapGroups={setMapGroups} privileged={f.privileged} />
          <FeatureFields f={features} set={setFeatures} privileged={f.privileged} />
          <Field label={tr("Root password (optional)")} hint={tr("For the login prompt of the console. The shell in the browser needs none.")}><Input type="password" autoComplete="new-password" value={f.rootPassword} onChange={(e) => set({ rootPassword: e.target.value })} /></Field>
          <div className="space-y-2">
            <Checkbox checked={f.start} onChange={(v) => set({ start: v })} label={tr("Start it once created")} />
            <Checkbox checked={f.autostart} onChange={(v) => set({ autostart: v })} label={tr("Start the container with the NAS")} />
            <Checkbox checked={f.privileged} onChange={(v) => set({ privileged: v })} label={<span className="flex items-center gap-1.5">{f.privileged && <AlertTriangle size={14} className="text-warn" />}{tr("Privileged container")}</span>} />
          </div>
          {f.privileged && <Notice tone="warn">{tr("A privileged container runs with the NAS's root as its root: software escaping it controls the whole NAS. Only for software that cannot run otherwise (some NFS servers, old init systems). It cannot be changed later.")}</Notice>}
          {chosen && <div className="text-muted text-[12px]">{tr("Summary: {0}, {1}, {2} on {3}.", chosen.label, f.backend === "dir" ? backendLabel.dir : tr("{0} GB", f.sizeGb), backendLabel[f.backend], f.location)}</div>}
        </div>
      )}
    </Modal>
  );
}

/** Imports a container backup (.tar.zst) or a container folder found on a volume. */
export function ImportContainer({ onClose }: { onClose: () => void }) {
  const list = useQuery({ queryKey: ["lxc-importable"], queryFn: () => api.get<LXCImportable[]>("/api/lxc/importable") });
  const [src, setSrc] = useState("");
  const [f, setF] = useState({ name: "", location: "", backend: "image" as LXCLocation["backends"][number], sizeGb: 8, start: false });
  const item = list.data?.find((x) => x.path === src);
  const run = useAction(() => api.post("/api/lxc/import", { source: src, name: f.name, location: item?.kind === "folder" ? "" : f.location, backend: item?.kind === "folder" ? "" : f.backend, sizeGb: f.sizeGb, start: f.start }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} wide title={tr("Import a container")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!src} loading={run.isPending} onClick={() => run.mutate()}>{tr("Import")}</Button></>}>
      <div className="space-y-4">
        <ErrorBox error={run.error || list.error} />
        <Field label={tr("Backup or container folder")} hint={tr("Container backups (.tar.zst) found in the volumes, and folders of containers moved from another NAS.")}>
          <Select value={src} onChange={(e) => { setSrc(e.target.value); const it = list.data?.find((x) => x.path === e.target.value); setF({ ...f, name: it?.container ?? "" }); }}>
            <option value="">{list.isLoading ? tr("Searching…") : tr("Choose…")}</option>
            {list.data?.map((x) => <option key={x.path} value={x.path}>{x.kind === "folder" ? tr("Folder") : tr("Backup")}: {x.path}{x.size ? ` (${bytes(x.size)})` : ""}</option>)}
          </Select>
        </Field>
        {item?.kind === "folder" && <Notice>{tr("The container is added where it is, with its snapshots. Files are given this NAS's id range if it differs.")}</Notice>}
        {item && item.kind === "archive" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Name")} hint={tr("Empty: the original name")}><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
            <LocationFields location={f.location} backend={f.backend} sizeGb={f.sizeGb} set={(k) => setF({ ...f, ...k })} />
          </div>
        )}
        {item?.kind === "archive" && <Checkbox checked={f.start} onChange={(v) => setF({ ...f, start: v })} label={tr("Start it once imported")} />}
      </div>
    </Modal>
  );
}
