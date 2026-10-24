import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Bell, Check, Edit3, Laptop, QrCode, Smartphone, Trash2, X } from "lucide-react";
import { api } from "../lib/api";
import { dateTime } from "../lib/format";
import { tr } from "../lib/i18n";
import { Badge, Button, ErrorBox, Input, Modal, Notice, Spinner, useUI } from "./ui";
import { DesktopApp } from "./DesktopApp";

interface Device {
  id: string;
  name: string;
  model: string;
  app?: string;
  kind: "android" | "desktop";
  os?: string;
  createdAt: string;
  lastSeen?: string;
  lastIp?: string;
  push: boolean;
  pushError?: string;
}

export interface Pairing {
  code: string;
  expires: string;
  fingerprint: string;
  httpsPort: string;
  user: string;
}

const releases = "https://github.com/Nathplay15/StrataOS/releases/latest";

/** Phones paired with the StrataOS app: pair with a QR code, rename, test push, revoke. */
export function MobileDevicesModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast, confirm } = useUI();
  const list = useQuery({ queryKey: ["mobile-devices"], queryFn: () => api.get<Device[]>("/api/me/devices"), enabled: open, refetchInterval: open ? 5000 : false });
  const [pair, setPair] = useState<(Pairing & { url: string; qr: string }) | null>(null);
  const [edit, setEdit] = useState<{ id: string; name: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setPair(null); setEdit(null); setErr(null); }
  }, [open]);
  // The QR code expires: hide it then.
  useEffect(() => {
    if (!pair) return;
    const t = setTimeout(() => setPair(null), Math.max(0, new Date(pair.expires).getTime() - Date.now()));
    return () => clearTimeout(t);
  }, [pair]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try { await fn(); } catch (e) { setErr(e); } finally { setBusy(false); qc.invalidateQueries({ queryKey: ["mobile-devices"] }); }
  };
  const startPairing = () => run(async () => {
    const p = await api.post<Pairing>("/api/me/devices/pairing");
    const host = window.location.hostname.includes(":") ? `[${window.location.hostname}]` : window.location.hostname;
    const url = `https://${host}:${p.httpsPort}`;
    const uri = `strataos://pair?${new URLSearchParams({ url, fp: p.fingerprint, code: p.code, user: p.user })}`;
    setPair({ ...p, url, qr: await QRCode.toDataURL(uri, { margin: 1, width: 260 }) });
  });
  const rename = () => run(async () => {
    await api.put(`/api/me/devices/${edit!.id}`, { name: edit!.name });
    setEdit(null);
  });
  const test = (d: Device) => run(async () => {
    await api.post(`/api/me/devices/${d.id}/test`);
    toast(tr("Test notification sent"));
  });
  const revoke = async (d: Device) => {
    const text = d.kind === "desktop"
      ? tr("The sync app on this computer is signed out at once and stops synchronizing (its files stay on the computer). Pair it again to use it.")
      : tr("The app on this phone is signed out at once and stops backing up. Pair it again to use it.");
    if (!(await confirm({ title: tr("Revoke {0}?", d.name), text, danger: true, confirm: tr("Revoke") }))) return;
    run(async () => {
      await api.del(`/api/me/devices/${d.id}`);
      toast(tr("Device revoked"));
    });
  };

  const devices = list.data;
  return (
    <Modal open={open} onClose={onClose} title={tr("Phones and computers")} wide>
      <div className="space-y-4">
        <p className="text-[13.5px]">{tr("The StrataOS app for Android browses your files, backs up the photos and videos of your phone and shows the NAS alerts.")}{" "}
          <a className="text-accent hover:underline" href={releases} target="_blank" rel="noreferrer">{tr("Download the app (APK)")}</a>
        </p>
        {list.isLoading || !devices ? <Spinner /> : devices.length === 0 ? <p className="text-muted text-[13px]">{tr("No phone or computer paired yet.")}</p> : (
          <div className="divide-y divide-border rounded-xl border border-border">
            {devices.map((d) => (
              <div key={d.id} className="flex items-center gap-3 px-4 py-3">
                {d.kind === "desktop" ? <Laptop size={18} className="text-accent" /> : <Smartphone size={18} className="text-accent" />}
                <div className="min-w-0 flex-1">
                  {edit?.id === d.id ? (
                    <div className="flex items-center gap-1">
                      <Input autoFocus value={edit.name} maxLength={64} onChange={(e) => setEdit({ ...edit, name: e.target.value })} onKeyDown={(e) => { if (e.key === "Enter") rename(); if (e.key === "Escape") setEdit(null); }} />
                      <Button size="sm" variant="ghost" icon={<Check size={14} />} loading={busy} onClick={rename} />
                      <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => setEdit(null)} />
                    </div>
                  ) : (
                    <div className="flex flex-wrap items-center gap-1.5 font-medium">
                      <span className="truncate">{d.name}</span>
                      {d.model && d.model !== d.name && <span className="text-muted text-[12px] font-normal">{d.model}</span>}
                      {d.os && <span className="text-muted text-[12px] font-normal">{d.os}</span>}
                      {d.kind === "desktop" ? <Badge tone="accent">{tr("Sync app")}</Badge> : d.push ? <Badge tone={d.pushError ? "warn" : "ok"}>{tr("Push")}</Badge> : <Badge>{tr("No push")}</Badge>}
                    </div>
                  )}
                  <div className="text-muted text-[12px]">
                    {tr("Paired {0}", dateTime(d.createdAt))}
                    {d.lastSeen && <> · {tr("last seen {0}", dateTime(d.lastSeen))}{d.lastIp && <> ({d.lastIp})</>}</>}
                    {d.app && <> · {tr("app {0}", d.app)}</>}
                  </div>
                  {d.pushError && <div className="text-warn text-[12px]">{tr("Last push failed: {0}", d.pushError)}</div>}
                </div>
                {edit?.id !== d.id && <>
                  {d.push && <Button size="sm" variant="ghost" title={tr("Send a test notification")} icon={<Bell size={14} />} onClick={() => test(d)} />}
                  <Button size="sm" variant="ghost" title={tr("Rename")} icon={<Edit3 size={14} />} onClick={() => setEdit({ id: d.id, name: d.name })} />
                  <Button size="sm" variant="ghost" className="text-danger" title={tr("Revoke")} icon={<Trash2 size={14} />} onClick={() => revoke(d)} />
                </>}
              </div>
            ))}
          </div>
        )}

        <ErrorBox error={err} />
        {pair ? (
          <div className="flex flex-col items-center gap-3 rounded-xl bg-panel-2 p-4 text-center sm:flex-row sm:items-start sm:text-left">
            <img src={pair.qr} alt="" className="h-[220px] w-[220px] rounded-lg bg-white p-1" />
            <div className="min-w-0 flex-1 space-y-2 text-[13px]">
              <p className="font-medium">{tr("In the app, choose Scan a QR code.")}</p>
              <p className="text-muted">{tr("The code works once, for 10 minutes, and signs the phone in as {0} without typing the password.", pair.user)}</p>
              <p className="text-muted">{tr("Address")}: <span className="mono">{pair.url}</span></p>
              {pair.fingerprint && <p className="text-muted break-all">{tr("Certificate fingerprint (SHA-256)")}: <span className="mono text-[11.5px]">{pair.fingerprint}</span></p>}
              <Notice>{tr("The phone must reach this address. Away from home, use the VPN (WireGuard, Tailscale) or a domain name.")}</Notice>
              <Button onClick={() => setPair(null)}>{tr("Done")}</Button>
            </div>
          </div>
        ) : (
          <Button variant="primary" icon={<QrCode size={15} />} loading={busy} onClick={startPairing}>{tr("Pair a phone")}</Button>
        )}
        <DesktopApp />
        <p className="text-muted text-[12px]">{tr("NAS notifications reach the phones of administrators through a UnifiedPush distributor installed on the phone (for example the ntfy app); without one, the app checks every 15 minutes.")}</p>
      </div>
    </Modal>
  );
}
