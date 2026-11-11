// Distributed firewall and virtual networks (GET /api/system/network/firewall, /vnets).
export type Rule = {
  disabled?: boolean; dir: "in" | "out"; action: "accept" | "drop" | "reject";
  proto?: "" | "tcp" | "udp" | "tcpudp" | "icmp" | "icmpv6"; ports?: string; sports?: string;
  source?: string; dest?: string; group?: string; log?: boolean; comment?: string;
};
export type Group = { name: string; comment?: string; rules: Rule[] };
export type IPSet = { name: string; comment?: string; entries: string[] };
export type GuestKind = "vm" | "lxc" | "docker";
export type Guest = {
  kind: GuestKind; name: string; enabled: boolean; policyIn?: string; policyOut?: string;
  isolate?: boolean; macFilter?: boolean; logDrops?: boolean; rules: Rule[];
};
export type Host = { enabled: boolean; policyIn?: string; logDrops?: boolean; rules: Rule[] };
export type FWConfig = { host: Host; guests?: Guest[]; groups?: Group[]; ipsets?: IPSet[] };
export type Binding = { macs: string[] | null; ifaces: string[] | null };
export type GuestInfo = { kind: GuestKind; name: string; state: string; bridges: string[]; firewall: boolean; rules: number; isolate: boolean; binding: Binding; missing?: boolean };
export type FWData = {
  config: FWConfig; trial: { deadline: string } | null; error: string; nft: boolean; guests: GuestInfo[];
  webPorts: number[]; nas: string[] | null; peers: string[] | null; ruleset: { bridge: string; host: string }; defaultHost: Host;
};
export type LogEntry = { time: string; scope: string; dir?: string; in?: string; out?: string; src?: string; dst?: string; proto?: string; sport?: number; dport?: number };

export type VNetKind = "nat" | "isolated" | "vlan" | "vxlan";
export type VNet = {
  id: string; label?: string; kind: VNetKind; bridge: string; use?: string;
  parent?: string; vlan?: number; vni?: number; peers?: string[]; local?: string; mtu?: number;
  address?: string; subnet?: string; gateway?: string; dhcp?: boolean; dns?: string[];
  isolate?: boolean; nasPorts?: string; logDrops?: boolean; docker?: boolean;
};
export type VNetView = VNet & {
  status: { exists: boolean; up: boolean; mtu?: number; addresses: string[]; docker: boolean };
  guests: { kind: GuestKind; name: string; state: string }[];
};
export type VNetData = {
  networks: VNetView[]; parents: string[] | null; peers: { name: string; addr: string; from: string }[]; dns: string[];
  pending: boolean; manager: string; docker: boolean; nextBridge: string; addresses: string[] | null;
};
