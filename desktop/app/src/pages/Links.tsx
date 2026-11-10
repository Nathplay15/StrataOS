// Share links and file requests of the user (all of them for an
// administrator), and the dialog creating one, also used by Files. Links
// point to the address the app reaches the NAS at (its server).
import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { writeText } from "@tauri-apps/plugin-clipboard-manager";
import { BarChart3, Copy, Download, Eye, FileIcon, Folder, Images, Inbox, Link2, Lock, Pencil, Trash2, Upload } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Input, Modal, Notice, Spinner, Switch, Tabs, useUI } from "@nas/components/ui";
import { dateInput, inDays, LinkFields, linkPayload, type LinkForm } from "@nas/components/ShareLinkModal";
import { useAction } from "@nas/lib/hooks";
import { ago, bytes, dateTime } from "@nas/lib/format";
import type { ShareLink } from "@nas/lib/types";
import { api } from "../lib/nasapi";
import { linkURL } from "../lib/shell";
import { useApp } from "../lib/state";
import { t } from "../lib/i18n";
import { PageHeader } from "../components/kit";

type Filter = "all" | "requests" | "inactive";

/** Copies a link to the clipboard of the system. */
function useCopy() {
  const { account } = useApp();
  const { toast } = useUI();
  const url = (id: string) => linkURL(account?.config.server, id);
  const copy = async (id: string) => {
    try {
      await writeText(url(id));
      toast(t("Link copied"));
    } catch (e) {
      toast(String(e), "error");
    }
  };
  return { url, copy };
}

/** Creates a public link to a file or folder, or a file request on a folder. */
export function ShareDialog({ path, dir, name, onClose }: { path: string; dir: boolean; name: string; onClose: () => void }) {
  const { url, copy } = useCopy();
  const [mode, setMode] = useState<"share" | "request">("share");
  const [access, setAccess] = useState({ download: true, upload: false });
  const [f, setF] = useState<LinkForm>({ password: "", expires: inDays(7), maxDownloads: 0, title: "", message: "", askGuest: "", guestFolders: false, maxFileMb: 0, maxTotalMb: 0, notify: false });
  const [made, setMade] = useState<ShareLink | null>(null);
  const req = mode === "request" ? { download: false, upload: true } : access;
  const create = useAction(() => api.post<ShareLink>("/api/links", { path, ...req, ...linkPayload(f) }), { onDone: (l) => setMade(l) });
  const pick = (m: "share" | "request") => { setMode(m); if (m === "request") setF({ ...f, notify: true, expires: inDays(30) }); };

  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2">{mode === "request" ? <Inbox size={17} /> : <Link2 size={17} />}{mode === "request" ? t("Request files in \"{0}\"", name) : t("Share \"{0}\"", name)}</span>}
      footer={made ? <Button variant="primary" onClick={onClose}>{t("Completed")}</Button> : <>
        <Button onClick={onClose}>{t("Cancel")}</Button>
        <Button variant="primary" loading={create.isPending} disabled={!req.download && !req.upload} onClick={() => create.mutate()}>{t("Create link")}</Button>
      </>}>
      {made ? (
        <div className="space-y-3">
          <p className="text-[13px] text-muted">{mode === "request" ? t("Anyone with this link can send you files, without an account and without seeing what the folder holds.") : t("Anyone with this link can open it, without an account on the NAS.")}</p>
          <div className="flex gap-2"><Input readOnly className="mono" value={url(made.id)} onFocus={(e) => e.target.select()} /><Button icon={<Copy size={15} />} onClick={() => void copy(made.id)}>{t("Copy")}</Button></div>
          <Notice>{t("To open it from the Internet, the NAS must be reachable from outside (see the assistant in Remote access). Find your links in Shared links.")}</Notice>
        </div>
      ) : (
        <div className="space-y-4">
          {dir && <Tabs value={mode} onChange={pick} tabs={[{ id: "share", label: t("Share") }, { id: "request", label: t("Request files") }]} />}
          {mode === "share" && <Switch checked={access.download} onChange={(v) => setAccess({ ...access, download: v })} label={dir ? t("View and download content") : t("Download file")} />}
          {mode === "share" && dir && <Switch checked={access.upload} onChange={(v) => setAccess({ ...access, upload: v })} label={t("Upload files")} hint={t("Visitors can upload files to this folder, without ever replacing existing ones. Without download, the content stays hidden (drop box).")} />}
          {mode === "request" && <p className="text-[13px] text-muted">{t("Guests can only upload: they never see the content of the folder, and never replace a file.")}</p>}
          <LinkFields f={f} setF={setF} upload={req.upload} download={req.download} />
          <ErrorBox error={create.error} />
        </div>
      )}
    </Modal>
  );
}

const stateBadge = (l: ShareLink) => ({
  active: null,
  expired: <Badge tone="danger">{t("Expired")}</Badge>,
  exhausted: <Badge tone="danger">{t("Download limit reached")}</Badge>,
  full: <Badge tone="warn">{t("Size limit reached")}</Badge>,
})[l.state ?? "active"];

function EditLink({ link, onClose }: { link: ShareLink; onClose: () => void }) {
  const [f, setF] = useState<LinkForm>({
    password: "", expires: dateInput(link.expires), maxDownloads: link.maxDownloads ?? 0, title: link.title ?? "", message: link.message ?? "",
    askGuest: link.askGuest ?? "", guestFolders: !!link.guestFolders, maxFileMb: Math.round((link.maxFileBytes ?? 0) / 1048576), maxTotalMb: Math.round((link.maxTotalBytes ?? 0) / 1048576), notify: !!link.notify,
  });
  const [clear, setClear] = useState(false);
  const save = useAction(() => api.put(`/api/links/${link.id}`, { ...linkPayload(f), clearPassword: clear && !f.password }), { success: t("Link saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><Pencil size={16} />{link.title || link.name}</span>} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{t("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <LinkFields f={f} setF={setF} upload={link.upload} download={link.download} editing />
        {link.password && <Switch checked={clear} onChange={setClear} label={t("Remove the password")} disabled={!!f.password} />}
        <p className="text-[12px] text-muted">{t("Changing the password signs out the visitors who had unlocked the link.")}</p>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function Stats({ link, onClose }: { link: ShareLink; onClose: () => void }) {
  const log = [...(link.uploadLog ?? [])].reverse();
  const stat = (label: string, v: ReactNode, sub?: string) => <div className="rounded-xl bg-panel-2 p-3"><div className="text-[12px] text-muted">{label}</div><div className="text-[18px] font-semibold">{v}</div>{sub && <div className="text-[11.5px] text-muted">{sub}</div>}</div>;
  return (
    <Modal open wide onClose={onClose} title={<span className="flex items-center gap-2"><BarChart3 size={16} />{link.title || link.name}</span>}>
      <div className="space-y-4">
        <div className="grid grid-cols-4 gap-3">
          {stat(t("Views"), link.views ?? 0, link.lastView ? ago(link.lastView) : undefined)}
          {link.download && stat(t("Downloads"), `${link.downloads}${link.maxDownloads ? ` / ${link.maxDownloads}` : ""}`, link.lastDownload ? ago(link.lastDownload) : undefined)}
          {link.upload && stat(t("Files received"), link.uploads, link.lastUpload ? ago(link.lastUpload) : undefined)}
          {link.upload && stat(t("Data received"), bytes(link.receivedBytes ?? 0), link.maxTotalBytes ? t("of {0}", bytes(link.maxTotalBytes)) : undefined)}
        </div>
        <div className="text-[12.5px] text-muted">{t("Created {0}", dateTime(link.created))}{link.lastUsed ? ` · ${t("Last accessed {0}", ago(link.lastUsed))}` : ""}</div>
        {link.upload && (log.length === 0 ? <Empty icon={<Inbox />} title={t("No file received yet")} /> : (
          <table className="table">
            <thead><tr><th>{t("File")}</th><th>{t("Size")}</th><th>{t("Sender")}</th><th>{t("Date")}</th></tr></thead>
            <tbody>
              {log.map((u, i) => (
                <tr key={i}>
                  <td className="mono break-all text-[12.5px]">{u.name}</td>
                  <td className="text-muted">{bytes(u.size)}</td>
                  <td className="text-[12.5px]">{u.guest || u.email ? <>{u.guest}{u.email && <div className="text-muted">{u.email}</div>}</> : <span className="text-muted">{t("Anonymous")}</span>}</td>
                  <td className="text-[12.5px] text-muted">{dateTime(u.time)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
      </div>
    </Modal>
  );
}

export function LinksPage() {
  const { info } = useApp();
  const { confirm } = useUI();
  const { copy } = useCopy();
  const { data, isLoading, error } = useQuery({ queryKey: ["links"], queryFn: () => api.get<ShareLink[]>("/api/links") });
  const del = useAction((id: string) => api.del(`/api/links/${id}`), { success: t("Link deleted") });
  const [filter, setFilter] = useState<Filter>("all");
  const [edit, setEdit] = useState<ShareLink | null>(null);
  const [stats, setStats] = useState<ShareLink | null>(null);
  const me = info?.user;
  const list = (data ?? []).filter((l) => filter === "all" || (filter === "requests" ? l.upload && !l.download : l.state !== "active"));

  return (
    <>
      <PageHeader title={t("Shared links")} subtitle={t("Files, folders and albums accessible without an account, and file requests")} />
      <ErrorBox error={error} />
      <div className="mb-3"><Tabs<Filter> value={filter} onChange={setFilter} tabs={[{ id: "all", label: t("All") }, { id: "requests", label: t("File requests") }, { id: "inactive", label: t("Expired or used up") }]} /></div>
      <Card pad={false}>
        {isLoading ? <Spinner /> : !list.length ? (
          <Empty icon={<Link2 />} title={t("No shared links")} text={t("In Files or Photos, choose \"Share a link\" on a file, folder or album. On a folder, \"Request files\" lets people send you files.")} />
        ) : (
          <table className="table">
            <thead><tr><th>{t("Item")}</th><th>{t("Access")}</th><th>{t("Expiration")}</th><th>{t("Usage")}</th><th /></tr></thead>
            <tbody>
              {list.map((l) => (
                <tr key={l.id} className={l.state !== "active" ? "opacity-60" : ""}>
                  <td>
                    <div className="flex items-center gap-2.5">
                      {l.upload && !l.download ? <Inbox size={16} className="text-accent" /> : l.album ? <Images size={16} className="text-accent" /> : l.dir ? <Folder size={16} className="text-accent" /> : <FileIcon size={16} className="text-muted" />}
                      <div className="min-w-0">
                        <div className="truncate font-medium">{l.title || l.name}</div>
                        <div className="mono truncate text-[11.5px] text-muted">{l.album ? t("Photo album") : l.path}{me?.role === "admin" && l.owner !== me.username ? ` · ${l.owner}` : ""}</div>
                      </div>
                    </div>
                  </td>
                  <td>
                    <div className="flex flex-wrap gap-1">
                      {l.download && <Badge><Download size={11} className="mr-1 inline" />{t("Downloads")}</Badge>}
                      {l.upload && <Badge tone="accent"><Upload size={11} className="mr-1 inline" />{l.download ? t("Drop box") : t("File request")}</Badge>}
                      {l.password && <Badge tone="warn"><Lock size={11} className="mr-1 inline" />{t("Password")}</Badge>}
                    </div>
                  </td>
                  <td className="text-[12.5px]">{stateBadge(l) ?? (l.expires ? dateTime(l.expires) : <span className="text-muted">{t("Never")}</span>)}</td>
                  <td className="text-[12.5px] text-muted">
                    <div><Eye size={11} className="mr-1 inline" />{t("{0} view(s)", l.views ?? 0)}{l.download ? ` · ${t("{0} download(s)", l.downloads)}${l.maxDownloads ? ` / ${l.maxDownloads}` : ""}` : ""}{l.upload ? ` · ${t("{0} file(s) received", l.uploads)}` : ""}</div>
                    {l.lastUsed && <div>{t("Last accessed {0}", ago(l.lastUsed))}</div>}
                  </td>
                  <td className="whitespace-nowrap text-right">
                    <Button size="sm" variant="ghost" icon={<BarChart3 size={14} />} title={t("Statistics")} onClick={() => setStats(l)} />
                    <Button size="sm" variant="ghost" icon={<Pencil size={14} />} title={t("Edit")} onClick={() => setEdit(l)} />
                    <Button size="sm" variant="ghost" icon={<Copy size={14} />} title={t("Copy link")} onClick={() => void copy(l.id)} />
                    <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={t("Delete")} onClick={async () => { if (await confirm({ title: t("Delete this link?"), text: t("The link stops working immediately. Files are not deleted."), danger: true })) del.mutate(l.id); }} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {edit && <EditLink link={edit} onClose={() => setEdit(null)} />}
      {stats && <Stats link={stats} onClose={() => setStats(null)} />}
    </>
  );
}
