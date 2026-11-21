import { useQuery, useQueryClient } from "@tanstack/react-query";
import { KeySquare, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { dateTime } from "../lib/format";
import { srv, tr } from "../lib/i18n";
import { Button, Empty, ErrorBox, Modal, Spinner, useUI } from "./ui";

interface MyApp {
  client: string;
  name: string;
  kind?: string;
  scopes: string[];
  created: string;
  lastUsed?: string;
  sessions: number;
}

/** The apps a user signed in to with the NAS (single sign-on), with revoke. */
export function MyAppsModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { confirm, toast } = useUI();
  const { data, error } = useQuery({ queryKey: ["my-sso"], queryFn: () => api.get<MyApp[]>("/api/me/sso"), enabled: open });
  const revoke = async (a: MyApp) => {
    if (!(await confirm({ title: tr("Revoke the access of {0}?", a.name), text: tr("You are signed out of it at once, and it asks for your permission again next time."), danger: true }))) return;
    try {
      await api.del(`/api/me/sso/${encodeURIComponent(a.client)}`);
      toast(tr("Access revoked"));
    } catch (e) {
      toast(srv((e as Error).message), "error");
    }
    qc.invalidateQueries({ queryKey: ["my-sso"] });
  };
  return (
    <Modal open={open} onClose={onClose} title={tr("Apps signed in with the NAS")} wide>
      <p className="mb-4 text-[13.5px]">{tr("These apps let you in with your NAS account (single sign-on). They never see your password.")}</p>
      <ErrorBox error={error} />
      {!data ? <Spinner /> : data.length === 0 ? <Empty icon={<KeySquare size={22} />} title={tr("No app yet")} /> : (
        <div className="divide-y divide-border rounded-xl border border-border">
          {data.map((a) => (
            <div key={a.client} className="flex items-center gap-3 px-4 py-3">
              <KeySquare size={18} className="shrink-0 text-accent" />
              <div className="min-w-0 flex-1">
                <div className="font-medium">{a.name}</div>
                <div className="text-[12px] text-muted">
                  {tr("Allowed on {0}", dateTime(a.created))}
                  {a.lastUsed ? " · " + tr("last sign-in {0}", dateTime(a.lastUsed)) : ""}
                  {" · "}{a.sessions === 1 ? tr("1 open session") : tr("{0} open sessions", a.sessions)}
                </div>
              </div>
              <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={() => revoke(a)}>{tr("Revoke")}</Button>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
