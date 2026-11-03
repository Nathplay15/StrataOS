import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { Box, Container as ContainerIcon, File, Folder, Loader2, Monitor, Palette, Search, Settings, Share2, User as UserIcon, LayoutPanelLeft, KeyRound, ShieldCheck, FingerprintPattern, TabletSmartphone } from "lucide-react";
import type { Container, FileEntry, Me, Share, User } from "../lib/types";
import type { VMData } from "../pages/vms/types";
import type { LXCData } from "../pages/vms/containers/types";
import { cx } from "../lib/format";
import { english, tr } from "../lib/i18n";
import { rank, type Searchable } from "../lib/searchRank";
import { openAccountDialog, postIntent, revealElement, type AccountDialog } from "../lib/intent";
import { APPS, AppIcon } from "../desktop/apps";

// Global search (top bar, Ctrl+K / ⌘K, "/"): pages and settings are matched
// in the browser; shared folders, files, machines, containers and users come
// from the NAS, each request on its own with a timeout so a slow one never
// holds the others back. Only what the user may open is listed.

type Group = "apps" | "settings" | "shares" | "files" | "vms" | "docker" | "users";

interface Hit extends Searchable {
  key: string;
  icon: ReactNode;
  sub?: string;
  run: () => void;
}

const GROUPS: { id: Group; label: string; max: number }[] = [
  { id: "apps", label: tr("Applications and pages"), max: 6 },
  { id: "settings", label: tr("Settings"), max: 4 },
  { id: "shares", label: tr("Shared folders"), max: 4 },
  { id: "files", label: tr("Files"), max: 8 },
  { id: "vms", label: tr("Virtual machines and containers"), max: 5 },
  { id: "docker", label: tr("Docker containers"), max: 5 },
  { id: "users", label: tr("Users"), max: 5 },
];

/** Extra words finding an app or a Control Panel section (English, and translated). */
const KEYWORDS: Record<string, string> = {
  overview: tr("dashboard home status"),
  files: tr("explorer documents upload download folder"),
  photos: tr("pictures images albums gallery"),
  media: tr("video music movies streaming"),
  links: tr("share link public"),
  myvpn: tr("wireguard phone"),
  storage: tr("disk volume pool raid"),
  volumes: tr("disk space btrfs ext4 snapshot"),
  disks: tr("smart health temperature hdd ssd"),
  analyzer: tr("space usage big files"),
  shares: tr("smb nfs permissions"),
  fileservices: tr("smb nfs ftp webdav afp"),
  users: tr("account group password"),
  directory: tr("active directory ldap domain"),
  schedules: tr("cron task scrub"),
  antivirus: tr("clamav virus scan"),
  guard: tr("firewall ban block ip"),
  network: tr("ip dhcp dns bridge bond vlan hostname interface"),
  remote: tr("internet tunnel vpn ddns"),
  ha: tr("failover replication cluster"),
  ups: tr("battery power outage nut"),
  energy: tr("sleep schedule wake power"),
  hardware: tr("fan sensors temperature"),
  services: tr("daemon systemd ssh"),
  updates: tr("upgrade apt version"),
  settings: tr("smtp email notifications time certificate"),
  backup: tr("rsync restic snapshot restore"),
  cloudsync: tr("s3 google drive dropbox rclone"),
  appcenter: tr("install apps store"),
  docker: tr("compose container image"),
  containers: tr("docker compose"),
  vms: tr("kvm qemu lxc virtual machine"),
  surveillance: tr("camera nvr cctv"),
  monitor: tr("cpu memory processes performance"),
  logs: tr("events journal audit"),
  terminal: tr("shell console ssh command"),
  fleet: tr("multiple nas"),
  help: tr("guide documentation faq"),
};

const words = (id: string) => (KEYWORDS[id] ? [...KEYWORDS[id].split(" "), ...english(KEYWORDS[id]).split(" ")] : []);
const both = (label: string) => [label, english(label)];

/** A GET that gives up after ms milliseconds. */
async function getWithin<T>(path: string, ms: number, signal?: AbortSignal): Promise<T> {
  const ctl = new AbortController();
  const t = setTimeout(() => ctl.abort(), ms);
  signal?.addEventListener("abort", () => ctl.abort());
  try {
    const r = await fetch(path, { headers: { "X-Strata": "1" }, credentials: "same-origin", signal: ctl.signal });
    if (!r.ok) throw new Error(r.statusText);
    return (await r.json()) as T;
  } finally {
    clearTimeout(t);
  }
}

/** A list from the NAS, shared with the cache of the page showing it. */
function useList<T>(key: string, path: string, enabled: boolean) {
  return useQuery({ queryKey: [key], queryFn: () => getWithin<T>(path, 4000), enabled, retry: false, staleTime: 30000 });
}

const isMac = /Mac|iPhone|iPad/.test(navigator.platform);

function editable(t: EventTarget | null) {
  const el = t as HTMLElement | null;
  return !!el && (el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName) || !!el.closest(".cm-editor, .xterm"));
}

/**
 * The search box of the top bar and its results. openPage opens a classic
 * route (a page of the classic layout, a window of the desktop mode).
 */
export function GlobalSearch({ me, openPage, dark }: { me: Me; openPage: (path: string) => void; dark?: boolean }) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      // Terminals keep Ctrl+K (kill line in shells).
      if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k" && !(e.target as HTMLElement | null)?.closest?.(".xterm")) {
        e.preventDefault();
        setOpen((o) => !o);
      } else if (e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey && !editable(e.target) && !document.querySelector("[role=dialog]")) {
        e.preventDefault();
        setOpen(true);
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, []);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={tr("Search")}
        className={cx(
          "flex h-9 items-center gap-2 rounded-lg px-2.5 text-[13px] transition md:w-60",
          dark ? "bg-white/10 text-white/70 hover:bg-white/15 hover:text-white" : "border border-line bg-panel-2 text-muted hover:border-accent/50 hover:text-fg",
        )}
      >
        <Search size={16} className="shrink-0" />
        <span className="hidden flex-1 text-left md:inline">{tr("Search…")}</span>
        <kbd className={cx("hidden rounded px-1.5 text-[11px] md:inline", dark ? "bg-white/10" : "border border-line")}>{isMac ? "⌘K" : "Ctrl K"}</kbd>
      </button>
      {open && <SearchPalette me={me} openPage={openPage} desktop={dark} onClose={() => setOpen(false)} />}
    </>
  );
}

function SearchPalette({ me, openPage, onClose, desktop }: { me: Me; openPage: (path: string) => void; onClose: () => void; desktop?: boolean }) {
  const admin = me.role === "admin";
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const done = (f: () => void) => () => { onClose(); f(); };

  // Static items: apps, Control Panel sections, settings, personal settings.
  const statics = useMemo(() => {
    const out: (Hit & { group: Group })[] = [];
    for (const a of APPS) {
      if (a.admin && !admin) continue;
      const path = a.path ?? a.sections?.[0]?.path ?? "/";
      out.push({ group: "apps", key: "app-" + a.id, labels: both(a.title), keywords: words(a.id), icon: <AppIcon app={a} size={26} />, run: done(() => openPage(path)) });
      for (const s of a.sections ?? []) {
        out.push({ group: "apps", key: "sec-" + s.id, labels: both(s.label), keywords: words(s.id), sub: a.title, icon: <AppIcon app={a} size={26} />, run: done(() => openPage(s.path)) });
      }
    }
    const setting = (id: string, label: string, kw: string, icon: ReactNode = <Settings size={18} />) => {
      if (admin) out.push({ group: "settings", key: id, labels: both(label), keywords: [...kw.split(" "), ...english(kw).split(" ")], sub: tr("Control Panel → Settings"), icon, run: done(() => { openPage("/system/settings"); revealElement(id); }) });
    };
    setting("settings-smb", tr("File sharing (SMB)"), tr("workgroup samba recycle"));
    setting("settings-security", tr("Security"), tr("session duration"));
    setting("settings-notifications", tr("Notifications"), tr("email smtp telegram ntfy webhook alerts report"));
    setting("settings-monitoring", tr("Monitoring (Prometheus, Grafana)"), tr("metrics prometheus grafana"));
    setting("settings-time", tr("Date and time"), tr("time zone ntp clock"));
    setting("settings-https", tr("HTTPS certificate"), tr("tls ssl let's encrypt certificate"));
    setting("settings-components", tr("Components"), tr("install uninstall packages"));
    const personal = (d: AccountDialog, label: string, kw: string, icon: ReactNode) =>
      out.push({ group: "settings", key: "me-" + d, labels: both(label), keywords: [...kw.split(" "), ...english(kw).split(" ")], sub: tr("My account"), icon, run: done(() => openAccountDialog(d)) });
    personal("appearance", tr("Appearance"), tr("wallpaper background accent colour color theme personalize"), <Palette size={18} />);
    if (desktop) personal("widgets", tr("Desktop widgets"), tr("widgets desktop panel personalize"), <LayoutPanelLeft size={18} />);
    personal("password", tr("Password"), tr("change password"), <KeyRound size={18} />);
    personal("2fa", tr("Two-factor authentication"), tr("2fa totp otp security"), <ShieldCheck size={18} />);
    personal("passkeys", tr("Passkeys"), tr("webauthn fingerprint security key"), <FingerprintPattern size={18} />);
    personal("devices", tr("Phones and computers"), tr("mobile app android sync devices"), <TabletSmartphone size={18} />);
    return out;
  }, [admin, desktop]);

  // Lists from the NAS, fetched once the search is open (shared with the pages' caches).
  const shares = useList<Share[]>("shares", "/api/shares", true);
  const vms = useList<VMData>("vms", "/api/vms", admin);
  const lxc = useList<LXCData>("lxc-list", "/api/lxc", admin);
  const docker = useList<Container[]>("containers", "/api/docker/containers", admin);
  const users = useList<User[]>("users", "/api/users", admin);

  // Files: searched on the NAS after a short pause in typing, at most 8.
  const [files, setFiles] = useState<{ q: string; entries: FileEntry[]; busy: boolean }>({ q: "", entries: [], busy: false });
  useEffect(() => {
    const term = q.trim();
    if (term.length < 2) {
      setFiles({ q: term, entries: [], busy: false });
      return;
    }
    const ctl = new AbortController();
    setFiles((f) => ({ ...f, busy: true }));
    const t = setTimeout(() => {
      getWithin<{ entries: FileEntry[] }>(`/api/files/search?q=${encodeURIComponent(term)}&limit=8`, 5000, ctl.signal)
        .then((r) => setFiles({ q: term, entries: r.entries ?? [], busy: false }))
        .catch(() => !ctl.signal.aborted && setFiles({ q: term, entries: [], busy: false }));
    }, 250);
    return () => { clearTimeout(t); ctl.abort(); };
  }, [q]);

  const groups = useMemo(() => {
    const term = q.trim();
    if (!term) return [];
    const by: Record<Group, Hit[]> = { apps: [], settings: [], shares: [], files: [], vms: [], docker: [], users: [] };
    for (const h of rank(term, statics)) by[h.group].push(h);
    by.shares = rank(term, (shares.data ?? []).map((s): Hit => ({
      key: "share-" + s.name, labels: [s.name], keywords: s.description ? [s.description] : [], sub: s.path, icon: <Share2 size={18} />,
      run: done(() => { openPage("/files"); postIntent({ page: "files", dir: s.path }); }),
    })));
    by.files = files.q === term ? files.entries.map((e): Hit => {
      const dir = e.dir ? e.path : e.path.slice(0, e.path.lastIndexOf("/")) || "/";
      return {
        key: "file-" + e.path, labels: [e.name], sub: e.path.replace(/^\/srv\/strata\//, ""), icon: e.dir ? <Folder size={18} className="text-accent" /> : <File size={18} />,
        run: done(() => { openPage("/files"); postIntent({ page: "files", dir, file: e.dir ? undefined : e }); }),
      };
    }) : [];
    if (admin) {
      by.vms = rank(term, [
        ...(vms.data?.vms ?? []).map((v): Hit => ({ key: "vm-" + v.name, labels: [v.name], keywords: [tr("virtual machine"), v.os ?? ""], sub: v.template ? tr("Template") : tr("Virtual machine"), icon: <Monitor size={18} />, run: done(() => { openPage("/vms"); postIntent({ page: "vms", kind: "vm", name: v.name }); }) })),
        ...(lxc.data?.containers ?? []).map((c): Hit => ({ key: "lxc-" + c.name, labels: [c.name], keywords: [c.image.distro, "lxc"], sub: tr("Container (LXC)"), icon: <Box size={18} />, run: done(() => { openPage("/vms"); postIntent({ page: "vms", kind: "lxc", name: c.name }); }) })),
      ]);
      by.docker = rank(term, (Array.isArray(docker.data) ? docker.data : []).map((c): Hit => {
        const name = (c.Names[0] ?? c.Id).replace(/^\//, "");
        return { key: "dk-" + c.Id, labels: [name], keywords: [c.Image], sub: c.Image, icon: <ContainerIcon size={18} />, run: done(() => { openPage("/docker/containers"); postIntent({ page: "docker", name }); }) };
      }));
      by.users = rank(term, (users.data ?? []).map((u): Hit => ({ key: "user-" + u.username, labels: [u.username, u.fullName], keywords: u.email ? [u.email] : [], sub: u.fullName || u.email, icon: <UserIcon size={18} />, run: done(() => { openPage("/users"); postIntent({ page: "users", name: u.username }); }) })));
    }
    return GROUPS.map((g) => ({ ...g, hits: by[g.id].slice(0, g.max) })).filter((g) => g.hits.length > 0);
  }, [q, statics, shares.data, files, vms.data, lxc.data, docker.data, users.data, admin]);

  const flat = groups.flatMap((g) => g.hits);
  useEffect(() => setSel(0), [q]);
  useEffect(() => {
    listRef.current?.querySelector(`[data-i="${sel}"]`)?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const loading = files.busy || (q.trim().length > 0 && (shares.isLoading || (admin && (vms.isLoading || users.isLoading))));

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") { e.preventDefault(); setSel((s) => Math.min(flat.length - 1, s + 1)); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setSel((s) => Math.max(0, s - 1)); }
    else if (e.key === "Enter") { e.preventDefault(); flat[sel]?.run(); }
    else if (e.key === "Escape") { e.preventDefault(); onClose(); }
  };

  let i = -1;
  // In a portal: the desktop's top bar has a backdrop blur, which would make it
  // the containing block of this fixed overlay and squash it to the bar's height.
  return createPortal(
    <div className="fixed inset-0 z-[10000] flex justify-center bg-black/40 px-4 pt-[10vh] text-fg" onMouseDown={onClose}>
      <div role="dialog" aria-modal="true" aria-label={tr("Search")} className="card flex max-h-[70vh] w-[640px] max-w-full flex-col overflow-hidden" onMouseDown={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-2 border-b border-line px-4">
          <Search size={17} className="shrink-0 text-muted" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={onKey}
            placeholder={tr("Search apps, settings, shared folders, files…")}
            aria-label={tr("Search")}
            role="combobox"
            aria-expanded={flat.length > 0}
            aria-controls="global-search-results"
            aria-activedescendant={flat[sel] ? `gs-${sel}` : undefined}
            className="h-12 min-w-0 flex-1 bg-transparent text-[14.5px] outline-none"
          />
          {loading && <Loader2 size={16} className="animate-spin text-muted" />}
        </div>
        <div ref={listRef} id="global-search-results" role="listbox" className="overflow-y-auto p-2">
          {!q.trim() && <div className="px-3 py-6 text-center text-muted text-[13px]">{tr("Type to search. ↑ ↓ to move, Enter to open, Esc to close.")}</div>}
          {q.trim() && flat.length === 0 && !loading && <div className="px-3 py-6 text-center text-muted text-[13px]">{tr("No results")}</div>}
          {groups.map((g) => (
            <div key={g.id} className="mb-1">
              <div className="px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wider text-muted">{g.label}</div>
              {g.hits.map((h) => {
                i++;
                const n = i;
                return (
                  <button
                    key={h.key}
                    id={`gs-${n}`}
                    data-i={n}
                    role="option"
                    aria-selected={n === sel}
                    type="button"
                    onMouseMove={() => n !== sel && setSel(n)}
                    onClick={h.run}
                    className={cx("flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left", n === sel ? "bg-accent-soft" : "hover:bg-panel-2")}
                  >
                    <span className="flex w-[26px] shrink-0 justify-center text-muted">{h.icon}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[13.5px]">{h.labels[0]}</span>
                      {h.sub && <span className="block truncate text-muted text-[11.5px]">{h.sub}</span>}
                    </span>
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>,
    document.body,
  );
}
