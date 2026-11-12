import { useEffect, useRef, useState, type ReactNode } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity, AppWindow, BatteryCharging, CalendarClock, DatabaseBackup, LayoutGrid, Boxes, ChevronDown, Container, Cpu, Database, FileText, FolderOpen, HardDrive, HardDriveUpload, Layers, LayoutDashboard, ListChecks, LogOut, Menu, Moon, Network, Package, Power, RefreshCcw, Server, Settings, Share2, ShieldCheck, SquareTerminal, Sun, Users, X, Loader2, CheckCircle2, XCircle, CircleStop, KeyRound, Monitor, CloudCog, FolderTree, BookUser, ServerCog, Waypoints, Images, Link2, Leaf, ShieldBan, LifeBuoy, PieChart, Copy, Fan, FingerprintPattern, Clapperboard, Cctv, Smartphone, TabletSmartphone, Palette,
} from "lucide-react";
import logo from "../logo.svg";
import { setUIMode } from "../desktop/mode";
import { TwoFactorModal } from "./TwoFactor";
import { PasskeysModal } from "./Passkeys";
import { SecurityKeysModal } from "./SecurityKeys";
import { S3KeysModal } from "./S3Keys";
import { TwoFactorPrompt } from "./TwoFactorPrompt";
import { MobileDevicesModal } from "./MobileDevices";
import { api } from "../lib/api";
import { useJobs } from "../lib/jobs";
import type { Me, Overview } from "../lib/types";
import { NotificationCenter } from "./NotificationCenter";
import { StopJobButton } from "./StopJob";
import { useDismiss } from "../lib/a11y";
import { cx, ago } from "../lib/format";
import { Button, Field, Input, Modal, PageBoundary, Progress, useUI } from "./ui";
import { tr, srv } from "../lib/i18n";
import { LangSwitch } from "./LangSwitch";
import { GlobalSearch } from "./GlobalSearch";
import { AppearanceModal } from "./Appearance";
import { useAccountDialog } from "../lib/intent";
import { useAccent } from "../lib/appearance";

type NavItem = { to: string; label: string; icon: ReactNode; admin?: boolean };

const nav: { title: string; items: NavItem[] }[] = [
  { title: "", items: [{ to: "/", label: tr("Dashboard"), icon: <LayoutDashboard size={17} /> }, { to: "/help", label: tr("Help"), icon: <LifeBuoy size={17} /> }] },
  {
    title: tr("Storage"),
    items: [
      { to: "/storage/disks", label: tr("Disks"), icon: <HardDrive size={17} />, admin: true },
      { to: "/storage/raid", label: "RAID", icon: <Layers size={17} />, admin: true },
      { to: "/storage/volumes", label: tr("Volumes"), icon: <Database size={17} />, admin: true },
      { to: "/storage/zfs", label: "ZFS", icon: <Boxes size={17} />, admin: true },
      { to: "/storage/lvm", label: "LVM", icon: <Server size={17} />, admin: true },
      { to: "/storage/iscsi", label: "iSCSI", icon: <HardDriveUpload size={17} />, admin: true },
      { to: "/storage/ceph", label: "Ceph", icon: <Network size={17} />, admin: true },
      { to: "/storage/analyzer", label: tr("Space analyzer"), icon: <PieChart size={17} />, admin: true },
      { to: "/storage/duplicates", label: tr("Duplicates"), icon: <Copy size={17} />, admin: true },
      { to: "/backup", label: tr("Backup"), icon: <DatabaseBackup size={17} />, admin: true },
      { to: "/cloudsync", label: tr("Cloud sync"), icon: <CloudCog size={17} />, admin: true },
    ],
  },
  {
    title: tr("Sharing"),
    items: [
      { to: "/files", label: tr("Files"), icon: <FolderOpen size={17} /> },
      { to: "/photos", label: tr("Photos"), icon: <Images size={17} /> },
      { to: "/media", label: tr("Media"), icon: <Clapperboard size={17} /> },
      { to: "/links", label: tr("Shared links"), icon: <Link2 size={17} /> },
      { to: "/vpn", label: tr("My VPN devices"), icon: <Smartphone size={17} /> },
      { to: "/shares", label: tr("Shared folders"), icon: <Share2 size={17} />, admin: true },
      { to: "/users", label: tr("Users"), icon: <Users size={17} />, admin: true },
      { to: "/system/fileservices", label: tr("File services"), icon: <FolderTree size={17} />, admin: true },
      { to: "/system/directory", label: tr("Domain / LDAP"), icon: <BookUser size={17} />, admin: true },
      { to: "/system/antivirus", label: tr("Antivirus"), icon: <ShieldCheck size={17} />, admin: true },
      { to: "/system/guard", label: tr("Address blocking"), icon: <ShieldBan size={17} />, admin: true },
      { to: "/system/security", label: tr("Security advisor"), icon: <ShieldCheck size={17} />, admin: true },
    ],
  },
  {
    title: tr("Applications"),
    items: [
      { to: "/apps", label: tr("App Center"), icon: <AppWindow size={17} />, admin: true },
      { to: "/docker/containers", label: tr("Containers"), icon: <Container size={17} />, admin: true },
      { to: "/docker/stacks", label: tr("Compose stacks"), icon: <Package size={17} />, admin: true },
      { to: "/docker/images", label: tr("Images & networks"), icon: <Layers size={17} />, admin: true },
      { to: "/vms", label: tr("Virtual machines"), icon: <Monitor size={17} />, admin: true },
      { to: "/surveillance", label: tr("Surveillance"), icon: <Cctv size={17} />, admin: true },
    ],
  },
  {
    title: tr("System"),
    items: [
      { to: "/system/monitor", label: tr("Monitor"), icon: <Activity size={17} />, admin: true },
      { to: "/fleet", label: tr("Multi-NAS Console"), icon: <Network size={17} />, admin: true },
      { to: "/system/schedules", label: tr("Scheduled tasks"), icon: <CalendarClock size={17} />, admin: true },
      { to: "/system/services", label: tr("Services"), icon: <Cpu size={17} />, admin: true },
      { to: "/system/network", label: tr("Network"), icon: <Network size={17} />, admin: true },
      { to: "/system/remote", label: tr("Remote access"), icon: <Waypoints size={17} />, admin: true },
      { to: "/system/ha", label: tr("High availability"), icon: <ServerCog size={17} />, admin: true },
      { to: "/system/ups", label: tr("UPS"), icon: <BatteryCharging size={17} />, admin: true },
      { to: "/system/energy", label: tr("Power"), icon: <Leaf size={17} />, admin: true },
      { to: "/system/hardware", label: tr("Hardware"), icon: <Fan size={17} />, admin: true },
      { to: "/system/updates", label: tr("Updates"), icon: <RefreshCcw size={17} />, admin: true },
      { to: "/system/logs", label: tr("Logs"), icon: <FileText size={17} />, admin: true },
      { to: "/system/terminal", label: tr("Terminal"), icon: <SquareTerminal size={17} />, admin: true },
      { to: "/system/settings", label: tr("Settings"), icon: <Settings size={17} />, admin: true },
    ],
  },
];

export function useTheme() {
  const [theme, setTheme] = useState<string>(() => {
    try {
      return localStorage.getItem("strata-theme") ?? "dark";
    } catch {
      return "dark";
    }
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("strata-theme", theme);
    } catch {
      /* storage unavailable */
    }
  }, [theme]);
  return [theme, setTheme] as const;
}

function JobIcon({ status }: { status: string }) {
  if (status === "running") return <Loader2 size={16} className="mt-0.5 shrink-0 animate-spin text-accent" />;
  if (status === "success") return <CheckCircle2 size={16} className="mt-0.5 shrink-0 text-ok" />;
  if (status === "cancelled") return <CircleStop size={16} className="mt-0.5 shrink-0 text-warn" />;
  return <XCircle size={16} className="mt-0.5 shrink-0 text-danger" />;
}

export function JobsMenu() {
  const { jobs } = useJobs();
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<string | null>(null);
  const running = jobs.filter((j) => j.status === "running");
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open && !detail, () => setOpen(false));
  const { data: job } = useQuery({
    queryKey: ["job", detail],
    queryFn: () => api.get<import("../lib/types").Job>(`/api/jobs/${detail}`),
    enabled: !!detail,
    refetchInterval: (q) => (q.state.data?.status === "running" ? 1500 : false),
  });
  return (
    <div className="relative" ref={ref}>
      <button onClick={() => setOpen(!open)} aria-expanded={open} aria-label={running.length ? tr("Tasks ({0} running)", running.length) : tr("Tasks")} className="relative flex h-9 items-center gap-2 rounded-lg px-2.5 text-muted hover:bg-panel-2 hover:text-fg">
        {running.length > 0 ? <Loader2 size={17} className="animate-spin text-accent" /> : <ListChecks size={17} />}
        <span className="hidden text-[13px] sm:inline">{tr("Tasks")}</span>
        {running.length > 0 && <span className="rounded-full bg-accent px-1.5 text-[11px] font-semibold text-white">{running.length}</span>}
      </button>
      {open && (
        <div className="card absolute right-0 top-11 z-40 w-[380px] max-w-[calc(100vw-2rem)] p-2">
          <div className="px-2 py-1.5 text-[12px] font-medium text-muted">{tr("Background tasks")}</div>
          {jobs.length === 0 && <div className="px-2 py-6 text-center text-muted text-[13px]">{tr("No recent tasks")}</div>}
          <div className="max-h-[420px] overflow-y-auto">
            {jobs.slice(0, 20).map((j) => (
              <div key={j.id} className="flex w-full items-start gap-1 rounded-lg hover:bg-panel-2">
                <button onClick={() => setDetail(j.id)} className="flex min-w-0 flex-1 items-start gap-2.5 px-2 py-2 text-left">
                  <JobIcon status={j.status} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px]">{srv(j.title)}</div>
                    <div className="text-[11.5px] text-muted">{ago(j.started)}{j.status === "cancelled" ? ` · ${tr("stopped by {0}", j.cancelledBy ?? "")}` : j.error ? ` · ${srv(j.error)}` : ""}</div>
                    {j.status === "running" && <Progress className="mt-1.5" value={j.progress} indeterminate={j.progress < 0} />}
                  </div>
                </button>
                {j.status === "running" && <StopJobButton job={j} className="mt-1.5 mr-1 shrink-0" />}
              </div>
            ))}
          </div>
        </div>
      )}
      <Modal open={!!detail} onClose={() => setDetail(null)} title={job ? srv(job.title) : tr("Task")} wide>
        {job && (
          <div className="space-y-3">
            {job.status === "running" && (
              <div className="flex items-center gap-3">
                <Progress className="flex-1" value={job.progress} indeterminate={job.progress < 0} />
                <StopJobButton job={job} variant="secondary" />
              </div>
            )}
            {job.status === "running" && job.cancellable === false && job.cancelReason && <div className="text-muted text-[12.5px]">{srv(job.cancelReason)}</div>}
            {job.status === "cancelled" && <div className="text-warn text-[13px]">{tr("Stopped by {0}", job.cancelledBy ?? "")}</div>}
            {job.error && <div className="text-danger text-[13px]">{srv(job.error)}</div>}
            <pre className="mono max-h-[50vh] overflow-auto whitespace-pre-wrap rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{job.log || tr("(no output)")}</pre>
          </div>
        )}
      </Modal>
    </div>
  );
}

export function PasswordModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { toast } = useUI();
  const [cur, setCur] = useState("");
  const [pw, setPw] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    try {
      await api.post("/api/me/password", { current: cur, new: pw });
      toast(tr("Password changed"));
      onClose();
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title={tr("Change my password")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={busy} onClick={submit} disabled={pw.length < 8}>{tr("Save")}</Button></>}>
      <div className="space-y-4">
        <Field label={tr("Current password")}><Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} /></Field>
        <Field label={tr("New password")} hint={tr("8 characters minimum")}><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
      </div>
    </Modal>
  );
}

export function Layout({ me }: { me: Me }) {
  const [theme, setTheme] = useTheme();
  const [mobileNav, setMobileNav] = useState(false);
  const [userMenu, setUserMenu] = useState(false);
  const [pwModal, setPwModal] = useState(false);
  const [tfa, setTfa] = useState(false);
  const [passkeys, setPasskeys] = useState(false);
  const [keys, setKeys] = useState(false);
  const [s3, setS3] = useState(false);
  const [mobile, setMobile] = useState(false);
  const [look, setLook] = useState(false);
  useAccent();
  useAccountDialog((d) => {
    if (d === "appearance") setLook(true);
    else if (d === "password") setPwModal(true);
    else if (d === "2fa") setTfa(true);
    else if (d === "passkeys") setPasskeys(true);
    else if (d === "securitykeys") setKeys(true);
    else if (d === "s3") setS3(true);
    else if (d === "devices") setMobile(true);
  });
  const loc = useLocation();
  const nav_ = useNavigate();
  const qc = useQueryClient();
  const { confirm } = useUI();
  const { data: info } = useQuery({ queryKey: ["setup"], queryFn: () => api.get<{ hostname: string; version: string }>("/api/setup"), staleTime: 60000 });
  useEffect(() => setMobileNav(false), [loc.pathname]);
  const { data: overview } = useQuery({ queryKey: ["overview"], queryFn: () => api.get<Overview>("/api/system/overview"), refetchInterval: 30000 });

  const logout = async () => {
    await api.post("/api/logout").catch(() => {});
    qc.clear();
    window.location.href = "/";
  };
  const power = async (action: "reboot" | "poweroff") => {
    const ok = await confirm({ title: action === "reboot" ? tr("Restart the NAS?") : tr("Shut down the NAS?"), text: tr("Shares and applications will be interrupted."), danger: true, confirm: action === "reboot" ? tr("Restart") : tr("Shut down") });
    if (ok) await api.post(`/api/system/power/${action}`);
  };

  const sidebar = (
    <nav className="flex h-full flex-col">
      <div className="flex items-center gap-2.5 px-5 py-5">
        <img src={logo} alt="" className="h-9 w-9" />
        <div>
          <div className="font-semibold tracking-tight text-[16px]">{tr("Strata")}<span className="text-accent">OS</span></div>
          <div className="text-[11px] text-muted">{info?.hostname ?? "NAS"}</div>
        </div>
      </div>
      <div className="flex-1 space-y-5 overflow-y-auto px-3 pb-6">
        {nav.map((g) => {
          const items = g.items.filter((i) => !i.admin || me.role === "admin");
          if (!items.length) return null;
          return (
            <div key={g.title}>
              {g.title && <div className="mb-1.5 px-3 text-[11px] font-semibold uppercase tracking-wider text-muted/80">{g.title}</div>}
              <div className="space-y-0.5">
                {items.map((i) => (
                  <NavLink key={i.to} to={i.to} end={i.to === "/"} className={({ isActive }) => cx("flex items-center gap-3 rounded-lg px-3 py-2 text-[13.5px] transition", isActive ? "bg-accent-soft text-accent font-medium" : "text-muted hover:bg-panel-2 hover:text-fg")}>
                    {i.icon}
                    {i.label}
                  </NavLink>
                ))}
              </div>
            </div>
          );
        })}
      </div>
      <div className="border-t border-line px-5 py-3 text-[11px] text-muted">StrataOS {info?.version}</div>
    </nav>
  );

  return (
    <div className="flex h-full">
      <aside className="hidden w-[248px] shrink-0 border-r border-line bg-panel lg:block">{sidebar}</aside>
      {mobileNav && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-black/50" onClick={() => setMobileNav(false)} />
          <aside className="absolute inset-y-0 left-0 w-[260px] border-r border-line bg-panel">{sidebar}</aside>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center justify-between gap-2 border-b border-line bg-panel/80 px-4 backdrop-blur">
          <button className="rounded-lg p-2 text-muted hover:bg-panel-2 lg:hidden" onClick={() => setMobileNav(true)} aria-label={tr("Menu")}>
            {mobileNav ? <X size={18} /> : <Menu size={18} />}
          </button>
          <div className="flex flex-1 justify-start lg:justify-center"><GlobalSearch me={me} openPage={(p) => nav_(p)} /></div>
          <JobsMenu />
          <NotificationCenter me={me} alerts={overview?.alerts ?? []} openPage={(p) => nav_(p)} />
          <button onClick={() => setTheme(theme === "dark" ? "light" : "dark")} className="flex h-9 w-9 items-center justify-center rounded-lg text-muted hover:bg-panel-2 hover:text-fg" title={tr("Theme")}>
            {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
          </button>
          <div className="relative">
            <button onClick={() => setUserMenu(!userMenu)} className="flex items-center gap-2 rounded-lg py-1.5 pl-1.5 pr-2 hover:bg-panel-2">
              <span className="flex h-7 w-7 items-center justify-center rounded-full bg-gradient-to-br from-accent to-accent-2 text-[12px] font-semibold text-white">{me.username[0]?.toUpperCase()}</span>
              <span className="hidden text-[13px] sm:inline">{me.fullName || me.username}</span>
              <ChevronDown size={14} className="text-muted" />
            </button>
            {userMenu && (
              <div onKeyDown={(e) => e.key === "Escape" && setUserMenu(false)}>
                <div className="fixed inset-0 z-30" onClick={() => setUserMenu(false)} />
                <div className="card absolute right-0 top-11 z-40 w-56 p-1.5 text-[13px]">
                  <div className="px-3 py-2 text-muted text-[12px]">{me.role === "admin" ? tr("Administrator") : tr("User")}</div>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setLook(true); }}><Palette size={15} /> {" "}{tr("Appearance")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setPwModal(true); }}><KeyRound size={15} /> {" "}{tr("Password")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setTfa(true); }}><ShieldCheck size={15} /> {" "}{tr("Two-factor authentication")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setPasskeys(true); }}><FingerprintPattern size={15} /> {" "}{tr("Passkeys")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setKeys(true); }}><KeyRound size={15} /> {" "}{tr("Security keys and sign-in")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setS3(true); }}><Database size={15} /> {" "}{tr("S3 access keys")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setMobile(true); }}><TabletSmartphone size={15} /> {" "}{tr("Phones and computers")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); nav_("/vpn"); }}><Smartphone size={15} /> {" "}{tr("My VPN devices")}</button>
                  <button className="hidden w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2 lg:flex" onClick={() => setUIMode("desktop")}><LayoutGrid size={15} /> {" "}{tr("Desktop mode")}</button>
                  {me.role === "admin" && (
                    <>
                      <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); power("reboot"); }}><RefreshCcw size={15} /> {" "}{tr("Restart")}</button>
                      <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); power("poweroff"); }}><Power size={15} /> {" "}{tr("Shut down")}</button>
                      <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); nav_("/system/settings"); }}><Settings size={15} /> {" "}{tr("Settings")}</button>
                    </>
                  )}
                  <LangSwitch className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" />
                  <div className="my-1 border-t border-line" />
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-danger hover:bg-panel-2" onClick={logout}><LogOut size={15} /> {" "}{tr("Sign out")}</button>
                </div>
              </div>
            )}
          </div>
        </header>
        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1400px] px-4 py-6 sm:px-6 lg:px-8">
            <PageBoundary key={loc.pathname}><Outlet /></PageBoundary>
          </div>
        </main>
      </div>
      <PasswordModal open={pwModal} onClose={() => setPwModal(false)} />
      <TwoFactorModal open={tfa} onClose={() => setTfa(false)} />
      <PasskeysModal open={passkeys} onClose={() => setPasskeys(false)} />
      <SecurityKeysModal open={keys} onClose={() => setKeys(false)} />
      <S3KeysModal open={s3} onClose={() => setS3(false)} />
      <MobileDevicesModal open={mobile} onClose={() => setMobile(false)} />
      <AppearanceModal open={look} onClose={() => setLook(false)} />
      <TwoFactorPrompt me={me} />
    </div>
  );
}
