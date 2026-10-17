import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Copy, Download, ShieldCheck, ShieldOff } from "lucide-react";
import { api } from "../lib/api";
import type { Me } from "../lib/types";
import { Button, ErrorBox, Field, Input, Modal, Notice, Spinner, useUI } from "./ui";
import { tr } from "../lib/i18n";

/** Enrolment and removal of TOTP two-factor authentication for the current user. */
export function TwoFactorModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me"), enabled: open });
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setSetup(null); setCodes(null); setCode(""); setPassword(""); setErr(null); }
  }, [open]);
  useEffect(() => {
    if (setup) QRCode.toDataURL(setup.uri, { margin: 1, width: 220 }).then(setQr);
  }, [setup]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try { await fn(); } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const start = () => run(async () => setSetup(await api.post("/api/me/2fa/setup")));
  const enable = () => run(async () => {
    const r = await api.post<{ recoveryCodes: string[] }>("/api/me/2fa/enable", { code });
    setCodes(r.recoveryCodes);
    qc.invalidateQueries({ queryKey: ["me"] });
  });
  const disable = () => run(async () => {
    await api.post("/api/me/2fa/disable", { password });
    toast(tr("Double authentification désactivée"));
    qc.invalidateQueries({ queryKey: ["me"] });
    onClose();
  });
  const download = () => {
    const blob = new Blob([tr("Codes de secours StrataOS ({0})\nChaque code ne fonctionne qu'une fois.\n\n{1}\n", me.data?.username, codes!.join("\n"))], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "strataos-codes-de-secours.txt";
    a.click();
  };

  return (
    <Modal open={open} onClose={onClose} title={tr("Double authentification")}>
      {me.isLoading ? <Spinner /> : codes ? (
        <div className="space-y-4">
          <Notice>{tr("La double authentification est active. Conservez ces")}{" "}<b>{tr("codes de secours")}</b> {" "}{tr("en lieu sûr : chacun permet une connexion si vous perdez votre téléphone. Ils ne seront plus affichés.")}</Notice>
          <div className="mono grid grid-cols-2 gap-2 rounded-xl bg-panel-2 p-4 text-center text-[14px]">{codes.map((c) => <span key={c}>{c}</span>)}</div>
          <div className="flex gap-2">
            <Button icon={<Copy size={15} />} onClick={() => { navigator.clipboard?.writeText(codes.join("\n")); toast(tr("Codes copiés")); }}>{tr("Copier")}</Button>
            <Button icon={<Download size={15} />} onClick={download}>{tr("Télécharger")}</Button>
            <div className="flex-1" />
            <Button variant="primary" onClick={onClose}>{tr("Terminé")}</Button>
          </div>
        </div>
      ) : me.data?.twoFactor ? (
        <div className="space-y-4">
          <div className="flex items-center gap-2 text-ok"><ShieldCheck size={18} /> {" "}{tr("Activée sur votre compte")}</div>
          <p className="text-muted text-[13px]">{tr("Un code de votre application est demandé à chaque connexion. Pour la désactiver, confirmez votre mot de passe.")}</p>
          <Field label={tr("Mot de passe")}><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} /></Field>
          <ErrorBox error={err} />
          <Button variant="danger" icon={<ShieldOff size={15} />} loading={busy} disabled={!password} onClick={disable}>{tr("Désactiver")}</Button>
        </div>
      ) : !setup ? (
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("Protégez votre compte avec un code à 6 chiffres généré par une application (Google Authenticator, Microsoft Authenticator, Authy, Bitwarden, 1Password…), demandé en plus du mot de passe.")}</p>
          <ErrorBox error={err} />
          <Button variant="primary" icon={<ShieldCheck size={15} />} loading={busy} onClick={start}>{tr("Configurer")}</Button>
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("1. Scannez ce QR code avec votre application.")}</p>
          <div className="flex flex-col items-center gap-2">
            {qr ? <img src={qr} alt={tr("QR code")} className="rounded-xl bg-white p-2" width={220} height={220} /> : <Spinner />}
            <code className="mono select-all break-all text-center text-muted text-[12px]">{setup.secret}</code>
          </div>
          <Field label={tr("2. Saisissez le code affiché")}><Input autoFocus inputMode="numeric" className="mono text-center text-[18px] tracking-[0.3em]" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
          <ErrorBox error={err} />
          <Button variant="primary" loading={busy} disabled={code.trim().length < 6} onClick={enable}>{tr("Activer")}</Button>
        </div>
      )}
    </Modal>
  );
}
