import { useState } from "react";
import { ShieldAlert, ShieldCheck, ShieldQuestion } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { dateTime } from "../lib/format";
import { Badge, Button, ErrorBox, Notice, Switch } from "./ui";
import { tr, srv } from "../lib/i18n";

/** How StrataOS checks the certificate of a server the administrator configured (store.TLSTrust). */
export type TLSTrust = { tlsCert?: string; tlsFingerprint?: string; tlsInsecure?: boolean };

type CertInfo = { subject: string; issuer: string; notBefore: string; notAfter: string; sha256: string; names?: string[]; selfSigned?: boolean; ca?: boolean; expired?: boolean };
type Inspection = { target: { host: string; port: string }; chain: CertInfo[]; pem: string; fingerprint: string; trusted: boolean; problem?: string; nameMismatch?: string };

/** AB:CD:… for a hex fingerprint. */
export const colons = (fp: string) => fp.toUpperCase().match(/.{2}/g)?.join(":") ?? "";

/** True when an address uses TLS (there is a certificate to check). */
export const usesTLS = (address: string) => /^(https|ldaps|ldap|smtps|smtp):\/\//i.test(address.trim());

/** A failed run whose message says the trusted certificate changed (tlstrust.ChangedError). */
export const certChanged = (message?: string) => !!message && /certificate of .+ changed/.test(message);

/**
 * "Server certificate" block of a connection form: checks the certificate the
 * server presents (POST /api/tls/inspect), lets the administrator trust it
 * for this connection only, or — when allowInsecure — ignore certificate
 * errors altogether.
 */
export function TLSTrustField({ address, value, onChange, allowInsecure = true, note }: { address: string; value: TLSTrust; onChange: (v: TLSTrust) => void; allowInsecure?: boolean; note?: string }) {
  const [seen, setSeen] = useState<Inspection | null>(null);
  const inspect = useAction(() => api.post<Inspection>("/api/tls/inspect", { url: address.trim() }), { onDone: (r) => setSeen(r) });
  const tls = usesTLS(address);
  if (!tls && !value.tlsCert && !value.tlsInsecure) return null;
  const changed = seen && value.tlsFingerprint && seen.fingerprint !== value.tlsFingerprint && !seen.trusted;
  const trust = () => { if (seen) { onChange({ tlsCert: seen.pem, tlsFingerprint: seen.fingerprint, tlsInsecure: false }); setSeen(null); } };

  return (
    <div className="space-y-3 rounded-xl border border-line p-3">
      <div className="flex flex-wrap items-center gap-2">
        {value.tlsInsecure ? <ShieldAlert size={16} className="text-warn" /> : value.tlsCert ? <ShieldCheck size={16} className="text-ok" /> : <ShieldQuestion size={16} className="text-muted" />}
        <span className="text-[13px] font-medium">{tr("Server certificate")}</span>
        {value.tlsInsecure ? <Badge tone="warn">{tr("Not checked")}</Badge> : value.tlsCert ? <Badge tone="ok">{tr("Trusted certificate")}</Badge> : <Badge>{tr("Recognised authorities")}</Badge>}
        <div className="flex-1" />
        <Button size="sm" loading={inspect.isPending} disabled={!tls} onClick={() => inspect.mutate()}>{tr("Check the certificate")}</Button>
      </div>
      {value.tlsCert && !value.tlsInsecure && value.tlsFingerprint && (
        <div className="flex flex-wrap items-center gap-2 text-[12px]">
          <span className="text-muted">SHA-256</span>
          <code className="mono break-all">{colons(value.tlsFingerprint ?? "")}</code>
          <Button size="sm" variant="ghost" onClick={() => onChange({ tlsCert: "", tlsFingerprint: "", tlsInsecure: false })}>{tr("Stop trusting")}</Button>
        </div>
      )}
      {!value.tlsCert && !value.tlsInsecure && <p className="text-muted text-[12px]">{note ?? tr("A server with a self-signed certificate (another NAS, a Nextcloud at home…) is refused: check its certificate, then trust it for this connection only.")}</p>}
      <ErrorBox error={inspect.error} />
      {seen && (
        <div className="space-y-2">
          {changed && <Notice tone="warn">{tr("The server presents a different certificate from the one trusted. Trust the new one only if you know why it changed (renewal, reinstallation).")}</Notice>}
          {seen.trusted ? (
            <Notice>{tr("This certificate is signed by a recognised authority: nothing to do.")}</Notice>
          ) : seen.problem ? (
            <Notice tone="warn">{tr("Not trusted by default: {0}.", srv(seen.problem))}</Notice>
          ) : null}
          {seen.chain.map((c, i) => (
            <div key={c.sha256} className="space-y-0.5 rounded-lg bg-panel-2 px-3 py-2 text-[12px]">
              <div className="font-medium">{i === 0 ? tr("Server certificate") : c.ca ? tr("Certificate authority") : tr("Intermediate certificate")}{c.selfSigned ? ` · ${tr("self-signed")}` : ""}</div>
              <div><span className="text-muted">{tr("Subject")}: </span><span className="break-all">{c.subject || "—"}</span></div>
              <div><span className="text-muted">{tr("Issuer")}: </span><span className="break-all">{c.issuer || "—"}</span></div>
              {c.names?.length ? <div><span className="text-muted">{tr("Names")}: </span><span className="break-all">{c.names.join(", ")}</span></div> : null}
              <div className={c.expired ? "text-danger" : ""}><span className="text-muted">{tr("Valid")}: </span>{tr("{0} to {1}", dateTime(c.notBefore), dateTime(c.notAfter))}{c.expired ? ` · ${tr("expired or not yet valid")}` : ""}</div>
              <div><span className="text-muted">SHA-256: </span><code className="mono break-all">{colons(c.sha256)}</code></div>
            </div>
          ))}
          {seen.nameMismatch && <Notice tone="warn">{srv(seen.nameMismatch)}</Notice>}
          {!seen.trusted && (
            <>
              <p className="text-muted text-[12px]">{tr("Compare the SHA-256 fingerprint with the one shown by the server (its administration page, or openssl x509 -fingerprint -sha256) before trusting it.")}</p>
              <div className="flex justify-end gap-2">
                <Button size="sm" onClick={() => setSeen(null)}>{tr("Cancel")}</Button>
                <Button size="sm" variant="primary" icon={<ShieldCheck size={14} />} onClick={trust}>{tr("Trust this certificate")}</Button>
              </div>
            </>
          )}
        </div>
      )}
      {allowInsecure && (
        <>
          <Switch checked={!!value.tlsInsecure} onChange={(v) => onChange({ ...value, tlsInsecure: v })} label={tr("Ignore certificate errors")} hint={tr("Last resort, off by default: any certificate is accepted.")} />
          {value.tlsInsecure && <Notice tone="warn">{tr("The connection is still encrypted, but the NAS no longer checks who it talks to: someone on the network could pose as the server and read the passwords and data sent. Prefer “Trust this certificate”.")}</Notice>}
        </>
      )}
    </div>
  );
}

/** Values of TLSTrust taken from an object holding them flat (backup task, cloud sync, datastore…). */
export const trustOf = (o: TLSTrust): TLSTrust => ({ tlsCert: o.tlsCert, tlsFingerprint: o.tlsFingerprint, tlsInsecure: o.tlsInsecure });
