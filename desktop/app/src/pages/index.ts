// The pages of the window, in the order of the side bar.
import { AppWindow, ArrowUpDown, Bell, DatabaseBackup, FolderOpen, FolderSync, Globe, HardDrive, Home, Image, Link2, type LucideIcon, Monitor, RefreshCw, Settings as SettingsIcon, Share2, ShieldCheck, Users as UsersIcon } from "lucide-react";
import type { PageComponent } from "../lib/nav";
import { t } from "../lib/i18n";
import { OverviewPage } from "./Overview";
import { FilesPage } from "./Files";
import { PhotosPage } from "./Photos";
import { LinksPage } from "./Links";
import { SyncPage } from "./Sync";
import { TransfersPage } from "./Transfers";
import { StoragePage } from "./Storage";
import { SharesPage } from "./Shares";
import { UsersPage } from "./Users";
import { AppsPage } from "./Apps";
import { VmsPage } from "./Vms";
import { BackupsPage } from "./Backups";
import { UpdatesPage } from "./Updates";
import { SecurityPage } from "./Security";
import { NotificationsPage } from "./Notifications";
import { WebPage } from "./Web";
import { SettingsPage } from "./Settings";

export type PageId =
  | "home" | "files" | "photos" | "links" | "sync" | "transfers" | "storage" | "shares" | "users" | "apps" | "vms"
  | "backups" | "updates" | "security" | "notifications" | "web" | "settings";

export type GroupId = "nas" | "computer" | "manage" | "app";

export const groups: { id: GroupId; label?: () => string }[] = [
  { id: "nas" },
  { id: "computer", label: () => t("This computer") },
  { id: "manage", label: () => t("Management") },
  { id: "app", label: () => t("App") },
];

export interface PageDef {
  id: PageId;
  label: () => string;
  icon: LucideIcon;
  group: GroupId;
  /** Administrators only. */
  admin?: boolean;
  /** Not in the side bar (reached from another page). */
  hidden?: boolean;
  component: PageComponent;
}

export const pages: PageDef[] = [
  { id: "home", label: () => t("Overview"), icon: Home, group: "nas", component: OverviewPage },
  { id: "files", label: () => t("Files"), icon: FolderOpen, group: "nas", component: FilesPage },
  { id: "photos", label: () => t("Photos"), icon: Image, group: "nas", component: PhotosPage },
  { id: "links", label: () => t("Share links"), icon: Link2, group: "nas", component: LinksPage },
  { id: "sync", label: () => t("Folder sync"), icon: FolderSync, group: "computer", component: SyncPage },
  { id: "transfers", label: () => t("Transfers"), icon: ArrowUpDown, group: "computer", component: TransfersPage },
  { id: "storage", label: () => t("Storage"), icon: HardDrive, group: "manage", admin: true, component: StoragePage },
  { id: "shares", label: () => t("Shared folders"), icon: Share2, group: "manage", admin: true, component: SharesPage },
  { id: "users", label: () => t("Users and groups"), icon: UsersIcon, group: "manage", admin: true, component: UsersPage },
  { id: "apps", label: () => t("Applications"), icon: AppWindow, group: "manage", admin: true, component: AppsPage },
  { id: "vms", label: () => t("Virtual machines"), icon: Monitor, group: "manage", admin: true, component: VmsPage },
  { id: "backups", label: () => t("Backups"), icon: DatabaseBackup, group: "manage", admin: true, component: BackupsPage },
  { id: "updates", label: () => t("Updates"), icon: RefreshCw, group: "manage", admin: true, component: UpdatesPage },
  { id: "security", label: () => t("Security advisor"), icon: ShieldCheck, group: "manage", admin: true, component: SecurityPage },
  { id: "notifications", label: () => t("Notifications"), icon: Bell, group: "manage", admin: true, component: NotificationsPage },
  { id: "web", label: () => t("Web interface"), icon: Globe, group: "manage", component: WebPage },
  { id: "settings", label: () => t("Settings"), icon: SettingsIcon, group: "app", component: SettingsPage },
];

