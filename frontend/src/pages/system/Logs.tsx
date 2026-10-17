import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FileText } from "lucide-react";
import { api, q } from "../../lib/api";
import type { LogEntry, StrataEvent } from "../../lib/types";
import { dateTime } from "../../lib/format";
import { Badge, Card, Empty, ErrorBox, Input, PageHeader, Select, Spinner, Tabs } from "../../components/ui";

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
          <option value="">Tous les services</option>
          {["strata", "smbd", "nfs-server", "docker", "ssh", "mdmonitor", "zfs-zed", "smartmontools", "kernel"].map((u) => <option key={u} value={u === "kernel" ? "" : u}>{u}</option>)}
        </Select>
        <Select className="!w-48" value={priority} onChange={(e) => setPriority(+e.target.value)}>
          {prio.map((p, i) => <option key={p} value={i}>≤ {p}</option>)}
        </Select>
        <Input className="!w-64" placeholder="Rechercher (Entrée)" value={grep} onChange={(e) => setGrep(e.target.value)} onKeyDown={(e) => e.key === "Enter" && setSearch(grep)} />
      </div>
      <ErrorBox error={error} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<FileText />} title="Aucune entrée" /> : (
          <div className="mono max-h-[70vh] overflow-auto text-[12px]">
            {data.map((l, i) => (
              <div key={i} className="flex gap-3 border-b border-line px-4 py-1.5 hover:bg-panel-2">
                <span className="shrink-0 text-muted">{new Date(l.time).toLocaleString("fr-FR")}</span>
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
  const { data, isLoading } = useQuery({ queryKey: ["events"], queryFn: () => api.get<StrataEvent[]>("/api/system/events"), refetchInterval: 10000 });
  return (
    <Card pad={false}>
      {isLoading ? <Spinner /> : !data?.length ? <Empty title="Aucun événement" /> : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>Date</th><th>Niveau</th><th>Catégorie</th><th>Utilisateur</th><th>Message</th></tr></thead>
            <tbody>
              {data.map((e, i) => (
                <tr key={i}>
                  <td className="whitespace-nowrap text-muted text-[12.5px]">{dateTime(e.time)}</td>
                  <td><Badge tone={e.level === "error" ? "danger" : e.level === "warning" ? "warn" : "neutral"}>{e.level}</Badge></td>
                  <td>{e.category}</td><td>{e.user || "—"}</td><td>{e.message}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

export default function Logs() {
  const [tab, setTab] = useState<"events" | "journal">("events");
  return (
    <div>
      <PageHeader title="Journaux" subtitle="Audit des actions et journal système" icon={<FileText size={20} />} />
      <Tabs value={tab} onChange={setTab} tabs={[{ id: "events", label: "Événements StrataOS" }, { id: "journal", label: "Journal système" }]} />
      {tab === "events" ? <Events /> : <Journal />}
    </div>
  );
}
