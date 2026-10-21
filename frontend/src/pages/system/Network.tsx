import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Link2, Waypoints, Network as NetIcon, Plus, RotateCcw, Trash2, Undo2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Badge, Button, Card, Checkbox, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, useUI } from "../../components/ui";
import { tr, locale } from "../../lib/i18n";

type Link = { name: string; mac: string; state: string; mtu: number; addresses: string[]; speed: number; kind: string };
type Status = { interfaces: Link[]; gateway: string; dns: string[]; hostname: string };
type IP = { mode: "dhcp" | "static" | "none"; addresses?: string[]; gateway?: string; dns?: string[]; mtu?: number };
type Iface = IP & { name: string };
type Bond = IP & { name: string; bondMode: string; members: string[] };
type Bridge = IP & { name: string; members: string[]; stp: boolean };
type VLAN = IP & { parent: string; id: number };
type Config = { interfaces: Iface[]; bonds: Bond[]; bridges: Bridge[]; vlans: VLAN[] };
export type Pending = { deadline: string; links: string[]; token?: string };
type Net = { status: Status; manager: string; config: Config; pending: Pending | null };

const bondModes: [string, string][] = [
  ["active-backup", tr("Active/backup (fault tolerance, no switch configuration)")],
  ["802.3ad", tr("LACP 802.3ad (aggregation, compatible switch required)")],
  ["balance-alb", tr("Adaptive load balancing (no special switch)")],
  ["balance-tlb", tr("Transmit load balancing")],
  ["balance-rr", "Round-robin"],
  ["balance-xor", "XOR"],
  ["broadcast", tr("Broadcast")],
];

const list = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

function IPEditor({ ip, onChange }: { ip: IP; onChange: (ip: IP) => void }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <Field label={tr("Addressing")}>
        <Select value={ip.mode} onChange={(e) => onChange({ ...ip, mode: e.target.value as IP["mode"] })}>
          <option value="dhcp">{tr("DHCP (automatic)")}</option>
          <option value="static">{tr("Static address")}</option>
          <option value="none">{tr("No IP address")}</option>
        </Select>
      </Field>
      {ip.mode === "static" && (
        <>
          <Field label={tr("Address(es) / mask")} hint={tr("e.g. 192.168.1.20/24")}><Input className="mono" value={(ip.addresses ?? []).join(" ")} onChange={(e) => onChange({ ...ip, addresses: list(e.target.value) })} /></Field>
          <Field label={tr("Gateway")}><Input className="mono" value={ip.gateway ?? ""} onChange={(e) => onChange({ ...ip, gateway: e.target.value.trim() })} /></Field>
        </>
      )}
      {ip.mode !== "none" && <Field label="DNS" hint={tr("optional, separated by spaces")}><Input className="mono" value={(ip.dns ?? []).join(" ")} onChange={(e) => onChange({ ...ip, dns: list(e.target.value) })} /></Field>}
      <Field label="MTU" hint={tr("empty = default, 9000 = jumbo frames")}><Input type="number" value={ip.mtu || ""} onChange={(e) => onChange({ ...ip, mtu: +e.target.value || 0 })} /></Field>
    </div>
  );
}

/** Watches a trial configuration: confirms it when the NAS answers, offers the new addresses otherwise. */
export function TrialModal({ pending, newURLs, onClose }: { pending: Pending; newURLs: string[]; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [left, setLeft] = useState(Math.round((new Date(pending.deadline).getTime() - Date.now()) / 1000));
  const [state, setState] = useState<"waiting" | "confirmed" | "unreachable">("waiting");
  useEffect(() => {
    const t = setInterval(() => setLeft(Math.round((new Date(pending.deadline).getTime() - Date.now()) / 1000)), 1000);
    return () => clearInterval(t);
  }, [pending.deadline]);
  useEffect(() => {
    if (!pending.token) return;
    let stop = false;
    (async () => {
      let fails = 0;
      // Give the links a few seconds to come up before judging connectivity.
      await new Promise((r) => setTimeout(r, 4000));
      while (!stop) {
        try {
          const r = await fetch("/api/system/network/confirm", { method: "POST", headers: { "Content-Type": "application/json", "X-Strata": "1" }, body: JSON.stringify({ token: pending.token }), signal: AbortSignal.timeout(4000) });
          if (r.ok) {
            setState("confirmed");
            toast(tr("New network configuration confirmed"));
            qc.invalidateQueries();
            return;
          }
          // 409: the NAS answers but the change is still settling — keep waiting.
          if (r.status !== 409) fails++;
        } catch {
          fails++;
        }
        if (fails >= 3) setState("unreachable");
        await new Promise((r) => setTimeout(r, 2500));
      }
    })();
    return () => { stop = true; };
  }, [pending.token, qc, toast]);
  const expired = left <= 0 && state !== "confirmed";
  return (
    <Modal open onClose={onClose} title={tr("Testing the new network configuration")} footer={state === "confirmed" || expired ? <Button variant="primary" onClick={onClose}>{tr("Close")}</Button> : undefined}>
      {state === "confirmed" ? (
        <div className="flex items-center gap-3 text-ok"><CheckCircle2 size={22} /> {" "}{tr("Configuration kept: the NAS is reachable.")}</div>
      ) : expired ? (
        <Notice tone="warn">{tr("Timeout elapsed: the previous configuration has been restored.")}</Notice>
      ) : (
        <div className="space-y-4">
          <div className="text-center">
            <div className="text-[34px] font-semibold tabular-nums">{left} s</div>
            <div className="text-muted text-[13px]">{tr("before the automatic rollback to the previous configuration")}</div>
          </div>
          {state === "waiting" ? <Spinner label={tr("Checking that the NAS is still reachable…")} /> : (
            <Notice tone="warn">
              
              {tr("The NAS no longer responds at this address. If its address has changed, open one of these links")}{" "}<b>{tr("before the timeout expires")}</b> {" "}{tr("to confirm:")}
              <div className="mt-2 space-y-1">{newURLs.map((u) => <a key={u} href={u} className="mono block text-accent underline">{u.split("?")[0]}</a>)}</div>
              {newURLs.length === 0 && <div className="mt-2">{tr("Otherwise, wait: the previous configuration will come back on its own.")}</div>}
            </Notice>
          )}
        </div>
      )}
    </Modal>
  );
}

export default function NetworkPage() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["network"], queryFn: () => api.get<Net>("/api/system/network"), refetchInterval: 10000 });
  const [cfg, setCfg] = useState<Config | null>(null);
  const [host, setHost] = useState<string | null>(null);
  const [trial, setTrial] = useState<{ p: Pending; urls: string[] } | null>(null);
  const saveHost = useAction((h: string) => api.put("/api/system/hostname", { hostname: h }), { success: tr("Hostname changed"), onDone: () => setHost(null) });
  const rollback = useAction(() => api.post("/api/system/network/rollback"), { success: tr("Previous configuration restored") });
  const apply = useAction(() => api.put<Pending>("/api/system/network/config", { config: cfg, timeout: 120 }), {
    onDone: (p) => {
      const port = location.port ? `:${location.port}` : "";
      const statics = [...(cfg?.interfaces ?? []), ...(cfg?.bonds ?? []), ...(cfg?.bridges ?? []), ...(cfg?.vlans ?? [])].filter((x) => x.mode === "static").flatMap((x) => x.addresses ?? []);
      const addrs = statics.map((a) => a.split("/")[0]).filter((a) => a !== location.hostname);
      setTrial({ p, urls: addrs.map((a) => `${location.protocol}//${a}${port}/?netconfirm=${p.token}`) });
    },
  });
  useEffect(() => {
    if (data && !cfg) setCfg(structuredClone({ ...data.config, bridges: data.config.bridges ?? [] }));
  }, [data, cfg]);

  const physical = useMemo(() => (data?.status.interfaces ?? []).filter((i) => i.kind === "physical" && !i.name.startsWith("bond") && !/^br\d/.test(i.name)), [data]);
  if (isLoading || !data || !cfg) return <div><PageHeader title={tr("Network")} icon={<NetIcon size={20} />} /><ErrorBox error={error} /><Spinner /></div>;

  const dirty = JSON.stringify(cfg) !== JSON.stringify({ ...data.config, bridges: data.config.bridges ?? [] });
  const members = new Set(cfg.bonds.flatMap((b) => b.members));
  const bridged = new Set(cfg.bridges.flatMap((b) => b.members));
  const managed = (name: string) => cfg.interfaces.find((i) => i.name === name);
  const setIface = (name: string, ip: IP | null) =>
    setCfg({
      ...cfg,
      interfaces: ip ? [...cfg.interfaces.filter((i) => i.name !== name), { ...ip, name }].sort((a, b) => a.name.localeCompare(b.name)) : cfg.interfaces.filter((i) => i.name !== name),
      vlans: ip ? cfg.vlans : cfg.vlans.filter((v) => v.parent !== name),
    });
  const parents = [...cfg.interfaces.map((i) => i.name), ...cfg.bonds.filter((b) => !bridged.has(b.name)).map((b) => b.name), ...cfg.bridges.map((b) => b.name)];
  const nm = data.manager === "NetworkManager";

  const doApply = async () => {
    if (await confirm({ title: tr("Apply network configuration?"), text: <>{tr("It will be applied")}{" "}<b>{tr("on trial")}</b> {" "}{tr("for 2 minutes. If the NAS cannot be reached to confirm it (wrong address, cable, switch…), the previous configuration is restored automatically.")}</>, confirm: tr("Apply on trial") })) apply.mutate();
  };

  return (
    <div>
      <PageHeader title={tr("Network")} subtitle={tr("Interfaces, link aggregation, bridges, VLANs and NAS name")} icon={<NetIcon size={20} />} actions={<>
        {dirty && <Button icon={<Undo2 size={15} />} onClick={() => setCfg(structuredClone({ ...data.config, bridges: data.config.bridges ?? [] }))}>{tr("Discard changes")}</Button>}
        <Button variant="primary" disabled={!dirty || nm || !!data.pending} loading={apply.isPending} onClick={doApply}>{tr("Apply")}</Button>
      </>} />
      <ErrorBox error={error || apply.error} />
      {nm && <div className="mb-4"><Notice tone="warn">{tr("NetworkManager manages the network on this machine: StrataOS does not change the configuration. Disable NetworkManager to manage the network from this page.")}</Notice></div>}
      {data.pending && !trial && (
        <div className="mb-4"><Notice tone="warn">
          <div className="flex flex-wrap items-center gap-3">{tr("A network configuration is on trial until")}{" "}{new Date(data.pending.deadline).toLocaleTimeString(locale)}.
            <Button size="sm" icon={<RotateCcw size={14} />} loading={rollback.isPending} onClick={() => rollback.mutate()}>{tr("Restore now")}</Button>
          </div>
        </Notice></div>
      )}

      <div className="space-y-5">
        <Card title={tr("General")}>
          <div className="grid gap-5 sm:grid-cols-4">
            <div className="flex items-end gap-2 sm:col-span-2">
              <Field label={tr("Hostname")} className="flex-1"><Input value={host ?? data.status.hostname} onChange={(e) => setHost(e.target.value)} /></Field>
              <Button disabled={!host || host === data.status.hostname} loading={saveHost.isPending} onClick={() => saveHost.mutate(host!)}>{tr("Save")}</Button>
            </div>
            <div><div className="text-muted text-[12px]">{tr("Current gateway")}</div><div className="mono mt-2">{data.status.gateway || "—"}</div></div>
            <div><div className="text-muted text-[12px]">{tr("Current DNS")}</div><div className="mono mt-2 truncate">{data.status.dns.join(", ") || "—"}</div></div>
          </div>
          <div className="mt-3 text-[12px] text-muted">{tr("Network manager detected:")}{" "}<b>{data.manager}</b></div>
        </Card>

        <Card title={tr("Interfaces")}>
          <div className="space-y-4">
            {physical.map((l) => {
              const m = managed(l.name);
              const inBond = cfg.bonds.find((b) => b.members.includes(l.name)) ?? cfg.bridges.find((b) => b.members.includes(l.name));
              return (
                <div key={l.name} className="rounded-xl border border-line p-4">
                  <div className="mb-3 flex flex-wrap items-center gap-2">
                    <span className="font-semibold">{l.name}</span>
                    <Badge tone={l.state === "UP" ? "ok" : "neutral"} dot>{l.state}</Badge>
                    {l.speed > 0 && <Badge tone="accent">{l.speed >= 1000 ? tr("{0} Gb/s", l.speed / 1000) : tr("{0} Mb/s", l.speed)}</Badge>}
                    <span className="mono text-muted text-[12px]">{l.mac} · {l.addresses.join(", ") || tr("no address")}</span>
                    <div className="flex-1" />
                    {inBond ? <Badge>{tr("member of")}{" "}{inBond.name}</Badge> : (
                      <Select className="!w-auto !py-1 !text-[12.5px]" value={m ? "managed" : "system"} onChange={(e) => setIface(l.name, e.target.value === "managed" ? { mode: "dhcp" } : null)}>
                        <option value="system">{tr("System configuration")}</option>
                        <option value="managed">{tr("Managed by StrataOS")}</option>
                      </Select>
                    )}
                  </div>
                  {m && !inBond && <IPEditor ip={m} onChange={(ip) => setIface(l.name, ip)} />}
                </div>
              );
            })}
          </div>
        </Card>

        <Card title={tr("Link aggregation (bonding)")} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => {
          let n = 0;
          while (cfg.bonds.some((b) => b.name === `bond${n}`)) n++;
          setCfg({ ...cfg, bonds: [...cfg.bonds, { name: `bond${n}`, bondMode: "active-backup", members: [], mode: "dhcp" }] });
        }}>{tr("Add")}</Button>}>
          {cfg.bonds.length === 0 ? <div className="text-muted text-[13px]">{tr("Combine several network ports for fault tolerance or higher throughput.")}</div> : (
            <div className="space-y-4">
              {cfg.bonds.map((b, bi) => {
                const upd = (x: Partial<Bond>) => setCfg({ ...cfg, bonds: cfg.bonds.map((y, i) => (i === bi ? { ...y, ...x } : y)), interfaces: x.members ? cfg.interfaces.filter((i) => !x.members!.includes(i.name)) : cfg.interfaces });
                return (
                  <div key={bi} className="space-y-3 rounded-xl border border-line p-4">
                    <div className="flex flex-wrap items-center gap-3">
                      <span className="flex items-center gap-2 font-semibold"><Link2 size={16} className="text-accent" />{b.name}</span>
                      <Select className="!w-auto !py-1 !text-[12.5px]" value={b.bondMode} onChange={(e) => upd({ bondMode: e.target.value })}>
                        {bondModes.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                      </Select>
                      <div className="flex-1" />
                      <IconButton title={tr("Delete")} onClick={() => setCfg({ ...cfg, bonds: cfg.bonds.filter((_, i) => i !== bi), vlans: cfg.vlans.filter((v) => v.parent !== b.name) })}><Trash2 size={15} /></IconButton>
                    </div>
                    <div className="flex flex-wrap gap-4">
                      {physical.filter((l) => !members.has(l.name) || b.members.includes(l.name)).map((l) => (
                        <Checkbox key={l.name} checked={b.members.includes(l.name)} onChange={(v) => upd({ members: v ? [...b.members, l.name] : b.members.filter((x) => x !== l.name) })} label={<span className="mono">{l.name}</span>} />
                      ))}
                    </div>
                    {b.bondMode === "active-backup" && b.members.length > 0 && <div className="text-muted text-[12px]">{tr("Main port:")}{" "}<span className="mono">{b.members[0]}</span></div>}
                    {bridged.has(b.name) ? <div className="text-muted text-[12px]">{tr("Bridge port")}{" "}{cfg.bridges.find((x) => x.members.includes(b.name))?.name} {" "}{tr(": the IP address is held by the bridge.")}</div> : <IPEditor ip={b} onChange={(ip) => upd(ip)} />}
                  </div>
                );
              })}
            </div>
          )}
        </Card>

        <Card title={tr("Bridges")} actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => {
          let n = 0;
          while (cfg.bridges.some((b) => b.name === `br${n}`)) n++;
          setCfg({ ...cfg, bridges: [...cfg.bridges, { name: `br${n}`, members: [], stp: false, mode: "dhcp" }] });
        }}>{tr("Add")}</Button>}>
          {cfg.bridges.length === 0 ? <div className="text-muted text-[13px]">{tr("A bridge connects virtual machines directly to the local network: they get their own address from your router, like a PC plugged into the switch.")}</div> : (
            <div className="space-y-4">
              {cfg.bridges.map((br, bi) => {
                const upd = (x: Partial<Bridge>) => setCfg({ ...cfg, bridges: cfg.bridges.map((y, i) => (i === bi ? { ...y, ...x } : y)), interfaces: x.members ? cfg.interfaces.filter((i) => !x.members!.includes(i.name)) : cfg.interfaces, bonds: x.members ? cfg.bonds.map((b) => (x.members!.includes(b.name) ? { ...b, mode: "none" as const, addresses: [], gateway: "", dns: [] } : b)) : cfg.bonds, vlans: x.members ? cfg.vlans.filter((v) => !x.members!.includes(v.parent)) : cfg.vlans });
                const candidates = [...physical.map((l) => l.name).filter((n) => !members.has(n)), ...cfg.bonds.map((b) => b.name)].filter((n) => !bridged.has(n) || br.members.includes(n));
                return (
                  <div key={bi} className="space-y-3 rounded-xl border border-line p-4">
                    <div className="flex flex-wrap items-center gap-3">
                      <span className="flex items-center gap-2 font-semibold"><Waypoints size={16} className="text-accent" />{br.name}</span>
                      <Checkbox checked={br.stp} onChange={(v) => upd({ stp: v })} label={tr("Spanning Tree (STP)")} />
                      <div className="flex-1" />
                      <IconButton title={tr("Delete")} onClick={() => setCfg({ ...cfg, bridges: cfg.bridges.filter((_, i) => i !== bi), vlans: cfg.vlans.filter((v) => v.parent !== br.name) })}><Trash2 size={15} /></IconButton>
                    </div>
                    <div className="flex flex-wrap gap-4">
                      {candidates.map((n) => (
                        <Checkbox key={n} checked={br.members.includes(n)} onChange={(v) => upd({ members: v ? [...br.members, n] : br.members.filter((x) => x !== n) })} label={<span className="mono">{n}</span>} />
                      ))}
                    </div>
                    {br.members.length > 0 && <div className="text-muted text-[12px]">{tr("The bridge takes the MAC address of")}{" "}<span className="mono">{br.members[0]}</span> {" "}{tr(": with DHCP, the NAS keeps the same IP address.")}</div>}
                    <IPEditor ip={br} onChange={(ip) => upd(ip)} />
                  </div>
                );
              })}
            </div>
          )}
        </Card>

        <Card title="VLAN" actions={<Button size="sm" icon={<Plus size={14} />} disabled={parents.length === 0} onClick={() => setCfg({ ...cfg, vlans: [...cfg.vlans, { parent: parents[0], id: 10, mode: "dhcp" }] })}>{tr("Add")}</Button>}>
          {parents.length === 0 && <div className="text-muted text-[13px]">{tr("First switch an interface to \"Managed by StrataOS\" to create VLANs on it.")}</div>}
          <div className="space-y-4">
            {cfg.vlans.map((v, vi) => {
              const upd = (x: Partial<VLAN>) => setCfg({ ...cfg, vlans: cfg.vlans.map((y, i) => (i === vi ? { ...y, ...x } : y)) });
              return (
                <div key={vi} className="space-y-3 rounded-xl border border-line p-4">
                  <div className="flex flex-wrap items-end gap-3">
                    <Field label={tr("Parent interface")}><Select value={v.parent} onChange={(e) => upd({ parent: e.target.value })}>{parents.map((p) => <option key={p}>{p}</option>)}</Select></Field>
                    <Field label={tr("VLAN ID")}><Input type="number" min={1} max={4094} value={v.id} onChange={(e) => upd({ id: +e.target.value })} /></Field>
                    <span className="mono pb-2 text-muted">{v.parent}.{v.id}</span>
                    <div className="flex-1" />
                    <IconButton title={tr("Delete")} onClick={() => setCfg({ ...cfg, vlans: cfg.vlans.filter((_, i) => i !== vi) })}><Trash2 size={15} /></IconButton>
                  </div>
                  <IPEditor ip={v} onChange={(ip) => upd(ip)} />
                </div>
              );
            })}
          </div>
        </Card>
      </div>
      {trial && <TrialModal pending={trial.p} newURLs={trial.urls} onClose={() => { setTrial(null); setCfg(null); }} />}
    </div>
  );
}
