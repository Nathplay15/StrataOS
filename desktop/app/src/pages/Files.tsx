// File browser of the NAS: browse and search, upload files and folders of
// this computer (picked, or dropped on the window), download, rename, copy
// and move, delete, share links and file requests, previous versions,
// previews of pictures and playback of audio and video. Transfers run in
// the agent (Transfers page), so they go on with the window closed.
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getCurrentWebview } from "@tauri-apps/api/webview";
import { invoke } from "@tauri-apps/api/core";
import { open, save } from "@tauri-apps/plugin-dialog";
import { ArrowUp, ChevronRight, Copy, Download, File, FileImage, FileVideo, Folder, FolderInput, FolderPlus, FolderUp, History, Link2, Lock, Pencil, RefreshCw, Search, Trash2, Upload, X } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Spinner, useUI } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { bytes, cx, dateTime } from "@nas/lib/format";
import type { FileEntry } from "@nas/lib/types";
import { fileKind } from "@nas/lib/filetypes";
import { agent, nasURL } from "../lib/agent";
import { api, currentAccount, q } from "../lib/nasapi";
import { t } from "../lib/i18n";
import { useNav, type PageProps } from "../lib/nav";
import { PageHeader, downloadToComputer } from "../components/kit";
import { ShareDialog } from "./Links";

const ROOT = "/srv/strata";
type Listing = { path: string; entries: FileEntry[]; writable: boolean; encrypted?: boolean };

const parent = (p: string) => (p === ROOT || !p.startsWith(ROOT + "/") ? ROOT : p.slice(0, p.lastIndexOf("/")) || ROOT);
const baseName = (p: string) => p.slice(p.lastIndexOf("/") + 1);
const viewable = (name: string) => ["image", "video", "audio"].includes(fileKind(name));

export function FilesPage({ params }: PageProps) {
  const { toast, confirm } = useUI();
  const { go } = useNav();
  const [path, setPath] = useState(params.path || ROOT);
  const [sel, setSel] = useState<string[]>(params.select ? [params.select] : []);
  const [search, setSearch] = useState("");
  const [found, setFound] = useState("");
  const [mkdir, setMkdir] = useState<string | null>(null);
  const [rename, setRename] = useState<FileEntry | null>(null);
  const [moving, setMoving] = useState<{ paths: string[]; move: boolean } | null>(null);
  const [share, setShare] = useState<FileEntry | null>(null);
  const [versions, setVersions] = useState<FileEntry | null>(null);
  const [preview, setPreview] = useState<FileEntry | null>(null);
  const [drop, setDrop] = useState(false);
  useEffect(() => {
    if (params.path) setPath(params.path);
    if (params.select) setSel([params.select]);
  }, [params.path, params.select]);

  const list = useQuery({ queryKey: ["files", path], queryFn: () => api.get<Listing>(`/api/files${q({ path })}`) });
  const results = useQuery({ queryKey: ["files-search", path, found], queryFn: () => api.get<Listing & { truncated: boolean }>(`/api/files/search${q({ path, q: found })}`), enabled: found.length >= 2 });
  const entries = useMemo(() => {
    const l = [...((found ? results.data?.entries : list.data?.entries) ?? [])];
    return l.sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
  }, [found, results.data, list.data]);
  const writable = !!list.data?.writable && !list.data.encrypted && path !== ROOT;
  const chosen = entries.filter((e) => sel.includes(e.path));
  const one = chosen.length === 1 ? chosen[0] : null;

  const del = useAction((paths: string[]) => api.post("/api/files/delete", { paths }), { success: t("Deleted"), onDone: () => setSel([]) });
  const mk = useAction((p: string) => api.post("/api/files/mkdir", { path: p }), { success: t("Folder created"), onDone: () => setMkdir(null) });
  const mv = useAction((p: { from: string; to: string }) => api.post("/api/files/rename", p), { success: t("Renamed"), onDone: () => setRename(null) });
  const cp = useAction((p: { paths: string[]; to: string; move: boolean }) => api.post("/api/files/copy", p), { onDone: () => { setMoving(null); setSel([]); } });

  const cd = (p: string) => { setPath(p); setSel([]); setFound(""); setSearch(""); };

  async function upload(paths: string[]) {
    if (!paths.length) return;
    try {
      const names = new Set(entries.map((e) => e.name));
      const clash = paths.map((p) => baseName(p.replace(/\\/g, "/"))).filter((n) => names.has(n));
      const overwrite = clash.length > 0 && await confirm({ title: t("Replace existing files?"), text: t("{0} already exist in this folder. Replace them? Otherwise they are skipped.", clash.slice(0, 5).join(", ")) });
      await agent.post(`/v1/accounts/${encodeURIComponent(currentAccount())}/upload`, { paths, folder: path, overwrite });
      toast(t("Upload started: see Transfers"), "info");
    } catch (e) {
      toast(String((e as Error).message ?? e), "error");
    }
  }

  async function download(list: FileEntry[]) {
    for (const e of list) {
      try {
        if (await downloadToComputer(e.path, e.name, e.dir, e.size)) toast(t("Download started: see Transfers"), "info");
      } catch (x) {
        toast(String((x as Error).message ?? x), "error");
      }
    }
  }

  // Files and folders dropped on the window from the file manager.
  useEffect(() => {
    let off: (() => void) | undefined;
    let gone = false;
    void getCurrentWebview().onDragDropEvent((ev) => {
      const p = ev.payload;
      if (p.type === "over" || p.type === "enter") setDrop(true);
      else if (p.type === "leave") setDrop(false);
      else if (p.type === "drop") {
        setDrop(false);
        if (writable) void upload(p.paths);
        else toast(t("This folder is read-only"), "error");
      }
    }).then((f) => { if (gone) f(); else off = f; });
    return () => { gone = true; off?.(); };
  });

  const crumbs = path === ROOT ? [] : path.slice(ROOT.length + 1).split("/");
  return (
    <div className={cx(drop && writable && "rounded-xl outline-2 outline-dashed outline-accent")}>
      <PageHeader title={t("Files")} actions={<>
        <form className="relative" onSubmit={(e) => { e.preventDefault(); setFound(search.trim()); }}>
          <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
          <Input value={search} onChange={(e) => { setSearch(e.target.value); if (!e.target.value) setFound(""); }} placeholder={t("Search")} className="w-[220px] !pl-8" />
        </form>
        <IconButton title={t("Refresh")} onClick={() => void list.refetch()}><RefreshCw size={15} /></IconButton>
      </>} />

      <div className="mb-3 flex flex-wrap items-center gap-1 text-[13px]">
        <IconButton title={t("Parent folder")} disabled={path === ROOT} onClick={() => cd(parent(path))}><ArrowUp size={15} /></IconButton>
        <button className="rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => cd(ROOT)}>{t("Shared folders")}</button>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight size={13} className="text-muted" />
            <button className="rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => cd(ROOT + "/" + crumbs.slice(0, i + 1).join("/"))}>{c}</button>
          </span>
        ))}
        {list.data?.encrypted && <Badge tone="ok"><Lock size={11} className="mr-1 inline" />{t("End-to-end encrypted")}</Badge>}
      </div>

      <div className="mb-3 flex flex-wrap gap-2">
        <Button size="sm" icon={<Upload size={14} />} disabled={!writable} onClick={async () => {
          const r = await open({ multiple: true, title: t("Upload files") });
          if (r) void upload(Array.isArray(r) ? r : [r]);
        }}>{t("Upload files")}</Button>
        <Button size="sm" icon={<FolderUp size={14} />} disabled={!writable} onClick={async () => {
          const r = await open({ directory: true, multiple: true, title: t("Upload a folder") });
          if (r) void upload(Array.isArray(r) ? r : [r]);
        }}>{t("Upload a folder")}</Button>
        <Button size="sm" icon={<FolderPlus size={14} />} disabled={!writable} onClick={() => setMkdir("")}>{t("New folder")}</Button>
        {chosen.length > 0 && <>
          <span className="mx-1 w-px bg-line" />
          <Button size="sm" icon={<Download size={14} />} onClick={() => void download(chosen)}>{t("Download")}</Button>
          {one && !one.encrypted && <Button size="sm" icon={<Link2 size={14} />} onClick={() => setShare(one)}>{t("Share")}</Button>}
          {one && !one.dir && <Button size="sm" icon={<History size={14} />} onClick={() => setVersions(one)}>{t("Versions")}</Button>}
          {one && writable && <Button size="sm" icon={<Pencil size={14} />} onClick={() => setRename(one)}>{t("Rename")}</Button>}
          <Button size="sm" icon={<Copy size={14} />} onClick={() => setMoving({ paths: sel, move: false })}>{t("Copy to…")}</Button>
          {writable && <Button size="sm" icon={<FolderInput size={14} />} onClick={() => setMoving({ paths: sel, move: true })}>{t("Move to…")}</Button>}
          {writable && <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => {
            if (await confirm({ title: t("Delete?"), text: t("{0} item(s) go to the recycle bin of the shared folder when it has one, otherwise they are deleted.", chosen.length), danger: true })) del.mutate(sel);
          }}>{t("Delete")}</Button>}
          <IconButton title={t("Clear selection")} onClick={() => setSel([])}><X size={15} /></IconButton>
        </>}
      </div>

      <ErrorBox error={list.error || results.error} />
      <Card pad={false}>
        {(found ? results.isLoading : list.isLoading) ? <Spinner /> : !entries.length ? (
          <Empty icon={<Folder />} title={found ? t("No results") : t("This folder is empty")} text={writable ? t("Drop files here to upload them.") : undefined} />
        ) : (
          <table className="table">
            <thead><tr><th className="w-8" /><th>{t("Name")}</th><th>{t("Modified")}</th><th className="text-right">{t("Size")}</th></tr></thead>
            <tbody>
              {entries.map((e) => {
                const on = sel.includes(e.path);
                const kind = e.dir ? "dir" : fileKind(e.name);
                const Icon = e.dir ? Folder : kind === "image" ? FileImage : kind === "video" ? FileVideo : File;
                return (
                  <tr key={e.path} className={cx("cursor-default", on && "[&>td]:!bg-accent-soft")}
                    onClick={(ev) => setSel(ev.ctrlKey || ev.metaKey ? (on ? sel.filter((x) => x !== e.path) : [...sel, e.path]) : [e.path])}
                    onDoubleClick={() => (e.dir ? cd(e.path) : viewable(e.name) && !e.encrypted ? setPreview(e) : void download([e]))}>
                    <td><input type="checkbox" checked={on} onClick={(ev) => ev.stopPropagation()} onChange={() => setSel(on ? sel.filter((x) => x !== e.path) : [...sel, e.path])} /></td>
                    <td>
                      <div className="flex items-center gap-2.5">
                        {e.encrypted ? <Lock size={16} className="text-ok" /> : <Icon size={16} className={e.dir ? "text-accent" : "text-muted"} />}
                        <span className="truncate">{e.name}</span>
                        {found && <span className="truncate text-[11.5px] text-muted">{parent(e.path).slice(ROOT.length)}</span>}
                      </div>
                    </td>
                    <td className="whitespace-nowrap text-[12.5px] text-muted">{dateTime(e.modTime)}</td>
                    <td className="whitespace-nowrap text-right text-[12.5px] text-muted">{e.dir ? "" : bytes(e.size)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>
      {found && results.data?.truncated && <p className="mt-2 text-[12px] text-muted">{t("Only the first results are shown.")}</p>}
      <div className="mt-4 text-right"><Button size="sm" variant="ghost" onClick={() => go("transfers")}>{t("Transfers")} →</Button></div>

      {mkdir !== null && (
        <Modal open onClose={() => setMkdir(null)} title={t("New folder")} footer={<>
          <Button onClick={() => setMkdir(null)}>{t("Cancel")}</Button>
          <Button variant="primary" loading={mk.isPending} disabled={!mkdir.trim() || mkdir.includes("/")} onClick={() => mk.mutate(`${path}/${mkdir.trim()}`)}>{t("Create")}</Button>
        </>}>
          <Field label={t("Name")}><Input autoFocus value={mkdir} onChange={(e) => setMkdir(e.target.value)} /></Field>
        </Modal>
      )}
      {rename && <RenameModal entry={rename} pending={mv.isPending} onClose={() => setRename(null)} onSave={(name) => mv.mutate({ from: rename.path, to: `${parent(rename.path)}/${name}` })} />}
      {moving && <FolderChooser start={path} title={moving.move ? t("Move to…") : t("Copy to…")} pending={cp.isPending} onClose={() => setMoving(null)} onChoose={(to) => cp.mutate({ paths: moving.paths, to, move: moving.move })} />}
      {share && <ShareDialog path={share.path} dir={share.dir} name={share.name} onClose={() => setShare(null)} />}
      {versions && <VersionsModal entry={versions} onClose={() => setVersions(null)} />}
      {preview && <Preview entry={preview} all={entries.filter((e) => !e.dir && !e.encrypted && viewable(e.name))} onClose={() => setPreview(null)} />}
    </div>
  );
}

function RenameModal({ entry, pending, onClose, onSave }: { entry: FileEntry; pending: boolean; onClose: () => void; onSave: (name: string) => void }) {
  const [name, setName] = useState(entry.name);
  const ok = !!name.trim() && !name.includes("/") && name !== entry.name;
  return (
    <Modal open onClose={onClose} title={t("Rename")} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={pending} disabled={!ok} onClick={() => onSave(name.trim())}>{t("Rename")}</Button>
    </>}>
      <Field label={t("Name")}><Input autoFocus value={name} onChange={(e) => setName(e.target.value)} /></Field>
    </Modal>
  );
}

/** Choosing a NAS folder (copy and move). */
function FolderChooser({ start, title, pending, onClose, onChoose }: { start: string; title: string; pending: boolean; onClose: () => void; onChoose: (p: string) => void }) {
  const [p, setP] = useState(start);
  const list = useQuery({ queryKey: ["files", p], queryFn: () => api.get<Listing>(`/api/files${q({ path: p })}`) });
  return (
    <Modal open onClose={onClose} title={title} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={pending} disabled={p === ROOT || !list.data?.writable} onClick={() => onChoose(p)}>{t("Choose this folder")}</Button>
    </>}>
      <div className="mb-2 flex items-center gap-2">
        <IconButton title={t("Parent folder")} disabled={p === ROOT} onClick={() => setP(parent(p))}><ArrowUp size={15} /></IconButton>
        <span className="mono truncate text-[12.5px] text-muted">{p.slice(ROOT.length) || "/"}</span>
      </div>
      <div className="max-h-[320px] overflow-y-auto rounded-xl border border-line p-1.5">
        {list.isLoading ? <Spinner /> : <ErrorBox error={list.error} />}
        {list.data?.entries.filter((e) => e.dir && !e.encrypted).map((e) => (
          <button key={e.path} onClick={() => setP(e.path)} className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-[13px] hover:bg-panel-2">
            <Folder size={15} className="text-accent" />{e.name}
          </button>
        ))}
      </div>
    </Modal>
  );
}

type Version = { snapshot: string; time: string; size: number; modTime: string };

/** Earlier versions of a file, from the snapshots of its volume. */
function VersionsModal({ entry, onClose }: { entry: FileEntry; onClose: () => void }) {
  const { toast, confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["versions", entry.path], queryFn: () => api.get<{ versions: Version[]; writable: boolean }>(`/api/files/versions${q({ path: entry.path })}`) });
  const restore = useAction((v: { snapshot: string; replace: boolean }) => api.post<{ path: string }>("/api/files/versions/restore", { path: entry.path, ...v }), { success: (r) => t("Version restored: {0}", baseName(r.path)) });
  // A version is fetched by the window and written by the shell (no agent route for it).
  const download = async (v: Version) => {
    const dest = await save({ defaultPath: entry.name });
    if (!dest) return;
    try {
      const res = await fetch(nasURL(currentAccount(), "/api/files/versions/download", { path: entry.path, snapshot: v.snapshot }));
      if (!res.ok) throw new Error(res.statusText);
      await invoke("save_bytes", { path: dest, data: Array.from(new Uint8Array(await res.arrayBuffer())) });
      toast(t("Saved"));
    } catch (e) {
      toast(String((e as Error).message ?? e), "error");
    }
  };
  return (
    <Modal open wide onClose={onClose} title={t("Versions of “{0}”", entry.name)} footer={<Button onClick={onClose}>{t("Close")}</Button>}>
      <ErrorBox error={error || restore.error} />
      {isLoading ? <Spinner /> : !data?.versions.length ? (
        <Empty icon={<History />} title={t("No previous versions")} text={t("Versions come from volume snapshots (Btrfs or ZFS): schedule snapshots in Scheduled Tasks.")} />
      ) : (
        <table className="table">
          <thead><tr><th>{t("Snapshot of")}</th><th>{t("Modified on")}</th><th>{t("Size")}</th><th /></tr></thead>
          <tbody>
            {data.versions.map((v) => (
              <tr key={v.snapshot}>
                <td>{dateTime(v.time)}</td>
                <td className="text-[12.5px] text-muted">{dateTime(v.modTime)}</td>
                <td className="text-muted">{bytes(v.size)}</td>
                <td className="whitespace-nowrap text-right">
                  <Button size="sm" variant="ghost" icon={<Download size={14} />} title={t("Download")} onClick={() => void download(v)} />
                  {data.writable && <>
                    <Button size="sm" variant="ghost" onClick={() => restore.mutate({ snapshot: v.snapshot, replace: false })}>{t("Restore a copy")}</Button>
                    <Button size="sm" variant="ghost" title={t("Replace the current file")} onClick={async () => {
                      if (await confirm({ title: t("Replace the file?"), text: t("The current file is replaced by the version of {0}. It remains available in more recent snapshots.", dateTime(v.time)), danger: true })) restore.mutate({ snapshot: v.snapshot, replace: true });
                    }}>{t("Replace")}</Button>
                  </>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Modal>
  );
}

/** Pictures, videos and music of the folder, one at a time. */
function Preview({ entry, all, onClose }: { entry: FileEntry; all: FileEntry[]; onClose: () => void }) {
  const [i, setI] = useState(Math.max(0, all.findIndex((e) => e.path === entry.path)));
  const e = all[i] ?? entry;
  const kind = fileKind(e.name);
  const acc = currentAccount();
  useEffect(() => {
    const key = (ev: KeyboardEvent) => {
      if (ev.key === "ArrowRight") setI((x) => Math.min(all.length - 1, x + 1));
      if (ev.key === "ArrowLeft") setI((x) => Math.max(0, x - 1));
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [all.length]);
  return (
    <Modal open wide="xl" onClose={onClose} title={e.name} footer={<>
      <Button disabled={i <= 0} onClick={() => setI(i - 1)}>←</Button>
      <Button disabled={i >= all.length - 1} onClick={() => setI(i + 1)}>→</Button>
      <Button icon={<Download size={14} />} onClick={() => void downloadToComputer(e.path, e.name, false, e.size)}>{t("Download")}</Button>
    </>}>
      <div className="flex min-h-[300px] items-center justify-center rounded-lg bg-black/80">
        {kind === "image" ? <img src={nasURL(acc, "/api/files/image", { path: e.path, size: 2560 })} alt={e.name} className="max-h-[70vh] max-w-full object-contain" />
          : kind === "video" ? <video key={e.path} src={nasURL(acc, "/api/files/download", { path: e.path, inline: 1 })} controls autoPlay className="max-h-[70vh] max-w-full" />
            : <audio key={e.path} src={nasURL(acc, "/api/files/download", { path: e.path, inline: 1 })} controls autoPlay />}
      </div>
      {kind === "video" && <p className="mt-2 text-[12px] text-muted">{t("Videos play as stored on the NAS: a format this computer cannot read plays in the web interface (Media).")}</p>}
    </Modal>
  );
}
