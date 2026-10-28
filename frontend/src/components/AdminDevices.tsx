import { useQuery } from "@tanstack/react-query";
import { Laptop, Smartphone, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { dateTime } from "../lib/format";
import { tr } from "../lib/i18n";
import { Badge, Button, Card, Empty, ErrorBox, Spinner, useUI } from "./ui";

interface AdminDevice {
  user: string;
  id: string;
  name: string;
  model: string;
  app?: string;
  kind: "android" | "desktop";
  os?: string;
  createdAt: string;
  lastSeen?: string;
  lastIp?: string;
}

/** Every paired phone and computer of every account, for administrators (revoke a lost laptop). */
export function AdminDevices() {
  const { confirm } = useUI();
  const list = useQuery({ queryKey: ["admin-devices"], queryFn: () => api.get<AdminDevice[]>("/api/devices"), refetchInterval: 15000 });
  const revoke = useAction((d: AdminDevice) => api.del(`/api/devices/${encodeURIComponent(d.user)}/${encodeURIComponent(d.id)}`), { success: tr("Device revoked") });
  return (
    <Card pad={false}>
      <ErrorBox error={list.error} />
      {list.isLoading ? <Spinner /> : !list.data?.length ? <Empty icon={<Laptop />} title={tr("No paired devices")} text={tr("Phones with the StrataOS app and computers with the desktop sync app appear here once paired.")} /> : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>{tr("Device")}</th><th>{tr("User")}</th><th>{tr("System")}</th><th>{tr("Paired")}</th><th>{tr("Last seen")}</th><th /></tr></thead>
            <tbody>
              {list.data.map((d) => (
                <tr key={d.user + d.id}>
                  <td><div className="flex items-center gap-2">{d.kind === "desktop" ? <Laptop size={16} className="text-accent" /> : <Smartphone size={16} className="text-accent" />}<span className="font-medium">{d.name}</span>{d.kind === "desktop" ? <Badge tone="accent">{tr("Sync app")}</Badge> : <Badge>Android</Badge>}</div></td>
                  <td>{d.user}</td>
                  <td className="text-[12.5px]">{[d.os, d.model !== d.name ? d.model : "", d.app && tr("app {0}", d.app)].filter(Boolean).join(" · ") || "—"}</td>
                  <td className="text-muted text-[12.5px]">{dateTime(d.createdAt)}</td>
                  <td className="text-muted text-[12.5px]">{d.lastSeen ? dateTime(d.lastSeen) : "—"}{d.lastIp && <> ({d.lastIp})</>}</td>
                  <td className="text-right"><Button size="sm" variant="ghost" className="text-danger" title={tr("Revoke")} icon={<Trash2 size={14} />} onClick={async () => {
                    if (await confirm({ title: tr("Revoke {0}?", d.name), text: tr("The device of {0} is signed out at once. Its files stay on the device; it must be paired again to reach the NAS.", d.user), danger: true, confirm: tr("Revoke") })) revoke.mutate(d);
                  }} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
