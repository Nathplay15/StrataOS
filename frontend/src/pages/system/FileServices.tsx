import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, FolderTree, Globe, KeyRound, Plug, Server } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Button, Card, ErrorBox, Field, IconButton, Input, Notice, PageHeader, Spinner, Switch, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Config = { webdav: boolean; ftp: boolean; ftpRequireTls: boolean; ftpPassiveMin: number; ftpPassiveMax: number; sftp: boolean };
type Data = { config: Config; ftpInstalled: boolean; sftpInstalled: boolean; httpsPort: string };

function Address({ value }: { value: string }) {
  const { toast } = useUI();
  return (
    <div className="flex items-center gap-2">
      <code className="mono min-w-0 flex-1 truncate rounded-lg bg-panel-2 px-2.5 py-1.5 text-[12.5px]">{value}</code>
      <IconButton title={tr("Copy")} onClick={() => { navigator.clipboard?.writeText(value); toast(tr("Address copied")); }}><Copy size={15} /></IconButton>
    </div>
  );
}

export default function FileServices() {
  const { data, isLoading, error } = useQuery({ queryKey: ["fileservices"], queryFn: () => api.get<Data>("/api/fileservices") });
  const [c, setC] = useState<Config | null>(null);
  const save = useAction(() => api.put("/api/fileservices", c), { success: tr("File services applied") });
  const installFTP = useAction(() => api.post("/api/system/components/ftp/install"));
  useEffect(() => { if (data) setC(data.config); }, [data]);
  if (isLoading || !data || !c) return <div><PageHeader title={tr("File services")} icon={<FolderTree size={20} />} /><ErrorBox error={error} /><Spinner /></div>;
  const set = (k: Partial<Config>) => setC({ ...c, ...k });
  const host = location.hostname;
  const dirty = JSON.stringify(c) !== JSON.stringify(data.config);

  return (
    <div>
      <PageHeader title={tr("File services")} subtitle={tr("Access to shared folders via WebDAV, FTP and SFTP (in addition to SMB and NFS)")} icon={<FolderTree size={20} />} actions={<Button variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate()}>{tr("Apply")}</Button>} />
      <ErrorBox error={error || save.error} />
      <div className="mb-4"><Notice>{tr("Each user sees the shared folders they have access to, read-only or read-write, as with SMB. Disabled accounts have no access.")}</Notice></div>
      <div className="space-y-5">
        <Card title={<span className="flex items-center gap-2"><Globe size={16} className="text-accent" />WebDAV</span>}>
          <div className="space-y-4">
            <Switch checked={c.webdav} onChange={(v) => set({ webdav: v })} label={tr("Enable WebDAV")} hint={tr("Mount shares as a network drive on Windows, macOS, Linux, or from a mobile app")} />
            {c.webdav && (
              <>
                <Field label={tr("Address (HTTPS, recommended)")}><Address value={`https://${host}:${data.httpsPort}/dav/`} /></Field>
                <div className="text-muted text-[12.5px]">{tr("Windows: Explorer → This PC → Map network drive. macOS: Finder → Go → Connect to Server. Accounts with two-factor authentication enabled cannot use WebDAV (the protocol does not allow entering the code).")}</div>
              </>
            )}
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Server size={16} className="text-accent" />{tr("FTP / FTPS")}</span>}>
          {!data.ftpInstalled ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-muted text-[13px]">{tr("The FTP server (vsftpd) is not installed.")}</div>
              <Button icon={<Plug size={15} />} loading={installFTP.isPending} onClick={() => installFTP.mutate()}>{tr("Install")}</Button>
            </div>
          ) : (
            <div className="space-y-4">
              <Switch checked={c.ftp} onChange={(v) => set({ ftp: v })} label={tr("Enable FTP server")} hint={tr("Port 21. Useful for cameras, scanner-printers and old software")} />
              {c.ftp && (
                <>
                  <Switch checked={c.ftpRequireTls} onChange={(v) => set({ ftpRequireTls: v })} label={tr("Require encryption (explicit FTPS)")} hint={tr("Recommended: without encryption, passwords are sent in clear text. Disable only for a device that does not support FTPS")} />
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={tr("Passive ports — start")}><Input type="number" min={1024} max={65535} value={c.ftpPassiveMin} onChange={(e) => set({ ftpPassiveMin: +e.target.value })} /></Field>
                    <Field label={tr("Passive ports — end")} hint={tr("To forward on your router for access from the Internet")}><Input type="number" min={1024} max={65535} value={c.ftpPassiveMax} onChange={(e) => set({ ftpPassiveMax: +e.target.value })} /></Field>
                  </div>
                  <Field label={tr("Address")}><Address value={`ftp://${host}`} /></Field>
                </>
              )}
            </div>
          )}
        </Card>

        <Card title={<span className="flex items-center gap-2"><KeyRound size={16} className="text-accent" />SFTP</span>}>
          {!data.sftpInstalled ? <div className="text-muted text-[13px]">{tr("The SSH server (openssh-server) is not installed.")}</div> : (
            <div className="space-y-4">
              <Switch checked={c.sftp} onChange={(v) => set({ sftp: v })} label={tr("Allow SFTP for all users")} hint={tr("Transfer encrypted over SSH (port 22), with FileZilla, WinSCP, Cyberduck…")} />
              {c.sftp && <Field label={tr("Address")}><Address value={`sftp://${host}`} /></Field>}
              <div className="text-muted text-[12.5px]">{tr("Users without \"SSH access\" only get SFTP, confined to the NAS storage. Those with SSH access (administrators) keep their full shell, SFTP included. Uploaded files can be modified by other authorized members, unless the client software enforces its own permissions (as with the command-line sftp client).")}</div>
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}
