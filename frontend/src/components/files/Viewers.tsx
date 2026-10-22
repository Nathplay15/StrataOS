// Viewers that need no heavy library: file details, archive contents,
// documents converted to PDF by the NAS, the end of large logs.
import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Archive, FileText, Folder, Hash, RefreshCw } from "lucide-react";
import { ApiError, api, q } from "../../lib/api";
import type { FileEntry } from "../../lib/types";
import { bytes, dateTime } from "../../lib/format";
import { tr } from "../../lib/i18n";
import { useAction } from "../../lib/hooks";
import { fileExt } from "../../lib/filetypes";
import { Badge, Button, Checkbox, ErrorBox, Input, Notice, Spinner } from "../ui";
import { Toolbar, ViewerCard, download, fetchBytes, tabFor, usePanel, useViewerCaps } from "./common";

const PdfViewer = lazy(() => import("./PdfViewer"));
const CodeEditor = lazy(() => import("./CodeEditor"));

interface Info {
  name: string; path: string; size: number; mode: string; modTime: string; changed?: string; owner?: string; group?: string;
  mime?: string; text?: boolean; dir: boolean; link: boolean; writable: boolean; archive: string;
}

export const useFileInfo = (path: string, modTime: string) => useQuery({ queryKey: ["file-info", path, modTime], queryFn: () => api.get<Info>(`/api/files/info${q({ path })}`), retry: false });

/* ---------- Details ---------- */

export function Details({ entry, note }: { entry: FileEntry; note?: string }) {
  const { office, onOpen } = usePanel();
  const info = useFileInfo(entry.path, entry.modTime);
  const [hash, setHash] = useState<string | null>(null);
  const sum = useAction(() => api.get<{ sha256: string }>(`/api/files/hash${q({ path: entry.path })}`), { onDone: (r) => setHash(r.sha256) });
  const i = info.data;
  const ext = fileExt(entry.name);
  const canOffice = office?.enabled && office.types[ext];
  return (
    <>
      <Toolbar entry={entry} />
      <ViewerCard>
        <div className="flex items-center gap-2 text-[15px] font-semibold"><FileText size={18} className="text-muted" /> <span className="break-all">{entry.name}</span></div>
        {note && <Notice>{note}</Notice>}
        <ErrorBox error={info.error} />
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
          <dt className="text-muted">{tr("Size")}</dt><dd>{bytes(i?.size ?? entry.size)} <span className="text-muted">({(i?.size ?? entry.size).toLocaleString()} {tr("bytes")})</span></dd>
          {i?.mime && <><dt className="text-muted">{tr("Type")}</dt><dd className="mono">{i.mime}{i.text ? ` · ${tr("text")}` : ""}</dd></>}
          <dt className="text-muted">{tr("Modified")}</dt><dd>{dateTime(entry.modTime)}</dd>
          {i?.changed && <><dt className="text-muted">{tr("Changed")}</dt><dd>{dateTime(i.changed)}</dd></>}
          {i?.owner && <><dt className="text-muted">{tr("Owner")}</dt><dd className="mono">{i.owner}:{i.group}</dd></>}
          <dt className="text-muted">{tr("Permissions")}</dt><dd className="mono">{i?.mode ?? entry.mode}</dd>
          <dt className="text-muted">{tr("Location")}</dt><dd className="mono break-all">{entry.path}</dd>
          <dt className="text-muted">SHA-256</dt>
          <dd>{hash ? <span className="mono text-[11.5px] break-all select-all">{hash}</span> : <Button size="sm" variant="ghost" icon={<Hash size={13} />} loading={sum.isPending} onClick={() => sum.mutate()}>{tr("Compute")}</Button>}</dd>
        </dl>
        <div className="flex flex-wrap gap-2 pt-2">
          <Button variant="primary" onClick={() => download(entry.path)}>{tr("Download")}</Button>
          {canOffice && <Button onClick={() => onOpen(tabFor(entry, "office"))}>{tr("Open in OnlyOffice")}</Button>}
          {i?.text && <Button variant="ghost" onClick={() => onOpen(tabFor(entry, "text"))}>{tr("Open as text")}</Button>}
        </div>
      </ViewerCard>
    </>
  );
}

/** Unknown types: shown as text when the content is text, else the details. */
export function OtherPanel({ entry, renderText }: { entry: FileEntry; renderText: () => React.ReactNode }) {
  const info = useFileInfo(entry.path, entry.modTime);
  if (info.isLoading) return <><Toolbar entry={entry} /><Spinner /></>;
  if (info.data?.text) return <>{renderText()}</>;
  return <Details entry={entry} />;
}

/* ---------- End of large text files ---------- */

export function TailPanel({ entry }: { entry: FileEntry }) {
  const [size, setSize] = useState(256 << 10);
  const tail = useQuery({ queryKey: ["tail", entry.path, size], queryFn: () => api.get<{ content: string; offset: number; size: number }>(`/api/files/tail${q({ path: entry.path, bytes: size })}`) });
  const t = tail.data;
  return (
    <>
      <Toolbar entry={entry}>
        <Badge tone="warn">{tr("Read only")}</Badge>
        {t && <span className="text-[12px] text-muted">{tr("Last {0} of {1}", bytes(t.size - t.offset), bytes(t.size))}</span>}
        {t && t.offset > 0 && size < 8 << 20 && <Button size="sm" variant="ghost" onClick={() => setSize(size * 4)}>{tr("Show more")}</Button>}
        <Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} title={tr("Reload")} onClick={() => tail.refetch()} />
      </Toolbar>
      <ErrorBox error={tail.error} />
      {!t ? <Spinner /> : (
        <div className="min-h-0 flex-1">
          <Suspense fallback={<Spinner />}>
            {/* Starts at the end: the newest lines of a log. */}
            <CodeEditor key={`${size}-${t.size}`} value={t.content} lang="" readOnly onChange={() => {}} onSave={() => {}} scrollToEnd />
          </Suspense>
        </div>
      )}
    </>
  );
}

/* ---------- Documents converted to PDF by the NAS ---------- */

export function DocumentPanel({ entry }: { entry: FileEntry }) {
  const caps = useViewerCaps();
  const { office, onOpen } = usePanel();
  const [data, setData] = useState<ArrayBuffer | null>(null);
  const [error, setError] = useState<unknown>(null);
  const installed = caps.data?.docPreview;
  useEffect(() => {
    if (!installed) return;
    const ac = new AbortController();
    setData(null);
    setError(null);
    fetchBytes(`/api/files/docpreview${q({ path: entry.path })}`, ac.signal).then(setData, (e) => { if (!ac.signal.aborted) setError(e); });
    return () => ac.abort();
  }, [entry.path, entry.modTime, installed]);
  const source = useMemo(() => (data ? { data } : null), [data]);
  const install = useAction(() => api.post("/api/system/components/docpreview/install"));
  const canOffice = office?.enabled && office.types[fileExt(entry.name)];
  if (caps.isLoading) return <><Toolbar entry={entry} /><Spinner /></>;
  if (!installed || (error instanceof ApiError && error.status === 409)) {
    return (
      <>
        <Toolbar entry={entry} />
        <ViewerCard>
          <div className="font-semibold">{tr("No preview for this document yet")}</div>
          {caps.data?.admin ? (
            <>
              <p className="text-muted">{tr("Install document previews: LibreOffice runs on the NAS (without interface) and shows Word, Excel, PowerPoint and OpenDocument files as PDF. Download size: {0}.", caps.data.docPreviewSize)}</p>
              <Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install document previews")}</Button>
            </>
          ) : <p className="text-muted">{tr("Ask your administrator to install document previews (Settings → Components) to read this document here.")}</p>}
          <div className="flex flex-wrap gap-2 pt-1">
            <Button onClick={() => download(entry.path)}>{tr("Download")}</Button>
            {canOffice && <Button onClick={() => onOpen(tabFor(entry, "office"))}>{tr("Open in OnlyOffice")}</Button>}
          </div>
        </ViewerCard>
      </>
    );
  }
  if (error) return <><Toolbar entry={entry} /><ViewerCard><div className="font-semibold">{tr("This document could not be converted")}</div><ErrorBox error={error} /><Button onClick={() => download(entry.path)}>{tr("Download")}</Button></ViewerCard></>;
  if (!source) return <><Toolbar entry={entry} /><Spinner label={tr("Converting the document…")} /></>;
  return (
    <Suspense fallback={<Spinner />}>
      <PdfViewer source={source} entry={entry} posKey={`pdf:${entry.path}`} pdfName={entry.name.replace(/\.[^.]+$/, "") + ".pdf"} />
    </Suspense>
  );
}

/* ---------- Archives ---------- */

interface ArchiveEntry { name: string; size: number; packed?: number; dir: boolean; modTime?: string; skipped?: string; encrypted?: boolean }
interface Listing { format: string; entries: ArchiveEntry[]; files: number; total: number; truncated: boolean }

const skippedLabel = (s: string) => ({ link: tr("link, not extracted"), "special file": tr("special file, not extracted"), "unsafe name": tr("unsafe name, not extracted"), encrypted: tr("encrypted") } as Record<string, string>)[s] ?? s;

export function ArchivePanel({ entry }: { entry: FileEntry }) {
  const caps = useViewerCaps();
  const list = useQuery({ queryKey: ["archive", entry.path, entry.modTime], queryFn: () => api.get<Listing>(`/api/files/archive${q({ path: entry.path })}`), retry: false });
  const info = useFileInfo(entry.path, entry.modTime);
  const [filter, setFilter] = useState("");
  const [sel, setSel] = useState<string[]>([]);
  const extract = useAction((entries: string[]) => api.post("/api/files/archive/extract", { path: entry.path, entries }));
  const l = list.data;
  const shown = useMemo(() => (l?.entries ?? []).filter((e) => !filter || e.name.toLowerCase().includes(filter.toLowerCase())).slice(0, 5000), [l, filter]);
  const files = shown.filter((e) => !e.dir && !e.skipped);
  const needs7z = /^(7z|rar)$/.test(l?.format ?? (/\.(7z|rar|cbr)$/i.test(entry.name) ? "7z" : "")) && caps.data && !caps.data.sevenZip;
  const writable = info.data?.writable ?? false;
  return (
    <>
      <Toolbar entry={entry}>
        {l && <span className="text-[12px] text-muted">{tr("{0} files, {1}", l.files, bytes(l.total))}</span>}
        {l && writable && <Button size="sm" variant="primary" icon={<Archive size={14} />} loading={extract.isPending} onClick={() => extract.mutate([])}>{tr("Extract all")}</Button>}
        {l && writable && sel.length > 0 && <Button size="sm" loading={extract.isPending} onClick={() => extract.mutate(sel)}>{tr("Extract {0} selected", sel.length)}</Button>}
      </Toolbar>
      {needs7z && <Notice tone="warn">{tr("7z and RAR archives need 7-Zip on the NAS (package 7zip).")}</Notice>}
      {l && !writable && info.data && <Notice>{tr("Read-only folder: extraction is not possible here.")}</Notice>}
      {l?.truncated && <Notice tone="warn">{tr("Only the first entries of this archive are listed.")}</Notice>}
      <ErrorBox error={list.error} />
      {list.isLoading ? <Spinner /> : l && (
        <>
          <div className="flex shrink-0 items-center gap-2 border-b border-line bg-panel px-3 py-1.5">
            <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={tr("Filter")} className="!h-8 !w-56 text-[12.5px]" />
            <span className="text-[12px] text-muted">{tr("Extract all: into a new folder next to the archive. Selected files: into this folder.")}</span>
          </div>
          <div className="min-h-0 flex-1 overflow-auto">
            <table className="table">
              <thead><tr>
                <th className="w-10"><Checkbox checked={files.length > 0 && sel.length === files.length} onChange={(v) => setSel(v ? files.map((f) => f.name) : [])} /></th>
                <th>{tr("Name")}</th><th>{tr("Size")}</th><th className="hidden md:table-cell">{tr("Compressed")}</th><th className="hidden md:table-cell">{tr("Modified")}</th>
              </tr></thead>
              <tbody>
                {shown.map((e) => (
                  <tr key={e.name}>
                    <td>{!e.dir && !e.skipped && <Checkbox checked={sel.includes(e.name)} onChange={(v) => setSel(v ? [...sel, e.name] : sel.filter((x) => x !== e.name))} />}</td>
                    <td className="mono text-[12.5px] break-all">
                      <span className="inline-flex items-center gap-1.5">{e.dir ? <Folder size={14} className="text-accent" /> : <FileText size={14} className="text-muted" />}{e.name}</span>
                      {e.skipped && <> <Badge tone="warn">{skippedLabel(e.skipped)}</Badge></>}
                    </td>
                    <td className="text-muted">{e.dir ? "—" : e.size >= 0 ? bytes(e.size) : "?"}</td>
                    <td className="hidden text-muted md:table-cell">{e.packed ? bytes(e.packed) : ""}</td>
                    <td className="hidden text-[12.5px] text-muted md:table-cell">{e.modTime ? dateTime(e.modTime) : ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}

/** Message shown in place of a viewer for files over its size limit. */
export function TooLarge({ entry, limit }: { entry: FileEntry; limit: number }) {
  return <Details entry={entry} note={tr("This file is larger than {0}: download it to open it.", bytes(limit))} />;
}

