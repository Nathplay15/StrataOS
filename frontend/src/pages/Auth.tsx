import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { FingerprintPattern, Lock, ShieldCheck } from "lucide-react";
import logo from "../logo.svg";
import { api, ApiError } from "../lib/api";
import type { Me } from "../lib/types";
import { Button, Checkbox, ErrorBox, Field, Input } from "../components/ui";
import { useTheme } from "../components/Layout";
import { srv, tr } from "../lib/i18n";
import { LangSwitch } from "../components/LangSwitch";
import { cancelled, getPasskey, passkeysSupported } from "../lib/webauthn";

function Shell({ children, subtitle }: { children: ReactNode; subtitle: string }) {
  useTheme();
  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden px-4 py-10">
      <div className="pointer-events-none absolute -top-40 left-1/2 h-[520px] w-[820px] -translate-x-1/2 rounded-full bg-gradient-to-r from-accent/25 to-accent-2/20 blur-3xl" />
      <div className="relative w-full max-w-[420px]">
        <div className="mb-8 flex flex-col items-center text-center">
          <img src={logo} alt="" className="mb-4 h-14 w-14" />
          <h1 className="text-[26px] font-semibold tracking-tight">Strata<span className="text-accent">OS</span></h1>
          <p className="text-muted">{subtitle}</p>
        </div>
        <div className="card p-7">{children}</div>
        <div className="mt-4 flex justify-center"><LangSwitch className="flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12.5px] text-muted hover:bg-panel-2 hover:text-fg" /></div>
      </div>
    </div>
  );
}

export function Login({ hostname, onLogin }: { hostname: string; onLogin: (m: Me) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [remember, setRemember] = useState(false);
  const [code, setCode] = useState("");
  const [needCode, setNeedCode] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  // Passkeys: only offered when the browser has WebAuthn and the page is open on a name.
  const [pk, setPk] = useState<{ available: boolean; reason?: string } | null>(null);
  const [pkBusy, setPkBusy] = useState(false);
  useEffect(() => {
    if (passkeysSupported()) api.get<{ available: boolean; reason?: string }>("/api/login/passkey").then(setPk, () => setPk(null));
  }, []);
  const passkey = async () => {
    setPkBusy(true);
    setErr(null);
    try {
      const b = await api.post<{ flow: string; publicKey: unknown }>("/api/login/passkey/begin", { username });
      const credential = await getPasskey(b.publicKey);
      onLogin(await api.post<Me>("/api/login/passkey/finish", { flow: b.flow, remember, credential }));
    } catch (e) {
      if (!cancelled(e)) setErr(e);
    } finally {
      setPkBusy(false);
    }
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      onLogin(await api.post<Me>("/api/login", { username, password, remember, code: needCode ? code : "" }));
    } catch (e) {
      // Right password, second factor required: ask for the code without showing an error.
      if (e instanceof ApiError && e.data.twoFactor) {
        if (needCode) setErr(e);
        setNeedCode(true);
        setCode("");
      } else setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Shell subtitle={tr("Connecting to {0}", hostname)}>
      <form onSubmit={submit} className="space-y-4">
        {!needCode ? (
          <>
            <Field label={tr("Username")}>
              <Input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
            </Field>
            <Field label={tr("Password")}>
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <Checkbox checked={remember} onChange={setRemember} label={<span className="text-[13px] text-muted">{tr("Stay signed in for 30 days")}</span>} />
          </>
        ) : (
          <>
            <div className="flex items-center gap-2 font-medium"><ShieldCheck size={17} className="text-accent" /> {" "}{tr("Two-factor authentication")}</div>
            <Field label={tr("Code from your authenticator app")} hint={tr("6 digits, or a recovery code (xxxxx-xxxxx)")}>
              <Input autoFocus inputMode="numeric" autoComplete="one-time-code" className="mono text-center text-[18px] tracking-[0.3em]" value={code} onChange={(e) => setCode(e.target.value)} />
            </Field>
            <button type="button" className="text-muted text-[12.5px] hover:text-fg" onClick={() => { setNeedCode(false); setErr(null); }}>{tr("← Back")}</button>
          </>
        )}
        <ErrorBox error={err} />
        <Button variant="primary" className="w-full" loading={busy} icon={<Lock size={15} />} type="submit" disabled={needCode && code.trim().length < 6}>
          {needCode ? tr("Check") : tr("Sign in")}
        </Button>
        {!needCode && pk?.available && (
          <Button type="button" className="w-full" loading={pkBusy} icon={<FingerprintPattern size={15} />} onClick={passkey}>{tr("Sign in with a passkey")}</Button>
        )}
        {!needCode && pk && !pk.available && <p className="text-center text-muted text-[12px]">{tr("Passkeys unavailable here:")}{" "}{srv(pk.reason)}</p>}
      </form>
    </Shell>
  );
}
