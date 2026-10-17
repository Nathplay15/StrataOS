import { useQuery } from "@tanstack/react-query";
import { Copy, Download, FileIcon, Folder, Images, Inbox, Link2, Lock, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Me, ShareLink } from "../lib/types";
import { ago, dateTime } from "../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, PageHeader, Spinner, useUI } from "../components/ui";
import { linkURL } from "../components/ShareLinkModal";
import { tr } from "../lib/i18n";

export default function Links({ me }: { me: Me }) {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["links"], queryFn: () => api.get<ShareLink[]>("/api/links") });
  const del = useAction((id: string) => api.del(`/api/links/${id}`), { success: tr("Lien supprimé") });
  const copy = (id: string) => { navigator.clipboard?.writeText(linkURL(id)); toast(tr("Lien copié")); };
  const expired = (l: ShareLink) => (!!l.expires && new Date(l.expires) < new Date()) || (!!l.maxDownloads && l.downloads >= l.maxDownloads);

  return (
    <div>
      <PageHeader title={tr("Liens partagés")} subtitle={tr("Fichiers, dossiers et albums accessibles sans compte")} icon={<Link2 size={20} />} />
      <ErrorBox error={error} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : !data?.length ? (
          <Empty icon={<Link2 />} title={tr("Aucun lien partagé")} text={tr("Dans Fichiers ou Photos, choisissez « Partager un lien » sur un fichier, un dossier ou un album.")} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Élément")}</th><th>{tr("Accès")}</th><th>{tr("Expiration")}</th><th>{tr("Utilisation")}</th><th /></tr></thead>
              <tbody>
                {data.map((l) => (
                  <tr key={l.id} className={expired(l) ? "opacity-60" : ""}>
                    <td>
                      <div className="flex items-center gap-2.5">
                        {l.album ? <Images size={16} className="text-accent" /> : l.dir ? <Folder size={16} className="text-accent" /> : <FileIcon size={16} className="text-muted" />}
                        <div className="min-w-0">
                          <div className="truncate font-medium">{l.name}</div>
                          <div className="mono truncate text-muted text-[11.5px]">{l.album ? tr("Album photo") : l.path}{me.role === "admin" && l.owner !== me.username ? ` · ${l.owner}` : ""}</div>
                        </div>
                      </div>
                    </td>
                    <td>
                      <div className="flex flex-wrap gap-1">
                        {l.download && <Badge><Download size={11} className="mr-1 inline" />{tr("Téléchargement")}</Badge>}
                        {l.upload && <Badge tone="accent"><Inbox size={11} className="mr-1 inline" />{tr("Dépôt")}</Badge>}
                        {l.password && <Badge tone="warn"><Lock size={11} className="mr-1 inline" />{tr("Mot de passe")}</Badge>}
                      </div>
                    </td>
                    <td className="text-[12.5px]">{expired(l) ? <Badge tone="danger">{tr("Expiré")}</Badge> : l.expires ? dateTime(l.expires) : <span className="text-muted">{tr("Jamais")}</span>}</td>
                    <td className="text-muted text-[12.5px]">
                      <div>{tr("{0} téléchargement(s)", l.downloads)}{l.maxDownloads ? ` / ${l.maxDownloads}` : ""}{l.upload ? ` · ${tr("{0} fichier(s) reçu(s)", l.uploads)}` : ""}</div>
                      {l.lastUsed && <div>{tr("Dernier accès {0}", ago(l.lastUsed))}</div>}
                    </td>
                    <td className="whitespace-nowrap text-right">
                      <Button size="sm" variant="ghost" icon={<Copy size={14} />} title={tr("Copier le lien")} onClick={() => copy(l.id)} />
                      <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={tr("Supprimer")} onClick={async () => { if (await confirm({ title: tr("Supprimer ce lien ?"), text: tr("Le lien cesse immédiatement de fonctionner. Les fichiers ne sont pas supprimés."), danger: true })) del.mutate(l.id); }} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
