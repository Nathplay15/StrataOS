import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Cable, Container as ContainerIcon, House, Lock, Network, Pencil, Plus, Share2, Trash2, Waypoints } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, Select, Spinner, Switch, useUI } from "../../../components/ui";
import { tr } from "../../../lib/i18n";
import { TrialModal, type Pending } from "../Network";
import type { VNet, VNetData, VNetKind, VNetView } from "./types";

const kinds: [VNetKind, string, string][] = [
  ["nat", tr("NAT network"), tr("Private network behind the NAS, with its own DHCP: guests reach the Internet through the NAS.")],
  ["isolated", tr("Isolated network"), tr("A switch with no uplink: guests only talk to each other (and to the NAS if it has an address there).")],
  ["vlan", tr("VLAN"), tr("Bridge on a VLAN (802.1Q) of a network port: guests join that VLAN of your switch.")],
  ["vxlan", tr("VXLAN between NAS"), tr("The same network on several NAS of the fleet or cluster, carried over the LAN (UDP 4789).")],
];
export const kindLabel = (k: VNetKind) => kinds.find((x) => x[0] === k)?.[1] ?? k;

const list = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

/** First 10.x.0.0/24 network the NAS does not use yet. */
function freeNet(d: VNetData): string {
  const used = [...(d.addresses ?? []), ...d.networks.flatMap((n) => [n.address ?? "", n.subnet ?? ""])];
  for (let i = 20; i < 250; i++) if (!used.some((a) => a.startsWith(`10.${i}.`))) return `10.${i}.0`;
  return "10.250.0";
}

type Preset = { id: string; label: string; text: string; icon: ReactNode; make: (d: VNetData) => VNet };

const presets: Preset[] = [
  {
    id: "home-automation", label: tr("Home automation"), icon: <House size={18} />,
    text: tr("A VLAN for connected devices and the home automation container or VM: they reach the Internet but neither your computers nor the NAS."),
    make: (d) => {
      const n = freeNet(d);
      return (d.parents ?? []).length > 0
        ? { id: "iot", label: tr("Home automation"), use: "home-automation", kind: "vlan", bridge: "", parent: d.parents![0], vlan: 30, subnet: `${n}.0/24`, gateway: `${n}.1`, isolate: true, logDrops: true }
        : { id: "iot", label: tr("Home automation"), use: "home-automation", kind: "nat", bridge: "", address: `${n}.1/24`, dhcp: true, dns: d.dns, isolate: true, logDrops: true };
    },
  },
  {
    id: "sensitive", label: tr("Sensitive shares"), icon: <Lock size={18} />,
    text: tr("An isolated network where guests reach only the file shares of the NAS (SMB), nothing else: no Internet, no local network."),
    make: (d) => { const n = freeNet(d); return { id: "vault", label: tr("Sensitive shares"), use: "sensitive", kind: "isolated", bridge: "", address: `${n}.1/24`, dhcp: true, isolate: true, nasPorts: "445", logDrops: true }; },
  },
  {
    id: "public", label: tr("Public containers"), icon: <Share2 size={18} />,
    text: tr("For services exposed to the Internet: NAT with DHCP and a Docker network; a compromised container cannot reach the local network or the NAS."),
    make: (d) => { const n = freeNet(d); return { id: "public", label: tr("Public containers"), use: "public", kind: "nat", bridge: "", address: `${n}.1/24`, dhcp: true, dns: d.dns, isolate: true, docker: d.docker, logDrops: true }; },
  },
];

function VNetForm({ v, set, d, editing }: { v: VNet; set: (v: VNet) => void; d: VNetData; editing: boolean }) {
  const upd = (x: Partial<VNet>) => set({ ...v, ...x });
  const peers = v.peers ?? [];
  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={tr("Name")} hint={tr("lower case letters and digits")}><Input className="mono" disabled={editing} value={v.id} onChange={(e) => upd({ id: e.target.value.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 12) })} /></Field>
        <Field label={tr("Description")}><Input value={v.label ?? ""} onChange={(e) => upd({ label: e.target.value })} /></Field>
      </div>
      <Field label={tr("Type")}>
        <Select value={v.kind} onChange={(e) => upd({ kind: e.target.value as VNetKind })}>
          {kinds.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </Select>
      </Field>
      <div className="text-muted -mt-2 text-[12px]">{kinds.find((k) => k[0] === v.kind)?.[2]}</div>
      {v.kind === "vlan" && (
        (d.parents ?? []).length === 0 ? <Notice tone="warn">{tr("First switch the network port to \"Managed by StrataOS\" (Interfaces tab).")}</Notice> : (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={tr("Network port")}><Select value={v.parent ?? ""} onChange={(e) => upd({ parent: e.target.value })}>{(d.parents ?? []).map((p) => <option key={p}>{p}</option>)}</Select></Field>
            <Field label={tr("VLAN ID")}><Input type="number" min={1} max={4094} value={v.vlan ?? ""} onChange={(e) => upd({ vlan: +e.target.value })} /></Field>
          </div>
        )
      )}
      {v.kind === "vxlan" && (
        <div className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={tr("VXLAN id (VNI)")} hint={tr("the same on every NAS")}><Input type="number" min={1} value={v.vni ?? ""} onChange={(e) => upd({ vni: +e.target.value })} /></Field>
            <Field label="MTU" hint={tr("1450 leaves room for the tunnel on a 1500 LAN")}><Input type="number" value={v.mtu || ""} placeholder="1450" onChange={(e) => upd({ mtu: +e.target.value || 0 })} /></Field>
          </div>
          {d.peers.length > 0 && (
            <div className="flex flex-wrap gap-4">{d.peers.map((p) => (
              <Checkbox key={p.from + p.addr} checked={peers.includes(p.addr)} onChange={(c) => upd({ peers: c ? [...peers, p.addr] : peers.filter((x) => x !== p.addr) })} label={<span>{p.name} <span className="mono text-muted">{p.addr}</span></span>} />
            ))}</div>
          )}
          <Field label={tr("Other NAS (addresses)")} hint={tr("Create the same network, with the same VNI, on each of them, listing this NAS.")}><Input className="mono" value={peers.join(" ")} onChange={(e) => upd({ peers: list(e.target.value) })} /></Field>
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={v.kind === "nat" ? tr("Address of the NAS (gateway)") : tr("Address of the NAS on this network")} hint={v.kind === "nat" ? "10.20.0.1/24" : tr("optional")}>
          <Input className="mono" value={v.address ?? ""} placeholder="10.20.0.1/24" onChange={(e) => upd({ address: e.target.value.trim(), ...(e.target.value.trim() ? {} : { dhcp: false }) })} />
        </Field>
        {v.kind !== "nat" && !v.address && <Field label={tr("Subnet")} hint={tr("for Docker and the isolation; optional")}><Input className="mono" value={v.subnet ?? ""} placeholder="10.42.0.0/24" onChange={(e) => upd({ subnet: e.target.value.trim() })} /></Field>}
        {v.kind !== "nat" && <Field label={tr("Router of the network")} hint={tr("optional")}><Input className="mono" value={v.gateway ?? ""} onChange={(e) => upd({ gateway: e.target.value.trim() })} /></Field>}
        {v.kind !== "vxlan" && <Field label="MTU" hint={tr("empty = 1500")}><Input type="number" value={v.mtu || ""} onChange={(e) => upd({ mtu: +e.target.value || 0 })} /></Field>}
      </div>
      {v.address && (
        <div className="space-y-3">
          <Switch checked={!!v.dhcp} onChange={(c) => upd({ dhcp: c, dns: c && !(v.dns ?? []).length ? d.dns : v.dns })} label={tr("DHCP server of the NAS on this network")} hint={tr("Hands out the middle half of the addresses; the last quarter is kept for Docker.")} />
          {v.dhcp && <Field label={tr("DNS servers given to the guests")}><Input className="mono" value={(v.dns ?? []).join(" ")} placeholder="1.1.1.1 9.9.9.9" onChange={(e) => upd({ dns: list(e.target.value) })} /></Field>}
        </div>
      )}
      <Switch checked={!!v.isolate} onChange={(c) => upd({ isolate: c })} label={tr("Isolate from the local network and the NAS")} hint={v.kind === "nat" ? tr("Guests still reach the Internet; DHCP and the DNS servers above stay allowed.") : tr("Guests reach neither the private addresses outside this network nor the NAS.")} />
      {v.isolate && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={tr("NAS ports still open to the guests")} hint={tr("e.g. 445 (SMB), 8123")}><Input className="mono" value={v.nasPorts ?? ""} onChange={(e) => upd({ nasPorts: e.target.value })} /></Field>
          <div className="flex items-end pb-2"><Switch checked={!!v.logDrops} onChange={(c) => upd({ logDrops: c })} label={tr("Log dropped packets")} /></div>
        </div>
      )}
      <Switch checked={!!v.docker} disabled={!d.docker} onChange={(c) => upd({ docker: c })} label={tr("Docker network")} hint={d.docker ? tr("Creates the Docker network strata-{0} (macvlan) so containers can be plugged into this network.", v.id || "…") : tr("Docker is not installed.")} />
    </div>
  );
}

/** Network → Virtual networks. */
export function VirtualNetworksTab() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["vnets"], queryFn: () => api.get<VNetData>("/api/system/network/vnets"), refetchInterval: 10000 });
  const [form, setForm] = useState<{ v: VNet; editing: boolean } | null>(null);
  const [trial, setTrial] = useState<Pending | null>(null);
  const [dock, setDock] = useState<{ id: string; container: string; address: string } | null>(null);
  const onTrial = (p: Pending) => { setForm(null); setTrial(p); };
  const create = useAction((v: VNet) => api.post<Pending>("/api/system/network/vnets", v), { onDone: onTrial });
  const update = useAction((v: VNet) => api.put<Pending>(`/api/system/network/vnets/${v.id}`, v), { onDone: onTrial });
  const remove = useAction((id: string) => api.del<Pending>(`/api/system/network/vnets/${id}`), { onDone: onTrial });
  const connect = useAction(() => api.post(`/api/system/network/vnets/${dock!.id}/docker`, { container: dock!.container, address: dock!.address }), { success: tr("Container connected"), onDone: () => setDock(null) });

  if (isLoading || !data) return <div><ErrorBox error={error} /><Spinner /></div>;
  const busy = data.pending || data.manager === "NetworkManager";
  const submit = () => form && (form.editing ? update : create).mutate({ ...form.v, peers: form.v.kind === "vxlan" ? form.v.peers : undefined });

  return (
    <div className="space-y-5">
      <ErrorBox error={error || remove.error} />
      {data.manager === "NetworkManager" && <Notice tone="warn">{tr("NetworkManager manages the network on this machine: virtual networks need systemd-networkd.")}</Notice>}
      {data.pending && <Notice tone="warn">{tr("A network change is on trial: confirm it or wait for the rollback before changing virtual networks.")}</Notice>}
      <Card title={tr("Create a virtual network")} actions={<Button size="sm" icon={<Plus size={14} />} disabled={busy} onClick={() => setForm({ v: { id: "", kind: "nat", bridge: "", address: `${freeNet(data)}.1/24`, dhcp: true, dns: data.dns }, editing: false })}>{tr("Custom")}</Button>}>
        <div className="grid gap-3 md:grid-cols-3">
          {presets.map((p) => (
            <button key={p.id} disabled={busy} onClick={() => setForm({ v: p.make(data), editing: false })} className="rounded-xl border border-line p-4 text-left transition hover:border-accent disabled:opacity-50">
              <div className="mb-1 flex items-center gap-2 font-semibold text-accent">{p.icon}{p.label}</div>
              <div className="text-muted text-[12.5px]">{p.text}</div>
            </button>
          ))}
        </div>
      </Card>

      {data.networks.length === 0 ? <Empty icon={<Network size={28} />} title={tr("No virtual network")} text={tr("Separate the traffic of your VMs and containers from your local network: each network is a bridge of its own, chosen in the VM and container wizards.")} /> : (
        <div className="space-y-4">
          {data.networks.map((n: VNetView) => (
            <Card key={n.id} title={<span className="flex flex-wrap items-center gap-2"><Waypoints size={16} className="text-accent" /><span className="mono">{n.id}</span>{n.label && <span className="font-normal text-muted">{n.label}</span>}</span>} actions={<>
              {n.docker && n.status.docker && <IconButton title={tr("Connect a Docker container")} onClick={() => setDock({ id: n.id, container: "", address: "" })}><ContainerIcon size={15} /></IconButton>}
              <IconButton title={tr("Edit")} disabled={busy} onClick={() => setForm({ v: structuredClone(n), editing: true })}><Pencil size={15} /></IconButton>
              <IconButton title={tr("Delete")} disabled={busy} onClick={async () => (await confirm({ title: tr("Delete the network {0}?", n.id), text: tr("Applied on trial like any network change."), confirm: tr("Delete"), danger: true })) && remove.mutate(n.id)}><Trash2 size={15} /></IconButton>
            </>}>
              <div className="flex flex-wrap gap-2 text-[12.5px]">
                <Badge tone="accent">{kindLabel(n.kind)}</Badge>
                <Badge tone={n.status.up ? "ok" : n.status.exists ? "warn" : "danger"} dot>{n.status.up ? tr("up") : n.status.exists ? tr("no guest connected") : tr("missing")}</Badge>
                <span className="mono">{n.bridge}</span>
                {n.kind === "vlan" && <span className="mono">{n.parent}.{n.vlan}</span>}
                {n.kind === "vxlan" && <span>VNI <span className="mono">{n.vni}</span> → <span className="mono">{(n.peers ?? []).join(", ")}</span></span>}
                {(n.address || n.subnet) && <span className="mono">{n.address || n.subnet}</span>}
                {n.dhcp && <Badge>DHCP</Badge>}
                {n.status.mtu && n.status.mtu !== 1500 && <Badge>MTU {n.status.mtu}</Badge>}
                {n.isolate && <Badge tone="warn">{tr("isolated from the LAN")}{n.nasPorts ? ` · ${tr("NAS ports")} ${n.nasPorts}` : ""}</Badge>}
                {n.docker && <Badge tone={n.status.docker ? "ok" : "neutral"}>Docker strata-{n.id}</Badge>}
              </div>
              <div className="mt-3 text-[12.5px]">
                {n.guests.length === 0 ? <span className="text-muted">{tr("No VM or container on this network yet: choose it in their network settings.")}</span> : (
                  <span className="flex flex-wrap items-center gap-2"><Cable size={14} />{n.guests.map((g) => <Badge key={g.kind + g.name}>{g.name} · {g.state}</Badge>)}</span>
                )}
              </div>
            </Card>
          ))}
        </div>
      )}

      {form && (
        <Modal open wide onClose={() => setForm(null)} title={form.editing ? tr("Network {0}", form.v.id) : tr("New virtual network")} footer={<>
          <Button onClick={() => setForm(null)}>{tr("Cancel")}</Button>
          <Button variant="primary" disabled={!form.v.id} loading={create.isPending || update.isPending} onClick={submit}>{tr("Apply on trial")}</Button>
        </>}>
          <ErrorBox error={create.error || update.error} />
          <VNetForm v={form.v} set={(v) => setForm({ ...form, v })} d={data} editing={form.editing} />
        </Modal>
      )}
      {dock && (
        <Modal open onClose={() => setDock(null)} title={tr("Connect a Docker container to {0}", dock.id)} footer={<Button variant="primary" disabled={!dock.container} loading={connect.isPending} onClick={() => connect.mutate()}>{tr("Connect")}</Button>}>
          <ErrorBox error={connect.error} />
          <div className="space-y-3">
            <Field label={tr("Container name")}><Input className="mono" value={dock.container} onChange={(e) => setDock({ ...dock, container: e.target.value.trim() })} /></Field>
            <Field label={tr("Address")} hint={tr("optional; otherwise taken from the last quarter of the network")}><Input className="mono" value={dock.address} onChange={(e) => setDock({ ...dock, address: e.target.value.trim() })} /></Field>
            <div className="text-muted text-[12px]">{tr("In a Compose file, use the external network strata-{0}.", dock.id)}</div>
          </div>
        </Modal>
      )}
      {trial && <TrialModal pending={trial} newURLs={[]} onClose={() => setTrial(null)} />}
    </div>
  );
}
