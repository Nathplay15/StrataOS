// LXC containers (GET /api/lxc…), shown with the VMs.
export type LXCNetwork = { mode: "nat" | "bridge" | "none"; bridge?: string; mac: string; address?: string; gateway?: string; dns?: string[] };
export type LXCMount = { share: string; source?: string; target: string; readOnly?: boolean };
export type LXCFeatures = { nesting?: boolean; fuse?: boolean; tun?: boolean; keyctl?: boolean };
export type LXCStorage = { backend: "dir" | "image" | "btrfs" | "zfs"; location: string; base: string; dataset?: string; sizeGb?: number };
export type Container = {
  name: string; description?: string; created: string;
  image: { distro: string; release: string; arch: string; variant?: string; build?: string };
  privileged?: boolean; idmap: { base: number; count: number; uids?: number[]; gids?: number[] };
  storage: LXCStorage; cpus?: number; memoryMb?: number; swapMb?: number;
  network: LXCNetwork; mounts?: LXCMount[]; features: LXCFeatures;
  autostart?: boolean; autostartOrder?: number; autostartDelay?: number;
  state: string; pid?: number; ips: string[]; snapshots: number; memoryBytes?: number; cpuUsec?: number; missing?: boolean;
  mapUsers: string[]; mapGroups: string[];
};
export type LXCData = { installed: boolean; containers: Container[]; bridges: string[]; natBridge: string; arch: string };
export type LXCImage = { distro: string; release: string; arch: string; variant: string; build: string; label: string; recommended?: boolean };
export type LXCLocation = { path: string; label: string; fsType: string; free: number; backends: LXCStorage["backend"][] };
export type LXCSnapshot = { name: string; description?: string; created: string; running: boolean; backend: string };
export type LXCStats = {
  time: string; state: string; cpuUsec: number; cpuLimit: number; cores: number; memory: number; memoryMax: number; swap: number; swapMax: number;
  pids: number; netRx: number; netTx: number; diskRead: number; diskWrite: number; diskUsed: number; diskLimit: number; ips: string[];
};
export type LXCArchive = { path: string; container: string; created: string; size: number };
export type LXCImportable = { kind: "archive" | "folder"; path: string; container: string; created: string; size?: number };

/** The settings PUT /api/lxc/{name} takes, from a container. */
export function settingsOf(c: Container) {
  return {
    description: c.description ?? "", cpus: c.cpus ?? 0, memoryMb: c.memoryMb ?? 0, swapMb: c.swapMb ?? 0, sizeGb: c.storage.sizeGb ?? 0,
    network: c.network, mounts: (c.mounts ?? []).map((m): LXCMount => ({ share: m.share, target: m.target, readOnly: !!m.readOnly })),
    features: c.features, autostart: !!c.autostart, autostartOrder: c.autostartOrder ?? 0, autostartDelay: c.autostartDelay ?? 0,
    mapUsers: c.mapUsers, mapGroups: c.mapGroups,
  };
}
export type LXCSettings = ReturnType<typeof settingsOf>;
