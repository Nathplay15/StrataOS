import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, Eraser, FlaskConical, HardDrive, Thermometer } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Disk, SmartInfo } from "../../lib/types";
import { bytes } from "../../lib/format";
import { useUI, Badge, Button, Card, Empty, ErrorBox, Modal, Notice, PageHeader, Spinner } from "../../components/ui";
import { tr, locale, srv } from "../../lib/i18n";
import { BurnInBadge, BurnInModal, useBurnIns } from "./BurnIn";

const usageLabel: Record<string, [string, "neutral" | "ok" | "warn" | "danger" | "accent"]> = {
  free: [tr("Free"), "ok"],
  system: [tr("System"), "neutral"],
  raid: ["RAID", "accent"],
  lvm: ["LVM", "accent"],
  zfs: ["ZFS", "accent"],
  btrfs: [tr("Btrfs"), "accent"],
  filesystem: [tr("File system"), "warn"],
  partitioned: [tr("Partitioned"), "warn"],
  ceph: ["Ceph", "accent"],
};

const kindLabel: Record<string, string> = { hdd: "HDD", ssd: "SSD", nvme: "NVMe", usb: "USB", virtual: "Virtuel" };

function SmartModal({ disk, onClose }: { disk: Disk | null; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["smart", disk?.path],
    queryFn: () => api.get<SmartInfo>(`/api/storage/disks/smart${q({ device: disk!.path })}`),
    enabled: !!disk,
  });
  const test = useAction((type: string) => api.post("/api/storage/disks/smart-test", { device: disk!.path, type }), { success: tr("SMART test started") });
  return (
    <Modal open={!!disk} onClose={onClose} title={tr("SMART — {0}", disk?.model || disk?.path)} wide footer={<>
      <Button onClick={() => test.mutate("short")} loading={test.isPending}>{tr("Short test")}</Button>
      <Button onClick={() => test.mutate("long")} loading={test.isPending}>{tr("Extended test")}</Button>
    </>}>
      {isLoading ? <Spinner /> : error ? <ErrorBox error={error} /> : data && (
        <div className="space-y-5">
          <div className="grid gap-3 sm:grid-cols-4">
            {[
              [tr("Status"), data.passed === null ? tr("Unknown") : !data.passed ? tr("FAILED") : data.warnings?.length ? tr("Watch") : tr("Healthy")],
              [tr("Temperature"), data.temperature ? tr("{0} °C", data.temperature) : "—"],
              [tr("Power-on hours"), data.powerOnHours ? tr("{0} h", data.powerOnHours.toLocaleString(locale)) : "—"],
              [tr("Power cycles"), data.powerCycles || "—"],
            ].map(([k, v]) => (
              <div key={String(k)} className="rounded-xl bg-panel-2 p-3">
                <div className="text-muted text-[12px]">{k}</div>
                <div className={`font-semibold ${v === tr("FAILED") ? "text-danger" : v === tr("Watch") ? "text-warn" : ""}`}>{v}</div>
              </div>
            ))}
          </div>
          <div className="text-[13px] text-muted">{data.model} {" "}{tr("· S/N")}{" "}{data.serial} {" "}{tr("· firmware")}{" "}{data.firmware}</div>
          {data.messages?.map((m) => <div key={m} className="text-warn text-[12.5px]">{m}</div>)}
          {!!data.warnings?.length && <Notice tone="warn"><b>{tr("Signs of wear:")}</b>{" "}{data.warnings.map((w) => srv(w)).join(", ")}. {tr("Check your backups and plan to replace the disk.")}</Notice>}
          {data.attributes.length > 0 && (
            <div className="overflow-x-auto rounded-xl border border-line">
              <table className="table">
                <thead><tr><th>ID</th><th>{tr("Attribute")}</th><th>{tr("Value")}</th><th>{tr("Worst")}</th><th>{tr("Threshold")}</th><th>{tr("Raw")}</th></tr></thead>
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
          {!data.available && <div className="text-muted text-[13px]">{tr("This device does not provide SMART data (virtual disk?).")}</div>}
        </div>
      )}
    </Modal>
  );
}

export default function Disks() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks"), refetchInterval: 15000 });
  const [smart, setSmart] = useState<Disk | null>(null);
  const [burn, setBurn] = useState<Disk | null>(null);
  const burnIns = useBurnIns();
  const wipe = useAction((d: string) => api.post("/api/storage/disks/wipe", { device: d }), { success: tr("Disk wiped") });

  const doWipe = async (d: Disk) => {
    const ok = await confirm({
      title: tr("Wipe {0}?", d.path),
      text: <>{tr("All data, partitions and signatures (RAID, LVM, ZFS…) on the disk")}{" "}<b>{d.model}</b> ({bytes(d.size)}{tr(") will be")}{" "}<b>{tr("permanently")}</b> {" "}{tr("destroyed.")}</>,
      danger: true, confirm: tr("Wipe"), typeToConfirm: d.name,
    });
    if (ok) wipe.mutate(d.path);
  };

  return (
    <div>
      <PageHeader title={tr("Disks")} subtitle={tr("Detected physical disks and their health status")} icon={<HardDrive size={20} />} />
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.length ? <Empty icon={<HardDrive />} title={tr("No disks detected")} /> : (
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
                      <div className="truncate text-muted text-[12px]">{d.path} · {d.serial || tr("no serial number")}</div>
                    </div>
                    <div className="text-right text-[15px] font-semibold">{bytes(d.size)}</div>
                  </div>
                  <div className="mt-4 flex flex-wrap gap-1.5">
                    <Badge>{kindLabel[d.kind ?? ""] ?? d.kind}</Badge>
                    {d.transport && <Badge>{d.transport.toUpperCase()}</Badge>}
                    <Badge tone={ut}>{ul}{d.usedBy ? ` · ${d.usedBy}` : ""}</Badge>
                    {d.healthy === true && <Badge tone="ok" dot>{tr("SMART OK")}</Badge>}
                    {d.healthy === false && <Badge tone="danger" dot>{tr("SMART failing")}</Badge>}
                    {burnIns.data?.running.some((r) => r.device === d.path) ? <Badge tone="accent" dot>{tr("Burn-in test running")}</Badge> : (() => {
                      const last = burnIns.data?.reports.find((r) => d.serial ? r.serial === d.serial : r.device === d.path);
                      return last && <BurnInBadge report={last} onClick={() => setBurn(d)} />;
                    })()}
                  </div>
                  <div className="mt-4 flex gap-5 text-[12.5px] text-muted">
                    <span className="flex items-center gap-1.5"><Thermometer size={14} /> {d.standby ? tr("Standby") : d.temperature ? tr("{0} °C", d.temperature) : "—"}</span>
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
                  {!d.system && <Button size="sm" variant="ghost" icon={<FlaskConical size={14} />} onClick={() => setBurn(d)}>{tr("Burn-in test")}</Button>}
                  {!d.system && d.usage !== "free" && d.usage !== "ceph" && (
                    <Button size="sm" variant="ghost" className="text-danger" icon={<Eraser size={14} />} onClick={() => doWipe(d)}>{tr("Wipe")}</Button>
                  )}
                </div>
              </Card>
            );
          })}
        </div>
      )}
      <SmartModal disk={smart} onClose={() => setSmart(null)} />
      <BurnInModal disk={burn} data={burnIns.data} onClose={() => setBurn(null)} />
    </div>
  );
}
