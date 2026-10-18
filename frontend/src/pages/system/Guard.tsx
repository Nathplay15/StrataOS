import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Ban, Globe2, Plus, ShieldBan, Trash2, Unlock } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Notice, PageHeader, Spinner, Switch } from "../../components/ui";
import { locale, tr } from "../../lib/i18n";

type Config = { enabled: boolean; maxFailures: number; windowMin: number; blockMin: number; neverLan: boolean; allow?: string[]; geo: { enabled: boolean; countries?: string[]; updated?: string; error?: string } };
type Block = { ip: string; until: string; at: string; service: string; user?: string; count: number; manual?: boolean };
type Data = { config: Config; blocks: Block[]; geoRanges: number; myIp: string; myIpLocal: boolean };

const CODES = "ad ae af ag al am ao ar at au az ba bb bd be bf bg bh bi bj bn bo br bs bt bw by bz ca cd cf cg ch ci cl cm cn co cr cu cv cy cz de dj dk dm do dz ec ee eg er es et fi fj fm fr ga gb gd ge gh gm gn gq gr gt gw gy hk hn hr ht hu id ie il in iq ir is it jm jo jp ke kg kh ki km kn kp kr kw kz la lb lc li lk lr ls lt lu lv ly ma mc md me mg mh mk ml mm mn mo mr mt mu mv mw mx my mz na ne ng ni nl no np nr nz om pa pe pg ph pk pl pr ps pt pw py qa re ro rs ru rw sa sb sc sd se sg si sk sl sm sn so sr ss st sv sy sz td tg th tj tl tm tn to tr tt tv tw tz ua ug us uy uz va vc ve vn vu ws ye za zm zw".split(" ");

export default function Guard() {
  const { data, isLoading, error } = useQuery({ queryKey: ["guard"], queryFn: () => api.get<Data>("/api/guard"), refetchInterval: 10000 });
  const [c, setC] = useState<Config | null>(null);
  const [allow, setAllow] = useState("");
  const [manual, setManual] = useState("");
  const [filter, setFilter] = useState("");
  useEffect(() => { if (data && !c) setC(data.config); }, [data, c]);
  const save = useAction(() => api.put("/api/guard", c), { success: tr("Protection saved"), onDone: () => setC(null) });
  const unblock = useAction((ip: string) => api.post("/api/guard/unblock", { ip }), { success: tr("Address unblocked") });
  const block = useAction(() => api.post("/api/guard/block", { ip: manual.trim(), minutes: 60 * 24 * 365 }), { success: tr("Blocked address"), onDone: () => setManual("") });
  const names = useMemo(() => { try { return new Intl.DisplayNames([locale], { type: "region" }); } catch { return null; } }, []);
  const name = (cc: string) => names?.of(cc.toUpperCase()) ?? cc.toUpperCase();
  const countries = useMemo(() => CODES.map((cc) => ({ cc, n: name(cc) })).sort((a, b) => a.n.localeCompare(b.n, locale)), [names]); // eslint-disable-line react-hooks/exhaustive-deps

  const header = <PageHeader title={tr("Address blocking")} subtitle={tr("Automatically blocks addresses that fail to sign in too many times, and restricts access to certain countries")} icon={<ShieldBan size={20} />} actions={c && <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>} />;
  if (isLoading || !data || !c) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  const sel = c.geo.countries ?? [];

  return (
    <div>
      {header}
      <ErrorBox error={save.error} />
      <div className="grid gap-5 lg:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Ban size={16} />{tr("Automatic blocking")}</span>}>
          <div className="space-y-4">
            <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Block addresses that fail too often")} hint={tr("Web interface, WebDAV, share links, SSH/SFTP, SMB and FTP.")} />
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label={tr("Failures")}><Input type="number" min={1} value={c.maxFailures} onChange={(e) => setC({ ...c, maxFailures: Math.max(1, +e.target.value) })} /></Field>
              <Field label={tr("In (minutes)")}><Input type="number" min={1} value={c.windowMin} onChange={(e) => setC({ ...c, windowMin: Math.max(1, +e.target.value) })} /></Field>
              <Field label={tr("Block duration (minutes)")} hint={tr("Doubled on repeat offense")}><Input type="number" min={1} value={c.blockMin} onChange={(e) => setC({ ...c, blockMin: Math.max(1, +e.target.value) })} /></Field>
            </div>
            <Switch checked={c.neverLan} onChange={(v) => setC({ ...c, neverLan: v })} label={tr("Never block the local network")} hint={tr("Private addresses (192.168…, 10…), VPN included: avoids locking yourself out.")} />
            <div>
              <div className="mb-1.5 text-[12.5px] font-medium text-muted">{tr("Never-blocked addresses")}</div>
              <div className="flex flex-wrap gap-1.5">
                {(c.allow ?? []).map((a) => <span key={a} className="flex items-center gap-1 rounded-lg bg-panel-2 px-2 py-1 font-mono text-[12px]">{a}<button onClick={() => setC({ ...c, allow: (c.allow ?? []).filter((x) => x !== a) })}><Trash2 size={12} /></button></span>)}
              </div>
              <div className="mt-2 flex gap-2"><Input className="mono" placeholder="203.0.113.0/24" value={allow} onChange={(e) => setAllow(e.target.value.trim())} /><Button icon={<Plus size={15} />} disabled={!allow} onClick={() => { setC({ ...c, allow: [...(c.allow ?? []), allow] }); setAllow(""); }} /></div>
            </div>
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Globe2 size={16} />{tr("Allowed countries")}</span>}>
          <div className="space-y-4">
            <Switch checked={c.geo.enabled} onChange={(v) => setC({ ...c, geo: { ...c.geo, enabled: v } })} label={tr("Only accept certain countries from the Internet")} hint={tr("The local network and never-blocked addresses are always allowed. Country address lists are updated weekly.")} />
            {c.geo.enabled && <>
              <div className="flex flex-wrap gap-1.5">{sel.map((cc) => <Badge key={cc} tone="accent">{name(cc)} <button className="ml-1" onClick={() => setC({ ...c, geo: { ...c.geo, countries: sel.filter((x) => x !== cc) } })}>×</button></Badge>)}</div>
              <Input placeholder={tr("Search for a country")} value={filter} onChange={(e) => setFilter(e.target.value)} />
              <div className="max-h-48 overflow-auto rounded-lg border border-line">
                {countries.filter((x) => !sel.includes(x.cc) && x.n.toLowerCase().includes(filter.toLowerCase())).map((x) => (
                  <button key={x.cc} className="block w-full px-3 py-1.5 text-left text-[13px] hover:bg-panel-2" onClick={() => setC({ ...c, geo: { ...c.geo, countries: [...sel, x.cc] } })}>{x.n}</button>
                ))}
              </div>
              {!data.myIpLocal && <Notice tone="warn">{tr("You are connected from {0}: this country must be in the list, otherwise saving is refused.", data.myIp)}</Notice>}
            </>}
            {data.config.geo.error && <div className="text-danger text-[13px]">{data.config.geo.error}</div>}
            {data.config.geo.enabled && data.geoRanges > 0 && <div className="text-muted text-[12px]">{tr("{0} address ranges loaded", data.geoRanges)}{data.config.geo.updated ? ` · ${dateTime(data.config.geo.updated)}` : ""}</div>}
          </div>
        </Card>
      </div>

      <Card className="mt-5" title={tr("Blocked addresses")} actions={<div className="flex gap-2"><Input className="mono w-44" placeholder={tr("Address to block")} value={manual} onChange={(e) => setManual(e.target.value)} /><Button size="sm" disabled={!manual || !data.config.enabled} loading={block.isPending} onClick={() => block.mutate()}>{tr("Block for 1 year")}</Button></div>}>
        <ErrorBox error={block.error} />
        {!data.blocks.length ? <Empty icon={<ShieldBan />} title={tr("No blocked addresses")} /> : (
          <table className="table">
            <thead><tr><th>{tr("Address")}</th><th>{tr("Service")}</th><th>{tr("Blocked on")}</th><th>{tr("Until")}</th><th /></tr></thead>
            <tbody>
              {data.blocks.map((b) => (
                <tr key={b.ip}>
                  <td className="mono">{b.ip}{b.count > 1 && <Badge tone="warn">{tr("repeat offense ×{0}", b.count)}</Badge>}</td>
                  <td>{b.service}{b.user ? <span className="text-muted"> · {b.user}</span> : null}</td>
                  <td className="text-muted text-[12.5px]">{dateTime(b.at)}</td>
                  <td className="text-[12.5px]">{dateTime(b.until)}</td>
                  <td className="text-right"><Button size="sm" variant="ghost" icon={<Unlock size={14} />} loading={unblock.isPending && unblock.variables === b.ip} onClick={() => unblock.mutate(b.ip)}>{tr("Unblock")}</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
