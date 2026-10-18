import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bug, CalendarClock, Plug, RotateCcw, ShieldAlert, ShieldCheck, Trash2, Unlock } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, dateTime } from "../../lib/format";
import { AppLink } from "../../components/AppLink";
import { Button, Card, Empty, ErrorBox, Field, IconButton, Input, Notice, PageHeader, Spinner, Switch, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Item = { id: string; path: string; virus: string; size: number; date: string };
type Data = { status: { installed: boolean; version: string }; quarantine: Item[] };

export default function Antivirus() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["antivirus"], queryFn: () => api.get<Data>("/api/antivirus"), refetchInterval: 15000 });
  const install = useAction(() => api.post("/api/system/components/antivirus/install"));
  const restore = useAction((id: string) => api.post<{ path: string }>(`/api/antivirus/quarantine/${id}/restore`), { success: (r) => tr("File restored: {0}", r.path) });
  const del = useAction((id: string) => api.del(`/api/antivirus/quarantine/${id}`), { success: tr("File deleted permanently") });
  const header = <PageHeader title={tr("Antivirus")} subtitle={tr("Scan shared folders with ClamAV")} icon={<ShieldCheck size={20} />} />;
  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.status.installed) {
    return <div>{header}<Card><Empty icon={<Plug />} title={tr("ClamAV is not installed")} text={tr("Virus signatures are then updated automatically several times a day.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card></div>;
  }
  const [version, db, dbDate] = data.status.version.split("/");
  return (
    <div>
      {header}
      <div className="space-y-5">
        <Card title={tr("Engine")}>
          <div className="grid gap-4 text-[13px] sm:grid-cols-3">
            <div><div className="text-muted text-[12px]">{tr("Version")}</div><div>{version}</div></div>
            <div><div className="text-muted text-[12px]">{tr("Signature database")}</div><div>{db ?? "—"}</div></div>
            <div><div className="text-muted text-[12px]">{tr("Updated")}</div><div>{dbDate ?? "—"}</div></div>
          </div>
          <div className="mt-4"><Notice><span className="flex flex-wrap items-center gap-2">{tr("Scans are scheduled in Scheduled tasks (type \"Antivirus scan\"). They run at low priority so the NAS stays responsive.")} <AppLink to="/system/schedules"><Button size="sm" icon={<CalendarClock size={14} />}>{tr("Scheduled tasks")}</Button></AppLink></span></Notice></div>
        </Card>
        <RansomwareCard />
        <Card title={tr("Quarantine")} pad={false}>
          {data.quarantine.length === 0 ? <Empty icon={<Bug />} title={tr("No file in quarantine")} /> : (
            <table className="w-full text-[13px]">
              <thead><tr className="text-left text-muted text-[12px]"><th className="px-5 py-2">{tr("File")}</th><th>{tr("Threat")}</th><th>{tr("Size")}</th><th>{tr("Date")}</th><th /></tr></thead>
              <tbody>
                {data.quarantine.map((it) => (
                  <tr key={it.id} className="border-t border-line">
                    <td className="mono max-w-[340px] truncate px-5 py-2.5" title={it.path}>{it.path}</td>
                    <td className="text-danger">{it.virus}</td>
                    <td>{bytes(it.size)}</td>
                    <td>{dateTime(it.date)}</td>
                    <td className="whitespace-nowrap pr-3 text-right">
                      <IconButton title={tr("Restore")} onClick={async () => { if (await confirm({ title: tr("Restore this file?"), text: tr("It was detected as {0}. Only restore it if this is a false positive.", it.virus), confirm: tr("Restore") })) restore.mutate(it.id); }}><RotateCcw size={15} /></IconButton>
                      <IconButton title={tr("Delete")} className="hover:!text-danger" onClick={async () => { if (await confirm({ title: tr("Delete permanently?"), text: it.path, danger: true })) del.mutate(it.id); }}><Trash2 size={15} /></IconButton>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      </div>
    </div>
  );
}

type Ransom = { enabled: boolean; threshold: number; block: boolean; blocked: string[]; traps?: boolean; nfsPaused?: boolean; frozen?: string[] };

function RansomwareCard() {
  const { data } = useQuery({ queryKey: ["ransomware"], queryFn: () => api.get<Ransom>("/api/ransomware"), refetchInterval: 10000 });
  const [c, setC] = useState<Ransom | null>(null);
  useEffect(() => { if (data && !c) setC(data); }, [data, c]);
  const save = useAction(() => api.put("/api/ransomware", c), { success: tr("Ransomware protection saved") });
  const unblock = useAction((user: string) => api.post("/api/ransomware/unblock", { user }), { success: tr("Account unblocked") });
  const unfreeze = useAction((share: string) => api.post("/api/ransomware/unfreeze", { share }), { success: tr("Share restored") });
  const resumeNFS = useAction(() => api.post("/api/ransomware/nfs-resume"), { success: tr("NFS shares restored") });
  if (!c || !data) return null;
  const clean = (x: Ransom) => JSON.stringify({ enabled: x.enabled, threshold: x.threshold, block: x.block, traps: !!x.traps });
  const dirty = clean(c) !== clean(data);
  return (
    <Card title={<span className="flex items-center gap-2"><ShieldAlert size={16} className="text-accent" />{tr("Ransomware protection")}</span>} actions={<Button size="sm" variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
      <div className="space-y-4">
        <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Monitor shares (SMB, WebDAV, SFTP, FTP)")} hint={tr("Detects mass renames and deletions and typical ransomware extensions (.locked, .encrypted, .wncry…); the account is blocked on all these protocols")} />
        {c.enabled && (
          <>
            <Field label={tr("Threshold (files renamed or deleted per minute and per account)")} className="max-w-sm"><Input type="number" min={5} value={c.threshold} onChange={(e) => setC({ ...c, threshold: Math.max(5, +e.target.value) })} /></Field>
            <Switch checked={c.block} onChange={(v) => setC({ ...c, block: v })} label={tr("Block the account and cut its connections")} hint={tr("Recommended: stops the encryption in progress. The account remains usable in the web interface.")} />
            <Switch checked={!!c.traps} onChange={(v) => setC({ ...c, traps: v })} label={tr("Trap files")} hint={tr("Two \"DO NOT MODIFY\" files in each share: if they are modified, renamed or deleted, the share becomes read-only on all protocols, including NFS. Mass renames over NFS also suspend NFS shares.")} />
            <div className="text-muted text-[12.5px]">{tr("To recover your files, schedule locked snapshots in scheduled tasks: even ransomware with an administrator account cannot delete them.")}</div>
          </>
        )}
        {!!data.frozen?.length && (
          <Notice tone="warn">
            <div className="space-y-2">
              <div>{tr("Shares set to read-only after a trap file was modified:")}</div>
              <div className="flex flex-wrap gap-2">{data.frozen.map((s) => <Button key={s} size="sm" icon={<Unlock size={14} />} loading={unfreeze.isPending} onClick={() => unfreeze.mutate(s)}>{tr("Restore {0}", s)}</Button>)}</div>
            </div>
          </Notice>
        )}
        {data.nfsPaused && (
          <Notice tone="warn">
            <div className="flex flex-wrap items-center gap-3"><span>{tr("NFS shares are suspended after mass renames over NFS.")}</span><Button size="sm" icon={<Unlock size={14} />} loading={resumeNFS.isPending} onClick={() => resumeNFS.mutate()}>{tr("Restore NFS")}</Button></div>
          </Notice>
        )}
        {data.blocked.length > 0 && (
          <Notice tone="warn">
            <div className="space-y-2">
              <div>{tr("Accounts blocked by protection:")}</div>
              <div className="flex flex-wrap gap-2">{data.blocked.map((u) => <Button key={u} size="sm" icon={<Unlock size={14} />} loading={unblock.isPending} onClick={() => unblock.mutate(u)}>{tr("Unblock {0}", u)}</Button>)}</div>
            </div>
          </Notice>
        )}
      </div>
    </Card>
  );
}
