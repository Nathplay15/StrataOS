import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Network } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { tr, srv } from "../lib/i18n";
import { Button, Modal, Notice } from "./ui";
import { TrialModal, type Pending } from "../pages/system/Network";

type Plan = {
  bridges: string[];
  link: string;
  bridge?: string;
  ip: { mode: "dhcp" | "static" | "none"; addresses?: string[]; gateway?: string };
  pending: boolean;
  error?: string;
};

/**
 * "Create a bridge on <link>" for the VM and container wizards when the NAS
 * has no bridge: the NAS's main link becomes a port of a new bridge that
 * keeps its address, applied on trial like Control Panel → Network.
 */
export function QuickBridge({ what, className, onCreated }: { what: "vm" | "container"; className?: string; onCreated: (bridge: string) => void }) {
  const qc = useQueryClient();
  const plan = useQuery({ queryKey: ["bridge-plan"], queryFn: () => api.get<Plan>("/api/system/network/bridge-plan") });
  const [ask, setAsk] = useState(false);
  const [trial, setTrial] = useState<{ p: Pending; bridge: string } | null>(null);
  const create = useAction((link: string) => api.post<Pending & { bridge: string }>("/api/system/network/quick-bridge", { link }), {
    onDone: (r) => {
      setAsk(false);
      setTrial({ p: r, bridge: r.bridge });
    },
  });
  const p = plan.data;
  const intro = what === "vm"
    ? tr("For the VM to appear directly on your local network (its own address from your router), it needs a bridge.")
    : tr("For the container to appear directly on your local network (its own address from your router), it needs a bridge.");
  if (!p) return null;
  if (p.bridges.length > 0 && !trial) return null;
  const ip = p.ip.mode === "dhcp"
    ? tr("The NAS keeps getting its address by DHCP: the bridge uses the MAC address of {0}, so your router gives the same address.", p.link)
    : tr("The bridge takes the fixed address of {0}: {1}.", p.link, [...(p.ip.addresses ?? []), p.ip.gateway ? tr("gateway {0}", p.ip.gateway) : ""].filter(Boolean).join(", "));
  return (
    <div className={className}>
      <Notice>
        <div>{intro}</div>
        {p.error ? (
          <div className="mt-1 text-muted">{tr("It cannot be created automatically here: {0}. Use Control Panel → Network.", srv(p.error))}</div>
        ) : p.pending ? (
          <div className="mt-1 text-muted">{tr("A network change is already on trial: confirm or restore it in Control Panel → Network first.")}</div>
        ) : (
          <div className="mt-2"><Button size="sm" icon={<Network size={14} />} onClick={() => setAsk(true)}>{tr("Create a bridge on {0}", p.link)}</Button></div>
        )}
      </Notice>
      <Modal open={ask} onClose={() => setAsk(false)} title={tr("Create a bridge on {0}?", p.link)} footer={<><Button onClick={() => setAsk(false)}>{tr("Cancel")}</Button><Button variant="primary" loading={create.isPending} onClick={() => create.mutate(p.link)}>{tr("Create the bridge")}</Button></>}>
        <div className="space-y-3 text-[13px]">
          <p>{tr("A bridge works like a small network switch inside the NAS: {0} is plugged into it, and so are the virtual machines and containers that use it. They then appear on your local network like any other device.", p.link)}</p>
          <p>{tr("StrataOS creates {0} and moves the NAS's address from {1} to it.", p.bridge ?? "br0", p.link)} {ip}</p>
          <p>{tr("The network is interrupted for a few seconds. The change is applied on trial: if this page cannot reach the NAS again within 2 minutes, the previous configuration comes back by itself.")}</p>
        </div>
      </Modal>
      {trial && (
        <TrialModal
          pending={trial.p}
          newURLs={[]}
          onConfirmed={() => {
            qc.invalidateQueries({ queryKey: ["vms"] });
            qc.invalidateQueries({ queryKey: ["lxc-list"] });
            qc.invalidateQueries({ queryKey: ["bridge-plan"] });
            onCreated(trial.bridge);
          }}
          onClose={() => setTrial(null)}
        />
      )}
    </div>
  );
}
