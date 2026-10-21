import { useQuery } from "@tanstack/react-query";
import { api } from "../../lib/api";
import type { BackupTask } from "../../lib/types";
import { Checkbox, Field, Select } from "../../components/ui";
import { tr } from "../../lib/i18n";

type VMList = { installed: boolean; vms: { name: string; template: boolean; state: string }[] };

// Virtual machines of a backup task: definition and disks, copied consistently.
export function BackupVMPicker({ b, set }: { b: BackupTask; set: (k: Partial<BackupTask>) => void }) {
  const { data } = useQuery({ queryKey: ["vms"], queryFn: () => api.get<VMList>("/api/vms") });
  if (!data?.installed || data.vms.length === 0) return null;
  const chosen = b.vms ?? [];
  return (
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
  );
}
