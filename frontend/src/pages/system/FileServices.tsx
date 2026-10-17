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
      <IconButton title={tr("Copier")} onClick={() => { navigator.clipboard?.writeText(value); toast(tr("Adresse copiée")); }}><Copy size={15} /></IconButton>
    </div>
  );
}

export default function FileServices() {
  const { data, isLoading, error } = useQuery({ queryKey: ["fileservices"], queryFn: () => api.get<Data>("/api/fileservices") });
  const [c, setC] = useState<Config | null>(null);
  const save = useAction(() => api.put("/api/fileservices", c), { success: tr("Services de fichiers appliqués") });
  const installFTP = useAction(() => api.post("/api/system/components/ftp/install"));
  useEffect(() => { if (data) setC(data.config); }, [data]);
  if (isLoading || !data || !c) return <div><PageHeader title={tr("Services de fichiers")} icon={<FolderTree size={20} />} /><ErrorBox error={error} /><Spinner /></div>;
  const set = (k: Partial<Config>) => setC({ ...c, ...k });
  const host = location.hostname;
  const dirty = JSON.stringify(c) !== JSON.stringify(data.config);

  return (
    <div>
      <PageHeader title={tr("Services de fichiers")} subtitle={tr("Accès aux dossiers partagés par WebDAV, FTP et SFTP (en plus de SMB et NFS)")} icon={<FolderTree size={20} />} actions={<Button variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate()}>{tr("Appliquer")}</Button>} />
      <ErrorBox error={error || save.error} />
      <div className="mb-4"><Notice>{tr("Chaque utilisateur retrouve les dossiers partagés auxquels il a droit, en lecture seule ou en lecture-écriture, comme en SMB. Les comptes désactivés n'ont accès à rien.")}</Notice></div>
      <div className="space-y-5">
        <Card title={<span className="flex items-center gap-2"><Globe size={16} className="text-accent" />WebDAV</span>}>
          <div className="space-y-4">
            <Switch checked={c.webdav} onChange={(v) => set({ webdav: v })} label={tr("Activer WebDAV")} hint={tr("Monter les partages comme un lecteur réseau sur Windows, macOS, Linux, ou depuis une application mobile")} />
            {c.webdav && (
              <>
                <Field label={tr("Adresse (HTTPS, recommandée)")}><Address value={`https://${host}:${data.httpsPort}/dav/`} /></Field>
                <div className="text-muted text-[12.5px]">{tr("Windows : Explorateur → Ce PC → Connecter un lecteur réseau. macOS : Finder → Aller → Se connecter au serveur. Les comptes avec la double authentification activée ne peuvent pas utiliser WebDAV (le protocole ne permet pas de saisir le code).")}</div>
              </>
            )}
          </div>
        </Card>

        <Card title={<span className="flex items-center gap-2"><Server size={16} className="text-accent" />{tr("FTP / FTPS")}</span>}>
          {!data.ftpInstalled ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="text-muted text-[13px]">{tr("Le serveur FTP (vsftpd) n'est pas installé.")}</div>
              <Button icon={<Plug size={15} />} loading={installFTP.isPending} onClick={() => installFTP.mutate()}>{tr("Installer")}</Button>
            </div>
          ) : (
            <div className="space-y-4">
              <Switch checked={c.ftp} onChange={(v) => set({ ftp: v })} label={tr("Activer le serveur FTP")} hint={tr("Port 21. Utile pour les caméras, imprimantes-scanners et vieux logiciels")} />
              {c.ftp && (
                <>
                  <Switch checked={c.ftpRequireTls} onChange={(v) => set({ ftpRequireTls: v })} label={tr("Exiger le chiffrement (FTPS explicite)")} hint={tr("Recommandé : sans chiffrement, les mots de passe circulent en clair. Désactivez seulement pour un appareil qui ne gère pas FTPS")} />
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={tr("Ports passifs — début")}><Input type="number" min={1024} max={65535} value={c.ftpPassiveMin} onChange={(e) => set({ ftpPassiveMin: +e.target.value })} /></Field>
                    <Field label={tr("Ports passifs — fin")} hint={tr("À rediriger sur la box pour un accès depuis Internet")}><Input type="number" min={1024} max={65535} value={c.ftpPassiveMax} onChange={(e) => set({ ftpPassiveMax: +e.target.value })} /></Field>
                  </div>
                  <Field label={tr("Adresse")}><Address value={`ftp://${host}`} /></Field>
                </>
              )}
            </div>
          )}
        </Card>

        <Card title={<span className="flex items-center gap-2"><KeyRound size={16} className="text-accent" />SFTP</span>}>
          {!data.sftpInstalled ? <div className="text-muted text-[13px]">{tr("Le serveur SSH (openssh-server) n'est pas installé.")}</div> : (
            <div className="space-y-4">
              <Switch checked={c.sftp} onChange={(v) => set({ sftp: v })} label={tr("Autoriser SFTP pour tous les utilisateurs")} hint={tr("Transfert chiffré par SSH (port 22), avec FileZilla, WinSCP, Cyberduck…")} />
              {c.sftp && <Field label={tr("Adresse")}><Address value={`sftp://${host}`} /></Field>}
              <div className="text-muted text-[12.5px]">{tr("Les utilisateurs sans « accès SSH » n'obtiennent que SFTP, enfermés dans le stockage du NAS. Ceux qui ont l'accès SSH (administrateurs) gardent leur shell complet, SFTP compris. Les fichiers envoyés sont modifiables par les autres membres autorisés, sauf si le logiciel client impose ses propres droits (cas du client sftp en ligne de commande).")}</div>
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}
