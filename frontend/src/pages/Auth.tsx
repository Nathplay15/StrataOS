import { useState, type FormEvent, type ReactNode } from "react";
import { ArrowRight, Lock, ShieldCheck } from "lucide-react";
import logo from "../logo.svg";
import { api, ApiError } from "../lib/api";
import type { Me } from "../lib/types";
import { Button, Checkbox, ErrorBox, Field, Input } from "../components/ui";
import { useTheme } from "../components/Layout";

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
    <Shell subtitle={`Connexion à ${hostname}`}>
      <form onSubmit={submit} className="space-y-4">
        {!needCode ? (
          <>
            <Field label="Nom d'utilisateur">
              <Input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
            </Field>
            <Field label="Mot de passe">
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <Checkbox checked={remember} onChange={setRemember} label={<span className="text-[13px] text-muted">Rester connecté 30 jours</span>} />
          </>
        ) : (
          <>
            <div className="flex items-center gap-2 font-medium"><ShieldCheck size={17} className="text-accent" /> Double authentification</div>
            <Field label="Code de votre application d'authentification" hint="6 chiffres, ou un code de secours (xxxxx-xxxxx)">
              <Input autoFocus inputMode="numeric" autoComplete="one-time-code" className="mono text-center text-[18px] tracking-[0.3em]" value={code} onChange={(e) => setCode(e.target.value)} />
            </Field>
            <button type="button" className="text-muted text-[12.5px] hover:text-fg" onClick={() => { setNeedCode(false); setErr(null); }}>← Revenir</button>
          </>
        )}
        <ErrorBox error={err} />
        <Button variant="primary" className="w-full" loading={busy} icon={<Lock size={15} />} type="submit" disabled={needCode && code.trim().length < 6}>
          {needCode ? "Vérifier" : "Se connecter"}
        </Button>
      </form>
    </Shell>
  );
}

export function Setup({ hostname, onDone }: { hostname: string; onDone: (m: Me) => void }) {
  const [step, setStep] = useState(0);
  const [f, setF] = useState({ username: "", fullName: "", password: "", confirm: "", hostname, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "Europe/Paris" });
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (step === 0) return setStep(1);
    if (f.password !== f.confirm) return setErr(new Error("Les mots de passe ne correspondent pas"));
    setBusy(true);
    setErr(null);
    try {
      onDone(await api.post<Me>("/api/setup", { username: f.username.toLowerCase(), fullName: f.fullName, password: f.password, hostname: f.hostname, timezone: f.timezone }));
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Shell subtitle="Bienvenue ! Configurons votre NAS.">
      <form onSubmit={submit} className="space-y-4">
        <div className="mb-2 flex gap-2">
          {[0, 1].map((i) => (
            <div key={i} className={`h-1 flex-1 rounded-full ${i <= step ? "bg-accent" : "bg-line"}`} />
          ))}
        </div>
        {step === 0 ? (
          <>
            <h2 className="font-semibold">Votre serveur</h2>
            <Field label="Nom du NAS" hint="Visible sur le réseau (SMB, mDNS)">
              <Input value={f.hostname} onChange={set("hostname")} />
            </Field>
            <Field label="Fuseau horaire">
              <Input value={f.timezone} onChange={set("timezone")} />
            </Field>
          </>
        ) : (
          <>
            <h2 className="flex items-center gap-2 font-semibold"><ShieldCheck size={17} className="text-accent" /> Compte administrateur</h2>
            <Field label="Nom d'utilisateur" hint="Lettres minuscules, chiffres, - et _">
              <Input autoFocus value={f.username} onChange={set("username")} />
            </Field>
            <Field label="Nom complet">
              <Input value={f.fullName} onChange={set("fullName")} />
            </Field>
            <Field label="Mot de passe" hint="8 caractères minimum. Sert aussi pour SMB et SSH.">
              <Input type="password" autoComplete="new-password" value={f.password} onChange={set("password")} />
            </Field>
            <Field label="Confirmation">
              <Input type="password" autoComplete="new-password" value={f.confirm} onChange={set("confirm")} />
            </Field>
          </>
        )}
        <ErrorBox error={err} />
        <div className="flex gap-2">
          {step === 1 && <Button type="button" onClick={() => setStep(0)}>Retour</Button>}
          <Button variant="primary" className="flex-1" loading={busy} type="submit" icon={<ArrowRight size={15} />} disabled={step === 1 && (!f.username || f.password.length < 8)}>
            {step === 0 ? "Continuer" : "Terminer l'installation"}
          </Button>
        </div>
      </form>
    </Shell>
  );
}
