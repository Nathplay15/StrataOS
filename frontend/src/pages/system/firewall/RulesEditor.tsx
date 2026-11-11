import { useId } from "react";
import { ArrowDown, ArrowUp, Layers, Plus, Trash2 } from "lucide-react";
import { Button, Checkbox, IconButton, Input, Select } from "../../../components/ui";
import { tr } from "../../../lib/i18n";
import type { Rule } from "./types";

const cell = "!py-1 !text-[12.5px]";

/** Address field: any, lan, nas, set:<name> or addresses, with suggestions. */
function AddrInput({ value, sets, onChange, label }: { value: string; sets: string[]; onChange: (v: string) => void; label: string }) {
  const id = useId();
  return (
    <>
      <Input aria-label={label} className={`mono ${cell}`} list={id} placeholder={tr("any")} value={value} onChange={(e) => onChange(e.target.value)} />
      <datalist id={id}>
        <option value="lan">{tr("Local network (private addresses)")}</option>
        <option value="nas">{tr("This NAS")}</option>
        {sets.map((s) => <option key={s} value={`set:${s}`}>{tr("Address set")} {s}</option>)}
      </datalist>
    </>
  );
}

/**
 * Ordered rule list: the first rule that matches decides. Rules naming a
 * security group stand for the group's rules, in place.
 */
export function RulesEditor({ rules, onChange, sets, groups, hostOnly, inGroup }: { rules: Rule[]; onChange: (r: Rule[]) => void; sets: string[]; groups: string[]; hostOnly?: boolean; inGroup?: boolean }) {
  const set = (i: number, x: Partial<Rule>) => onChange(rules.map((r, k) => (k === i ? { ...r, ...x } : r)));
  const move = (i: number, d: number) => {
    const j = i + d;
    if (j < 0 || j >= rules.length) return;
    const next = [...rules];
    [next[i], next[j]] = [next[j], next[i]];
    onChange(next);
  };
  const free = groups.filter((g) => !rules.some((r) => r.group === g));
  return (
    <div className="space-y-2">
      {rules.length === 0 && <div className="text-muted text-[13px]">{tr("No rule: the default policy applies to everything.")}</div>}
      {rules.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[860px] text-[12.5px]">
            <thead className="text-muted text-left">
              <tr>
                <th className="w-8 pb-1" title={tr("On")} />
                {!hostOnly && <th className="pb-1">{tr("Direction")}</th>}
                <th className="pb-1">{tr("Action")}</th>
                <th className="pb-1">{tr("Protocol")}</th>
                <th className="pb-1">{tr("Ports")}</th>
                <th className="pb-1">{tr("Source")}</th>
                <th className="pb-1">{tr("Destination")}</th>
                <th className="pb-1" title={tr("Log matching packets")}>{tr("Log")}</th>
                <th className="pb-1">{tr("Comment")}</th>
                <th className="w-24" />
              </tr>
            </thead>
            <tbody>
              {rules.map((r, i) => (
                <tr key={i} className={`border-t border-line ${r.disabled ? "opacity-50" : ""}`}>
                  <td className="py-1"><Checkbox checked={!r.disabled} onChange={(v) => set(i, { disabled: !v })} /></td>
                  {r.group ? (
                    <td colSpan={hostOnly ? 7 : 8} className="py-1">
                      <span className="flex items-center gap-2 font-medium"><Layers size={14} className="text-accent" />{tr("Security group")} <span className="mono">{r.group}</span></span>
                    </td>
                  ) : (
                    <>
                      {!hostOnly && (
                        <td className="py-1 pr-1"><Select aria-label={tr("Direction")} className={cell} value={r.dir} onChange={(e) => set(i, { dir: e.target.value as Rule["dir"] })}>
                          <option value="in">{tr("In")}</option><option value="out">{tr("Out")}</option>
                        </Select></td>
                      )}
                      <td className="py-1 pr-1"><Select aria-label={tr("Action")} className={cell} value={r.action} onChange={(e) => set(i, { action: e.target.value as Rule["action"] })}>
                        <option value="accept">{tr("Allow")}</option><option value="drop">{tr("Drop")}</option><option value="reject">{tr("Reject")}</option>
                      </Select></td>
                      <td className="py-1 pr-1"><Select aria-label={tr("Protocol")} className={cell} value={r.proto ?? ""} onChange={(e) => { const p = e.target.value as Rule["proto"]; set(i, p === "tcp" || p === "udp" || p === "tcpudp" ? { proto: p } : { proto: p, ports: "", sports: "" }); }}>
                        <option value="">{tr("Any")}</option><option value="tcp">TCP</option><option value="udp">UDP</option><option value="tcpudp">TCP+UDP</option><option value="icmp">ICMP</option><option value="icmpv6">ICMPv6</option>
                      </Select></td>
                      <td className="py-1 pr-1"><Input aria-label={tr("Ports")} className={`mono ${cell}`} placeholder="80,443" disabled={!["tcp", "udp", "tcpudp"].includes(r.proto ?? "")} value={r.ports ?? ""} onChange={(e) => set(i, { ports: e.target.value })} /></td>
                      <td className="py-1 pr-1"><AddrInput label={tr("Source")} value={r.source ?? ""} sets={sets} onChange={(v) => set(i, { source: v })} /></td>
                      <td className="py-1 pr-1"><AddrInput label={tr("Destination")} value={r.dest ?? ""} sets={sets} onChange={(v) => set(i, { dest: v })} /></td>
                      <td className="py-1"><Checkbox checked={!!r.log} onChange={(v) => set(i, { log: v })} /></td>
                      <td className="py-1 pr-1"><Input aria-label={tr("Comment")} className={cell} value={r.comment ?? ""} onChange={(e) => set(i, { comment: e.target.value })} /></td>
                    </>
                  )}
                  <td className="py-1 whitespace-nowrap text-right">
                    <IconButton title={tr("Move up")} disabled={i === 0} onClick={() => move(i, -1)}><ArrowUp size={14} /></IconButton>
                    <IconButton title={tr("Move down")} disabled={i === rules.length - 1} onClick={() => move(i, 1)}><ArrowDown size={14} /></IconButton>
                    <IconButton title={tr("Delete")} onClick={() => onChange(rules.filter((_, k) => k !== i))}><Trash2 size={14} /></IconButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" icon={<Plus size={14} />} onClick={() => onChange([...rules, { dir: "in", action: "accept", proto: "tcp", ports: "" }])}>{tr("Add a rule")}</Button>
        {!inGroup && free.length > 0 && (
          <Select aria-label={tr("Add a security group")} className="!w-auto !py-1 !text-[12.5px]" value="" onChange={(e) => e.target.value && onChange([...rules, { dir: "in", action: "accept", group: e.target.value }])}>
            <option value="">{tr("Add a security group…")}</option>
            {free.map((g) => <option key={g} value={g}>{g}</option>)}
          </Select>
        )}
      </div>
    </div>
  );
}

/** Short reminder of the address keywords. */
export function AddrHelp() {
  return <div className="text-muted text-[12px]">{tr("Addresses: empty = any; \"lan\" = private networks (192.168…, 10…, 172.16…, VPN); \"nas\" = this NAS; \"set:name\" = an address set; or addresses and networks separated by commas (192.168.1.20, 10.0.0.0/8). The first matching rule decides.")}</div>;
}
