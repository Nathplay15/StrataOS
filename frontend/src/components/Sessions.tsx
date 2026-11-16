import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, History, Laptop, LogOut, Smartphone, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { ago, dateTime } from "../lib/format";
import { tr } from "../lib/i18n";
import { Badge, Button, Empty, ErrorBox, Modal, Spinner, useUI } from "./ui";

interface SessionInfo {
  id: string;
  user: string;
  role?: string;
  created: string;
  expires: string;
  ip: string;
  device?: string;
  mfa?: string;
  current: boolean;
}

interface LoginEntry {
  time: string;
  user: string;
  ip: string;
  ok: boolean;
  how: string;
}

const howLabel: Record<string, string> = {
  password: tr("Password"),
  twofactor: tr("Two-factor code"),
  key: tr("Security key"),
  passkey: tr("Passkey"),
  oidc: tr("Sign-in provider"),
};

/** The web sessions of the account (every account for an administrator) and the sign-in history. */
export function SessionsModal({ open, onClose, me }: { open: boolean; onClose: () => void; me: { username: string; role: string } }) {
  const qc = useQueryClient();
  const { confirm, toast } = useUI();
  const admin = me.role === "admin";
  const sessions = useQuery({ queryKey: ["sessions"], queryFn: () => api.get<SessionInfo[]>("/api/sessions"), enabled: open, refetchInterval: open ? 10000 : false });
  const history = useQuery({ queryKey: ["logins", admin], queryFn: () => api.get<LoginEntry[]>(admin ? "/api/logins" : "/api/me/logins"), enabled: open });
  const revoke = useMutation({
    mutationFn: (id: string) => api.del(`/api/sessions/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
  const revokeOthers = useMutation({
    mutationFn: () => api.del<{ ended: number }>("/api/sessions/others"),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["sessions"] });
      toast(tr("{0} session(s) signed out", r.ended));
    },
  });

  const end = async (x: SessionInfo) => {
    const who = x.user === me.username ? tr("this session") : x.user;
    if (!(await confirm({ title: tr("End {0}?", who), text: tr("The person will have to sign in again."), danger: true, confirm: tr("End session") }))) return;
    revoke.mutate(x.id);
  };
  const endOthers = async () => {
    if (!(await confirm({ title: tr("Sign out everywhere else?"), text: tr("Every session of your account except this one is ended."), danger: true, confirm: tr("Sign out") }))) return;
    revokeOthers.mutate();
  };

  const list = sessions.data ?? [];
  const severalUsers = admin && list.some((x) => x.user !== me.username);

  return (
    <Modal open={open} onClose={onClose} title={tr("Sessions and sign-ins")} wide>
      <div className="space-y-6">
        <section>
          <div className="mb-2 flex items-center justify-between">
            <h3 className="text-[13px] font-semibold">{tr("Active sessions")}</h3>
            <Button size="sm" icon={<LogOut size={14} />} loading={revokeOthers.isPending} onClick={endOthers}>{tr("Sign out everywhere else")}</Button>
          </div>
          {sessions.isLoading ? <Spinner /> : list.length === 0 ? <Empty icon={<Laptop />} title={tr("No session")} /> : (
            <div className="divide-y divide-border rounded-xl border border-border">
              {list.map((x) => (
                <div key={x.id} className="flex items-center gap-3 px-4 py-3">
                  {x.device ? <Smartphone size={17} className="shrink-0 text-accent" /> : <Laptop size={17} className="shrink-0 text-accent" />}
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5 text-[13px] font-medium">
                      {severalUsers && <span>{x.user}</span>}
                      {x.current && <Badge tone="ok">{tr("This device")}</Badge>}
                      {x.device && <Badge tone="accent">{tr("From the mobile app")}</Badge>}
                      {x.mfa && <Badge tone="accent">{tr("Second factor checked")}</Badge>}
                    </div>
                    <div className="text-muted text-[12px]">
                      <span className="mono">{x.ip}</span> · {tr("opened {0}", ago(x.created))} · {tr("expires {0}", dateTime(x.expires))}
                    </div>
                  </div>
                  {!x.current && <Button size="sm" variant="ghost" loading={revoke.isPending && revoke.variables === x.id} onClick={() => end(x)}>{tr("End")}</Button>}
                </div>
              ))}
            </div>
          )}
          <ErrorBox error={revoke.error ?? revokeOthers.error} />
        </section>

        <section>
          <h3 className="mb-2 text-[13px] font-semibold">{tr("Sign-in history")}</h3>
          {history.isLoading ? <Spinner /> : !history.data?.length ? <Empty icon={<History />} title={tr("No sign-in recorded yet")} /> : (
            <div className="max-h-[40vh] overflow-y-auto rounded-xl border border-border">
              <table className="table">
                <thead><tr><th>{tr("When")}</th>{admin && <th>{tr("User")}</th>}<th>{tr("Address")}</th><th>{tr("Method")}</th><th>{tr("Result")}</th></tr></thead>
                <tbody>
                  {history.data.map((l, i) => (
                    <tr key={i}>
                      <td className="whitespace-nowrap text-[12.5px]">{dateTime(l.time)} <span className="text-muted text-[11px]">({ago(l.time)})</span></td>
                      {admin && <td className="text-[12.5px]">{l.user}</td>}
                      <td className="mono text-[12px]">{l.ip}</td>
                      <td className="text-[12.5px]">{howLabel[l.how] ?? l.how}</td>
                      <td>{l.ok ? <Badge tone="ok"><CheckCircle2 size={12} /> {tr("Signed in")}</Badge> : <Badge tone="danger"><XCircle size={12} /> {tr("Refused")}</Badge>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <p className="mt-2 text-[12px] text-muted">{tr("The last sign-in attempts, successful or not, seen by the NAS.")}</p>
        </section>
      </div>
    </Modal>
  );
}
