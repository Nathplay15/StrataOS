import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, Eraser, HardDrive, Thermometer } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Disk, SmartInfo } from "../../lib/types";
import { bytes } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Modal, PageHeader, Spinner, useUI } from "../../components/ui";

const usageLabel: Record<string, [string, "neutral" | "ok" | "warn" | "danger" | "accent"]> = {
  free: ["Libre", "ok"],
  system: ["Système", "neutral"],
  raid: ["RAID", "accent"],
  lvm: ["LVM", "accent"],
  zfs: ["ZFS", "accent"],
  btrfs: ["Btrfs", "accent"],
  filesystem: ["Système de fichiers", "warn"],
  partitioned: ["Partitionné", "warn"],
};

const kindLabel: Record<string, string> = { hdd: "HDD", ssd: "SSD", nvme: "NVMe", usb: "USB", virtual: "Virtuel" };

function SmartModal({ disk, onClose }: { disk: Disk | null; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["smart", disk?.path],
    queryFn: () => api.get<SmartInfo>(`/api/storage/disks/smart${q({ device: disk!.path })}`),
    enabled: !!disk,
  });
  const test = useAction((type: string) => api.post("/api/storage/disks/smart-test", { device: disk!.path, type }), { success: "Test SMART lancé" });
  return (
    <Modal open={!!disk} onClose={onClose} title={`SMART — ${disk?.model || disk?.path}`} wide footer={<>
      <Button onClick={() => test.mutate("short")} loading={test.isPending}>Test court</Button>
      <Button onClick={() => test.mutate("long")} loading={test.isPending}>Test long</Button>
    </>}>
      {isLoading ? <Spinner /> : error ? <ErrorBox error={error} /> : data && (
        <div className="space-y-5">
          <div className="grid gap-3 sm:grid-cols-4">
            {[
              ["État", data.passed === null ? "Inconnu" : data.passed ? "Sain" : "DÉFAILLANT"],
              ["Température", data.temperature ? `${data.temperature} °C` : "—"],
              ["Heures de fonctionnement", data.powerOnHours ? `${data.powerOnHours.toLocaleString("fr-FR")} h` : "—"],
              ["Cycles d'alimentation", data.powerCycles || "—"],
            ].map(([k, v]) => (
              <div key={String(k)} className="rounded-xl bg-panel-2 p-3">
                <div className="text-muted text-[12px]">{k}</div>
                <div className={`font-semibold ${v === "DÉFAILLANT" ? "text-danger" : ""}`}>{v}</div>
              </div>
            ))}
          </div>
          <div className="text-[13px] text-muted">{data.model} · S/N {data.serial} · firmware {data.firmware}</div>
          {data.messages?.map((m) => <div key={m} className="text-warn text-[12.5px]">{m}</div>)}
          {data.attributes.length > 0 && (
            <div className="overflow-x-auto rounded-xl border border-line">
              <table className="table">
                <thead><tr><th>ID</th><th>Attribut</th><th>Valeur</th><th>Pire</th><th>Seuil</th><th>Brut</th></tr></thead>
                <tbody>
                  {data.attributes.map((a) => (
                    <tr key={a.id} className={a.failed && a.failed !== "-" ? "text-danger" : ""}>
                      <td className="mono">{a.id}</td><td>{a.name.replace(/_/g, " ")}</td><td>{a.value}</td><td>{a.worst}</td><td>{a.thresh}</td><td className="mono">{a.raw}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {data.nvme && (
            <div className="grid gap-2 sm:grid-cols-2">
              {Object.entries(data.nvme).map(([k, v]) => (
                <div key={k} className="flex justify-between rounded-lg bg-panel-2 px-3 py-1.5 text-[12.5px]"><span className="text-muted">{k.replace(/_/g, " ")}</span><span className="mono">{JSON.stringify(v)}</span></div>
              ))}
            </div>
          )}
          {!data.available && <div className="text-muted text-[13px]">Ce périphérique ne fournit pas de données SMART (disque virtuel ?).</div>}
        </div>
      )}
    </Modal>
  );
}

export default function Disks() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks"), refetchInterval: 15000 });
  const [smart, setSmart] = useState<Disk | null>(null);
  const wipe = useAction((d: string) => api.post("/api/storage/disks/wipe", { device: d }), { success: "Disque effacé" });

  const doWipe = async (d: Disk) => {
    const ok = await confirm({
      title: `Effacer ${d.path} ?`,
      text: <>Toutes les données, partitions et signatures (RAID, LVM, ZFS…) du disque <b>{d.model}</b> ({bytes(d.size)}) seront <b>définitivement</b> détruites.</>,
      danger: true, confirm: "Effacer", typeToConfirm: d.name,
    });
    if (ok) wipe.mutate(d.path);
  };

  return (
    <div>
      <PageHeader title="Disques" subtitle="Disques physiques détectés et leur état de santé" icon={<HardDrive size={20} />} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<HardDrive />} title="Aucun disque détecté" /> : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {data.map((d) => {
            const [ul, ut] = usageLabel[d.usage ?? ""] ?? [d.usage ?? "?", "neutral"];
            return (
              <Card key={d.path} pad={false}>
                <div className="p-5">
                  <div className="flex items-start gap-3">
                    <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-accent-soft text-accent"><HardDrive size={20} /></div>
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-semibold">{d.model || d.name}</div>
                      <div className="truncate text-muted text-[12px]">{d.path} · {d.serial || "sans n° de série"}</div>
                    </div>
                    <div className="text-right text-[15px] font-semibold">{bytes(d.size)}</div>
                  </div>
                  <div className="mt-4 flex flex-wrap gap-1.5">
                    <Badge>{kindLabel[d.kind ?? ""] ?? d.kind}</Badge>
                    {d.transport && <Badge>{d.transport.toUpperCase()}</Badge>}
                    <Badge tone={ut}>{ul}{d.usedBy ? ` · ${d.usedBy}` : ""}</Badge>
                    {d.healthy === true && <Badge tone="ok" dot>SMART OK</Badge>}
                    {d.healthy === false && <Badge tone="danger" dot>SMART défaillant</Badge>}
                  </div>
                  <div className="mt-4 flex gap-5 text-[12.5px] text-muted">
                    <span className="flex items-center gap-1.5"><Thermometer size={14} /> {d.temperature ? `${d.temperature} °C` : "—"}</span>
                    <span className="flex items-center gap-1.5"><Activity size={14} /> {d.powerOnHours ? `${d.powerOnHours.toLocaleString("fr-FR")} h` : "—"}</span>
                  </div>
                  {d.children && d.children.length > 0 && (
                    <div className="mt-3 space-y-1 border-t border-line pt-3">
                      {d.children.map((c) => (
                        <div key={c.path} className="flex justify-between gap-2 text-[12px]">
                          <span className="mono truncate">{c.name}</span>
                          <span className="truncate text-muted">{[c.fsType, c.label, c.mountpoints.join(", ")].filter(Boolean).join(" · ")} · {bytes(c.size)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
                <div className="flex justify-end gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
                  <Button size="sm" variant="ghost" icon={<Activity size={14} />} onClick={() => setSmart(d)}>SMART</Button>
                  {!d.system && d.usage !== "free" && (
                    <Button size="sm" variant="ghost" className="text-danger" icon={<Eraser size={14} />} onClick={() => doWipe(d)}>Effacer</Button>
                  )}
                </div>
              </Card>
            );
          })}
        </div>
      )}
      <SmartModal disk={smart} onClose={() => setSmart(null)} />
    </div>
  );
}
