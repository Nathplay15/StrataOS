import { useQuery } from "@tanstack/react-query";
import { Cpu, Play, RotateCw, Square } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Service } from "../../lib/types";
import { Badge, Card, ErrorBox, IconButton, PageHeader, Spinner, Switch } from "../../components/ui";

export default function Services() {
  const { data, isLoading, error } = useQuery({ queryKey: ["services"], queryFn: () => api.get<Service[]>("/api/system/services"), refetchInterval: 5000 });
  const act = useAction((p: { unit: string; action: string }) => api.post(`/api/system/services/${p.unit}/${p.action}`), { success: "Service mis à jour" });
  return (
    <div>
      <PageHeader title="Services" subtitle="Services système utilisés par le NAS" icon={<Cpu size={20} />} />
      <ErrorBox error={error} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>Service</th><th>État</th><th>Démarrage auto</th><th /></tr></thead>
              <tbody>
                {data?.map((s) => (
                  <tr key={s.unit}>
                    <td><div className="font-medium">{s.name}</div><div className="mono text-muted text-[11.5px]">{s.unit}</div></td>
                    <td><Badge tone={s.active === "active" ? "ok" : s.active === "failed" ? "danger" : "neutral"} dot>{s.active} ({s.sub})</Badge></td>
                    <td>
                      {s.unit !== "strata.service" && ["enabled", "disabled"].includes(s.enabled) ? (
                        <Switch checked={s.enabled === "enabled"} onChange={(v) => act.mutate({ unit: s.unit, action: v ? "enable" : "disable" })} />
                      ) : <span className="text-muted text-[12.5px]">{s.enabled}</span>}
                    </td>
                    <td className="text-right whitespace-nowrap">
                      {s.unit !== "strata.service" && (s.active === "active"
                        ? <IconButton title="Arrêter" onClick={() => act.mutate({ unit: s.unit, action: "stop" })}><Square size={15} /></IconButton>
                        : <IconButton title="Démarrer" onClick={() => act.mutate({ unit: s.unit, action: "start" })}><Play size={15} /></IconButton>)}
                      <IconButton title="Redémarrer" onClick={() => act.mutate({ unit: s.unit, action: "restart" })}><RotateCw size={15} /></IconButton>
                    </td>
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
