import { useEffect, useRef, useState, type ReactNode } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity, AppWindow, BatteryCharging, CalendarClock, DatabaseBackup, LayoutGrid, Boxes, ChevronDown, Container, Cpu, Database, FileText, FolderOpen, HardDrive, Layers, LayoutDashboard,
  ListChecks, LogOut, Menu, Moon, Network, Package, Power, RefreshCcw, Server, Settings, Share2, ShieldCheck, SquareTerminal, Sun, Users, X, Loader2, CheckCircle2, XCircle, KeyRound, Monitor, CloudCog, FolderTree, BookUser, ServerCog, Waypoints, Images, Link2, Leaf, ShieldBan } from "lucide-react";
import logo from "../logo.svg";
import { setUIMode } from "../desktop/mode";
import { TwoFactorModal } from "./TwoFactor";
import { api } from "../lib/api";
import { useJobs } from "../lib/jobs";
import type { Me } from "../lib/types";
import { cx, ago } from "../lib/format";
import { Button, Field, Input, Modal, Progress, useUI } from "./ui";
import { tr, srv } from "../lib/i18n";
import { LangSwitch } from "./LangSwitch";

type NavItem = { to: string; label: string; icon: ReactNode; admin?: boolean };

const nav: { title: string; items: NavItem[] }[] = [
  { title: "", items: [{ to: "/", label: tr("Tableau de bord"), icon: <LayoutDashboard size={17} /> }] },
  {
    title: tr("Stockage"),
    items: [
      { to: "/storage/disks", label: tr("Disques"), icon: <HardDrive size={17} />, admin: true },
      { to: "/storage/raid", label: "RAID", icon: <Layers size={17} />, admin: true },
      { to: "/storage/volumes", label: tr("Volumes"), icon: <Database size={17} />, admin: true },
      { to: "/storage/zfs", label: "ZFS", icon: <Boxes size={17} />, admin: true },
      { to: "/storage/lvm", label: "LVM", icon: <Server size={17} />, admin: true },
      { to: "/backup", label: tr("Sauvegarde"), icon: <DatabaseBackup size={17} />, admin: true },
      { to: "/cloudsync", label: tr("Synchronisation cloud"), icon: <CloudCog size={17} />, admin: true },
    ],
  },
  {
    title: tr("Partage"),
    items: [
      { to: "/files", label: tr("Fichiers"), icon: <FolderOpen size={17} /> },
      { to: "/photos", label: tr("Photos"), icon: <Images size={17} /> },
      { to: "/links", label: tr("Liens partagés"), icon: <Link2 size={17} /> },
      { to: "/shares", label: tr("Dossiers partagés"), icon: <Share2 size={17} />, admin: true },
      { to: "/users", label: tr("Utilisateurs"), icon: <Users size={17} />, admin: true },
      { to: "/system/fileservices", label: tr("Services de fichiers"), icon: <FolderTree size={17} />, admin: true },
      { to: "/system/directory", label: tr("Domaine / LDAP"), icon: <BookUser size={17} />, admin: true },
      { to: "/system/antivirus", label: tr("Antivirus"), icon: <ShieldCheck size={17} />, admin: true },
      { to: "/system/guard", label: tr("Blocage d'adresses"), icon: <ShieldBan size={17} />, admin: true },
    ],
  },
  {
    title: tr("Applications"),
    items: [
      { to: "/apps", label: tr("App Center"), icon: <AppWindow size={17} />, admin: true },
      { to: "/docker/containers", label: tr("Conteneurs"), icon: <Container size={17} />, admin: true },
      { to: "/docker/stacks", label: tr("Stacks Compose"), icon: <Package size={17} />, admin: true },
      { to: "/docker/images", label: tr("Images & réseaux"), icon: <Layers size={17} />, admin: true },
      { to: "/vms", label: tr("Machines virtuelles"), icon: <Monitor size={17} />, admin: true },
    ],
  },
  {
    title: tr("Système"),
    items: [
      { to: "/system/monitor", label: tr("Moniteur"), icon: <Activity size={17} />, admin: true },
      { to: "/fleet", label: tr("Console multi-NAS"), icon: <Network size={17} />, admin: true },
      { to: "/system/schedules", label: tr("Tâches planifiées"), icon: <CalendarClock size={17} />, admin: true },
      { to: "/system/services", label: tr("Services"), icon: <Cpu size={17} />, admin: true },
      { to: "/system/network", label: tr("Réseau"), icon: <Network size={17} />, admin: true },
      { to: "/system/remote", label: tr("Accès à distance"), icon: <Waypoints size={17} />, admin: true },
      { to: "/system/ha", label: tr("Haute disponibilité"), icon: <ServerCog size={17} />, admin: true },
      { to: "/system/ups", label: tr("Onduleur"), icon: <BatteryCharging size={17} />, admin: true },
      { to: "/system/energy", label: tr("Énergie"), icon: <Leaf size={17} />, admin: true },
      { to: "/system/updates", label: tr("Mises à jour"), icon: <RefreshCcw size={17} />, admin: true },
      { to: "/system/logs", label: tr("Journaux"), icon: <FileText size={17} />, admin: true },
      { to: "/system/terminal", label: tr("Terminal"), icon: <SquareTerminal size={17} />, admin: true },
      { to: "/system/settings", label: tr("Paramètres"), icon: <Settings size={17} />, admin: true },
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

export function JobsMenu() {
  const { jobs } = useJobs();
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<string | null>(null);
  const running = jobs.filter((j) => j.status === "running");
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const h = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", h);
    return () => document.removeEventListener("mousedown", h);
  }, []);
  const { data: job } = useQuery({
    queryKey: ["job", detail],
    queryFn: () => api.get<import("../lib/types").Job>(`/api/jobs/${detail}`),
    enabled: !!detail,
    refetchInterval: (q) => (q.state.data?.status === "running" ? 1500 : false),
  });
  return (
    <div className="relative" ref={ref}>
      <button onClick={() => setOpen(!open)} className="relative flex h-9 items-center gap-2 rounded-lg px-2.5 text-muted hover:bg-panel-2 hover:text-fg">
        {running.length > 0 ? <Loader2 size={17} className="animate-spin text-accent" /> : <ListChecks size={17} />}
        <span className="hidden text-[13px] sm:inline">{tr("Tâches")}</span>
        {running.length > 0 && <span className="rounded-full bg-accent px-1.5 text-[11px] font-semibold text-white">{running.length}</span>}
      </button>
      {open && (
        <div className="card absolute right-0 top-11 z-40 w-[380px] max-w-[calc(100vw-2rem)] p-2">
          <div className="px-2 py-1.5 text-[12px] font-medium text-muted">{tr("Tâches en arrière-plan")}</div>
          {jobs.length === 0 && <div className="px-2 py-6 text-center text-muted text-[13px]">{tr("Aucune tâche récente")}</div>}
          <div className="max-h-[420px] overflow-y-auto">
            {jobs.slice(0, 20).map((j) => (
              <button key={j.id} onClick={() => setDetail(j.id)} className="flex w-full items-start gap-2.5 rounded-lg px-2 py-2 text-left hover:bg-panel-2">
                {j.status === "running" ? <Loader2 size={16} className="mt-0.5 animate-spin text-accent" /> : j.status === "success" ? <CheckCircle2 size={16} className="mt-0.5 text-ok" /> : <XCircle size={16} className="mt-0.5 text-danger" />}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px]">{srv(j.title)}</div>
                  <div className="text-[11.5px] text-muted">{ago(j.started)}{j.error ? ` · ${j.error}` : ""}</div>
                  {j.status === "running" && <Progress className="mt-1.5" value={j.progress} indeterminate={j.progress < 0} />}
                </div>
              </button>
            ))}
          </div>
        </div>
      )}
      <Modal open={!!detail} onClose={() => setDetail(null)} title={job ? srv(job.title) : tr("Tâche")} wide>
        {job && (
          <div className="space-y-3">
            {job.status === "running" && <Progress value={job.progress} indeterminate={job.progress < 0} />}
            {job.error && <div className="text-danger text-[13px]">{job.error}</div>}
            <pre className="mono max-h-[50vh] overflow-auto whitespace-pre-wrap rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{job.log || tr("(aucune sortie)")}</pre>
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
      toast(tr("Mot de passe modifié"));
      onClose();
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title={tr("Changer mon mot de passe")} footer={<><Button onClick={onClose}>{tr("Annuler")}</Button><Button variant="primary" loading={busy} onClick={submit} disabled={pw.length < 8}>{tr("Enregistrer")}</Button></>}>
      <div className="space-y-4">
        <Field label={tr("Mot de passe actuel")}><Input type="password" value={cur} onChange={(e) => setCur(e.target.value)} /></Field>
        <Field label={tr("Nouveau mot de passe")} hint={tr("8 caractères minimum")}><Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
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
  const loc = useLocation();
  const nav_ = useNavigate();
  const qc = useQueryClient();
  const { confirm } = useUI();
  const { data: info } = useQuery({ queryKey: ["setup"], queryFn: () => api.get<{ hostname: string; version: string }>("/api/setup"), staleTime: 60000 });
  useEffect(() => setMobileNav(false), [loc.pathname]);

  const logout = async () => {
    await api.post("/api/logout").catch(() => {});
    qc.clear();
    window.location.href = "/";
  };
  const power = async (action: "reboot" | "poweroff") => {
    const ok = await confirm({ title: action === "reboot" ? tr("Redémarrer le NAS ?") : tr("Éteindre le NAS ?"), text: tr("Les partages et les applications seront interrompus."), danger: true, confirm: action === "reboot" ? tr("Redémarrer") : tr("Éteindre") });
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
          <div className="flex-1" />
          <JobsMenu />
          <button onClick={() => setTheme(theme === "dark" ? "light" : "dark")} className="flex h-9 w-9 items-center justify-center rounded-lg text-muted hover:bg-panel-2 hover:text-fg" title={tr("Thème")}>
            {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
          </button>
          <div className="relative">
            <button onClick={() => setUserMenu(!userMenu)} className="flex items-center gap-2 rounded-lg py-1.5 pl-1.5 pr-2 hover:bg-panel-2">
              <span className="flex h-7 w-7 items-center justify-center rounded-full bg-gradient-to-br from-accent to-accent-2 text-[12px] font-semibold text-white">{me.username[0]?.toUpperCase()}</span>
              <span className="hidden text-[13px] sm:inline">{me.fullName || me.username}</span>
              <ChevronDown size={14} className="text-muted" />
            </button>
            {userMenu && (
              <>
                <div className="fixed inset-0 z-30" onClick={() => setUserMenu(false)} />
                <div className="card absolute right-0 top-11 z-40 w-56 p-1.5 text-[13px]">
                  <div className="px-3 py-2 text-muted text-[12px]">{me.role === "admin" ? tr("Administrateur") : tr("Utilisateur")}</div>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setPwModal(true); }}><KeyRound size={15} /> {" "}{tr("Mot de passe")}</button>
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); setTfa(true); }}><ShieldCheck size={15} /> {" "}{tr("Double authentification")}</button>
                  <button className="hidden w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2 lg:flex" onClick={() => setUIMode("desktop")}><LayoutGrid size={15} /> {" "}{tr("Mode bureau")}</button>
                  {me.role === "admin" && (
                    <>
                      <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); power("reboot"); }}><RefreshCcw size={15} /> {" "}{tr("Redémarrer")}</button>
                      <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); power("poweroff"); }}><Power size={15} /> {" "}{tr("Éteindre")}</button>
                      <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" onClick={() => { setUserMenu(false); nav_("/system/settings"); }}><Settings size={15} /> {" "}{tr("Paramètres")}</button>
                    </>
                  )}
                  <LangSwitch className="flex w-full items-center gap-2 rounded-lg px-3 py-2 hover:bg-panel-2" />
                  <div className="my-1 border-t border-line" />
                  <button className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-danger hover:bg-panel-2" onClick={logout}><LogOut size={15} /> {" "}{tr("Déconnexion")}</button>
                </div>
              </>
            )}
          </div>
        </header>
        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1400px] px-4 py-6 sm:px-6 lg:px-8">
            <Outlet />
          </div>
        </main>
      </div>
      <PasswordModal open={pwModal} onClose={() => setPwModal(false)} />
      <TwoFactorModal open={tfa} onClose={() => setTfa(false)} />
    </div>
  );
}
