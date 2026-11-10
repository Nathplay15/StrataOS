import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Box, Container as ContainerIcon, Layers, ListChecks, Monitor, Plus, RotateCcw, Server, ShieldCheck, Trash2 } from "lucide-react";
import { api } from "../../../lib/api";
import { useAction } from "../../../lib/hooks";
import { Badge, Button, Card, ErrorBox, Field, IconButton, Input, Notice, Select, Spinner, Switch, useUI } from "../../../components/ui";
import { locale, tr } from "../../../lib/i18n";
import { TrialModal, type Pending } from "../Network";
import { DropLog, GuestFirewallModal } from "./GuestFirewall";
import { AddrHelp, RulesEditor } from "./RulesEditor";
import type { FWConfig, FWData, GuestKind } from "./types";

const kindIcon = { vm: <Monitor size={15} />, lxc: <Box size={15} />, docker: <ContainerIcon size={15} /> };
const kindName = (k: GuestKind) => (k === "vm" ? tr("VM") : k === "lxc" ? tr("Container (LXC)") : "Docker");

/** Network → Firewall: the NAS firewall, the guests' firewalls, security groups and address sets. */
export function FirewallTab() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["firewall"], queryFn: () => api.get<FWData>("/api/system/network/firewall"), refetchInterval: 10000 });
  const [cfg, setCfg] = useState<FWConfig | null>(null);
  const [trial, setTrial] = useState<Pending | null>(null);
  const [edit, setEdit] = useState<{ kind: GuestKind; name: string } | null>(null);
  const [newSet, setNewSet] = useState("");
  const [newGroup, setNewGroup] = useState("");
  useEffect(() => { if (data && !cfg) setCfg(structuredClone(data.config)); }, [data, cfg]);
  const save = useAction(() => api.put<{ applied?: boolean; token?: string; deadline?: string }>("/api/system/network/firewall", { config: { ...cfg, guests: data?.config.guests ?? [], ipsets: (cfg?.ipsets ?? []).map((x) => ({ ...x, entries: x.entries.filter(Boolean) })) }, timeout: 90 }), {
    onDone: (r) => { if (r.token) setTrial({ token: r.token, deadline: r.deadline!, links: [] }); else setCfg(null); },
    success: (r) => (r.token ? tr("NAS firewall applied on trial") : tr("Firewall saved")),
  });
  const rollback = useAction(() => api.post("/api/system/network/firewall/rollback"), { success: tr("Previous firewall restored"), onDone: () => setCfg(null) });
  const removeGuest = useAction((g: { kind: GuestKind; name: string }) => api.put(`/api/system/network/firewall/guest/${g.kind}/${encodeURIComponent(g.name)}`, { kind: g.kind, name: g.name, enabled: false, rules: [] }), { success: tr("Firewall removed") });

  if (isLoading || !data || !cfg) return <div><ErrorBox error={error} /><Spinner /></div>;
  const sets = (cfg.ipsets ?? []).map((s) => s.name);
  const groups = (cfg.groups ?? []).map((g) => g.name);
  const local = { ...cfg, guests: data.config.guests };
  const dirty = JSON.stringify(local) !== JSON.stringify(data.config);
  const h = cfg.host;
  const setHost = (x: Partial<FWConfig["host"]>) => setCfg({ ...cfg, host: { ...h, ...x } });
  const hostChanged = JSON.stringify(cfg.host) !== JSON.stringify(data.config.host);

  const doSave = async () => {
    if (hostChanged && (h.enabled || data.config.host.enabled)) {
      if (!(await confirm({ title: tr("Apply the NAS firewall?"), text: <>{tr("It is applied")}{" "}<b>{tr("on trial")}</b> {" "}{tr("for 90 seconds. If this page cannot confirm it (the rules lock you out), the previous rules come back automatically. The web interface always stays reachable from the local network.")}</>, confirm: tr("Apply on trial") }))) return;
    }
    save.mutate();
  };

  return (
    <div className="space-y-5">
      <ErrorBox error={error || save.error} />
      {!data.nft && <Notice tone="warn">{tr("nftables is not installed: the firewall cannot work.")}</Notice>}
      {data.error && <Notice tone="warn">{tr("The rules could not be loaded:")}{" "}<span className="mono">{data.error}</span></Notice>}
      {data.trial && !trial && (
        <Notice tone="warn">
          <div className="flex flex-wrap items-center gap-3">{tr("A firewall change is on trial until")}{" "}{new Date(data.trial.deadline).toLocaleTimeString(locale)}.
            <Button size="sm" icon={<RotateCcw size={14} />} loading={rollback.isPending} onClick={() => rollback.mutate()}>{tr("Restore now")}</Button>
          </div>
        </Notice>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        {dirty && <Button onClick={() => setCfg(structuredClone(data.config))}>{tr("Discard changes")}</Button>}
        <Button variant="primary" disabled={!dirty || !!data.trial} loading={save.isPending} onClick={doSave}>{tr("Apply")}</Button>
      </div>

      <Card title={<span className="flex items-center gap-2"><Server size={16} />{tr("NAS firewall")}</span>}>
        <div className="space-y-4">
          <Switch checked={h.enabled} onChange={(v) => setHost(v && h.rules.length === 0 ? { ...data.defaultHost, enabled: true } : { enabled: v })} label={tr("Filter the traffic reaching the NAS")} hint={tr("Incoming connections to the NAS itself (shares, web interface, SSH…). Docker apps' published ports and guests are not concerned.")} />
          {h.enabled && (
            <>
              <Notice>{tr("Always allowed, whatever the rules: the web interface and SSH (ports {0}) from the local network, replies, the other NAS of the cluster and VXLAN peers, ping, DHCP and the virtual networks of guests (each guest has its own firewall).", data.webPorts.join(", "))}</Notice>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label={tr("Traffic not matched")}>
                  <Select value={h.policyIn || "drop"} onChange={(e) => setHost({ policyIn: e.target.value })}>
                    <option value="drop">{tr("Drop")}</option><option value="reject">{tr("Reject")}</option><option value="accept">{tr("Allow")}</option>
                  </Select>
                </Field>
                <div className="flex items-end pb-2"><Switch checked={!!h.logDrops} onChange={(v) => setHost({ logDrops: v })} label={tr("Log dropped packets")} /></div>
              </div>
              <RulesEditor hostOnly rules={h.rules} onChange={(rules) => setHost({ rules })} sets={sets} groups={groups} />
              <AddrHelp />
            </>
          )}
        </div>
      </Card>

      <Card title={<span className="flex items-center gap-2"><ShieldCheck size={16} />{tr("VMs and containers")}</span>}>
        {data.guests.length === 0 ? <div className="text-muted text-[13px]">{tr("No VM or container on this NAS.")}</div> : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[640px] text-[13px]">
              <thead className="text-muted text-left"><tr><th className="pb-2">{tr("Name")}</th><th className="pb-2">{tr("State")}</th><th className="pb-2">{tr("Networks")}</th><th className="pb-2">{tr("Firewall")}</th><th /></tr></thead>
              <tbody>
                {data.guests.map((g) => (
                  <tr key={`${g.kind}:${g.name}`} className="border-t border-line">
                    <td className="py-2"><span className="flex items-center gap-2">{kindIcon[g.kind]}<b>{g.name}</b><span className="text-muted text-[12px]">{kindName(g.kind)}</span></span></td>
                    <td className="py-2">{g.missing ? <Badge tone="warn">{tr("deleted")}</Badge> : g.state}</td>
                    <td className="mono py-2 text-[12px]">{g.bridges.join(", ") || "—"}</td>
                    <td className="py-2"><span className="flex flex-wrap gap-1">
                      {g.firewall ? <Badge tone="ok" dot>{tr("on")}</Badge> : <Badge>{tr("off")}</Badge>}
                      {g.isolate && <Badge tone="accent">{tr("isolated")}</Badge>}
                      {g.rules > 0 && <Badge>{tr("{0} rules", g.rules)}</Badge>}
                    </span></td>
                    <td className="py-2 text-right whitespace-nowrap">
                      {g.missing ? <IconButton title={tr("Delete")} onClick={() => removeGuest.mutate(g)}><Trash2 size={15} /></IconButton> : <Button size="sm" onClick={() => setEdit(g)}>{tr("Edit")}</Button>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title={<span className="flex items-center gap-2"><Layers size={16} />{tr("Security groups")}</span>} actions={
        <div className="flex gap-2">
          <Input aria-label={tr("Name of the new group")} className="!w-40 !py-1 !text-[12.5px] mono" placeholder="web" value={newGroup} onChange={(e) => setNewGroup(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""))} />
          <Button size="sm" icon={<Plus size={14} />} disabled={!newGroup || groups.includes(newGroup)} onClick={() => { setCfg({ ...cfg, groups: [...(cfg.groups ?? []), { name: newGroup, rules: [] }] }); setNewGroup(""); }}>{tr("Add")}</Button>
        </div>
      }>
        {(cfg.groups ?? []).length === 0 ? <div className="text-muted text-[13px]">{tr("Named rule lists to reuse on several guests (\"web server\", \"admin from the office\"…): add them to a rule list with \"Add a security group\".")}</div> : (
          <div className="space-y-4">
            {(cfg.groups ?? []).map((g, gi) => (
              <div key={gi} className="space-y-3 rounded-xl border border-line p-4">
                <div className="flex flex-wrap items-center gap-3">
                  <span className="mono font-semibold">{g.name}</span>
                  <Input aria-label={tr("Comment")} className="!w-auto flex-1 !py-1 !text-[12.5px]" placeholder={tr("Comment")} value={g.comment ?? ""} onChange={(e) => setCfg({ ...cfg, groups: (cfg.groups ?? []).map((x, i) => (i === gi ? { ...x, comment: e.target.value } : x)) })} />
                  <IconButton title={tr("Delete")} onClick={() => setCfg({ ...cfg, groups: (cfg.groups ?? []).filter((_, i) => i !== gi), host: { ...h, rules: h.rules.filter((r) => r.group !== g.name) } })}><Trash2 size={15} /></IconButton>
                </div>
                <RulesEditor inGroup rules={g.rules} onChange={(rules) => setCfg({ ...cfg, groups: (cfg.groups ?? []).map((x, i) => (i === gi ? { ...x, rules } : x)) })} sets={sets} groups={[]} />
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card title={<span className="flex items-center gap-2"><ListChecks size={16} />{tr("Address sets")}</span>} actions={
        <div className="flex gap-2">
          <Input aria-label={tr("Name of the new set")} className="!w-40 !py-1 !text-[12.5px] mono" placeholder="office" value={newSet} onChange={(e) => setNewSet(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""))} />
          <Button size="sm" icon={<Plus size={14} />} disabled={!newSet || sets.includes(newSet)} onClick={() => { setCfg({ ...cfg, ipsets: [...(cfg.ipsets ?? []), { name: newSet, entries: [] }] }); setNewSet(""); }}>{tr("Add")}</Button>
        </div>
      }>
        {(cfg.ipsets ?? []).length === 0 ? <div className="text-muted text-[13px]">{tr("Named lists of addresses and networks (aliases), used in rules as set:name.")}</div> : (
          <div className="grid gap-4 md:grid-cols-2">
            {(cfg.ipsets ?? []).map((s, si) => (
              <div key={si} className="space-y-2 rounded-xl border border-line p-4">
                <div className="flex items-center gap-2"><span className="mono flex-1 font-semibold">set:{s.name}</span>
                  <IconButton title={tr("Delete")} onClick={() => setCfg({ ...cfg, ipsets: (cfg.ipsets ?? []).filter((_, i) => i !== si) })}><Trash2 size={15} /></IconButton>
                </div>
                <textarea aria-label={tr("Addresses")} className="mono h-24 w-full rounded-lg border border-line bg-panel-2 p-2 text-[12.5px]" placeholder={"192.168.10.0/24\n203.0.113.7"} value={s.entries.join("\n")}
                  onChange={(e) => setCfg({ ...cfg, ipsets: (cfg.ipsets ?? []).map((x, i) => (i === si ? { ...x, entries: e.target.value.split(/[\n,]+/).map((v) => v.trim()).filter((v, k, a) => v !== "" || k === a.length - 1) } : x)) })} />
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card title={tr("Dropped traffic")}>
        <DropLog limit={200} />
      </Card>

      <details className="rounded-xl border border-line p-4 text-[12px]">
        <summary className="cursor-pointer font-medium">{tr("Generated nftables rules")}</summary>
        <pre className="mono mt-3 max-h-96 overflow-auto whitespace-pre text-[11.5px]">{[data.ruleset.host, data.ruleset.bridge].filter(Boolean).join("\n") || tr("(no rule: nothing is filtered)")}</pre>
      </details>

      {edit && <GuestFirewallModal kind={edit.kind} name={edit.name} onClose={() => setEdit(null)} />}
      {trial && <TrialModal pending={trial} newURLs={[]} confirmPath="/api/system/network/firewall/confirm" title={tr("Testing the new firewall rules")} onClose={() => { setTrial(null); setCfg(null); }} />}
    </div>
  );
}
