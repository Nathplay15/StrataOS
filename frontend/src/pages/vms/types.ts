export type Disk = { device: string; target: string; bus: string; source: string; size?: number };
export type NIC = { type: string; source: string; mac: string; model: string; ip?: string };
export type USBDevice = { vendor: string; product: string; serial?: string; name?: string; bus?: number; device?: number; speed?: string; usedBy?: string; internal?: boolean };
export type VM = {
  name: string; state: string; vcpus: number; memoryMb: number; autostart: boolean; autostartOrder?: number; autostartDelay?: number;
  firmware: string; os?: string; disks: Disk[]; nics: NIC[]; vncPort: number; dir?: string;
  template: boolean; cloud?: string; usb: USBDevice[]; pci: string[]; snapshots: number;
};
export type VMData = { installed: boolean; kvm: boolean; vms: VM[]; bridges?: string[]; stats?: Record<string, [number, number]> };
export type Snapshot = { name: string; description: string; created: string; state: string; memory: boolean; kind: "internal" | "external"; parent?: string; current: boolean };
export type CloudImage = { id: string; name: string; file: string; algo: string; cached: boolean };
export type OVF = { name: string; vcpus: number; memoryMb: number; firmware: string; os: string; disks: string[] };
export type ImportInfo = { format: string; ovf?: OVF; size: number };
export type AutostartEntry = { name: string; order: number; delay: number };
export type PCIDevice = { address: string; vendor: string; device: string; class: string; className?: string; name?: string; driver?: string; group: number; protected?: string; usedBy?: string };
export type IOMMU = { enabled: boolean; groups: number; cpu: string; params: string; cmdline: boolean; grub: boolean; pending: boolean; supports: boolean };
export type VMBackup = { task: string; taskName: string; vm: string; created: string; size: number; consistency: string; snapshot?: string };
