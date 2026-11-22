import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AppWindow, LogOut, ShieldCheck, XCircle } from "lucide-react";
import { api } from "../lib/api";
import type { Me } from "../lib/types";
import { srv, tr } from "../lib/i18n";
import { Button, ErrorBox, Spinner } from "../components/ui";
import { Login } from "./Auth";

// Sign-in of an app with the NAS (/sso/r/<id>) and sign-out asked by an app
// (/sso/logout/<id>). The server sends the browser here: this page's own
// requests carry the session cookie of the NAS.

interface RequestView {
  signedIn: boolean;
  client: string;
  clientId: string;
  host?: string;
  user?: string;
  scopes?: string[];
  consent?: boolean;
  reauth?: boolean;
  denied?: string;
  redirect?: string;
}

interface LogoutView {
  signedIn: boolean;
  client?: string;
  user?: string;
  sameUser?: boolean;
  redirect: boolean;
}

const scopeText: Record<string, string> = {
  profile: "Your name and user name",
  email: "Your email address",
  groups: "The groups you belong to",
  offline_access: "Stay signed in when you are away",
};

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-bg p-4">
      <div className="card w-full max-w-md p-6 text-[14px]">{children}</div>
    </div>
  );
}

function go(url: string) {
  window.location.replace(url);
}

export function SSOFlow({ hostname, me, onLogin }: { hostname: string; me: Me | null; onLogin: (m: Me) => void }) {
  const m = /^\/sso\/(r|logout)\/([0-9a-f]{32})\/?$/.exec(location.pathname);
  if (!m) return <Frame><p>{tr("This address is not valid.")}</p></Frame>;
  return m[1] === "r" ? <SignIn id={m[2]} hostname={hostname} me={me} onLogin={onLogin} /> : <SignOut id={m[2]} />;
}

function SignIn({ id, hostname, me, onLogin }: { id: string; hostname: string; me: Me | null; onLogin: (m: Me) => void }) {
  const qc = useQueryClient();
  const { data, error, refetch } = useQuery({ queryKey: ["sso-request", id, me?.username], queryFn: () => api.get<RequestView>(`/api/sso/requests/${id}`), retry: false });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const answer = async (approve: boolean) => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.post<{ redirect: string }>(`/api/sso/requests/${id}`, { approve });
      go(r.redirect);
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  };
  const auto = !!data && data.signedIn && !data.consent && !data.reauth && !data.denied && !data.redirect;
  useEffect(() => {
    if (data?.redirect) go(data.redirect);
    else if (auto) answer(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  if (error) return <Frame><ErrorBox error={error} /></Frame>;
  if (!data || data.redirect || auto) return <Frame><Spinner label={tr("Signing in…")} /></Frame>;
  if (!data.signedIn) {
    return (
      <div>
        <div className="bg-bg pt-6 text-center text-[13.5px] text-muted">{tr("Sign in to the NAS to continue to {0}", data.client)}</div>
        <Login hostname={hostname} onLogin={(u) => { onLogin(u); qc.invalidateQueries(); refetch(); }} />
      </div>
    );
  }
  const signOutFirst = async () => {
    await api.post("/api/logout").catch(() => {});
    window.location.reload();
  };
  if (data.denied) {
    return (
      <Frame>
        <div className="mb-3 flex items-center gap-2 text-danger"><XCircle size={20} /><h1 className="text-[17px] font-semibold">{tr("Access denied")}</h1></div>
        <p className="mb-5">{srv(data.denied)}</p>
        <div className="flex justify-end gap-2">
          <Button onClick={signOutFirst}>{tr("Use another account")}</Button>
          <Button variant="primary" loading={busy} onClick={() => answer(false)}>{tr("Back to {0}", data.client)}</Button>
        </div>
      </Frame>
    );
  }
  if (data.reauth) {
    return (
      <Frame>
        <h1 className="mb-2 text-[17px] font-semibold">{tr("Sign in again")}</h1>
        <p className="mb-5">{tr("{0} asks you to sign in again before continuing.", data.client)}</p>
        <div className="flex justify-end gap-2">
          <Button loading={busy} onClick={() => answer(false)}>{tr("Cancel")}</Button>
          <Button variant="primary" onClick={signOutFirst}>{tr("Sign in again")}</Button>
        </div>
      </Frame>
    );
  }
  return (
    <Frame>
      <div className="mb-4 flex items-center gap-3">
        <div className="flex size-11 items-center justify-center rounded-xl bg-accent/10 text-accent"><AppWindow size={22} /></div>
        <div>
          <h1 className="text-[17px] font-semibold">{tr("Sign in to {0}", data.client)}</h1>
          {data.host && <p className="mono text-[12px] text-muted">{data.host}</p>}
        </div>
      </div>
      <p className="mb-3">{tr("{0} will know you as {1} and receive:", data.client, data.user ?? "")}</p>
      <ul className="mb-4 space-y-1.5">
        {(data.scopes ?? []).filter((s) => scopeText[s]).map((s) => (
          <li key={s} className="flex items-center gap-2"><ShieldCheck size={15} className="text-ok" /> {tr(scopeText[s])}</li>
        ))}
      </ul>
      <p className="mb-5 text-[12.5px] text-muted">{tr("Your password is never given to the app. You can revoke its access at any time from your account menu (Apps signed in with the NAS).")}</p>
      <ErrorBox error={err} />
      <div className="flex justify-end gap-2">
        <Button disabled={busy} onClick={() => answer(false)}>{tr("Deny")}</Button>
        <Button variant="primary" loading={busy} onClick={() => answer(true)}>{tr("Allow")}</Button>
      </div>
    </Frame>
  );
}

function SignOut({ id }: { id: string }) {
  const { data, error } = useQuery({ queryKey: ["sso-logout", id], queryFn: () => api.get<LogoutView>(`/api/sso/logouts/${id}`), retry: false });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [done, setDone] = useState(false);
  const confirm = async () => {
    setBusy(true);
    try {
      const r = await api.post<{ redirect: string }>(`/api/sso/logouts/${id}`);
      if (r.redirect && r.redirect !== "/") go(r.redirect);
      else { setDone(true); setBusy(false); }
    } catch (e) {
      setErr(e);
      setBusy(false);
    }
  };
  // Nothing to confirm when nobody is signed in, or when the app named the
  // account signed in here.
  const auto = !!data && (!data.signedIn || data.sameUser);
  useEffect(() => {
    if (auto) confirm();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto]);
  if (error) return <Frame><ErrorBox error={error} /></Frame>;
  if (done) return <Frame><p>{tr("You are signed out of the NAS.")}</p><div className="mt-4 flex justify-end"><Button onClick={() => go("/")}>{tr("Sign in again")}</Button></div></Frame>;
  if (!data || auto) return <Frame><Spinner label={tr("Signing out…")} /></Frame>;
  return (
    <Frame>
      <div className="mb-3 flex items-center gap-2"><LogOut size={20} /><h1 className="text-[17px] font-semibold">{tr("Sign out of the NAS?")}</h1></div>
      <p className="mb-5">{data.client ? tr("{0} asks to sign {1} out of the NAS and its apps.", data.client, data.user ?? "") : tr("An app asks to sign {0} out of the NAS and its apps.", data.user ?? "")}</p>
      <ErrorBox error={err} />
      <div className="flex justify-end gap-2">
        <Button onClick={() => go("/")}>{tr("Stay signed in")}</Button>
        <Button variant="primary" loading={busy} onClick={confirm}>{tr("Sign out")}</Button>
      </div>
    </Frame>
  );
}
