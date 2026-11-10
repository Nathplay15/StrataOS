import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { Badge, Button, Card, ErrorBox, Field, Modal, Notice, Select, Spinner, Switch } from "../../../components/ui";
import { tr } from "../../../lib/i18n";
import { AddrHelp, RulesEditor } from "./RulesEditor";
import type { Binding, Guest, GuestKind, LogEntry } from "./types";

type Data = { guest: Guest; binding: Binding; groups: string[]; sets: string[] };

const enc = encodeURIComponent;

/** Recent drops of one scope (host, vm.web, lxc.ha, vnet.iot…). */
export function DropLog({ scope, limit = 50 }: { scope?: string; limit?: number }) {
  const q = useQuery({ queryKey: ["fw-log", scope ?? ""], queryFn: () => api.get<LogEntry[]>(`/api/system/network/firewall/log?limit=${limit}${scope ? `&scope=${enc(scope)}` : ""}`), refetchInterval: 10000 });
  if (q.isLoading) return <Spinner />;
  const l = q.data ?? [];
  if (l.length === 0) return <div className="text-muted text-[13px]">{tr("No dropped packet logged recently.")}</div>;
  return (
    <div className="max-h-80 overflow-auto">
      <table className="w-full text-[12px]">
        <thead className="text-muted text-left"><tr><th className="pb-1">{tr("Time")}</th>{!scope && <th className="pb-1">{tr("Firewall")}</th>}<th className="pb-1">{tr("Direction")}</th><th className="pb-1">{tr("Source")}</th><th className="pb-1">{tr("Destination")}</th><th className="pb-1">{tr("Protocol")}</th></tr></thead>
        <tbody>
          {l.map((e, i) => (
            <tr key={i} className="border-t border-line">
              <td className="py-1 pr-2 whitespace-nowrap">{e.time.replace("T", " ").slice(0, 19)}</td>
              {!scope && <td className="mono py-1 pr-2">{e.scope}</td>}
              <td className="py-1 pr-2">{e.dir === "in" ? tr("In") : e.dir === "out" ? tr("Out") : "—"}</td>
              <td className="mono py-1 pr-2">{e.src ?? "—"}{e.sport ? `:${e.sport}` : ""}</td>
              <td className="mono py-1 pr-2">{e.dst ?? "—"}{e.dport ? `:${e.dport}` : ""}</td>
              <td className="py-1">{e.proto ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Firewall of a VM or container: the Firewall tab of its settings. */
export function GuestFirewall({ kind, name, onSaved }: { kind: GuestKind; name: string; onSaved?: () => void }) {
  const path = `/api/system/network/firewall/guest/${kind}/${enc(name)}`;
  const q = useQuery({ queryKey: ["fw-guest", kind, name], queryFn: () => api.get<Data>(path) });
  const [g, setG] = useState<Guest | null>(null);
  useEffect(() => { if (q.data && !g) setG(q.data.guest); }, [q.data, g]);
  const save = useAction(() => api.put(path, g), { success: tr("Firewall saved"), onDone: () => { setG(null); onSaved?.(); } });
  if (q.isLoading || !q.data || !g) return <div><ErrorBox error={q.error} /><Spinner /></div>;
  const b = q.data.binding;
  const dirty = JSON.stringify(g) !== JSON.stringify(q.data.guest);
  return (
    <div className="space-y-5">
      <ErrorBox error={save.error} />
      <Card title={<span className="flex items-center gap-2"><ShieldCheck size={16} />{tr("Firewall")}</span>} actions={<Button variant="primary" size="sm" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
        <div className="space-y-4">
          <Switch checked={g.enabled} onChange={(v) => setG({ ...g, enabled: v })} label={tr("Filter the traffic of this guest")} hint={tr("Applied to each of its network interfaces on the NAS's bridges, from its first packet (by MAC address), and again at each start.")} />
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={tr("Incoming traffic not matched")}>
              <Select value={g.policyIn || "drop"} onChange={(e) => setG({ ...g, policyIn: e.target.value })}>
                <option value="drop">{tr("Drop")}</option><option value="accept">{tr("Allow")}</option>
              </Select>
            </Field>
            <Field label={tr("Outgoing traffic not matched")}>
              <Select value={g.policyOut || "accept"} onChange={(e) => setG({ ...g, policyOut: e.target.value })}>
                <option value="accept">{tr("Allow")}</option><option value="drop">{tr("Drop")}</option><option value="reject">{tr("Reject")}</option>
              </Select>
            </Field>
          </div>
          <Switch checked={!!g.isolate} onChange={(v) => setG({ ...g, isolate: v })} label={tr("Isolate from the local network")} hint={tr("The guest reaches the Internet but neither the local network nor the NAS (DHCP and DNS excepted). Open a NAS port with an outgoing rule to \"nas\".")} />
          <Switch checked={!!g.macFilter} onChange={(v) => setG({ ...g, macFilter: v })} label={tr("Refuse other MAC addresses")} hint={tr("Drops what the guest sends with a MAC address that is not its own (spoofing).")} />
          <Switch checked={!!g.logDrops} onChange={(v) => setG({ ...g, logDrops: v })} label={tr("Log dropped packets")} hint={tr("At most 10 a minute, shown below and in Network → Firewall.")} />
          <div className="text-muted text-[12px]">
            {tr("MAC addresses:")}{" "}<span className="mono">{(b.macs ?? []).join(", ") || "—"}</span>{" · "}
            {tr("Interfaces on the NAS:")}{" "}<span className="mono">{(b.ifaces ?? []).join(", ") || tr("none (stopped)")}</span>
          </div>
          {kind === "docker" && <Notice>{tr("Containers on a Docker network bound to a virtual network (macvlan) are filtered by MAC address, between the containers and the rest of the network; two such containers talk directly to each other.")}</Notice>}
        </div>
      </Card>
      <Card title={tr("Rules")}>
        <div className="space-y-3">
          <div className="text-muted text-[12px]">{tr("In: traffic towards the guest. Out: traffic it sends. Replies of allowed connections always pass; reject acts as drop for incoming traffic.")}</div>
          <RulesEditor rules={g.rules} onChange={(rules) => setG({ ...g, rules })} sets={q.data.sets} groups={q.data.groups} />
          <AddrHelp />
        </div>
      </Card>
      <Card title={<span className="flex items-center gap-2">{tr("Dropped traffic")}{!g.logDrops && <Badge>{tr("logging off")}</Badge>}</span>}>
        <DropLog scope={`${kind}.${name}`} />
      </Card>
    </div>
  );
}

/** The guest firewall in a dialog (VMs, Docker containers). */
export function GuestFirewallModal({ kind, name, onClose }: { kind: GuestKind; name: string; onClose: () => void }) {
  return (
    <Modal open wide="xl" onClose={onClose} title={tr("Firewall of {0}", name)}>
      <GuestFirewall kind={kind} name={name} />
    </Modal>
  );
}
