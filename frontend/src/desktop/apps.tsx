import { lazy, type ComponentType, type ReactNode } from "react";
import { Activity, AppWindow, BookUser, ServerCog, ShieldCheck, CloudCog, FolderTree, BatteryCharging, DatabaseBackup, Boxes, CalendarClock, Container, Cpu, Database, FileText, FolderOpen, HardDrive, Layers, LayoutDashboard, Monitor as MonitorIcon, Network, Package, RefreshCcw, Server, Settings, Share2, SlidersHorizontal, SquareTerminal, Users, Waypoints, Images as PhotosIcon, HardDriveUpload, Link2, Leaf, ShieldBan, LifeBuoy, PieChart, Copy, Fan } from "lucide-react";
import type { Me } from "../lib/types";
import { tr } from "../lib/i18n";

const Dashboard = lazy(() => import("../pages/Dashboard"));
const Disks = lazy(() => import("../pages/storage/Disks"));
const Raid = lazy(() => import("../pages/storage/Raid"));
const Volumes = lazy(() => import("../pages/storage/Volumes"));
const Zfs = lazy(() => import("../pages/storage/Zfs"));
const Lvm = lazy(() => import("../pages/storage/Lvm"));
const Iscsi = lazy(() => import("../pages/storage/Iscsi"));
const SpaceAnalyzer = lazy(() => import("../pages/storage/SpaceAnalyzer"));
const Duplicates = lazy(() => import("../pages/storage/Duplicates"));
const Shares = lazy(() => import("../pages/Shares"));
const UsersPage = lazy(() => import("../pages/Users"));
const Files = lazy(() => import("../pages/Files"));
const PhotosPage = lazy(() => import("../pages/Photos"));
const LinksPage = lazy(() => import("../pages/Links"));
const AppStore = lazy(() => import("../pages/docker/AppStore"));
const Containers = lazy(() => import("../pages/docker/Containers"));
const Stacks = lazy(() => import("../pages/docker/Stacks"));
const Images = lazy(() => import("../pages/docker/Images"));
const Monitor = lazy(() => import("../pages/system/Monitor"));
const Services = lazy(() => import("../pages/system/Services"));
const NetworkPage = lazy(() => import("../pages/system/Network"));
const Updates = lazy(() => import("../pages/system/Updates"));
const Logs = lazy(() => import("../pages/system/Logs"));
const TerminalPage = lazy(() => import("../pages/system/TerminalPage"));
const SettingsPage = lazy(() => import("../pages/system/Settings"));
const Schedules = lazy(() => import("../pages/system/Schedules"));
const Backups = lazy(() => import("../pages/system/Backups"));
const UPSPage = lazy(() => import("../pages/system/UPS"));
const EnergyPage = lazy(() => import("../pages/system/Energy"));
const HardwarePage = lazy(() => import("../pages/system/Hardware"));
const GuardPage = lazy(() => import("../pages/system/Guard"));
const FleetPage = lazy(() => import("../pages/Fleet"));
const HelpPage = lazy(() => import("../pages/Help"));
const VirtualMachines = lazy(() => import("../pages/vms/VirtualMachines"));
const CloudSync = lazy(() => import("../pages/system/CloudSync"));
const FileServicesPage = lazy(() => import("../pages/system/FileServices"));
const DirectoryPage = lazy(() => import("../pages/system/Directory"));
const AntivirusPage = lazy(() => import("../pages/system/Antivirus"));
const HAPage = lazy(() => import("../pages/system/HighAvailability"));
const RemotePage = lazy(() => import("../pages/system/RemoteAccess"));

type PageComponent = ComponentType<{ me: Me }>;

export interface Section {
  id: string;
  label: string;
  icon: ReactNode;
  component: PageComponent;
  /** Classic-mode route, used to translate links into windows. */
  path: string;
}

export interface AppDef {
  id: string;
  title: string;
  icon: ComponentType<{ size?: number; className?: string; strokeWidth?: number }>;
  /** Two colors of the icon gradient. */
  colors: [string, string];
  admin: boolean;
  size: [number, number];
  /** A single page, or a suite of sections shown with a side menu (like DSM). */
  component?: PageComponent;
  path?: string;
  sections?: Section[];
}

const page = (C: ComponentType<object>): PageComponent => C as PageComponent;

export const APPS: AppDef[] = [
  { id: "overview", title: tr("Overview"), icon: LayoutDashboard, colors: ["#6366f1", "#8b5cf6"], admin: false, size: [1080, 760], component: Dashboard as PageComponent, path: "/" },
  { id: "files", title: tr("Files"), icon: FolderOpen, colors: ["#f59e0b", "#f97316"], admin: false, size: [1000, 660], component: page(Files), path: "/files" },
  { id: "photos", title: tr("Photos"), icon: PhotosIcon, colors: ["#f472b6", "#db2777"], admin: false, size: [1120, 760], component: PhotosPage as PageComponent, path: "/photos" },
  { id: "links", title: tr("Shared links"), icon: Link2, colors: ["#22d3ee", "#0891b2"], admin: false, size: [1000, 640], component: LinksPage as PageComponent, path: "/links" },
  {
    id: "storage", title: tr("Storage Manager"), icon: HardDrive, colors: ["#0ea5e9", "#2563eb"], admin: true, size: [1120, 760],
    sections: [
      { id: "volumes", label: tr("Volumes"), icon: <Database size={16} />, component: page(Volumes), path: "/storage/volumes" },
      { id: "raid", label: "RAID", icon: <Layers size={16} />, component: page(Raid), path: "/storage/raid" },
      { id: "zfs", label: "ZFS", icon: <Boxes size={16} />, component: page(Zfs), path: "/storage/zfs" },
      { id: "lvm", label: "LVM", icon: <Server size={16} />, component: page(Lvm), path: "/storage/lvm" },
      { id: "iscsi", label: "iSCSI", icon: <HardDriveUpload size={16} />, component: page(Iscsi), path: "/storage/iscsi" },
      { id: "disks", label: tr("Disks"), icon: <HardDrive size={16} />, component: page(Disks), path: "/storage/disks" },
      { id: "analyzer", label: tr("Space analyzer"), icon: <PieChart size={16} />, component: page(SpaceAnalyzer), path: "/storage/analyzer" },
      { id: "duplicates", label: tr("Duplicates"), icon: <Copy size={16} />, component: page(Duplicates), path: "/storage/duplicates" },
    ],
  },
  {
    id: "control", title: tr("Control Panel"), icon: SlidersHorizontal, colors: ["#64748b", "#334155"], admin: true, size: [1120, 760],
    sections: [
      { id: "shares", label: tr("Shared folders"), icon: <Share2 size={16} />, component: page(Shares), path: "/shares" },
      { id: "fileservices", label: tr("File services"), icon: <FolderTree size={16} />, component: page(FileServicesPage), path: "/system/fileservices" },
      { id: "users", label: tr("Users and groups"), icon: <Users size={16} />, component: UsersPage as PageComponent, path: "/users" },
      { id: "directory", label: tr("Domain / LDAP"), icon: <BookUser size={16} />, component: page(DirectoryPage), path: "/system/directory" },
      { id: "schedules", label: tr("Scheduled tasks"), icon: <CalendarClock size={16} />, component: page(Schedules), path: "/system/schedules" },
      { id: "antivirus", label: tr("Antivirus"), icon: <ShieldCheck size={16} />, component: page(AntivirusPage), path: "/system/antivirus" },
      { id: "guard", label: tr("Address blocking"), icon: <ShieldBan size={16} />, component: page(GuardPage), path: "/system/guard" },
      { id: "network", label: tr("Network"), icon: <Network size={16} />, component: page(NetworkPage), path: "/system/network" },
      { id: "remote", label: tr("Remote access"), icon: <Waypoints size={16} />, component: page(RemotePage), path: "/system/remote" },
      { id: "ha", label: tr("High availability"), icon: <ServerCog size={16} />, component: page(HAPage), path: "/system/ha" },
      { id: "ups", label: tr("UPS"), icon: <BatteryCharging size={16} />, component: page(UPSPage), path: "/system/ups" },
      { id: "energy", label: tr("Power"), icon: <Leaf size={16} />, component: page(EnergyPage), path: "/system/energy" },
      { id: "hardware", label: tr("Hardware"), icon: <Fan size={16} />, component: page(HardwarePage), path: "/system/hardware" },
      { id: "services", label: tr("Services"), icon: <Cpu size={16} />, component: page(Services), path: "/system/services" },
      { id: "updates", label: tr("Updates"), icon: <RefreshCcw size={16} />, component: page(Updates), path: "/system/updates" },
      { id: "settings", label: tr("Settings"), icon: <Settings size={16} />, component: page(SettingsPage), path: "/system/settings" },
    ],
  },
  { id: "backup", title: tr("Backup"), icon: DatabaseBackup, colors: ["#14b8a6", "#0d9488"], admin: true, size: [1040, 720], component: page(Backups), path: "/backup" },
  { id: "cloudsync", title: tr("Cloud sync"), icon: CloudCog, colors: ["#38bdf8", "#0ea5e9"], admin: true, size: [1040, 720], component: page(CloudSync), path: "/cloudsync" },
  { id: "appcenter", title: tr("App Center"), icon: AppWindow, colors: ["#ec4899", "#f43f5e"], admin: true, size: [1120, 760], component: page(AppStore), path: "/apps" },
  {
    id: "docker", title: tr("Docker"), icon: Container, colors: ["#06b6d4", "#0284c7"], admin: true, size: [1120, 720],
    sections: [
      { id: "containers", label: tr("Containers"), icon: <Container size={16} />, component: page(Containers), path: "/docker/containers" },
      { id: "stacks", label: tr("Compose stacks"), icon: <Package size={16} />, component: page(Stacks), path: "/docker/stacks" },
      { id: "images", label: tr("Images and networks"), icon: <Layers size={16} />, component: page(Images), path: "/docker/images" },
    ],
  },
  { id: "vms", title: tr("Virtual machines"), icon: MonitorIcon, colors: ["#8b5cf6", "#6d28d9"], admin: true, size: [1080, 720], component: page(VirtualMachines), path: "/vms" },
  { id: "help", title: tr("Help"), icon: LifeBuoy, colors: ["#a3e635", "#16a34a"], admin: false, size: [900, 760], component: page(HelpPage), path: "/help" },
  { id: "fleet", title: tr("Multi-NAS Console"), icon: Network, colors: ["#38bdf8", "#6366f1"], admin: true, size: [1120, 760], component: page(FleetPage), path: "/fleet" },
  { id: "monitor", title: tr("Resource Monitor"), icon: Activity, colors: ["#22c55e", "#059669"], admin: true, size: [1040, 720], component: page(Monitor), path: "/system/monitor" },
  { id: "logs", title: tr("Logs"), icon: FileText, colors: ["#a855f7", "#7c3aed"], admin: true, size: [1040, 680], component: page(Logs), path: "/system/logs" },
  { id: "terminal", title: tr("Terminal"), icon: SquareTerminal, colors: ["#334155", "#0f172a"], admin: true, size: [900, 560], component: page(TerminalPage), path: "/system/terminal" },
];

export const appById = (id: string) => APPS.find((a) => a.id === id);

/** Finds the app (and section) behind a classic route, to open links as windows. */
export function appForPath(path: string): { app: AppDef; section?: string } | undefined {
  for (const app of APPS) {
    if (app.path === path) return { app };
    const s = app.sections?.find((x) => x.path === path);
    if (s) return { app, section: s.id };
  }
  return undefined;
}

export function AppIcon({ app, size = 56 }: { app: AppDef; size?: number }) {
  const I = app.icon;
  return (
    <div
      className="flex shrink-0 items-center justify-center text-white shadow-lg"
      style={{ width: size, height: size, borderRadius: size * 0.26, background: `linear-gradient(145deg, ${app.colors[0]}, ${app.colors[1]})`, boxShadow: `0 6px 18px -6px ${app.colors[1]}` }}
    >
      <I size={size * 0.5} strokeWidth={1.8} />
    </div>
  );
}
