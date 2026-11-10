import { useQuery } from "@tanstack/react-query";
import { api } from "../../../lib/api";
import { tr } from "../../../lib/i18n";
import type { VNetData } from "./types";

/** Label of a bridge in the VM and container wizards: virtual networks by their name. */
export function useBridgeLabel(): (bridge: string) => string {
  const q = useQuery({ queryKey: ["vnets"], queryFn: () => api.get<VNetData>("/api/system/network/vnets"), staleTime: 30000 });
  return (b: string) => {
    const v = q.data?.networks.find((n) => n.bridge === b);
    if (v) return `${tr("Virtual network")} ${v.id}${v.label ? ` (${v.label})` : ""}${v.status.mtu && v.status.mtu < 1500 ? ` · MTU ${v.status.mtu}` : ""}`;
    return `${tr("Bridge")} ${b} ${tr("(on the local network)")}`;
  };
}
