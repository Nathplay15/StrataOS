import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, Eraser, HardDrive, Thermometer } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Disk, SmartInfo } from "../../lib/types";
import { bytes } from "../../lib/format";
import { useUI, Badge, Button, Card, Empty, ErrorBox, Modal, Notice, PageHeader, Spinner } from "../../components/ui";
import { tr, locale, srv } from "../../lib/i18n";

const usageLabel: Record<string, [string, "neutral" | "ok" | "warn" | "danger" | "accent"]> = {
  free: [tr("Libre"), "ok"],
  system: [tr("Système"), "neutral"],
  raid: ["RAID", "accent"],
  lvm: ["LVM", "accent"],
  zfs: ["ZFS", "accent"],
  btrfs: [tr("Btrfs"), "accent"],
  filesystem: [tr("Système de fichiers"), "warn"],
  partitioned: [tr("Partitionné"), "warn"],
};

const kindLabel: Record<string, string> = { hdd: "HDD", ssd: "SSD", nvme: "NVMe", usb: "USB", virtual: "Virtuel" };

function SmartModal({ disk, onClose }: { disk: Disk | null; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["smart", disk?.path],
    queryFn: () => api.get<SmartInfo>(`/api/storage/disks/smart${q({ device: disk!.path })}`),
    enabled: !!disk,
  });
  const test = useAction((type: string) => api.post("/api/storage/disks/smart-test", { device: disk!.path, type }), { success: tr("Test SMART lancé") });
  return (
    <Modal open={!!disk} onClose={onClose} title={tr("SMART — {0}", disk?.model || disk?.path)} wide footer={<>
      <Button onClick={() => test.mutate("short")} loading={test.isPending}>{tr("Test court")}</Button>
      <Button onClick={() => test.mutate("long")} loading={test.isPending}>{tr("Test long")}</Button>
    </>}>
      {isLoading ? <Spinner /> : error ? <ErrorBox error={error} /> : data && (
        <div className="space-y-5">
          <div className="grid gap-3 sm:grid-cols-4">
            {[
              [tr("État"), data.passed === null ? tr("Inconnu") : !data.passed ? tr("DÉFAILLANT") : data.warnings?.length ? tr("À surveiller") : tr("Sain")],
              [tr("Température"), data.temperature ? tr("{0} °C", data.temperature) : "—"],
              [tr("Heures de fonctionnement"), data.powerOnHours ? tr("{0} h", data.powerOnHours.toLocaleString(locale)) : "—"],
              [tr("Cycles d'alimentation"), data.powerCycles || "—"],
            ].map(([k, v]) => (
              <div key={String(k)} className="rounded-xl bg-panel-2 p-3">
                <div className="text-muted text-[12px]">{k}</div>
                <div className={`font-semibold ${v === tr("DÉFAILLANT") ? "text-danger" : v === tr("À surveiller") ? "text-warn" : ""}`}>{v}</div>
              </div>
            ))}
          </div>
          <div className="text-[13px] text-muted">{data.model} {" "}{tr("· S/N")}{" "}{data.serial} {" "}{tr("· firmware")}{" "}{data.firmware}</div>
          {data.messages?.map((m) => <div key={m} className="text-warn text-[12.5px]">{m}</div>)}
          {!!data.warnings?.length && <Notice tone="warn"><b>{tr("Signes d'usure :")}</b>{" "}{data.warnings.map((w) => srv(w)).join(", ")}. {tr("Vérifiez vos sauvegardes et prévoyez le remplacement du disque.")}</Notice>}
          {data.attributes.length > 0 && (
            <div className="overflow-x-auto rounded-xl border border-line">
              <table className="table">
                <thead><tr><th>ID</th><th>{tr("Attribut")}</th><th>{tr("Valeur")}</th><th>{tr("Pire")}</th><th>{tr("Seuil")}</th><th>{tr("Brut")}</th></tr></thead>
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
          {!data.available && <div className="text-muted text-[13px]">{tr("Ce périphérique ne fournit pas de données SMART (disque virtuel ?).")}</div>}
        </div>
      )}
    </Modal>
  );
}

export default function Disks() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks"), refetchInterval: 15000 });
  const [smart, setSmart] = useState<Disk | null>(null);
  const wipe = useAction((d: string) => api.post("/api/storage/disks/wipe", { device: d }), { success: tr("Disque effacé") });

  const doWipe = async (d: Disk) => {
    const ok = await confirm({
      title: tr("Effacer {0} ?", d.path),
      text: <>{tr("Toutes les données, partitions et signatures (RAID, LVM, ZFS…) du disque")}{" "}<b>{d.model}</b> ({bytes(d.size)}{tr(") seront")}{" "}<b>{tr("définitivement")}</b> {" "}{tr("détruites.")}</>,
      danger: true, confirm: tr("Effacer"), typeToConfirm: d.name,
    });
    if (ok) wipe.mutate(d.path);
  };

  return (
    <div>
      <PageHeader title={tr("Disques")} subtitle={tr("Disques physiques détectés et leur état de santé")} icon={<HardDrive size={20} />} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<HardDrive />} title={tr("Aucun disque détecté")} /> : (
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
                      <div className="truncate text-muted text-[12px]">{d.path} · {d.serial || tr("sans n° de série")}</div>
                    </div>
                    <div className="text-right text-[15px] font-semibold">{bytes(d.size)}</div>
                  </div>
                  <div className="mt-4 flex flex-wrap gap-1.5">
                    <Badge>{kindLabel[d.kind ?? ""] ?? d.kind}</Badge>
                    {d.transport && <Badge>{d.transport.toUpperCase()}</Badge>}
                    <Badge tone={ut}>{ul}{d.usedBy ? ` · ${d.usedBy}` : ""}</Badge>
                    {d.healthy === true && <Badge tone="ok" dot>{tr("SMART OK")}</Badge>}
                    {d.healthy === false && <Badge tone="danger" dot>{tr("SMART défaillant")}</Badge>}
                  </div>
                  <div className="mt-4 flex gap-5 text-[12.5px] text-muted">
                    <span className="flex items-center gap-1.5"><Thermometer size={14} /> {d.standby ? tr("En veille") : d.temperature ? tr("{0} °C", d.temperature) : "—"}</span>
                    <span className="flex items-center gap-1.5"><Activity size={14} /> {d.powerOnHours ? tr("{0} h", d.powerOnHours.toLocaleString(locale)) : "—"}</span>
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
                    <Button size="sm" variant="ghost" className="text-danger" icon={<Eraser size={14} />} onClick={() => doWipe(d)}>{tr("Effacer")}</Button>
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
