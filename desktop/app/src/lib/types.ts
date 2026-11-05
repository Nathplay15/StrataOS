// What the agent answers (desktop/internal/agent, engine.Status).

export interface SyncPair {
  id: string;
  local: string;
  remote: string;
  exclude?: string[];
  paused?: boolean;
  encrypted?: boolean;
}

export interface PairStatus {
  id: string;
  local: string;
  remote: string;
  state: "idle" | "syncing" | "paused" | "error" | "brake";
  error?: string;
  pending: number;
  done: number;
  current?: string;
  progress?: number;
  lastSync?: string;
  files: number;
  conflicts: number;
  skipped?: { path: string; reason: string }[];
  links?: number;
  brake?: { localDeletes: number; remoteDeletes: number; files: number; since: string };
}

export interface EngineStatus {
  paused: boolean;
  signedOut: boolean;
  pairs: PairStatus[] | null;
}

export interface AccountConfig {
  server?: string;
  fingerprint?: string;
  user?: string;
  nasName?: string;
  deviceName?: string;
  pairs?: SyncPair[];
  ignore?: string[] | null;
  upKBps?: number;
  downKBps?: number;
  paused?: boolean;
  noNotify?: boolean;
}

export interface Account {
  id: string;
  paired: boolean;
  config: AccountConfig;
  status: EngineStatus;
}

export interface AgentState {
  version: string;
  dir: string;
  home: string;
  sep: string;
  defaultIgnore: string[];
  language: string;
  systemLanguage: string;
  accounts: Account[];
  seq: number;
}

export interface AgentEvent {
  seq: number;
  kind: "notify" | "changed" | "transfer";
  account?: string;
  title?: string;
  body?: string;
}

export interface Transfer {
  id: string;
  account: string;
  kind: "download" | "upload";
  name: string;
  local: string;
  remote: string;
  size: number;
  done: number;
  files?: number;
  state: "running" | "done" | "error" | "cancelled";
  error?: string;
  started: string;
  ended?: string;
}

/** GET /api/mobile/info of a NAS. */
export interface NasInfo {
  hostname: string;
  version: string;
  features?: string[];
  user?: { username: string; fullName: string; role: "admin" | "user"; twoFactor?: boolean };
}
