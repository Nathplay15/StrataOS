import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Download, ScrollText } from "lucide-react";
import { api, q } from "../lib/api";
import type { StrataEvent } from "../lib/types";
import { Badge, Button, Card, Empty, ErrorBox, Input, PageHeader, Select, Spinner } from "../components/ui";
import { tr, locale, srv } from "../lib/i18n";

/** What was done with this account on the NAS: its own audit events only. */
export default function Activity() {
  const { data, isLoading, error } = useQuery({ queryKey: ["my-activity"], queryFn: () => api.get<StrataEvent[]>(`/api/me/activity${q({ limit: 2000 })}`), refetchInterval: 15000 });
  const [cat, setCat] = useState("");
  const [level, setLevel] = useState("");
  const [period, setPeriod] = useState("0");
  const [search, setSearch] = useState("");
  const cats = [...new Set((data ?? []).map((e) => e.category))].sort((a, b) => a.localeCompare(b));
  const since = period === "0" ? 0 : Date.now() - Number(period) * 86400000;
  const list = (data ?? []).filter((e) =>
    (!cat || e.category === cat) &&
    (!level || e.level === level) &&
    (!since || new Date(e.time).getTime() >= since) &&
    (!search || e.message.toLowerCase().includes(search.toLowerCase())));
  const exportCsv = () => {
    const esc = (v: string) => `"${v.replaceAll('"', '""')}"`;
    const rows = [["time", "level", "category", "message"], ...list.map((e) => [new Date(e.time).toISOString(), e.level, e.category, srv(e.message)])];
    const blob = new Blob([rows.map((r) => r.map(esc).join(",")).join("\n")], { type: "text/csv;charset=utf-8" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `strataos-activity-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  };
  return (
    <div>
      <PageHeader title={tr("My activity")} subtitle={tr("What was done with your account on the NAS")} icon={<ScrollText size={20} />} />
      <ErrorBox error={error} />
      {!isLoading && !!data?.length && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
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
        {isLoading ? <Spinner /> : !data?.length ? (
          <Empty icon={<ScrollText />} title={tr("No events")} text={tr("Actions done with your account (files, sign-ins, links) appear here.")} />
        ) : !list.length ? (
          <Empty title={tr("No events")} text={tr("No event matches these filters.")} />
        ) : (
          <div className="max-h-[70vh] overflow-auto">
            <table className="table">
              <thead className="sticky top-0 bg-panel"><tr><th>{tr("Date")}</th><th>{tr("Level")}</th><th>{tr("Category")}</th><th>{tr("Message")}</th></tr></thead>
              <tbody>
                {list.map((e, i) => (
                  <tr key={i}>
                    <td className="whitespace-nowrap text-muted text-[12.5px]">{new Date(e.time).toLocaleString(locale)}</td>
                    <td><Badge tone={e.level === "error" ? "danger" : e.level === "warning" ? "warn" : "neutral"}>{e.level}</Badge></td>
                    <td>{e.category}</td><td>{srv(e.message)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
