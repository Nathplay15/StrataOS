export interface Me {
  username: string;
  fullName: string;
  role: "admin" | "user";
  twoFactor?: boolean;
}

export interface Job {
  id: string;
  type: string;
  title: string;
  user: string;
  status: "running" | "success" | "failed";
  progress: number;
  error?: string;
  started: string;
  finished?: string;
  log?: string;
}

export interface BlockDevice {
  name: string;
  path: string;
  type: string;
  size: number;
  model: string;
  serial: string;
  vendor: string;
  rotational: boolean;
  transport: string;
  fsType: string;
  label: string;
  uuid: string;
  mountpoints: string[];
  removable: boolean;
  children?: BlockDevice[];
  kind?: string;
  system?: boolean;
  usage?: string;
  usedBy?: string;
}

export interface Disk extends BlockDevice {
  temperature: number;
  healthy: boolean | null;
  powerOnHours: number;
  standby?: boolean;
}

export interface SmartInfo {
  device: string;
  available: boolean;
  enabled: boolean;
  passed: boolean | null;
  model: string;
  serial: string;
  firmware: string;
  capacity: number;
  temperature: number;
  powerOnHours: number;
  powerCycles: number;
  attributes: { id: number; name: string; value: number; worst: number; thresh: number; raw: string; failed?: string }[];
  nvme?: Record<string, unknown>;
  messages?: string[];
  warnings?: string[];
}

export interface RaidArray {
  name: string;
  device: string;
  label: string;
  level: string;
  size: number;
  state: string;
  health: "healthy" | "degraded" | "rebuilding" | "failed" | "inactive";
  uuid: string;
  raidDevices: number;
  activeDevices: number;
  failedDevices: number;
  spareDevices: number;
  chunkSize?: string;
  members: { device: string; raidDevice: number; state: string }[];
  syncAction?: string;
  syncProgress?: number;
  syncFinish?: string;
  syncSpeed?: string;
  fsType?: string;
  usedBy?: string;
}

export interface Usage {
  total: number;
  used: number;
  free: number;
}

export interface Volume {
  id: string;
  name: string;
  devices: string[];
  fsType: string;
  uuid: string;
  mountPoint: string;
  profile?: string;
  createdAt: string;
  mounted: boolean;
  usage: Usage;
  shares: string[];
  health: string;
}

export interface BtrfsInfo {
  mountPoint: string;
  label: string;
  uuid: string;
  devices: { id: number; path: string; size: number; used: number }[];
  dataProfile: string;
  metaProfile: string;
  size: number;
  used: number;
  freeEstimated: number;
  subvolumes: { id: number; path: string; snapshot: boolean; readOnly: boolean; lockedUntil?: number }[];
  scrub: string;
  balance: string;
}

export interface ZDataset {
  name: string;
  type: string;
  used: number;
  avail: number;
  refer: number;
  mountpoint: string;
  compression: string;
  quota: number;
  creation?: number;
  lockedUntil?: number;
}

export interface ZPool {
  name: string;
  size: number;
  alloc: number;
  free: number;
  fragmentation: string;
  capacity: string;
  dedup: string;
  health: string;
  status?: string;
  datasets?: ZDataset[];
  problems?: { device: string; state: string; was?: string }[];
}

export interface VG {
  name: string;
  size: number;
  free: number;
  pvCount: number;
  lvCount: number;
  pvs: { name: string; vg: string; size: number; free: number }[];
  lvs: { name: string; vg: string; path: string; size: number; attr: string; type: string; origin?: string; fsType?: string; snapshot: boolean }[];
}

export interface User {
  username: string;
  fullName: string;
  email: string;
  role: "admin" | "user";
  disabled: boolean;
  ssh: boolean;
  docker: boolean;
  groups: string[];
  createdAt: string;
  twoFactor?: boolean;
  source?: string;
}

export interface Group {
  name: string;
  gid: number;
  members: string[];
  system: boolean;
}

export type Perm = "rw" | "ro" | "none";

export interface Share {
  name: string;
  path: string;
  description: string;
  smb: boolean;
  nfs: boolean;
  guest: boolean;
  browseable: boolean;
  recycleBin: boolean;
  timeMachine: boolean;
  permissions: Record<string, Perm>;
  nfsClients: string;
  nfsOptions: string;
  quotaGb?: number;
  quotaBlock?: boolean;
  quotaFull?: boolean;
  used?: number;
  createdAt?: string;
  encrypted?: boolean;
  locked?: boolean;
}

export interface Sample {
  t: number;
  cpu: number;
  memUsed: number;
  memTotal: number;
  netRx: number;
  netTx: number;
  diskRead: number;
  diskWrite: number;
}

export interface SystemInfo {
  hostname: string;
  os: string;
  kernel: string;
  arch: string;
  cpuModel: string;
  cores: number;
  memTotal: number;
  swapTotal: number;
  swapFree: number;
  uptime: number;
  bootTime: string;
  load: [number, number, number];
  timezone: string;
  temps: { label: string; value: number }[];
  version: string;
  vendor: string;
  product: string;
}

export interface Overview {
  disks: number;
  raids: number;
  pools: number;
  users: number;
  shares: number;
  containers: { running: number; total: number };
  volumes: { name: string; kind: string; mountPoint: string; total: number; used: number; free: number; health: string }[];
  alerts: { level: "error" | "warning"; message: string }[];
}

export interface Container {
  Id: string;
  Names: string[];
  Image: string;
  ImageID: string;
  Command: string;
  Created: number;
  State: string;
  Status: string;
  Ports: { IP?: string; PrivatePort: number; PublicPort?: number; Type: string }[];
  Labels: Record<string, string>;
  Mounts: { Type: string; Source: string; Destination: string; RW: boolean }[];
}

export interface ContainerStats {
  cpuPercent: number;
  memUsage: number;
  memLimit: number;
  netRx: number;
  netTx: number;
  blockRead: number;
  blockWrite: number;
  pids: number;
}

export interface DockerImage {
  Id: string;
  RepoTags: string[] | null;
  RepoDigests: string[] | null;
  Created: number;
  Size: number;
  Containers: number;
}

export interface Stack {
  name: string;
  status: "running" | "partial" | "stopped";
  services: number;
  running: number;
  app?: string;
  modifiedAt: string;
}

export interface AppVar {
  key: string;
  label: string;
  default: string;
  type: "text" | "port" | "path" | "password";
}

export interface App {
  id: string;
  name: string;
  category: string;
  description: string;
  icon: string;
  webPort: string;
  vars: AppVar[];
  compose: string;
}

export interface FileEntry {
  name: string;
  path: string;
  dir: boolean;
  size: number;
  mode: string;
  modTime: string;
  link: boolean;
}

export interface Service {
  unit: string;
  name: string;
  description: string;
  active: string;
  sub: string;
  enabled: string;
  loaded: boolean;
}

export interface LogEntry {
  time: number;
  unit: string;
  priority: number;
  message: string;
  ident: string;
}

export interface StrataEvent {
  time: string;
  level: "info" | "warning" | "error";
  category: string;
  message: string;
  user?: string;
}

export interface Settings {
  workgroup: string;
  serverString: string;
  sessionHours: number;
  webhookUrl: string;
  smtpHost: string;
  smtpPort: number;
  smtpSecurity: string;
  smtpUser: string;
  smtpPassword: string;
  smtpFrom: string;
  notifyEmail: string;
  telegramToken?: string;
  telegramChatId?: string;
  monthlyReport?: boolean;
  notifyLang?: string;
  ntfyUrl?: string;
  ntfyToken?: string;
  smbMinProtocol: string;
  trashDays: number;
}

export interface ScheduleRun {
  started: string;
  duration: number;
  status: "success" | "failed";
  message: string;
  manual?: boolean;
}

export interface Schedule {
  id: string;
  name: string;
  type: "snapshot" | "scrub" | "smart" | "appupdate" | "antivirus";
  avAction?: "report" | "quarantine";
  cron: string;
  enabled: boolean;
  targetKind: "zfs" | "btrfs" | "mdadm" | "disks" | "stacks" | "path";
  target: string;
  subvolume?: string;
  recursive?: boolean;
  keepCount?: number;
  keepDays?: number;
  lockDays?: number;
  smartTest?: "short" | "long";
  createdAt?: string;
  history: ScheduleRun[];
  nextRun?: string | null;
  running?: boolean;
}

export interface ScheduleTarget {
  kind: Schedule["targetKind"];
  value: string;
  label: string;
  subvolumes?: string[];
}

export interface BackupTask {
  id: string;
  name: string;
  kind: "rsync" | "restic" | "zfs";
  sources: string[];
  destType: "local" | "ssh" | "sftp" | "s3" | "rest";
  destPath: string;
  host?: string;
  user?: string;
  port?: number;
  s3Endpoint?: string;
  accessKey?: string;
  secretKey?: string;
  password?: string;
  delete?: boolean;
  excludes?: string[];
  recursive?: boolean;
  keepLast?: number;
  keepDaily?: number;
  keepWeekly?: number;
  keepMonthly?: number;
  cron: string;
  enabled: boolean;
  history: ScheduleRun[];
  nextRun?: string | null;
  running?: boolean;
}

export interface ResticSnapshot {
  id: string;
  short_id: string;
  time: string;
  paths: string[];
  hostname: string;
  summary?: { total_bytes_processed: number; total_files_processed: number };
}

export interface QuotaTarget {
  kind: "ext4" | "xfs" | "zfs" | "btrfs";
  target: string;
  name: string;
  mountPoint: string;
  enabled: boolean;
  supported: boolean;
}

export interface UserQuota extends QuotaTarget {
  used: number;
  limit: number;
}

export type ShareLink = {
  id: string; owner: string; path?: string; album?: string; dir: boolean; download: boolean; upload: boolean;
  expires?: string; maxDownloads?: number; downloads: number; uploads: number; created: string; lastUsed?: string;
  password: boolean; name: string;
};
