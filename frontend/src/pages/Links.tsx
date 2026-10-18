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
  const del = useAction((id: string) => api.del(`/api/links/${id}`), { success: tr("Link deleted") });
  const copy = (id: string) => { navigator.clipboard?.writeText(linkURL(id)); toast(tr("Link copied")); };
  const expired = (l: ShareLink) => (!!l.expires && new Date(l.expires) < new Date()) || (!!l.maxDownloads && l.downloads >= l.maxDownloads);

  return (
    <div>
      <PageHeader title={tr("Shared links")} subtitle={tr("Files, folders and albums accessible without an account")} icon={<Link2 size={20} />} />
      <ErrorBox error={error} />
      <Card pad={false}>
        {isLoading ? <Spinner /> : !data?.length ? (
          <Empty icon={<Link2 />} title={tr("No shared links")} text={tr("In Files or Photos, choose \"Share a link\" on a file, folder or album.")} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Item")}</th><th>{tr("Access")}</th><th>{tr("Expiration")}</th><th>{tr("Usage")}</th><th /></tr></thead>
              <tbody>
                {data.map((l) => (
                  <tr key={l.id} className={expired(l) ? "opacity-60" : ""}>
                    <td>
                      <div className="flex items-center gap-2.5">
                        {l.album ? <Images size={16} className="text-accent" /> : l.dir ? <Folder size={16} className="text-accent" /> : <FileIcon size={16} className="text-muted" />}
                        <div className="min-w-0">
                          <div className="truncate font-medium">{l.name}</div>
                          <div className="mono truncate text-muted text-[11.5px]">{l.album ? tr("Photo album") : l.path}{me.role === "admin" && l.owner !== me.username ? ` · ${l.owner}` : ""}</div>
                        </div>
                      </div>
                    </td>
                    <td>
                      <div className="flex flex-wrap gap-1">
                        {l.download && <Badge><Download size={11} className="mr-1 inline" />{tr("Download")}</Badge>}
                        {l.upload && <Badge tone="accent"><Inbox size={11} className="mr-1 inline" />{tr("Drop box")}</Badge>}
                        {l.password && <Badge tone="warn"><Lock size={11} className="mr-1 inline" />{tr("Password")}</Badge>}
                      </div>
                    </td>
                    <td className="text-[12.5px]">{expired(l) ? <Badge tone="danger">{tr("Expired")}</Badge> : l.expires ? dateTime(l.expires) : <span className="text-muted">{tr("Never")}</span>}</td>
                    <td className="text-muted text-[12.5px]">
                      <div>{tr("{0} download(s)", l.downloads)}{l.maxDownloads ? ` / ${l.maxDownloads}` : ""}{l.upload ? ` · ${tr("{0} file(s) received", l.uploads)}` : ""}</div>
                      {l.lastUsed && <div>{tr("Last accessed {0}", ago(l.lastUsed))}</div>}
                    </td>
                    <td className="whitespace-nowrap text-right">
                      <Button size="sm" variant="ghost" icon={<Copy size={14} />} title={tr("Copy link")} onClick={() => copy(l.id)} />
                      <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={tr("Delete")} onClick={async () => { if (await confirm({ title: tr("Delete this link?"), text: tr("The link stops working immediately. Files are not deleted."), danger: true })) del.mutate(l.id); }} />
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
