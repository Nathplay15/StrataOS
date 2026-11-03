import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { api } from "../lib/api";
import type { Me } from "../lib/types";
import { useAction } from "../lib/hooks";
import { dateTime } from "../lib/format";
import { openAccountDialog } from "../lib/intent";
import { tr } from "../lib/i18n";
import { Badge, Button, Card, ErrorBox, Field, Notice, Select, Spinner, Switch } from "./ui";

type Admin = { username: string; totp: boolean; passkeys: number; deadline?: string; postponedUntil?: string; enforced: boolean };
type Policy = { require: boolean; graceDays: number; since?: string; consoleRecovery: boolean; admins: Admin[] };

/** Settings card "Two-factor for administrators" (api/h_access.go). */
export function AdminTwoFactorPolicy() {
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me") }).data?.username;
  const { data, error } = useQuery({ queryKey: ["2fa-policy"], queryFn: () => api.get<Policy>("/api/security/2fa-policy") });
  const [f, setF] = useState<{ require: boolean; graceDays: number } | null>(null);
  useEffect(() => { if (data) setF({ require: data.require, graceDays: data.graceDays }); }, [data]);
  const save = useAction(() => api.put("/api/security/2fa-policy", f), { success: tr("Saved") });
  const mine = data?.admins.find((a) => a.username === me);
  const unprotected = !!mine && !mine.totp && !mine.passkeys;
  return (
    <Card id="settings-2fa" className="xl:col-span-2" title={<span className="flex items-center gap-2"><ShieldCheck size={16} /> {" "}{tr("Two-factor authentication for administrators")}</span>}
      actions={f && <Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
      {!data || !f ? <><ErrorBox error={error} /><Spinner /></> : (
        <div className="space-y-4">
          <p className="text-[13px] text-muted">{tr("Administrators without an authenticator app or a passkey are invited to set one up when they sign in; they may postpone it by 7 days.")}</p>
          <Switch checked={f.require} onChange={(v) => setF({ ...f, require: v })} label={tr("Require two-factor for administrators")}
            hint={tr("After the grace period, an administrator without a second factor can still sign in, but may only set one up. Nobody is locked out.")} />
          {f.require && (
            <Field label={tr("Grace period")} className="max-w-xs">
              <Select value={f.graceDays} onChange={(e) => setF({ ...f, graceDays: +e.target.value })}>
                <option value={-1}>{tr("None (at the next sign-in)")}</option>
                {[3, 0, 14, 30].map((n) => <option key={n} value={n}>{tr("{0} days", n || 7)}</option>)}
              </Select>
            </Field>
          )}
          {unprotected && (
            <Notice tone="warn">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span>{tr("Your own account has no second factor yet.")}</span>
                <span className="flex gap-2">
                  <Button size="sm" onClick={() => openAccountDialog("2fa")}>{tr("Authenticator app")}</Button>
                  <Button size="sm" onClick={() => openAccountDialog("passkeys")}>{tr("Passkey")}</Button>
                </span>
              </div>
            </Notice>
          )}
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Administrator")}</th><th>{tr("Second factor")}</th><th /></tr></thead>
              <tbody>
                {data.admins.map((a) => (
                  <tr key={a.username}>
                    <td className="font-medium">{a.username}</td>
                    <td>
                      <div className="flex flex-wrap gap-1">
                        {a.totp && <Badge tone="ok">{tr("Authenticator app")}</Badge>}
                        {a.passkeys > 0 && <Badge tone="ok">{tr("{0} passkey(s)", a.passkeys)}</Badge>}
                        {!a.totp && !a.passkeys && <Badge tone={a.enforced ? "danger" : "warn"}>{tr("None")}</Badge>}
                      </div>
                    </td>
                    <td className="text-muted text-[12.5px]">
                      {a.enforced ? tr("Must set it up at the next sign-in") : a.deadline ? tr("Required from {0}", dateTime(a.deadline)) : a.postponedUntil ? tr("Postponed until {0}", dateTime(a.postponedUntil)) : ""}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {!data.consoleRecovery && <Notice tone="warn">{tr("Recovery codes from the NAS console are disabled: an administrator who loses their phone or passkey can only be helped by another administrator.")}</Notice>}
          <p className="text-[12px] text-muted">{tr("Lost phone or passkey? Another administrator can reset it in Users. Alone, generate a recovery code on the NAS screen (console), then use \"Forgot password?\" on the sign-in page: it resets the password and removes the second factors.")}</p>
          <ErrorBox error={save.error} />
        </div>
      )}
    </Card>
  );
}
