import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Database, FingerprintPattern, History, KeyRound, KeySquare, Link2, LogOut, Palette, RefreshCcw,
  ScrollText, Settings, ShieldCheck, Smartphone, TabletSmartphone,
} from "lucide-react";
import { api } from "../lib/api";
import type { Me } from "../lib/types";
import { openAccountDialog, type AccountDialog } from "../lib/intent";
import { tr } from "../lib/i18n";
import { cx } from "../lib/format";
import { PageHeader } from "../components/ui";

// The Me page of the phone app: who is signed in on which NAS, the account
// and security dialogs, and the way out. The phone tab bar points here.

const ACCOUNT: { d: AccountDialog; label: string; icon: React.ReactNode }[] = [
  { d: "appearance", label: "Appearance", icon: <Palette size={16} /> },
  { d: "password", label: "Password", icon: <KeyRound size={16} /> },
  { d: "2fa", label: "Two-factor authentication", icon: <ShieldCheck size={16} /> },
  { d: "passkeys", label: "Passkeys", icon: <FingerprintPattern size={16} /> },
  { d: "securitykeys", label: "Security keys and sign-in", icon: <KeyRound size={16} /> },
  { d: "s3", label: "S3 access keys", icon: <Database size={16} /> },
  { d: "tokens", label: "API tokens", icon: <KeyRound size={16} /> },
  { d: "devices", label: "Phones and computers", icon: <TabletSmartphone size={16} /> },
  { d: "apps", label: "Apps signed in with the NAS", icon: <KeySquare size={16} /> },
  { d: "sessions", label: "Sessions and sign-ins", icon: <History size={16} /> },
];

export default function MePage({ me }: { me: Me }) {
  const nav = useNavigate();
  const qc = useQueryClient();
  const { data: info } = useQuery({ queryKey: ["setup"], queryFn: () => api.get<{ hostname: string; version: string }>("/api/setup"), staleTime: 60000 });
  const admin = me.role === "admin";
  const name = me.fullName || me.username;
  const initials = name.split(/[ ._-]/).filter(Boolean).slice(0, 2).map((w) => w[0]?.toUpperCase()).join("") || "?";
  const row = "flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left text-[13.5px] text-muted hover:bg-panel-2 hover:text-fg";
  const group = (title: string, rows: React.ReactNode) => (
    <div className="card p-1.5">
      <div className="px-3 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wider text-muted/80">{title}</div>
      {rows}
    </div>
  );
  const logout = async () => {
    await api.post("/api/logout").catch(() => {});
    qc.clear();
    window.location.href = "/";
  };
  return (
    <div className="mx-auto max-w-[560px] space-y-4">
      <PageHeader title={tr("Me")} subtitle={me.username} />
      <div className="flex items-center gap-4 rounded-2xl bg-gradient-to-br from-accent to-accent-2 p-5">
        <span className="flex h-16 w-16 shrink-0 items-center justify-center rounded-full bg-white/20 text-[20px] font-semibold text-white">{initials}</span>
        <div className="min-w-0">
          <div className="truncate text-[17px] font-semibold text-white">{name}</div>
          <div className="truncate text-[12.5px] text-white/85">{admin ? tr("Administrator") : tr("User")} · {info?.hostname ?? "NAS"}</div>
          <div className="truncate text-[11.5px] text-white/70">StrataOS {info?.version}</div>
        </div>
      </div>
      {group(tr("My space"), (
        <>
          <button className={row} onClick={() => nav("/links")}><Link2 size={16} /> {tr("Shared links")}</button>
          <button className={row} onClick={() => nav("/activity")}><ScrollText size={16} /> {tr("My activity")}</button>
          <button className={row} onClick={() => nav("/vpn")}><Smartphone size={16} /> {tr("My VPN devices")}</button>
        </>
      ))}
      {group(tr("Account"), ACCOUNT.map((a) => (
        <button key={a.d} className={row} onClick={() => openAccountDialog(a.d)}>
          {a.icon} {tr(a.label)}
        </button>
      )))}
      {admin && group(tr("System"), (
        <>
          <button className={row} onClick={() => nav("/system/settings")}><Settings size={16} /> {tr("Settings")}</button>
          <button className={row} onClick={() => nav("/system/updates")}><RefreshCcw size={16} /> {tr("Updates")}</button>
        </>
      ))}
      <button className={cx(row, "text-danger hover:text-danger")} onClick={logout}><LogOut size={16} /> {tr("Sign out")}</button>
      <div className="pb-2 text-center text-[11px] text-muted">StrataOS {info?.version}</div>
    </div>
  );
}
