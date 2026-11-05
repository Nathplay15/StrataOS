// Pairing this computer with a NAS: a pairing link (copied, or opened from
// the NAS as strataos://pair?…), or its address with a one-time code or a
// user name and password. The certificate of a NAS without a public
// certificate is shown first and pinned once accepted (same rules as the
// Android app); a link carries the fingerprint to compare.
import { useEffect, useState } from "react";
import { CheckCircle2, Radar, ShieldAlert, X } from "lucide-react";
import { Button, ErrorBox, Field, Input, Notice, Tabs } from "@nas/components/ui";
import { ApiError, agent } from "../lib/agent";
import { useApp } from "../lib/state";
import { t, tk } from "../lib/i18n";
import logo from "@nas/logo.svg";

type Probe = { fingerprint: string; trusted: boolean; expected: string; url: string };
type Found = { name: string; host: string; port: number; url?: string; addrs?: string[] };

export function PairingPage({ link: initialLink, onDone, onCancel }: { link: string; onDone: () => void; onCancel?: () => void }) {
  const { refresh, setAccountId } = useApp();
  const [mode, setMode] = useState<"link" | "code" | "password">(initialLink ? "link" : "link");
  const [link, setLink] = useState(initialLink);
  const [url, setUrl] = useState("");
  const [code, setCode] = useState("");
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  const [otp, setOtp] = useState("");
  const [needOtp, setNeedOtp] = useState(false);
  const [probe, setProbe] = useState<Probe | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [found, setFound] = useState<Found[] | null>(null);
  const [searching, setSearching] = useState(false);

  useEffect(() => { if (initialLink) { setLink(initialLink); setMode("link"); } }, [initialLink]);
  // Another address, link or way: the certificate is checked again.
  useEffect(() => { setProbe(null); setAccepted(false); }, [url, link, mode]);

  const mismatch = probe && probe.expected && probe.expected !== probe.fingerprint && !probe.trusted;

  async function submit() {
    setError(null);
    setBusy(true);
    try {
      let p = probe;
      if (!p) {
        p = await agent.post<Probe>("/v1/probe", mode === "link" ? { link } : { url });
        setProbe(p);
        // A public certificate, or the one the link expects: no question.
        if (!p.trusted && !(p.expected && p.expected === p.fingerprint)) return;
      }
      if (p.expected && p.expected !== p.fingerprint && !p.trusted) return;
      if (!p.trusted && !p.expected && !accepted) {
        setError(new Error(tk("pair.checkcert")));
        return;
      }
      const body = mode === "link" ? { link }
        : mode === "code" ? { url: p.url, code: code.trim(), fingerprint: p.trusted ? "" : p.fingerprint }
          : { url: p.url, username: user.trim(), password, otp: otp.trim(), fingerprint: p.trusted ? "" : p.fingerprint };
      const r = await agent.post<{ id: string }>("/v1/accounts", body);
      setAccountId(r.id);
      refresh();
      onDone();
    } catch (e) {
      if (e instanceof ApiError && e.data.twoFactor) setNeedOtp(true);
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  async function discover() {
    setSearching(true);
    try {
      const l = await agent.post<Found[]>("/v1/discover", {});
      setFound(l);
    } catch (e) {
      setError(e);
    } finally {
      setSearching(false);
    }
  }

  return (
    <div className="flex h-full items-start justify-center overflow-y-auto bg-bg px-6 py-10">
      <div className="card w-full max-w-[560px] p-7">
        <div className="mb-5 flex items-center gap-3">
          <img src={logo} alt="" className="h-11 w-11" />
          <div className="flex-1">
            <h1 className="text-[18px] font-semibold">{onCancel ? t("Add a NAS") : t("Welcome to StrataOS")}</h1>
            <p className="text-[12.5px] text-muted">{tk("pair.title")}</p>
          </div>
          {onCancel && <Button variant="ghost" size="sm" icon={<X size={15} />} onClick={onCancel}>{t("Cancel")}</Button>}
        </div>
        <p className="mb-4 text-[13px] text-muted">{tk("pair.intro")}</p>
        <Tabs value={mode} onChange={setMode} tabs={[
          { id: "link", label: tk("pair.link") },
          { id: "code", label: tk("pair.withcode") },
          { id: "password", label: tk("pair.withpassword") },
        ]} />
        <form className="mt-4 space-y-3.5" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
          {mode === "link" ? (
            <Field label={tk("pair.link")}><Input autoFocus value={link} onChange={(e) => setLink(e.target.value)} placeholder="strataos://pair?url=…" className="mono" /></Field>
          ) : (
            <>
              <Field label={tk("pair.address")}>
                <div className="flex gap-2">
                  <Input autoFocus value={url} onChange={(e) => setUrl(e.target.value)} placeholder="nas.local:7443" />
                  <Button type="button" loading={searching} icon={<Radar size={15} />} onClick={() => void discover()}>{tk("pair.discover")}</Button>
                </div>
              </Field>
              {found && (found.length === 0 ? <Notice>{tk("pair.nonefound")}</Notice> : (
                <div className="flex flex-wrap gap-2">
                  {found.map((f) => {
                    const u = f.url || `${f.host}:${f.port}`;
                    return <Button key={u} type="button" size="sm" variant={url === u ? "primary" : "secondary"} onClick={() => setUrl(u)}>{f.name || f.host}</Button>;
                  })}
                </div>
              ))}
              {mode === "code" ? (
                <Field label={tk("pair.code")}><Input value={code} onChange={(e) => setCode(e.target.value)} className="mono" autoComplete="off" /></Field>
              ) : (
                <>
                  <div className="grid grid-cols-2 gap-3">
                    <Field label={tk("pair.username")}><Input value={user} onChange={(e) => setUser(e.target.value)} autoComplete="username" /></Field>
                    <Field label={tk("pair.password")}><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" /></Field>
                  </div>
                  {needOtp && <Field label={tk("pair.otp")}><Input autoFocus value={otp} onChange={(e) => setOtp(e.target.value)} inputMode="numeric" autoComplete="one-time-code" /></Field>}
                </>
              )}
            </>
          )}

          {probe && !probe.trusted && (
            mismatch ? (
              <div className="flex gap-2.5 rounded-xl border border-danger/40 bg-danger/10 p-3 text-[13px]"><ShieldAlert size={18} className="shrink-0 text-danger" />{tk("pair.certmismatch", probe.fingerprint)}</div>
            ) : probe.expected ? (
              <div className="flex items-center gap-2 text-[13px] text-ok"><CheckCircle2 size={16} />{tk("pair.certok")}</div>
            ) : (
              <div className="space-y-2 rounded-xl border border-warn/40 bg-warn/10 p-3 text-[13px]">
                <p>{tk("pair.cert")}</p>
                <p className="mono selectable break-all text-[12px]">{probe.fingerprint}</p>
                <label className="flex items-center gap-2"><input type="checkbox" checked={accepted} onChange={(e) => setAccepted(e.target.checked)} />{t("This is the fingerprint shown by the NAS")}</label>
              </div>
            )
          )}

          <ErrorBox error={error} />
          <div className="flex justify-end">
            <Button type="submit" variant="primary" loading={busy} disabled={!!mismatch || (mode === "link" ? !link.trim() : !url.trim())}>{tk("pair.button")}</Button>
          </div>
        </form>
      </div>
    </div>
  );
}
