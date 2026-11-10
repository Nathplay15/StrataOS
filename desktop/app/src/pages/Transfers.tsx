// Downloads and uploads of the file browser, done by the agent.
import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, FolderOpen, X } from "lucide-react";
import { openPath, revealItemInDir } from "@tauri-apps/plugin-opener";
import { Badge, Button, Card, Empty, Progress, Spinner } from "@nas/components/ui";
import { bytes, dateTime } from "@nas/lib/format";
import { agent } from "../lib/agent";
import { useApp } from "../lib/state";
import { t } from "../lib/i18n";
import { PageHeader } from "../components/kit";
import type { Transfer } from "../lib/types";

export function TransfersPage() {
  const { state } = useApp();
  const { data, isLoading, refetch } = useQuery({
    queryKey: ["transfers"],
    queryFn: () => agent.get<Transfer[]>("/v1/transfers"),
    refetchInterval: (q) => ((q.state.data ?? []).some((x) => x.state === "running") ? 700 : 5000),
  });
  const nasName = (id: string) => state?.accounts.find((a) => a.id === id)?.config.nasName ?? id;
  const list = data ?? [];
  return (
    <>
      <PageHeader title={t("Transfers")} subtitle={t("Downloads and uploads go on when the window is closed.")}
        actions={list.some((x) => x.state !== "running") && <Button onClick={async () => { await agent.post("/v1/transfers/clear", {}); void refetch(); }}>{t("Clear finished")}</Button>} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : !list.length ? <Empty icon={<ArrowDown />} title={t("No transfers")} text={t("Download or upload files in Files.")} /> : list.map((x) => (
          <div key={x.id} className="flex items-center gap-3 border-b border-line px-4 py-3 last:border-0">
            {x.kind === "download" ? <ArrowDown size={17} className="text-accent" /> : <ArrowUp size={17} className="text-ok" />}
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate font-medium">{x.name}</span>
                {x.state === "done" ? <Badge tone="ok">{t("Completed")}</Badge> : x.state === "error" ? <Badge tone="danger">{t("Failed")}</Badge> : x.state === "cancelled" ? <Badge>{t("Cancelled")}</Badge> : null}
              </div>
              <div className="mono truncate text-[11.5px] text-muted">{x.kind === "download" ? `${nasName(x.account)}:${x.remote} → ${x.local}` : `${x.local} → ${nasName(x.account)}:${x.remote}`}</div>
              {x.state === "running" && <Progress className="mt-1.5" value={x.size ? (x.done / x.size) * 100 : 0} indeterminate={!x.size} />}
              {x.error && <div className="selectable mt-1 text-[12px] text-danger">{x.error}</div>}
              <div className="mt-0.5 text-[11.5px] text-muted">{bytes(x.done)}{x.size ? ` / ${bytes(x.size)}` : ""}{x.files ? ` · ${t("{0} file(s)", x.files)}` : ""} · {dateTime(x.started)}</div>
            </div>
            {x.state === "running" && <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => void agent.del(`/v1/transfers/${x.id}`)}>{t("Cancel")}</Button>}
            {x.state === "done" && x.kind === "download" && <>
              <Button size="sm" variant="ghost" onClick={() => void openPath(x.local)}>{t("Open")}</Button>
              <Button size="sm" variant="ghost" icon={<FolderOpen size={14} />} title={t("Show in folder")} onClick={() => void revealItemInDir(x.local)} />
            </>}
          </div>
        ))}
      </Card>
    </>
  );
}
