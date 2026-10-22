// Files opened in tabs: text/code editor, Markdown with preview, pictures,
// PDF (pdf.js), Word and spreadsheet previews, documents converted to PDF,
// audio and video (any codec, through the NAS), archives, file details,
// OnlyOffice. Every panel stays mounted while its tab is open, so switching keeps the edits and the undo history.
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Columns2, Eye, Folder, Maximize, Minus, Plus, RefreshCw, Save, X } from "lucide-react";
import { ApiError, api, q } from "../../lib/api";
import { cx } from "../../lib/format";
import { tr, srv, lang as uiLang } from "../../lib/i18n";
import { editorLang, imageNeedsConversion, type OfficeStatus } from "../../lib/filetypes";
import { Badge, Button, ErrorBox, Modal, Notice, Spinner, useUI } from "../ui";
import { Panel as PanelCtx, Toolbar, ViewerCard, fromField, inlineURL, useSiblings, type OpenTab } from "./common";
import { ArchivePanel, Details, DocumentPanel, OtherPanel, TailPanel, TooLarge } from "./Viewers";

export { tabFor, type OpenTab, type TabView } from "./common";

const CodeEditor = lazy(() => import("./CodeEditor"));
const MarkdownPreview = lazy(() => import("./MarkdownPreview"));
const PdfViewer = lazy(() => import("./PdfViewer"));
const MediaPlayer = lazy(() => import("./MediaPlayer"));
const DocxViewer = lazy(() => import("./OfficeViewers").then((m) => ({ default: m.DocxViewer })));
const SheetViewer = lazy(() => import("./OfficeViewers").then((m) => ({ default: m.SheetViewer })));

/** Files read whole by the browser (Word, spreadsheets) stop at this size. */
const clientMaxBytes = 50 << 20;

export function Workspace({ tabs, active, dirty, setDirty, onActivate, onClose, onReplace, onOpen, office }: {
  tabs: OpenTab[];
  active: string | null;
  dirty: Record<string, boolean>;
  setDirty: (id: string, v: boolean) => void;
  onActivate: (id: string | null) => void;
  onClose: (id: string) => void;
  onReplace: (id: string, t: OpenTab) => void;
  onOpen: (t: OpenTab) => void;
  office?: OfficeStatus;
}) {
  return (
    <>
      <div className="flex shrink-0 items-end gap-1 overflow-x-auto border-b border-line" role="tablist">
        <button role="tab" aria-selected={active === null} onClick={() => onActivate(null)} className={cx("flex shrink-0 items-center gap-1.5 rounded-t-lg px-3 py-2 text-[13px]", active === null ? "bg-panel font-medium text-accent" : "text-muted hover:bg-panel-2")}>
          <Folder size={14} /> {tr("Browse")}
        </button>
        {tabs.map((t) => (
          <div key={t.id} role="tab" aria-selected={active === t.id} className={cx("group flex max-w-[220px] shrink-0 items-center gap-1 rounded-t-lg py-1.5 pr-1 pl-3 text-[13px]", active === t.id ? "bg-panel font-medium" : "text-muted hover:bg-panel-2")}>
            <button className="truncate" title={t.entry.path} onClick={() => onActivate(t.id)}>{t.entry.name}</button>
            {dirty[t.id] && <span className="h-2 w-2 shrink-0 rounded-full bg-accent" title={tr("Unsaved changes")} />}
            <button className="rounded p-0.5 text-muted hover:bg-panel-2 hover:text-fg" title={tr("Close")} onClick={() => onClose(t.id)}><X size={13} /></button>
          </div>
        ))}
      </div>
      {tabs.map((t) => (
        <div key={t.id} className={cx("min-h-0 flex-1 flex-col", active === t.id ? "flex" : "hidden")}>
          <Panel tab={t} office={office} setDirty={(v) => setDirty(t.id, v)} onReplace={(n) => onReplace(t.id, n)} onOpen={onOpen} />
        </div>
      ))}
    </>
  );
}

function Panel({ tab, office, setDirty, onReplace, onOpen }: { tab: OpenTab; office?: OfficeStatus; setDirty: (v: boolean) => void; onReplace: (t: OpenTab) => void; onOpen: (t: OpenTab) => void }) {
  const root = useRef<HTMLDivElement>(null);
  const e = tab.entry;
  const text = () => <TextPanel tab={tab} setDirty={setDirty} onReplace={onReplace} />;
  let body: ReactNode;
  switch (tab.view) {
    case "text":
    case "markdown":
      body = text();
      break;
    case "image":
      body = <ImagePanel tab={tab} />;
      break;
    case "pdf":
      body = <PdfViewer key={e.path} source={{ url: inlineURL(e.path) }} entry={e} posKey={`pdf:${e.path}`} />;
      break;
    case "video":
    case "audio":
      body = <MediaPlayer key={e.path} entry={e} audioOnly={tab.view === "audio"} />;
      break;
    case "docx":
      body = e.size > clientMaxBytes ? <TooLarge entry={e} limit={clientMaxBytes} /> : <DocxViewer entry={e} />;
      break;
    case "sheet":
      body = e.size > clientMaxBytes ? <TooLarge entry={e} limit={clientMaxBytes} /> : <SheetViewer entry={e} />;
      break;
    case "document":
      body = <DocumentPanel entry={e} />;
      break;
    case "archive":
      body = <ArchivePanel entry={e} />;
      break;
    case "office":
      body = <OfficePanel tab={tab} />;
      break;
    case "details":
      body = <Details entry={e} />;
      break;
    default:
      body = <OtherPanel entry={e} renderText={text} />;
  }
  return (
    <PanelCtx.Provider value={{ tab, office, onReplace, onOpen, root }}>
      <div ref={root} className="flex min-h-0 flex-1 flex-col bg-bg">
        <Suspense fallback={<Spinner />}>{body}</Suspense>
      </div>
    </PanelCtx.Provider>
  );
}

/* ---------- Text, code and Markdown ---------- */

interface Content {
  path: string;
  content?: string;
  etag: string;
  modTime: string;
  size: number;
  writable: boolean;
  readOnlyReason: string;
  binary?: boolean;
  tooLarge?: boolean;
}

function TextPanel({ tab, setDirty, onReplace }: { tab: OpenTab; setDirty: (v: boolean) => void; onReplace: (t: OpenTab) => void }) {
  const { toast } = useUI();
  const qc = useQueryClient();
  const [loaded, setLoaded] = useState<Content | null>(tab.create ? { path: tab.entry.path, content: "", etag: "", modTime: "", size: 0, writable: true, readOnlyReason: "" } : null);
  const [error, setError] = useState<unknown>(null);
  const [gen, setGen] = useState(0); // remounts the editor on reload
  const [text, setText] = useState(loaded?.content ?? "");
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [preview, setPreview] = useState(tab.view === "markdown");
  const create = useRef(!!tab.create);
  const textRef = useRef(text);
  const isDirty = useRef(false);

  const setDirtyRef = useRef(setDirty);
  setDirtyRef.current = setDirty;
  // Stable: the parent passes a new setDirty on every render.
  const markDirty = useCallback((v: boolean) => { if (isDirty.current !== v) { isDirty.current = v; setDirtyRef.current(v); } }, []);

  const load = useCallback(async () => {
    setError(null);
    try {
      const c = await api.get<Content>(`/api/files/content${q({ path: tab.entry.path })}`);
      setLoaded(c);
      textRef.current = c.content ?? "";
      setText(textRef.current);
      setGen((g) => g + 1);
      markDirty(false);
      create.current = false;
    } catch (e) {
      setError(e);
    }
  }, [tab.entry.path, markDirty]);

  useEffect(() => { if (!create.current) load(); }, [load]);

  const save = useCallback(async (force = false) => {
    if (!loaded?.writable || saving) return;
    setSaving(true);
    try {
      const res = await fetch(`/api/files/content${q({ path: tab.entry.path, etag: create.current ? undefined : loaded.etag, create: create.current && !force ? 1 : undefined, force: force ? 1 : undefined })}`, {
        method: "PUT", credentials: "same-origin", headers: { "X-Strata": "1", "Content-Type": "text/plain; charset=utf-8" }, body: textRef.current,
      });
      const data = await res.json().catch(() => ({}));
      if (res.status === 409) { setConflict(true); return; }
      if (!res.ok) throw new ApiError(res.status, data.error ?? res.statusText, data);
      setLoaded({ ...loaded, etag: data.etag, modTime: data.modTime, size: data.size });
      if (create.current) {
        create.current = false;
        onReplace({ ...tab, create: false, entry: { ...tab.entry, size: data.size, modTime: data.modTime } });
      }
      markDirty(false);
      setConflict(false);
      toast(tr("Saved"));
      qc.invalidateQueries({ queryKey: ["files"] });
    } catch (e) {
      toast(srv((e as Error).message), "error");
    } finally {
      setSaving(false);
    }
  }, [loaded, saving, tab, toast, qc, markDirty, onReplace]);

  if (error) return <div className="p-4"><ErrorBox error={error} /></div>;
  if (!loaded) return <Spinner />;
  if (loaded.tooLarge) return <TailPanel entry={tab.entry} />;
  if (loaded.binary) return <Details entry={{ ...tab.entry, size: loaded.size }} note={tr("This file is not text.")} />;
  const md = tab.view === "markdown";
  return (
    <>
      <Toolbar entry={tab.entry}>
        {!loaded.writable && <Badge tone="warn">{tr("Read only")}{loaded.readOnlyReason ? ` — ${srv(loaded.readOnlyReason)}` : ""}</Badge>}
        {md && <Button size="sm" variant="ghost" icon={preview ? <Columns2 size={14} /> : <Eye size={14} />} onClick={() => setPreview(!preview)}>{preview ? tr("Hide preview") : tr("Preview")}</Button>}
        <Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} title={tr("Reload")} disabled={create.current} onClick={load} />
        {loaded.writable && <Button size="sm" variant="primary" icon={<Save size={14} />} loading={saving} disabled={!isDirty.current && !create.current} onClick={() => save()}>{tr("Save")}</Button>}
      </Toolbar>
      <div className="flex min-h-0 flex-1">
        <div className={cx("min-h-0 min-w-0", md && preview ? "w-1/2 border-r border-line" : "w-full")}>
          <Suspense fallback={<Spinner />}>
            <CodeEditor key={gen} value={loaded.content ?? ""} lang={editorLang(tab.entry.name)} readOnly={!loaded.writable}
              onChange={(d) => { textRef.current = d; markDirty(true); if (md) setText(d); }} onSave={() => save()} />
          </Suspense>
        </div>
        {md && preview && <div className="min-h-0 w-1/2"><Suspense fallback={<Spinner />}><MarkdownPreview text={text} /></Suspense></div>}
      </div>
      <Modal open={conflict} onClose={() => setConflict(false)} title={tr("File changed")} footer={<>
        <Button onClick={() => setConflict(false)}>{tr("Cancel")}</Button>
        <Button onClick={() => { setConflict(false); load(); }}>{tr("Reload (lose my changes)")}</Button>
        <Button variant="danger" onClick={() => save(true)}>{tr("Overwrite")}</Button>
      </>}>
        <p className="text-[13.5px]">{tr("{0} was changed or deleted by someone else since you opened it. Reload it, or overwrite it with your version.", tab.entry.name)}</p>
      </Modal>
    </>
  );
}

/* ---------- Images ---------- */

function ImagePanel({ tab }: { tab: OpenTab }) {
  const { go } = useSiblings();
  const [zoom, setZoom] = useState(0); // 0 = fit
  const [error, setError] = useState<unknown>(null);
  const converted = imageNeedsConversion(tab.entry.name);
  const src = converted ? `/api/files/image${q({ path: tab.entry.path, size: 2560, v: tab.entry.modTime })}` : inlineURL(tab.entry.path);
  useEffect(() => { setZoom(0); setError(null); }, [src]);
  const box = useRef<HTMLDivElement>(null);
  const goRef = useRef(go);
  goRef.current = go;
  useEffect(() => {
    const k = (e: KeyboardEvent) => {
      if (!box.current?.offsetParent || e.altKey || fromField(e)) return; // hidden tab; Alt: see useSiblings
      if (e.key === "ArrowRight") goRef.current(1);
      if (e.key === "ArrowLeft") goRef.current(-1);
    };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, []);
  // A failed conversion answers JSON: show its message.
  const onError = async () => {
    try {
      const res = await fetch(src, { headers: { "X-Strata": "1" }, credentials: "same-origin" });
      const data = await res.json().catch(() => ({}));
      setError(new ApiError(res.status, data.error ?? tr("This picture cannot be displayed")));
    } catch (e) {
      setError(e);
    }
  };
  return (
    <>
      <Toolbar entry={tab.entry}>
        {converted && <Badge>{tr("Converted")}</Badge>}
        <Button size="sm" variant="ghost" icon={<Minus size={14} />} title={tr("Zoom out")} onClick={() => setZoom(Math.max(0.1, (zoom || 1) / 1.25))} />
        <span className="w-12 text-center text-[12px] text-muted">{zoom ? `${Math.round(zoom * 100)} %` : tr("Fit")}</span>
        <Button size="sm" variant="ghost" icon={<Plus size={14} />} title={tr("Zoom in")} onClick={() => setZoom(Math.min(8, (zoom || 1) * 1.25))} />
        <Button size="sm" variant="ghost" icon={<Maximize size={14} />} title={tr("Fit")} onClick={() => setZoom(0)} />
      </Toolbar>
      {error ? <ViewerCard><ErrorBox error={error} /></ViewerCard> : (
        <div ref={box} className={cx("min-h-0 flex-1 overflow-auto bg-panel-2 p-3", !zoom && "flex items-center justify-center")} onDoubleClick={() => setZoom(zoom ? 0 : 1)}>
          {/* SVG too goes through <img>: its scripts never run. */}
          <img src={src} alt={tab.entry.name} onError={() => void onError()} className={cx("rounded", !zoom && "max-h-full max-w-full object-contain")} style={zoom ? { width: "auto", maxWidth: "none", transform: `scale(${zoom})`, transformOrigin: "0 0" } : undefined} />
        </div>
      )}
    </>
  );
}

/* ---------- OnlyOffice ---------- */

declare global {
  interface Window { DocsAPI?: { DocEditor: new (id: string, config: unknown) => { destroyEditor?: () => void } } }
}

const scripts: Record<string, Promise<void>> = {};
function loadScript(src: string) {
  scripts[src] ??= new Promise<void>((res, rej) => {
    const s = document.createElement("script");
    s.src = src;
    s.onload = () => res();
    s.onerror = () => { delete scripts[src]; rej(new Error(tr("The OnlyOffice document server cannot be reached ({0}).", src))); };
    document.head.appendChild(s);
  });
  return scripts[src];
}

function OfficePanel({ tab }: { tab: OpenTab }) {
  const [error, setError] = useState<unknown>(null);
  const [mixed, setMixed] = useState(false);
  const id = useMemo(() => `oo-${Math.random().toString(36).slice(2)}`, []);
  useEffect(() => {
    let editor: { destroyEditor?: () => void } | null = null;
    let live = true;
    (async () => {
      try {
        const r = await api.get<{ serverUrl: string; port: string; config: Record<string, unknown> }>(`/api/office/config${q({ path: tab.entry.path, lang: uiLang })}`);
        // The App Center server listens in plain HTTP on the NAS address.
        const server = r.serverUrl || `http://${location.hostname}:${r.port}`;
        if (location.protocol === "https:" && server.startsWith("http:")) setMixed(true);
        await loadScript(`${server}/web-apps/apps/api/documents/api.js`);
        if (!live || !window.DocsAPI) return;
        editor = new window.DocsAPI.DocEditor(id, { ...r.config, width: "100%", height: "100%", type: window.innerWidth < 768 ? "mobile" : "desktop" });
      } catch (e) {
        if (live) setError(e);
      }
    })();
    return () => { live = false; editor?.destroyEditor?.(); };
  }, [tab.entry.path, id]);
  return (
    <>
      {mixed && <Notice tone="warn">{tr("This page uses HTTPS but the document server uses HTTP: the browser blocks it. Open StrataOS over HTTP, or give the document server an HTTPS address in Settings.")}</Notice>}
      <ErrorBox error={error} />
      <div className="min-h-0 flex-1"><div id={id} /></div>
    </>
  );
}
