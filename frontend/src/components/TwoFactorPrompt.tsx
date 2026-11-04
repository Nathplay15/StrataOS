import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BellOff, FingerprintPattern, LogOut, ShieldAlert, Smartphone } from "lucide-react";
import { api } from "../lib/api";
import type { Me, TwoFactorPrompt as Prompt } from "../lib/types";
import { dateTime } from "../lib/format";
import { tr } from "../lib/i18n";
import { passkeysSupported } from "../lib/webauthn";
import { Button, ErrorBox, Modal, Notice } from "./ui";
import { TwoFactorModal } from "./TwoFactor";
import { PasskeysModal } from "./Passkeys";

function Choices({ prompt, onApp, onPasskey }: { prompt: Prompt; onApp: () => void; onPasskey: () => void }) {
  const choice = "flex w-full items-start gap-3 rounded-xl border border-line p-4 text-left transition hover:border-accent hover:bg-accent-soft";
  return (
    <div className="space-y-3">
      <p className="text-[13.5px]">{tr("Your account can change everything on this NAS. With a second step at sign-in, a stolen or guessed password is no longer enough to get in.")}</p>
      {prompt.enforced ? (
        <Notice tone="warn">{tr("Two-factor authentication is required for administrators on this NAS. Set it up now to continue.")}</Notice>
      ) : prompt.required && prompt.deadline ? (
        <Notice tone="warn">{tr("Two-factor authentication is required for administrators: set it up before {0}.", dateTime(prompt.deadline))}</Notice>
      ) : null}
      <button className={choice} onClick={onApp}>
        <Smartphone size={22} className="mt-0.5 shrink-0 text-accent" />
        <span><span className="block font-medium">{tr("An authenticator app")}</span><span className="block text-muted text-[12.5px]">{tr("Scan a QR code with Google Authenticator, Microsoft Authenticator, Bitwarden… then type its 6-digit code at each sign-in.")}</span></span>
      </button>
      {passkeysSupported() && (
        <button className={choice} onClick={onPasskey}>
          <FingerprintPattern size={22} className="mt-0.5 shrink-0 text-accent" />
          <span><span className="block font-medium">{tr("A passkey")}</span><span className="block text-muted text-[12.5px]">{tr("Sign in with your fingerprint, face or a security key, on this device or your phone.")}</span></span>
        </button>
      )}
    </div>
  );
}

/**
 * The two-factor setup step shown to administrators without a second
 * factor, after signing in. Postponing hides it for 7 days; once the NAS
 * requires it and the grace period is over, it covers the interface until
 * done (the server only answers the setup requests meanwhile).
 */
export function TwoFactorPrompt({ me }: { me: Me }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me"), initialData: me, staleTime: 60_000, enabled: me.role === "admin" });
  const prompt = (q.data ?? me).twoFactorPrompt;
  const [hidden, setHidden] = useState(false);
  const [tfa, setTfa] = useState(false);
  const [passkeys, setPasskeys] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  // A sub-dialog closed: the account may be protected now.
  const refresh = () => qc.invalidateQueries({ queryKey: ["me"] });
  useEffect(() => { if (prompt?.enforced) setHidden(false); }, [prompt?.enforced]);
  const subs = <>
    <TwoFactorModal open={tfa} onClose={() => { setTfa(false); refresh(); }} />
    <PasskeysModal open={passkeys} onClose={() => { setPasskeys(false); refresh(); }} />
  </>;
  // Once protected, the sub-dialog stays until closed (recovery codes).
  if (!prompt?.show || (hidden && !prompt.enforced)) return tfa || passkeys ? subs : null;

  const postpone = async () => {
    setBusy(true);
    setErr(null);
    try {
      await api.post("/api/me/2fa/postpone");
      setHidden(true);
      refresh();
    } catch (e) { setErr(e); } finally { setBusy(false); }
  };
  const logout = async () => { await api.post("/api/logout").catch(() => {}); qc.clear(); window.location.href = "/"; };
  const title = <span className="flex items-center gap-2"><ShieldAlert size={18} className="text-accent" />{tr("Protect your administrator account")}</span>;

  if (prompt.enforced) {
    // Not a Modal: it cannot be closed. Below the sub-dialogs (z 9500).
    return createPortal(
      <div className="fixed inset-0 z-[9400] flex items-center justify-center bg-black/70 p-4 backdrop-blur">
        <div className="card w-full max-w-lg">
          <header className="border-b border-line px-5 py-3.5 font-semibold">{title}</header>
          <div className="p-5"><Choices prompt={prompt} onApp={() => setTfa(true)} onPasskey={() => setPasskeys(true)} /></div>
          <footer className="flex justify-between gap-2 border-t border-line px-5 py-3.5">
            <Button variant="ghost" icon={<LogOut size={15} />} onClick={logout}>{tr("Sign out")}</Button>
          </footer>
        </div>
        {subs}
      </div>,
      document.body,
    );
  }
  return (
    <>
      <Modal open={!tfa && !passkeys} onClose={() => setHidden(true)} title={title} footer={<>
        {prompt.canPostpone && <Button icon={<BellOff size={15} />} loading={busy} onClick={postpone}>{tr("Remind me in 7 days")}</Button>}
      </>}>
        <Choices prompt={prompt} onApp={() => setTfa(true)} onPasskey={() => setPasskeys(true)} />
        <div className="mt-3"><ErrorBox error={err} /></div>
      </Modal>
      {subs}
    </>
  );
}
