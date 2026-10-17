import { lazy, type ComponentType, type ReactNode } from "react";
import {
  Activity, AppWindow, BookUser, CloudCog, FolderTree, BatteryCharging, DatabaseBackup, Boxes, CalendarClock, Container, Cpu, Database, FileText, FolderOpen, HardDrive, Layers, LayoutDashboard, Monitor as MonitorIcon,
  Network, Package, RefreshCcw, Server, Settings, Share2, SlidersHorizontal, SquareTerminal, Users,
} from "lucide-react";
import type { Me } from "../lib/types";
import { tr } from "../lib/i18n";

const Dashboard = lazy(() => import("../pages/Dashboard"));
const Disks = lazy(() => import("../pages/storage/Disks"));
const Raid = lazy(() => import("../pages/storage/Raid"));
const Volumes = lazy(() => import("../pages/storage/Volumes"));
const Zfs = lazy(() => import("../pages/storage/Zfs"));
const Lvm = lazy(() => import("../pages/storage/Lvm"));
const Shares = lazy(() => import("../pages/Shares"));
const UsersPage = lazy(() => import("../pages/Users"));
const Files = lazy(() => import("../pages/Files"));
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
const VirtualMachines = lazy(() => import("../pages/vms/VirtualMachines"));
const CloudSync = lazy(() => import("../pages/system/CloudSync"));
const FileServicesPage = lazy(() => import("../pages/system/FileServices"));
const DirectoryPage = lazy(() => import("../pages/system/Directory"));

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
  { id: "overview", title: tr("Vue d'ensemble"), icon: LayoutDashboard, colors: ["#6366f1", "#8b5cf6"], admin: false, size: [1080, 760], component: Dashboard as PageComponent, path: "/" },
  { id: "files", title: tr("Fichiers"), icon: FolderOpen, colors: ["#f59e0b", "#f97316"], admin: false, size: [1000, 660], component: page(Files), path: "/files" },
  {
    id: "storage", title: tr("Gestionnaire de stockage"), icon: HardDrive, colors: ["#0ea5e9", "#2563eb"], admin: true, size: [1120, 760],
    sections: [
      { id: "volumes", label: tr("Volumes"), icon: <Database size={16} />, component: page(Volumes), path: "/storage/volumes" },
      { id: "raid", label: "RAID", icon: <Layers size={16} />, component: page(Raid), path: "/storage/raid" },
      { id: "zfs", label: "ZFS", icon: <Boxes size={16} />, component: page(Zfs), path: "/storage/zfs" },
      { id: "lvm", label: "LVM", icon: <Server size={16} />, component: page(Lvm), path: "/storage/lvm" },
      { id: "disks", label: tr("Disques"), icon: <HardDrive size={16} />, component: page(Disks), path: "/storage/disks" },
    ],
  },
  {
    id: "control", title: tr("Panneau de configuration"), icon: SlidersHorizontal, colors: ["#64748b", "#334155"], admin: true, size: [1120, 760],
    sections: [
      { id: "shares", label: tr("Dossiers partagés"), icon: <Share2 size={16} />, component: page(Shares), path: "/shares" },
      { id: "fileservices", label: tr("Services de fichiers"), icon: <FolderTree size={16} />, component: page(FileServicesPage), path: "/system/fileservices" },
      { id: "users", label: tr("Utilisateurs et groupes"), icon: <Users size={16} />, component: UsersPage as PageComponent, path: "/users" },
      { id: "directory", label: tr("Domaine / LDAP"), icon: <BookUser size={16} />, component: page(DirectoryPage), path: "/system/directory" },
      { id: "schedules", label: tr("Tâches planifiées"), icon: <CalendarClock size={16} />, component: page(Schedules), path: "/system/schedules" },
      { id: "network", label: tr("Réseau"), icon: <Network size={16} />, component: page(NetworkPage), path: "/system/network" },
      { id: "ups", label: tr("Onduleur"), icon: <BatteryCharging size={16} />, component: page(UPSPage), path: "/system/ups" },
      { id: "services", label: tr("Services"), icon: <Cpu size={16} />, component: page(Services), path: "/system/services" },
      { id: "updates", label: tr("Mises à jour"), icon: <RefreshCcw size={16} />, component: page(Updates), path: "/system/updates" },
      { id: "settings", label: tr("Paramètres"), icon: <Settings size={16} />, component: page(SettingsPage), path: "/system/settings" },
    ],
  },
  { id: "backup", title: tr("Sauvegarde"), icon: DatabaseBackup, colors: ["#14b8a6", "#0d9488"], admin: true, size: [1040, 720], component: page(Backups), path: "/backup" },
  { id: "cloudsync", title: tr("Synchronisation cloud"), icon: CloudCog, colors: ["#38bdf8", "#0ea5e9"], admin: true, size: [1040, 720], component: page(CloudSync), path: "/cloudsync" },
  { id: "appcenter", title: tr("App Center"), icon: AppWindow, colors: ["#ec4899", "#f43f5e"], admin: true, size: [1120, 760], component: page(AppStore), path: "/apps" },
  {
    id: "docker", title: tr("Docker"), icon: Container, colors: ["#06b6d4", "#0284c7"], admin: true, size: [1120, 720],
    sections: [
      { id: "containers", label: tr("Conteneurs"), icon: <Container size={16} />, component: page(Containers), path: "/docker/containers" },
      { id: "stacks", label: tr("Stacks Compose"), icon: <Package size={16} />, component: page(Stacks), path: "/docker/stacks" },
      { id: "images", label: tr("Images et réseaux"), icon: <Layers size={16} />, component: page(Images), path: "/docker/images" },
    ],
  },
  { id: "vms", title: tr("Machines virtuelles"), icon: MonitorIcon, colors: ["#8b5cf6", "#6d28d9"], admin: true, size: [1080, 720], component: page(VirtualMachines), path: "/vms" },
  { id: "monitor", title: tr("Moniteur de ressources"), icon: Activity, colors: ["#22c55e", "#059669"], admin: true, size: [1040, 720], component: page(Monitor), path: "/system/monitor" },
  { id: "logs", title: tr("Journaux"), icon: FileText, colors: ["#a855f7", "#7c3aed"], admin: true, size: [1040, 680], component: page(Logs), path: "/system/logs" },
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
