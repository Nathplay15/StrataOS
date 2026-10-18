import { useRef, useState, type DragEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, Eye, EyeOff, File, FileImage, FileText, FileVideo, Folder, FolderOpen, FolderPlus, FolderUp, HardDrive, History, Link2, Pencil, Trash2, Upload } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { FileEntry, UserQuota } from "../lib/types";
import { bytes, cx, dateTime, pct } from "../lib/format";
import { ShareLinkModal } from "../components/ShareLinkModal";
import { VersionsModal } from "../components/VersionsModal";
import { Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, PageHeader, Progress, Spinner, useUI } from "../components/ui";
import { tr } from "../lib/i18n";

const ROOT = "/srv/strata";

function icon(e: FileEntry) {
  if (e.dir) return <Folder size={18} className="text-accent" fill="currentColor" fillOpacity={0.15} />;
  const ext = e.name.split(".").pop()?.toLowerCase() ?? "";
  if (["jpg", "jpeg", "png", "gif", "webp", "svg", "heic", "bmp"].includes(ext)) return <FileImage size={18} className="text-ok" />;
  if (["mp4", "mkv", "avi", "mov", "webm"].includes(ext)) return <FileVideo size={18} className="text-warn" />;
  if (["txt", "md", "log", "json", "yml", "yaml", "conf", "csv", "xml", "ini", "sh"].includes(ext)) return <FileText size={18} className="text-muted" />;
  return <File size={18} className="text-muted" />;
}

const previewable = (n: string) => /\.(jpe?g|png|gif|webp|svg|bmp|mp4|webm|mp3|ogg|wav|pdf|txt|md|log|json|ya?ml|conf|csv|xml|ini|sh)$/i.test(n);

export default function Files() {
  const { confirm, toast } = useUI();
  const qc = useQueryClient();
  const [path, setPath] = useState(ROOT);
  const [hidden, setHidden] = useState(false);
  const [sel, setSel] = useState<string[]>([]);
  const [mkdir, setMkdir] = useState<string | null>(null);
  const [rename, setRename] = useState<{ from: string; name: string } | null>(null);
  const [share, setShare] = useState<{ path: string; name: string; dir: boolean } | null>(null);
  const [versions, setVersions] = useState<{ path: string; name: string } | null>(null);
  const [preview, setPreview] = useState<FileEntry | null>(null);
  const [upload, setUpload] = useState<{ pct: number; name: string } | null>(null);
  const [drag, setDrag] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);

  const myQuotas = useQuery({ queryKey: ["my-quotas"], queryFn: () => api.get<UserQuota[]>("/api/me/quotas"), staleTime: 60000 });
  const { data, isLoading, error } = useQuery({
    queryKey: ["files", path, hidden],
    queryFn: () => api.get<{ path: string; entries: FileEntry[]; writable: boolean }>(`/api/files${q({ path, hidden: hidden ? 1 : undefined })}`),
  });
  const go = (p: string) => { setPath(p); setSel([]); };
  const del = useAction((paths: string[]) => api.post("/api/files/delete", { paths }), { success: tr("Deleted"), onDone: () => setSel([]) });
  const mk = useAction((p: string) => api.post("/api/files/mkdir", { path: p }), { success: tr("Folder created"), onDone: () => setMkdir(null) });
  const mv = useAction((p: { from: string; to: string }) => api.post("/api/files/rename", p), { success: tr("Renamed"), onDone: () => setRename(null) });

  /** Uploads files; each item may carry a relative path (folder uploads). */
  const doUpload = (files: FileList | File[] | { file: File; rel: string }[]) => {
    type Item = { file: File; rel: string };
    const list: Item[] = Array.from(files as ArrayLike<File | Item>).map((x): Item => {
      if ("file" in x) return x;
      const f = x as File & { webkitRelativePath?: string };
      return { file: f, rel: f.webkitRelativePath || f.name };
    });
    if (!list.length) return;
    const fd = new FormData();
    for (const { file, rel } of list) fd.append("file", file, rel);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/files/upload${q({ path })}`);
    xhr.setRequestHeader("X-Strata", "1");
    xhr.upload.onprogress = (e) => e.lengthComputable && setUpload({ pct: (e.loaded / e.total) * 100, name: list.length === 1 ? list[0].rel : tr("{0} files", list.length) });
    xhr.onload = () => {
      setUpload(null);
      if (xhr.status >= 300) toast(JSON.parse(xhr.responseText || "{}").error ?? tr("Send failed"), "error");
      else toast(tr("{0} file(s) uploaded", list.length));
      qc.invalidateQueries({ queryKey: ["files"] });
    };
    xhr.onerror = () => { setUpload(null); toast(tr("Send failed"), "error"); };
    setUpload({ pct: 0, name: list[0].rel });
    xhr.send(fd);
  };

  // Dropped folders are walked recursively so their structure is kept.
  const onDrop = async (e: DragEvent) => {
    e.preventDefault();
    setDrag(false);
    if (!data?.writable) return;
    const entries = Array.from(e.dataTransfer.items).map((i) => i.webkitGetAsEntry?.()).filter(Boolean) as FileSystemEntry[];
    if (!entries.some((x) => x.isDirectory)) return doUpload(e.dataTransfer.files);
    const out: { file: File; rel: string }[] = [];
    const walk = async (entry: FileSystemEntry, prefix: string): Promise<void> => {
      if (entry.isFile) {
        const file = await new Promise<File>((res, rej) => (entry as FileSystemFileEntry).file(res, rej));
        out.push({ file, rel: prefix + entry.name });
      } else if (entry.isDirectory) {
        const reader = (entry as FileSystemDirectoryEntry).createReader();
        // readEntries returns batches: call it until it returns nothing.
        for (;;) {
          const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej));
          if (!batch.length) break;
          for (const child of batch) await walk(child, `${prefix}${entry.name}/`);
        }
      }
    };
    for (const entry of entries) await walk(entry, "");
    doUpload(out);
  };

  const crumbs = path === ROOT ? [] : path.slice(ROOT.length + 1).split("/");
  const entries = data?.entries ?? [];
  const allSel = entries.length > 0 && sel.length === entries.length;
  const download = (p: string) => { window.location.href = `/api/files/download${q({ path: p })}`; };

  return (
    <div onDragOver={(e) => { e.preventDefault(); if (data?.writable) setDrag(true); }} onDragLeave={() => setDrag(false)} onDrop={onDrop}>
      <PageHeader title={tr("Files")} subtitle={tr("Browse and manage the files on your volumes")} icon={<FolderOpen size={20} />} actions={<>
        <Button variant="ghost" icon={hidden ? <EyeOff size={15} /> : <Eye size={15} />} onClick={() => setHidden(!hidden)}>{hidden ? tr("Hide hidden") : tr("Hidden files")}</Button>
        <Button icon={<FolderPlus size={15} />} disabled={!data?.writable} onClick={() => setMkdir("")}>{tr("Folder")}</Button>
        <Button variant="primary" icon={<Upload size={15} />} disabled={!data?.writable} onClick={() => fileInput.current?.click()}>{tr("Send")}</Button>
        <input ref={fileInput} type="file" multiple hidden onChange={(e) => { if (e.target.files) doUpload(e.target.files); e.target.value = ""; }} />
        <Button icon={<FolderUp size={15} />} disabled={!data?.writable} onClick={() => folderInput.current?.click()}>{tr("Upload a folder")}</Button>
        <input ref={folderInput} type="file" multiple hidden {...{ webkitdirectory: "" }} onChange={(e) => { if (e.target.files) doUpload(e.target.files); e.target.value = ""; }} />
      </>} />

      {!!myQuotas.data?.length && (
        <div className="mb-4 flex flex-wrap gap-3">
          {myQuotas.data.map((q) => (
            <div key={q.kind + q.target} className="card min-w-[220px] flex-1 px-4 py-2.5 text-[12.5px]">
              <div className="mb-1.5 flex justify-between"><span>{tr("My space on")}{" "}{q.name}</span><span className="text-muted">{bytes(q.used)} / {bytes(q.limit)}</span></div>
              <Progress value={pct(q.used, q.limit)} />
            </div>
          ))}
        </div>
      )}
      <div className="mb-4 flex flex-wrap items-center gap-1 text-[13px]">
        <button onClick={() => go(ROOT)} className="flex items-center gap-1.5 rounded-lg px-2 py-1 hover:bg-panel-2"><HardDrive size={15} /> {" "}{tr("Storage")}</button>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight size={14} className="text-muted" />
            <button onClick={() => go(`${ROOT}/${crumbs.slice(0, i + 1).join("/")}`)} className="rounded-lg px-2 py-1 hover:bg-panel-2">{c}</button>
          </span>
        ))}
      </div>

      {upload && (
        <div className="card mb-4 p-4">
          <div className="mb-2 flex justify-between text-[13px]"><span>{tr("Uploading")}{" "}{upload.name}</span><span>{upload.pct.toFixed(0)} %</span></div>
          <Progress value={upload.pct} />
        </div>
      )}

      {sel.length > 0 && (
        <div className="card mb-4 flex items-center gap-3 px-4 py-2.5 text-[13px]">
          <span>{sel.length} {" "}{tr("item(s) selected")}</span>
          <div className="flex-1" />
          {sel.length === 1 && <Button size="sm" variant="ghost" icon={<Download size={14} />} onClick={() => download(sel[0])}>{tr("Download")}</Button>}
          {data?.writable && <Button size="sm" variant="danger" icon={<Trash2 size={14} />} onClick={async () => { if (await confirm({ title: tr("Delete permanently?"), text: tr("{0} item(s) will be deleted. This action is irreversible (the recycle bin only applies to SMB).", sel.length), danger: true })) del.mutate(sel); }}>{tr("Delete")}</Button>}
        </div>
      )}

      <ErrorBox error={error} />
      <Card pad={false} className={cx(drag && "ring-2 ring-accent")}>
        {isLoading ? <Spinner /> : entries.length === 0 ? (
          <Empty icon={<Folder />} title={path === ROOT ? tr("No volumes") : tr("Empty folder")} text={data?.writable ? tr("Drag and drop files here to upload them.") : undefined} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table">
              <thead>
                <tr>
                  <th className="w-10"><Checkbox checked={allSel} onChange={(v) => setSel(v ? entries.map((e) => e.path) : [])} /></th>
                  <th>{tr("Name")}</th><th>{tr("Size")}</th><th>{tr("Modified")}</th><th className="hidden md:table-cell">{tr("Permissions")}</th><th />
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <tr key={e.path} onDoubleClick={() => (e.dir ? go(e.path) : previewable(e.name) ? setPreview(e) : download(e.path))}>
                    <td><Checkbox checked={sel.includes(e.path)} onChange={(v) => setSel(v ? [...sel, e.path] : sel.filter((x) => x !== e.path))} /></td>
                    <td>
                      <button className="flex items-center gap-2.5 text-left" onClick={() => (e.dir ? go(e.path) : previewable(e.name) ? setPreview(e) : download(e.path))}>
                        {icon(e)}<span className="hover:text-accent">{e.name}</span>
                      </button>
                    </td>
                    <td className="text-muted">{e.dir ? "—" : bytes(e.size)}</td>
                    <td className="text-muted text-[12.5px]">{dateTime(e.modTime)}</td>
                    <td className="mono hidden text-muted md:table-cell">{e.mode}</td>
                    <td className="text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" icon={<Download size={14} />} onClick={() => download(e.path)} title={tr("Download")} />
                      {!e.dir && path !== ROOT && <Button size="sm" variant="ghost" icon={<History size={14} />} onClick={() => setVersions({ path: e.path, name: e.name })} title={tr("Previous versions")} />}
                      {path !== ROOT && <Button size="sm" variant="ghost" icon={<Link2 size={14} />} onClick={() => setShare({ path: e.path, name: e.name, dir: e.dir })} title={tr("Share a link")} />}
                      {data?.writable && path !== ROOT && <Button size="sm" variant="ghost" icon={<Pencil size={14} />} onClick={() => setRename({ from: e.path, name: e.name })} title={tr("Rename")} />}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {versions && <VersionsModal path={versions.path} name={versions.name} onClose={() => setVersions(null)} />}
      {share && <ShareLinkModal path={share.path} name={share.name} dir={share.dir} onClose={() => setShare(null)} />}
      <Modal open={mkdir !== null} onClose={() => setMkdir(null)} title={tr("New folder")} footer={<>
        <Button onClick={() => setMkdir(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!mkdir} loading={mk.isPending} onClick={() => mk.mutate(`${path}/${mkdir}`)}>{tr("Create")}</Button>
      </>}>
        <Field label={tr("Name")}><Input autoFocus value={mkdir ?? ""} onChange={(e) => setMkdir(e.target.value.replace(/\//g, ""))} onKeyDown={(e) => e.key === "Enter" && mkdir && mk.mutate(`${path}/${mkdir}`)} /></Field>
      </Modal>
      <Modal open={!!rename} onClose={() => setRename(null)} title={tr("Rename")} footer={<>
        <Button onClick={() => setRename(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!rename?.name} loading={mv.isPending} onClick={() => mv.mutate({ from: rename!.from, to: `${path}/${rename!.name}` })}>{tr("Rename")}</Button>
      </>}>
        {rename && <Field label={tr("New name")}><Input autoFocus value={rename.name} onChange={(e) => setRename({ ...rename, name: e.target.value.replace(/\//g, "") })} /></Field>}
      </Modal>
      <Modal open={!!preview} onClose={() => setPreview(null)} title={preview?.name} wide="xl" footer={preview && <Button icon={<Download size={15} />} onClick={() => download(preview.path)}>{tr("Download")}</Button>}>
        {preview && <Preview e={preview} />}
      </Modal>
    </div>
  );
}

function Preview({ e }: { e: FileEntry }) {
  const url = `/api/files/download${q({ path: e.path, inline: 1 })}`;
  const ext = e.name.split(".").pop()?.toLowerCase() ?? "";
  const text = useQuery({ queryKey: ["preview", e.path], queryFn: () => fetch(url).then((r) => r.text()), enabled: /^(txt|md|log|json|ya?ml|conf|csv|xml|ini|sh)$/.test(ext) && e.size < 2 << 20 });
  if (/^(jpe?g|png|gif|webp|svg|bmp)$/.test(ext)) return <img src={url} alt={e.name} className="mx-auto max-h-[70vh] rounded-lg" />;
  if (/^(mp4|webm)$/.test(ext)) return <video src={url} controls className="mx-auto max-h-[70vh] w-full rounded-lg" />;
  if (/^(mp3|ogg|wav)$/.test(ext)) return <audio src={url} controls className="w-full" />;
  if (ext === "pdf") return <iframe src={url} title={e.name} className="h-[70vh] w-full rounded-lg border border-line" />;
  if (text.isLoading) return <Spinner />;
  if (text.data !== undefined) return <pre className="mono max-h-[70vh] overflow-auto whitespace-pre-wrap rounded-xl bg-panel-2 p-4">{text.data}</pre>;
  return <div className="text-muted">{tr("Preview unavailable (file too large).")}</div>;
}
