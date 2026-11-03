import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { BarChart3, Copy, Download, Eye, FileIcon, Folder, Images, Inbox, Link2, Lock, Pencil, Trash2, Upload } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Me, ShareLink } from "../lib/types";
import { ago, bytes, dateTime } from "../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Modal, PageHeader, Spinner, Switch, Tabs, useUI } from "../components/ui";
import { dateInput, LinkFields, linkPayload, linkURL, type LinkForm } from "../components/ShareLinkModal";
import { tr } from "../lib/i18n";

type Filter = "all" | "requests" | "inactive";

const stateBadge = (l: ShareLink) => ({
  active: null,
  expired: <Badge tone="danger">{tr("Expired")}</Badge>,
  exhausted: <Badge tone="danger">{tr("Download limit reached")}</Badge>,
  full: <Badge tone="warn">{tr("Size limit reached")}</Badge>,
})[l.state ?? "active"];

function EditLink({ link, onClose }: { link: ShareLink; onClose: () => void }) {
  const [f, setF] = useState<LinkForm>({
    password: "", expires: dateInput(link.expires), maxDownloads: link.maxDownloads ?? 0, title: link.title ?? "", message: link.message ?? "",
    askGuest: link.askGuest ?? "", guestFolders: !!link.guestFolders, maxFileMb: Math.round((link.maxFileBytes ?? 0) / 1048576), maxTotalMb: Math.round((link.maxTotalBytes ?? 0) / 1048576), notify: !!link.notify,
  });
  const [clear, setClear] = useState(false);
  const save = useAction(() => api.put(`/api/links/${link.id}`, { ...linkPayload(f), clearPassword: clear && !f.password }), { success: tr("Link saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><Pencil size={16} />{link.title || link.name}</span>} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <LinkFields f={f} setF={setF} upload={link.upload} download={link.download} editing />
        {link.password && <Switch checked={clear} onChange={setClear} label={tr("Remove the password")} disabled={!!f.password} />}
        <p className="text-[12px] text-muted">{tr("Changing the password signs out the visitors who had unlocked the link.")}</p>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function Stats({ link, onClose }: { link: ShareLink; onClose: () => void }) {
  const log = [...(link.uploadLog ?? [])].reverse();
  const stat = (label: string, v: React.ReactNode, sub?: string) => <div className="rounded-xl bg-panel-2 p-3"><div className="text-muted text-[12px]">{label}</div><div className="text-[18px] font-semibold">{v}</div>{sub && <div className="text-muted text-[11.5px]">{sub}</div>}</div>;
  return (
    <Modal open wide onClose={onClose} title={<span className="flex items-center gap-2"><BarChart3 size={16} />{link.title || link.name}</span>}>
      <div className="space-y-4">
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {stat(tr("Views"), link.views ?? 0, link.lastView ? ago(link.lastView) : undefined)}
          {link.download && stat(tr("Downloads"), `${link.downloads}${link.maxDownloads ? ` / ${link.maxDownloads}` : ""}`, link.lastDownload ? ago(link.lastDownload) : undefined)}
          {link.upload && stat(tr("Files received"), link.uploads, link.lastUpload ? ago(link.lastUpload) : undefined)}
          {link.upload && stat(tr("Data received"), bytes(link.receivedBytes ?? 0), link.maxTotalBytes ? tr("of {0}", bytes(link.maxTotalBytes)) : undefined)}
        </div>
        <div className="text-muted text-[12.5px]">{tr("Created {0}", dateTime(link.created))}{link.lastUsed ? ` · ${tr("Last accessed {0}", ago(link.lastUsed))}` : ""}</div>
        {link.upload && (log.length === 0 ? <Empty icon={<Inbox />} title={tr("No file received yet")} /> : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("File")}</th><th>{tr("Size")}</th><th>{tr("Sender")}</th><th>{tr("Date")}</th></tr></thead>
              <tbody>
                {log.map((u, i) => (
                  <tr key={i}>
                    <td className="mono break-all text-[12.5px]">{u.name}</td>
                    <td className="text-muted">{bytes(u.size)}</td>
                    <td className="text-[12.5px]">{u.guest || u.email ? <>{u.guest}{u.email && <div className="text-muted">{u.email}</div>}</> : <span className="text-muted">{tr("Anonymous")}</span>}</td>
                    <td className="text-muted text-[12.5px]">{dateTime(u.time)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
        {link.upload && <p className="text-[12px] text-muted">{tr("The last 100 files received are listed.")}</p>}
      </div>
    </Modal>
  );
}

export default function Links({ me }: { me: Me }) {
  const { confirm, toast } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["links"], queryFn: () => api.get<ShareLink[]>("/api/links") });
  const del = useAction((id: string) => api.del(`/api/links/${id}`), { success: tr("Link deleted") });
  const [filter, setFilter] = useState<Filter>("all");
  const [edit, setEdit] = useState<ShareLink | null>(null);
  const [stats, setStats] = useState<ShareLink | null>(null);
  const copy = (id: string) => { navigator.clipboard?.writeText(linkURL(id)); toast(tr("Link copied")); };
  const list = (data ?? []).filter((l) => filter === "all" || (filter === "requests" ? l.upload && !l.download : l.state !== "active"));

  return (
    <div>
      <PageHeader title={tr("Shared links")} subtitle={tr("Files, folders and albums accessible without an account, and file requests")} icon={<Link2 size={20} />} />
      <ErrorBox error={error} />
      <div className="mb-3"><Tabs<Filter> value={filter} onChange={setFilter} tabs={[{ id: "all", label: tr("All") }, { id: "requests", label: tr("File requests") }, { id: "inactive", label: tr("Expired or used up") }]} /></div>
      <Card pad={false}>
        {isLoading ? <Spinner /> : !list.length ? (
          <Empty icon={<Link2 />} title={tr("No shared links")} text={tr("In Files or Photos, choose \"Share a link\" on a file, folder or album. On a folder, \"Request files\" lets people send you files.")} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead><tr><th>{tr("Item")}</th><th>{tr("Access")}</th><th>{tr("Expiration")}</th><th>{tr("Usage")}</th><th /></tr></thead>
              <tbody>
                {list.map((l) => (
                  <tr key={l.id} className={l.state !== "active" ? "opacity-60" : ""}>
                    <td>
                      <div className="flex items-center gap-2.5">
                        {l.upload && !l.download ? <Inbox size={16} className="text-accent" /> : l.album ? <Images size={16} className="text-accent" /> : l.dir ? <Folder size={16} className="text-accent" /> : <FileIcon size={16} className="text-muted" />}
                        <div className="min-w-0">
                          <div className="truncate font-medium">{l.title || l.name}</div>
                          <div className="mono truncate text-muted text-[11.5px]">{l.album ? tr("Photo album") : l.path}{me.role === "admin" && l.owner !== me.username ? ` · ${l.owner}` : ""}</div>
                        </div>
                      </div>
                    </td>
                    <td>
                      <div className="flex flex-wrap gap-1">
                        {l.download && <Badge><Download size={11} className="mr-1 inline" />{tr("Downloads")}</Badge>}
                        {l.upload && <Badge tone="accent"><Upload size={11} className="mr-1 inline" />{l.download ? tr("Drop box") : tr("File request")}</Badge>}
                        {l.password && <Badge tone="warn"><Lock size={11} className="mr-1 inline" />{tr("Password")}</Badge>}
                      </div>
                    </td>
                    <td className="text-[12.5px]">{stateBadge(l) ?? (l.expires ? dateTime(l.expires) : <span className="text-muted">{tr("Never")}</span>)}</td>
                    <td className="text-muted text-[12.5px]">
                      <div><Eye size={11} className="mr-1 inline" />{tr("{0} view(s)", l.views ?? 0)}{l.download ? ` · ${tr("{0} download(s)", l.downloads)}${l.maxDownloads ? ` / ${l.maxDownloads}` : ""}` : ""}{l.upload ? ` · ${tr("{0} file(s) received", l.uploads)}` : ""}</div>
                      {l.lastUsed && <div>{tr("Last accessed {0}", ago(l.lastUsed))}</div>}
                    </td>
                    <td className="whitespace-nowrap text-right">
                      <Button size="sm" variant="ghost" icon={<BarChart3 size={14} />} title={tr("Statistics")} onClick={() => setStats(l)} />
                      <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={tr("Edit")} onClick={() => setEdit(l)} />
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
      {edit && <EditLink link={edit} onClose={() => setEdit(null)} />}
      {stats && <Stats link={stats} onClose={() => setStats(null)} />}
    </div>
  );
}
