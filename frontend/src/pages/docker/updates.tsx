// One-click container updates (backend: api/h_docker_updates.go): the last
// check, "Update available" badges and the Update buttons of the Docker pages.
import { useQuery } from "@tanstack/react-query";
import { ArrowUpCircle, RefreshCw } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago } from "../../lib/format";
import { Badge, Button } from "../../components/ui";
import { tr } from "../../lib/i18n";

export type ContainerUpdate = { id: string; name: string; image: string; stack?: string; status: "current" | "available" | "unsupported" | "error"; error?: string };
export type DockerUpdates = { checked?: string; running: boolean; containers: ContainerUpdate[]; stacks: Record<string, boolean>; available: number };

export function useDockerUpdates() {
  return useQuery({ queryKey: ["docker-updates"], queryFn: () => api.get<DockerUpdates>("/api/docker/updates"), refetchInterval: 15000 });
}

/** Starts an update job for a container (ID or name) or a stack. */
export function useApplyUpdate() {
  return useAction((p: { container?: string; stack?: string }) => api.post("/api/docker/updates/apply", p));
}

export function UpdateBadge() {
  return <Badge tone="warn" dot>{tr("Update available")}</Badge>;
}

/** Last check, "Check now" and "Update all". */
export function UpdatesBar() {
  const { data } = useDockerUpdates();
  const check = useAction(() => api.post("/api/docker/updates/check"));
  const all = useAction(() => api.post("/api/docker/updates/apply-all"));
  return (
    <div className="mb-4 flex flex-wrap items-center gap-2 text-[12.5px] text-muted">
      <span className="mr-auto">
        {data?.checked ? tr("Updates checked {0}", ago(data.checked)) : tr("Updates not checked yet")}
        {data && data.available > 0 && <> · <b className="text-warn">{tr("{0} update(s) available", data.available)}</b></>}
      </span>
      <Button size="sm" icon={<RefreshCw size={13} />} loading={check.isPending || data?.running} onClick={() => check.mutate()}>{tr("Check for updates")}</Button>
      {data && data.available > 0 && <Button size="sm" variant="primary" icon={<ArrowUpCircle size={13} />} loading={all.isPending} onClick={() => all.mutate()}>{tr("Update all")}</Button>}
    </div>
  );
}
