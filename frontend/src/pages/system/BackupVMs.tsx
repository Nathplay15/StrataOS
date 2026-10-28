import { useQuery } from "@tanstack/react-query";
import { api } from "../../lib/api";
import type { BackupTask } from "../../lib/types";
import { Checkbox, Field, Select } from "../../components/ui";
import { tr } from "../../lib/i18n";

type VMList = { installed: boolean; vms: { name: string; template: boolean; state: string }[] };
type CTList = { installed: boolean; containers: { name: string }[] };

// Virtual machines of a backup task: definition and disks, copied consistently.
export function BackupVMPicker({ b, set }: { b: BackupTask; set: (k: Partial<BackupTask>) => void }) {
  const { data } = useQuery({ queryKey: ["vms"], queryFn: () => api.get<VMList>("/api/vms") });
  const cts = useQuery({ queryKey: ["lxc-list"], queryFn: () => api.get<CTList>("/api/lxc") });
  const ctPicker = <BackupContainerPicker b={b} set={set} list={cts.data?.installed ? cts.data.containers : []} />;
  if (!data?.installed || data.vms.length === 0) return ctPicker;
  const chosen = b.vms ?? [];
  return (
    <div className="space-y-4">
      <div>
      <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Virtual machines")}</div>
      <div className="grid gap-2 sm:grid-cols-2">
        {data.vms.map((vm) => (
          <Checkbox key={vm.name} checked={chosen.includes(vm.name)} onChange={(v) => set({ vms: v ? [...chosen, vm.name] : chosen.filter((x) => x !== vm.name) })}
            label={<span className="text-[13px]">{vm.name}{vm.template && <span className="text-muted text-[11px]"> · {tr("template")}</span>}</span>} />
        ))}
      </div>
      {chosen.length > 0 && (
        <Field label={tr("Running VMs")} className="mt-3" hint={tr("The definition, the disks and the UEFI variables are copied into a \"vms\" folder at the destination.")}>
          <Select value={b.vmMode ?? ""} onChange={(e) => set({ vmMode: e.target.value as BackupTask["vmMode"] })}>
            <option value="">{tr("Live snapshot (file systems frozen by the guest agent when available)")}</option>
            <option value="suspend">{tr("Pause the VM during the copy")}</option>
            <option value="shutdown">{tr("Shut the VM down during the copy")}</option>
          </Select>
        </Field>
      )}
      </div>
      {ctPicker}
    </div>
  );
}

// LXC containers of a backup task: a tar.zst archive each, taken from a snapshot when they run.
function BackupContainerPicker({ b, set, list }: { b: BackupTask; set: (k: Partial<BackupTask>) => void; list: { name: string }[] }) {
  if (list.length === 0) return null;
  const chosen = b.containers ?? [];
  return (
    <div>
      <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Containers (LXC)")}</div>
      <div className="grid gap-2 sm:grid-cols-2">
        {list.map((c) => (
          <Checkbox key={c.name} checked={chosen.includes(c.name)} onChange={(v) => set({ containers: v ? [...chosen, c.name] : chosen.filter((x) => x !== c.name) })} label={<span className="text-[13px]">{c.name}</span>} />
        ))}
      </div>
      {chosen.length > 0 && <div className="text-muted mt-2 text-[12px]">{tr("Each container is written as lxc/<name>.tar.zst at the destination (from a snapshot when it runs on Btrfs, ZFS or a disk image).")}</div>}
    </div>
  );
}
