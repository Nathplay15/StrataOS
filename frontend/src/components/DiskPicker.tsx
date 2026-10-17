import { useQuery } from "@tanstack/react-query";
import { HardDrive } from "lucide-react";
import { api } from "../lib/api";
import { bytes, cx } from "../lib/format";
import type { Disk, RaidArray, VG } from "../lib/types";
import { Badge, Spinner } from "./ui";
import { tr } from "../lib/i18n";

export interface Candidate {
  path: string;
  label: string;
  size: number;
  sub: string;
  kind: "disk" | "raid" | "lv";
}

/** Lists devices that can receive a new array, pool or filesystem. */
export function useCandidates(includeVirtual: boolean) {
  const disks = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks") });
  const raids = useQuery({ queryKey: ["raid"], queryFn: () => api.get<RaidArray[]>("/api/storage/raid"), enabled: includeVirtual });
  const lvm = useQuery({ queryKey: ["lvm"], queryFn: () => api.get<VG[]>("/api/storage/lvm"), enabled: includeVirtual });
  const list: Candidate[] = [];
  for (const d of disks.data ?? []) {
    if (d.usage === "free") list.push({ path: d.path, label: d.model || d.name, size: d.size, sub: `${d.path} · ${d.serial || d.kind}`, kind: "disk" });
  }
  if (includeVirtual) {
    for (const r of raids.data ?? []) {
      if (!r.fsType) list.push({ path: `/dev/md/${r.label || r.name}`, label: tr("RAID {0} « {1} »", r.level, r.label || r.name), size: r.size, sub: r.device, kind: "raid" });
    }
    for (const vg of lvm.data ?? []) {
      for (const lv of vg.lvs) {
        if (!lv.fsType && !lv.snapshot) list.push({ path: lv.path, label: tr("LV {0}/{1}", vg.name, lv.name), size: lv.size, sub: lv.path, kind: "lv" });
      }
    }
  }
  return { list, loading: disks.isLoading };
}

export function DiskPicker({ value, onChange, includeVirtual = false, exclude = [], single = false }: { value: string[]; onChange: (v: string[]) => void; includeVirtual?: boolean; exclude?: string[]; single?: boolean }) {
  const { list, loading } = useCandidates(includeVirtual);
  if (loading) return <Spinner />;
  const items = list.filter((c) => !exclude.includes(c.path) || value.includes(c.path));
  if (items.length === 0) {
    return <div className="rounded-xl border border-dashed border-line px-4 py-6 text-center text-muted text-[13px]">{tr("Aucun disque libre. Effacez un disque depuis la page Disques pour le réutiliser.")}</div>;
  }
  const toggle = (p: string) => {
    if (single) onChange(value.includes(p) ? [] : [p]);
    else onChange(value.includes(p) ? value.filter((x) => x !== p) : [...value, p]);
  };
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {items.map((c) => {
        const on = value.includes(c.path);
        return (
          <button type="button" key={c.path} onClick={() => toggle(c.path)} className={cx("flex items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition", on ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
            <HardDrive size={18} className={on ? "text-accent" : "text-muted"} />
            <div className="min-w-0 flex-1">
              <div className="truncate font-medium text-[13px]">{c.label}</div>
              <div className="truncate text-muted text-[11.5px]">{c.sub}</div>
            </div>
            <div className="text-right">
              <div className="text-[12.5px] font-medium">{bytes(c.size)}</div>
              {c.kind !== "disk" && <Badge tone="accent">{c.kind === "raid" ? "RAID" : "LVM"}</Badge>}
            </div>
          </button>
        );
      })}
    </div>
  );
}
