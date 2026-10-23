import { useState, type FormEvent } from "react";
import { KeyRound, LifeBuoy } from "lucide-react";
import { api } from "../lib/api";
import { Button, Checkbox, ErrorBox, Field, Input, Notice, Select } from "../components/ui";
import { tr } from "../lib/i18n";

interface Admin { username: string; fullName: string; twoFactor: boolean }
interface Check { admins: Admin[]; canCreate: boolean }

/**
 * Sign-in page, "Forgot password?": a one-time code shown on the NAS screen
 * (menu 3 of the console) resets an administrator password, or creates an
 * administrator when none is left.
 */
export function RecoveryForm({ onBack }: { onBack: () => void }) {
  const [code, setCode] = useState("");
  const [check, setCheck] = useState<Check | null>(null);
  const [username, setUsername] = useState("");
  const [fullName, setFullName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [removeTwoFactor, setRemoveTwoFactor] = useState(false);
  const [done, setDone] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try {
      await fn();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  const submitCode = (e: FormEvent) => {
    e.preventDefault();
    run(async () => {
      const c = await api.post<Check>("/api/login/recovery/check", { code });
      setCheck(c);
      setUsername(c.admins[0]?.username ?? "");
    });
  };
  const submitReset = (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      setErr(new Error(tr("The passwords do not match")));
      return;
    }
    run(async () => {
      await api.post("/api/login/recovery/reset", { code, username, password, fullName, create: check?.canCreate ?? false, removeTwoFactor });
      setDone(check?.canCreate ? tr("Administrator {0} created: sign in with it.", username) : tr("Password of {0} changed: sign in with it.", username));
    });
  };
  const admin = check?.admins.find((a) => a.username === username);

  if (done) {
    return (
      <div className="space-y-4">
        <Notice>{done}</Notice>
        <Button variant="primary" className="w-full" onClick={onBack}>{tr("Sign in")}</Button>
      </div>
    );
  }
  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 font-medium"><LifeBuoy size={17} className="text-accent" /> {tr("Administrator recovery")}</div>
      {!check ? (
        <form onSubmit={submitCode} className="space-y-4">
          <p className="text-muted text-[13px]">{tr("On a screen plugged into the NAS, choose \"Administrator recovery code\" in the StrataOS menu: it shows a one-time code, valid 15 minutes.")}</p>
          <Field label={tr("Recovery code")}>
            <Input autoFocus autoComplete="one-time-code" className="mono text-center text-[18px] tracking-[0.2em]" placeholder="XXXX-XXXX-XXXX" value={code} onChange={(e) => setCode(e.target.value)} />
          </Field>
          <ErrorBox error={err} />
          <Button variant="primary" className="w-full" loading={busy} type="submit" disabled={code.replace(/[\s-]/g, "").length < 12}>{tr("Check")}</Button>
        </form>
      ) : (
        <form onSubmit={submitReset} className="space-y-4">
          {check.canCreate ? (
            <>
              <Notice>{tr("No administrator account can be used: create a new one.")}</Notice>
              <Field label={tr("Username")}><Input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} /></Field>
              <Field label={tr("Full name")}><Input value={fullName} onChange={(e) => setFullName(e.target.value)} /></Field>
            </>
          ) : (
            <Field label={tr("Administrator")}>
              <Select value={username} onChange={(e) => setUsername(e.target.value)}>
                {check.admins.map((a) => <option key={a.username} value={a.username}>{a.fullName ? `${a.fullName} (${a.username})` : a.username}</option>)}
              </Select>
            </Field>
          )}
          <Field label={tr("New password")}><Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} /></Field>
          <Field label={tr("Confirm the password")}><Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} /></Field>
          {admin?.twoFactor && <Checkbox checked={removeTwoFactor} onChange={setRemoveTwoFactor} label={<span className="text-[13px] text-muted">{tr("Also remove two-factor authentication (lost phone)")}</span>} />}
          <p className="text-muted text-[12px]">{tr("The code is used up by this change. Administrators are notified.")}</p>
          <ErrorBox error={err} />
          <Button variant="primary" className="w-full" loading={busy} icon={<KeyRound size={15} />} type="submit" disabled={!username || !password}>
            {check.canCreate ? tr("Create the administrator") : tr("Change the password")}
          </Button>
        </form>
      )}
      <button type="button" className="text-muted text-[12.5px] hover:text-fg" onClick={onBack}>{tr("← Back")}</button>
    </div>
  );
}
