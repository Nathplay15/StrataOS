import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Download, FileText } from "lucide-react";
import { api, q } from "../../lib/api";
import type { LogEntry, StrataEvent } from "../../lib/types";
import { dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Input, PageHeader, Select, Spinner, Tabs } from "../../components/ui";
import { tr, locale, srv } from "../../lib/i18n";

const prio = ["emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"];

function Journal() {
  const [unit, setUnit] = useState("");
  const [priority, setPriority] = useState(6);
  const [grep, setGrep] = useState("");
  const [search, setSearch] = useState("");
  const { data, isLoading, error } = useQuery({ queryKey: ["journal", unit, priority, search], queryFn: () => api.get<LogEntry[]>(`/api/system/logs${q({ unit, priority, grep: search, lines: 500 })}`), refetchInterval: 10000 });
  return (
    <>
      <div className="mb-4 flex flex-wrap gap-2">
        <Select className="!w-56" value={unit} onChange={(e) => setUnit(e.target.value)}>
          <option value="">{tr("All services")}</option>
          {["strata", "smbd", "nfs-server", "docker", "ssh", "mdmonitor", "zfs-zed", "smartmontools", "kernel"].map((u) => <option key={u} value={u === "kernel" ? "" : u}>{u}</option>)}
        </Select>
        <Select className="!w-48" value={priority} onChange={(e) => setPriority(+e.target.value)}>
          {prio.map((p, i) => <option key={p} value={i}>≤ {p}</option>)}
        </Select>
        <Input className="!w-64" placeholder={tr("Search (Enter)")} value={grep} onChange={(e) => setGrep(e.target.value)} onKeyDown={(e) => e.key === "Enter" && setSearch(grep)} />
      </div>
      <ErrorBox error={error} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<FileText />} title={tr("No entries")} /> : (
          <div className="mono max-h-[70vh] overflow-auto text-[12px]">
            {data.map((l, i) => (
              <div key={i} className="flex gap-3 border-b border-line px-4 py-1.5 hover:bg-panel-2">
                <span className="shrink-0 text-muted">{new Date(l.time).toLocaleString(locale)}</span>
                <span className={`w-16 shrink-0 ${l.priority <= 3 ? "text-danger" : l.priority === 4 ? "text-warn" : "text-muted"}`}>{prio[l.priority]}</span>
                <span className="w-32 shrink-0 truncate text-accent">{l.ident || l.unit}</span>
                <span className="whitespace-pre-wrap break-all">{l.message}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}

function Events() {
  const { data, isLoading } = useQuery({ queryKey: ["events"], queryFn: () => api.get<StrataEvent[]>(`/api/system/events${q({ limit: 2000 })}`), refetchInterval: 10000 });
  const [user, setUser] = useState("");
  const [cat, setCat] = useState("");
  const [level, setLevel] = useState("");
  const [period, setPeriod] = useState("0");
  const [search, setSearch] = useState("");
  const users = [...new Set((data ?? []).map((e) => e.user).filter((u): u is string => !!u))].sort((a, b) => a.localeCompare(b));
  const cats = [...new Set((data ?? []).map((e) => e.category))].sort((a, b) => a.localeCompare(b));
  const since = period === "0" ? 0 : Date.now() - Number(period) * 86400000;
  const list = (data ?? []).filter((e) =>
    (!user || e.user === user) &&
    (!cat || e.category === cat) &&
    (!level || e.level === level) &&
    (!since || new Date(e.time).getTime() >= since) &&
    (!search || e.message.toLowerCase().includes(search.toLowerCase())));
  const exportCsv = () => {
    const esc = (v: string) => `"${v.replaceAll('"', '""')}"`;
    const rows = [["time", "level", "category", "user", "message"], ...list.map((e) => [new Date(e.time).toISOString(), e.level, e.category, e.user ?? "", srv(e.message)])];
    const blob = new Blob([rows.map((r) => r.map(esc).join(",")).join("\n")], { type: "text/csv;charset=utf-8" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `strataos-audit-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  };
  return (
    <>
      {!isLoading && !!data?.length && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          <Select className="!w-44" value={user} onChange={(e) => setUser(e.target.value)}>
            <option value="">{tr("All users")}</option>
            {users.map((u) => <option key={u} value={u}>{u}</option>)}
          </Select>
          <Select className="!w-44" value={cat} onChange={(e) => setCat(e.target.value)}>
            <option value="">{tr("All categories")}</option>
            {cats.map((c) => <option key={c} value={c}>{c}</option>)}
          </Select>
          <Select className="!w-36" value={level} onChange={(e) => setLevel(e.target.value)}>
            <option value="">{tr("All levels")}</option>
            {["info", "warning", "error"].map((l) => <option key={l} value={l}>{l}</option>)}
          </Select>
          <Select className="!w-44" value={period} onChange={(e) => setPeriod(e.target.value)}>
            <option value="0">{tr("All time")}</option>
            <option value="1">{tr("Last 24 hours")}</option>
            <option value="7">{tr("Last 7 days")}</option>
            <option value="30">{tr("Last 30 days")}</option>
          </Select>
          <Input className="!w-64" placeholder={tr("Search (Enter)")} value={search} onChange={(e) => setSearch(e.target.value)} />
          <span className="text-muted text-[12.5px]">{tr("{0} of {1} events", list.length, data.length)}</span>
          <Button size="sm" className="ml-auto" icon={<Download size={13} />} disabled={!list.length} onClick={exportCsv}>{tr("Export CSV")}</Button>
        </div>
      )}
      <Card pad={false}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty title={tr("No events")} /> : !list.length ? <Empty title={tr("No events")} text={tr("No event matches these filters.")} /> : (
          <div className="max-h-[70vh] overflow-auto">
            <table className="table">
              <thead className="sticky top-0 bg-panel"><tr><th>{tr("Date")}</th><th>{tr("Level")}</th><th>{tr("Category")}</th><th>{tr("User")}</th><th>{tr("Message")}</th></tr></thead>
              <tbody>
                {list.map((e, i) => (
                  <tr key={i}>
                    <td className="whitespace-nowrap text-muted text-[12.5px]">{dateTime(e.time)}</td>
                    <td><Badge tone={e.level === "error" ? "danger" : e.level === "warning" ? "warn" : "neutral"}>{e.level}</Badge></td>
                    <td>{e.category}</td><td>{e.user || "—"}</td><td>{srv(e.message)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

export default function Logs() {
  const [tab, setTab] = useState<"events" | "journal">("events");
  return (
    <div>
      <PageHeader title={tr("Logs")} subtitle={tr("Action audit and system log")} icon={<FileText size={20} />} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "events", label: tr("StrataOS events") }, { id: "journal", label: tr("System log") }]} />
      {tab === "events" ? <Events /> : <Journal />}
    </div>
  );
}
