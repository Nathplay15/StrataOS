import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Copy, Download, Laptop } from "lucide-react";
import { api } from "../lib/api";
import { tr } from "../lib/i18n";
import { Button, ErrorBox, Notice, useUI } from "./ui";
import type { Pairing } from "./MobileDevices";

const repo = "https://github.com/Nathplay15/StrataOS";

/** The release files of the desktop sync app for this NAS version ("0.8.0~beta.1" is tagged v0.8.0-beta.1). */
function assets(version: string) {
  const v = version.replace(/~/g, "-");
  const base = `${repo}/releases/download/v${v}/strataos-sync-${v}`;
  return [
    { os: "Windows", label: tr("Installer (.msi)"), url: `${base}-windows-x64.msi` },
    { os: "Windows", label: tr("Portable (.zip)"), url: `${base}-windows-x64.zip` },
    { os: "macOS", label: tr("Disk image (.dmg)"), url: `${base}-macos.dmg` },
    { os: "Linux", label: "Debian/Ubuntu amd64 (.deb)", url: `${base}-linux-amd64.deb` },
    { os: "Linux", label: "Debian/Ubuntu arm64 (.deb)", url: `${base}-linux-arm64.deb` },
    { os: "Linux", label: tr("Other distributions (.tar.gz)"), url: `${base}-linux-amd64.tar.gz` },
  ];
}

/** Groups a pairing code for typing: "abcd-efgh-…". */
const grouped = (c: string) => c.toUpperCase().match(/.{1,4}/g)?.join("-") ?? c;

/** The desktop sync app: downloads, and pairing a computer with a code (or a link to paste). */
export function DesktopApp() {
  const qc = useQueryClient();
  const { toast } = useUI();
  const info = useQuery({ queryKey: ["sync-info"], queryFn: () => api.get<{ version: string }>("/api/sync/info") });
  const [pair, setPair] = useState<(Pairing & { url: string; link: string; qr: string }) | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!pair) return;
    const t = setTimeout(() => setPair(null), Math.max(0, new Date(pair.expires).getTime() - Date.now()));
    return () => clearTimeout(t);
  }, [pair]);

  const start = async () => {
    setBusy(true);
    setErr(null);
    try {
      const p = await api.post<Pairing>("/api/me/devices/pairing");
      const host = window.location.hostname.includes(":") ? `[${window.location.hostname}]` : window.location.hostname;
      const url = `https://${host}:${p.httpsPort}`;
      const link = `strataos://pair?${new URLSearchParams({ url, fp: p.fingerprint, code: p.code, user: p.user })}`;
      setPair({ ...p, url, link, qr: await QRCode.toDataURL(link, { margin: 1, width: 200 }) });
    } catch (e) { setErr(e); } finally { setBusy(false); qc.invalidateQueries({ queryKey: ["mobile-devices"] }); }
  };
  const copy = (v: string) => { navigator.clipboard?.writeText(v); toast(tr("Copied")); };

  return (
    <div className="space-y-3 rounded-xl border border-border p-4">
      <div className="flex items-center gap-2 font-medium"><Laptop size={17} className="text-accent" /> {tr("Desktop app")}</div>
      <p className="text-[13px]">{tr("StrataOS Sync keeps folders of your computer and of the NAS identical, in both directions, in the background (Windows, macOS, Linux).")}</p>
      {info.data && (
        <div className="grid gap-1.5 sm:grid-cols-2">
          {assets(info.data.version).map((a) => (
            <a key={a.url} className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-[13px] hover:bg-panel-2" href={a.url} target="_blank" rel="noreferrer">
              <Download size={14} className="text-accent" /> <span className="font-medium">{a.os}</span> <span className="text-muted">{a.label}</span>
            </a>
          ))}
        </div>
      )}
      <p className="text-muted text-[12px]">{tr("The app is not signed: on macOS, right-click it and choose Open the first time; on Windows, choose More info, then Run anyway.")}{" "}
        <a className="text-accent hover:underline" href={`${repo}/releases`} target="_blank" rel="noreferrer">{tr("All versions")}</a>
      </p>
      <ErrorBox error={err} />
      {pair ? (
        <div className="flex flex-col gap-3 rounded-xl bg-panel-2 p-4 sm:flex-row sm:items-start">
          <img src={pair.qr} alt="" className="h-[160px] w-[160px] self-center rounded-lg bg-white p-1 sm:self-start" />
          <div className="min-w-0 flex-1 space-y-2 text-[13px]">
            <p className="font-medium">{tr("In the app, choose Pair with a code and type:")}</p>
            <p>{tr("Server address")}: <span className="mono select-all">{pair.url}</span></p>
            <p className="flex flex-wrap items-center gap-2">{tr("Pairing code")}: <code className="mono select-all rounded bg-panel px-2 py-1 text-[15px] tracking-wider">{grouped(pair.code)}</code>
              <Button size="sm" variant="ghost" icon={<Copy size={14} />} onClick={() => copy(pair.code)} title={tr("Copy")} /></p>
            {pair.fingerprint && <p className="text-muted break-all">{tr("Certificate fingerprint (SHA-256)")}: <span className="mono text-[11.5px]">{pair.fingerprint}</span></p>}
            <p className="text-muted">{tr("Or copy the pairing link and paste it in the app: it carries the address, the code and the fingerprint.")}</p>
            <Notice>{tr("The code works once, for 10 minutes, and signs the computer in as {0}. The app shows the certificate fingerprint: check it matches before accepting.", pair.user)}</Notice>
            <div className="flex flex-wrap gap-2">
              <Button icon={<Copy size={14} />} onClick={() => copy(pair.link)}>{tr("Copy the pairing link")}</Button>
              <Button onClick={() => setPair(null)}>{tr("Done")}</Button>
            </div>
          </div>
        </div>
      ) : (
        <Button icon={<Laptop size={15} />} loading={busy} onClick={start}>{tr("Pair a computer")}</Button>
      )}
    </div>
  );
}
