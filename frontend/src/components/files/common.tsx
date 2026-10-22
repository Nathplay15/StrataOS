// Shared pieces of the file viewers: URLs, the panel context (tab, siblings,
// open with…), the toolbar, positions remembered during the session.
import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronLeft, ChevronRight, Download, Maximize2, Minimize2 } from "lucide-react";
import { ApiError, api, q } from "../../lib/api";
import type { FileEntry } from "../../lib/types";
import { cx } from "../../lib/format";
import { tr } from "../../lib/i18n";
import { fileExt, fileKind, type FileKind, type OfficeStatus } from "../../lib/filetypes";
import { Button } from "../ui";

export type TabView = FileKind | "office" | "details";

export interface OpenTab {
  id: string;
  entry: FileEntry;
  view: TabView;
  /** New file, not written yet. */
  create?: boolean;
}

export const tabFor = (entry: FileEntry, view?: TabView, create?: boolean): OpenTab => ({ id: `${view ?? fileKind(entry.name)}:${entry.path}`, entry, view: view ?? fileKind(entry.name), create });

export const inlineURL = (p: string) => `/api/files/download${q({ path: p, inline: 1 })}`;
export const download = (p: string) => { window.location.href = `/api/files/download${q({ path: p })}`; };

/** What the viewers can do on this NAS (GET /api/files/viewers). */
export interface ViewerCaps {
  admin: boolean;
  docPreview: boolean;
  docPreviewSize: string;
  media: boolean;
  vaapi: boolean;
  images: boolean;
  raw: boolean;
  sevenZip: boolean;
}

export const useViewerCaps = () => useQuery({ queryKey: ["viewer-caps"], queryFn: () => api.get<ViewerCaps>("/api/files/viewers"), staleTime: 60000 });

/** Reads a file of the API as bytes, with the session (errors as ApiError). */
export async function fetchBytes(url: string, signal?: AbortSignal): Promise<ArrayBuffer> {
  const res = await fetch(url, { credentials: "same-origin", headers: { "X-Strata": "1" }, signal });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new ApiError(res.status, data.error ?? res.statusText, data);
  }
  return res.arrayBuffer();
}

/* ---------- Positions (page, time…) kept for the browser session ---------- */

const posKey = "strata-viewer-positions";
const positions: Record<string, number> = (() => {
  try {
    return JSON.parse(sessionStorage.getItem(posKey) ?? "{}");
  } catch {
    return {};
  }
})();
let saveTimer: ReturnType<typeof setTimeout> | undefined;

export const getPos = (key: string) => positions[key];
export function setPos(key: string, v: number) {
  positions[key] = v;
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    try {
      sessionStorage.setItem(posKey, JSON.stringify(positions));
    } catch {
      /* private mode: kept in memory only */
    }
  }, 500);
}

/* ---------- Panel context ---------- */

export interface PanelCtx {
  tab: OpenTab;
  office?: OfficeStatus;
  onReplace: (t: OpenTab) => void;
  onOpen: (t: OpenTab) => void;
  /** The panel element, for full screen and visibility. */
  root: RefObject<HTMLDivElement | null>;
}

export const Panel = createContext<PanelCtx | null>(null);
export const usePanel = () => useContext(Panel)!;

/** Same-kind files of the tab's folder, to go to the previous or next one. */
export function useSiblings() {
  const { tab, onReplace, root } = usePanel();
  const dir = tab.entry.path.slice(0, tab.entry.path.lastIndexOf("/"));
  const kind = fileKind(tab.entry.name);
  const list = useQuery({ queryKey: ["files", dir, false], queryFn: () => api.get<{ entries: FileEntry[] }>(`/api/files${q({ path: dir })}`), enabled: !tab.create });
  const files = useMemo(() => (list.data?.entries ?? []).filter((e) => !e.dir && fileKind(e.name) === kind), [list.data, kind]);
  const index = files.findIndex((e) => e.path === tab.entry.path);
  const go = (d: number) => {
    if (files.length < 2 || index < 0) return;
    const n = files[(index + d + files.length) % files.length];
    onReplace({ ...tab, entry: n });
  };
  const goRef = useRef(go);
  goRef.current = go;
  // Alt+←/→ in every viewer (plain arrows in pictures, see ImagePanel).
  useEffect(() => {
    const k = (e: KeyboardEvent) => {
      if (!root.current?.offsetParent || !e.altKey || (e.key !== "ArrowLeft" && e.key !== "ArrowRight")) return;
      e.preventDefault();
      goRef.current(e.key === "ArrowRight" ? 1 : -1);
    };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [root]);
  return { files, index, go };
}

/** True when the keyboard event comes from a field (no shortcuts then). */
export const fromField = (e: KeyboardEvent) => {
  const t = e.target as HTMLElement | null;
  return !!t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || !!t.closest?.(".cm-editor"));
};

/* ---------- Open with ---------- */

export interface OpenChoice { label: string; view?: TabView; download?: boolean }

/** Ways to open a file: its viewer, the editor, OnlyOffice, download. */
export function openChoices(entry: FileEntry, office?: OfficeStatus, caps?: ViewerCaps): OpenChoice[] {
  const kind = fileKind(entry.name);
  const out: OpenChoice[] = [];
  const viewer: Partial<Record<FileKind, string>> = {
    image: tr("Picture viewer"), pdf: tr("PDF viewer"), video: tr("Video player"), audio: tr("Audio player"), docx: tr("Document preview"),
    sheet: tr("Spreadsheet preview"), document: tr("Document preview"), archive: tr("Archive contents"), markdown: tr("Markdown editor"), text: tr("Text editor"),
  };
  if (viewer[kind]) out.push({ label: viewer[kind]!, view: kind });
  if ((kind === "docx" || kind === "sheet") && caps?.docPreview) out.push({ label: tr("Print layout (PDF)"), view: "document" });
  if (kind !== "text" && kind !== "markdown" && entry.size <= 20 << 20) out.push({ label: tr("Text editor"), view: "text" });
  if (kind !== "video" && kind !== "audio" && /^(ts|mts|vob|bin|dat)$/.test(fileExt(entry.name))) out.push({ label: tr("Video player"), view: "video" });
  if (office?.enabled && office.types[fileExt(entry.name)]) out.push({ label: tr("OnlyOffice"), view: "office" });
  out.push({ label: tr("Details"), view: "details" });
  out.push({ label: tr("Download"), download: true });
  return out;
}

export function OpenWith({ entry, office, onOpen, size = "sm", label }: { entry: FileEntry; office?: OfficeStatus; onOpen: (t: OpenTab) => void; size?: "sm" | "md"; label?: string }) {
  const [open, setOpen] = useState(false);
  const caps = useViewerCaps();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => { document.removeEventListener("mousedown", close); document.removeEventListener("keydown", esc); };
  }, [open]);
  return (
    <div ref={ref} className="relative inline-block">
      <Button size={size} variant="ghost" icon={<ChevronDown size={14} />} title={tr("Open with")} onClick={() => setOpen(!open)}>{label}</Button>
      {open && (
        <div className="absolute right-0 z-50 mt-1 min-w-[190px] rounded-lg border border-line bg-panel py-1 text-left text-[13px] shadow-lg" role="menu">
          {openChoices(entry, office, caps.data).map((c) => (
            <button key={c.label + (c.view ?? "")} role="menuitem" className="block w-full px-3 py-1.5 text-left hover:bg-panel-2"
              onClick={() => { setOpen(false); if (c.download) download(entry.path); else onOpen(tabFor(entry, c.view)); }}>
              {c.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/* ---------- Toolbar ---------- */

export function useFullscreen(root: RefObject<HTMLElement | null>) {
  const [on, setOn] = useState(false);
  useEffect(() => {
    const h = () => setOn(!!document.fullscreenElement && document.fullscreenElement === root.current);
    document.addEventListener("fullscreenchange", h);
    return () => document.removeEventListener("fullscreenchange", h);
  }, [root]);
  const toggle = () => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void root.current?.requestFullscreen?.();
  };
  return { on, toggle };
}

/** Toolbar of a viewer: path, the viewer's own buttons, previous/next file,
 * open with, full screen, download. */
export function Toolbar({ entry, children, nav = true }: { entry: FileEntry; children?: ReactNode; nav?: boolean }) {
  const ctx = useContext(Panel);
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line bg-panel px-3 py-2">
      <span className="mono min-w-0 flex-1 basis-40 truncate text-[12px] text-muted" title={entry.path}>{entry.path}</span>
      {children}
      {ctx && nav && !ctx.tab.create && <SiblingNav />}
      {ctx && !ctx.tab.create && <OpenWith entry={entry} office={ctx.office} onOpen={ctx.onOpen} />}
      {ctx && <FullscreenButton />}
      <Button size="sm" variant="ghost" icon={<Download size={14} />} onClick={() => download(entry.path)} title={tr("Download")} />
    </div>
  );
}

function SiblingNav() {
  const { files, index, go } = useSiblings();
  if (files.length < 2) return null;
  return (
    <span className="flex items-center">
      <Button size="sm" variant="ghost" icon={<ChevronLeft size={14} />} title={tr("Previous file (Alt+←)")} onClick={() => go(-1)} />
      <span className="text-[12px] text-muted">{index + 1}/{files.length}</span>
      <Button size="sm" variant="ghost" icon={<ChevronRight size={14} />} title={tr("Next file (Alt+→)")} onClick={() => go(1)} />
    </span>
  );
}

function FullscreenButton() {
  const { root } = usePanel();
  const fs = useFullscreen(root);
  if (!document.fullscreenEnabled) return null;
  return <Button size="sm" variant="ghost" icon={fs.on ? <Minimize2 size={14} /> : <Maximize2 size={14} />} title={fs.on ? tr("Exit full screen") : tr("Full screen")} onClick={fs.toggle} />;
}

/** A centred message in a viewer (errors, missing components). */
export function ViewerCard({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className="flex-1 overflow-auto p-6">
      <div className={cx("card mx-auto max-w-lg space-y-3 p-5 text-[13px]", className)}>{children}</div>
    </div>
  );
}
