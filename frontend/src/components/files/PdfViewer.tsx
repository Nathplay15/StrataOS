// PDF viewer built on pdf.js (lazy chunk, worker and data files served by the
// NAS). Pages are drawn on canvases with a text layer for selection and
// search; the document's scripts, forms actions and links never run. Used for
// PDF files and for office documents converted by the NAS.
import { memo, useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import * as pdfjs from "pdfjs-dist";
import type { PDFDocumentProxy, PDFPageProxy } from "pdfjs-dist";
import workerUrl from "pdfjs-dist/build/pdf.worker.min.mjs?url";
import { ChevronDown, ChevronUp, FileDown, Minus, Plus, Printer, Search, StretchHorizontal } from "lucide-react";
import type { FileEntry } from "../../lib/types";
import { tr } from "../../lib/i18n";
import { Button, ErrorBox, Input, Modal, Spinner } from "../ui";
import { Toolbar, getPos, setPos, usePanel } from "./common";
import "./pdf.css";

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl;

export type PdfSource = { url: string } | { data: ArrayBuffer };

interface Size { w: number; h: number }
const GAP = 12;

export default function PdfViewer({ source, entry, posKey, pdfName }: { source: PdfSource; entry: FileEntry; posKey: string; pdfName?: string }) {
  const { root } = usePanel();
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [password, setPassword] = useState<{ cb: (p: string) => void; wrong: boolean } | null>(null);
  const [pw, setPw] = useState("");
  const [sizes, setSizes] = useState<Size[]>([]);
  const [zoom, setZoom] = useState<number | "fit">("fit");
  const [width, setWidth] = useState(0);
  const [page, setPage] = useState(1);
  const [pageInput, setPageInput] = useState("1");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState<{ q: string; pages: number[]; busy: boolean } | null>(null);
  const [printing, setPrinting] = useState<string | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const searchBox = useRef<HTMLInputElement>(null);
  const texts = useRef(new Map<number, string>());

  // Load the document; data sources are copied (pdf.js hands them to its worker).
  // Keyed by the URL or the buffer: callers may pass a new object each render.
  const sourceKey = "url" in source ? source.url : source.data;
  useEffect(() => {
    setDoc(null);
    setError(null);
    texts.current.clear();
    const task = pdfjs.getDocument({
      ...("url" in source ? { url: source.url } : { data: new Uint8Array(source.data.slice(0)) }),
      // fetch() from this page: the session cookie and the API header go along.
      httpHeaders: { "X-Strata": "1" },
      withCredentials: true,
      cMapUrl: "/pdfjs/cmaps/",
      cMapPacked: true,
      standardFontDataUrl: "/pdfjs/standard_fonts/",
      wasmUrl: "/pdfjs/wasm/",
      iccUrl: "/pdfjs/iccs/",
      enableXfa: false,
    });
    task.onPassword = (cb: (p: string) => void, reason: number) => setPassword({ cb, wrong: reason === pdfjs.PasswordResponses.INCORRECT_PASSWORD });
    let live = true;
    task.promise.then(async (d) => {
      const first = (await d.getPage(1)).getViewport({ scale: 1 });
      if (!live) return;
      setSizes(Array.from({ length: d.numPages }, () => ({ w: first.width, h: first.height })));
      setDoc(d);
    }, (e) => { if (live) setError(e); });
    return () => { live = false; void task.destroy(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sourceKey]);

  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, [doc]);

  const maxW = useMemo(() => sizes.reduce((m, s) => Math.max(m, s.w), 1), [sizes]);
  const scale = zoom === "fit" ? Math.max(0.1, Math.min(4, (width - 32) / maxW)) : zoom;
  const tops = useMemo(() => {
    const t: number[] = [];
    let y = 16;
    for (const s of sizes) { t.push(y); y += s.h * scale + GAP; }
    return t;
  }, [sizes, scale]);

  const goPage = useCallback((n: number) => {
    const el = scroller.current;
    if (!el || !tops.length) return;
    n = Math.max(1, Math.min(tops.length, n));
    el.scrollTop = tops[n - 1] - 8;
    setPage(n);
    setPageInput(String(n));
  }, [tops]);

  // Back to the page seen last in this session; same page across zoom changes.
  const restored = useRef(false);
  const pageRef = useRef(page);
  pageRef.current = page;
  useEffect(() => {
    if (!doc || !width) return;
    if (!restored.current) {
      restored.current = true;
      const p = getPos(posKey);
      if (p && p > 1) requestAnimationFrame(() => goPage(p));
      return;
    }
    requestAnimationFrame(() => goPage(pageRef.current));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, scale]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const y = el.scrollTop + el.clientHeight / 3;
    let n = 1;
    while (n < tops.length && tops[n] <= y) n++;
    if (n !== page) {
      setPage(n);
      setPageInput(String(n));
      setPos(posKey, n);
    }
  };

  const onSize = useCallback((n: number, s: Size) => {
    setSizes((l) => (l[n - 1] && Math.abs(l[n - 1].w - s.w) < 0.5 && Math.abs(l[n - 1].h - s.h) < 0.5 ? l : l.map((x, i) => (i === n - 1 ? s : x))));
  }, []);

  const pageText = async (n: number) => {
    let t = texts.current.get(n);
    if (t === undefined && doc) {
      const c = await (await doc.getPage(n)).getTextContent();
      t = c.items.map((i) => ("str" in i ? i.str : "")).join(" ").toLowerCase();
      texts.current.set(n, t);
    }
    return t ?? "";
  };
  const find = async (back = false) => {
    const qy = query.trim().toLowerCase();
    if (!doc || !qy) { setSearch(null); return; }
    let s = search;
    if (!s || s.q !== qy) {
      setSearch({ q: qy, pages: [], busy: true });
      const pages: number[] = [];
      for (let n = 1; n <= doc.numPages; n++) if ((await pageText(n)).includes(qy)) pages.push(n);
      s = { q: qy, pages, busy: false };
      setSearch(s);
    }
    if (!s.pages.length) return;
    const next = back ? [...s.pages].reverse().find((n) => n < page) ?? s.pages[s.pages.length - 1] : s.pages.find((n) => n > page || (n === page && !search)) ?? s.pages[0];
    goPage(next);
  };

  // Ctrl+F searches the document while this tab is shown.
  useEffect(() => {
    const k = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "f" && root.current?.offsetParent) {
        e.preventDefault();
        searchBox.current?.focus();
        searchBox.current?.select();
      }
    };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [root]);

  // Printing draws the pages as pictures into a frame: the browser's own PDF
  // viewer (and any script of the document) is never involved.
  const print = async () => {
    if (!doc) return;
    const urls: string[] = [];
    const max = Math.min(doc.numPages, 500);
    try {
      for (let n = 1; n <= max; n++) {
        setPrinting(tr("Preparing page {0} of {1}…", n, max));
        const p = await doc.getPage(n);
        const vp = p.getViewport({ scale: 150 / 72 });
        const c = document.createElement("canvas");
        c.width = Math.floor(vp.width);
        c.height = Math.floor(vp.height);
        await p.render({ canvas: c, viewport: vp }).promise;
        const blob = await new Promise<Blob | null>((res) => c.toBlob(res, "image/jpeg", 0.92));
        if (blob) urls.push(URL.createObjectURL(blob));
        p.cleanup();
      }
      const frame = document.createElement("iframe");
      frame.style.cssText = "position:fixed;width:0;height:0;border:0;right:0;bottom:0";
      document.body.appendChild(frame);
      const d = frame.contentDocument!;
      d.open();
      d.write(`<!doctype html><html><head><title></title><style>@page{margin:0}body{margin:0}img{display:block;width:100%;page-break-after:always;break-after:page}</style></head><body></body></html>`);
      d.close();
      await Promise.all(urls.map((u) => new Promise<void>((res) => { const img = d.createElement("img"); img.onload = img.onerror = () => res(); img.src = u; d.body.appendChild(img); })));
      setPrinting(null);
      frame.contentWindow!.focus();
      frame.contentWindow!.print();
      setTimeout(() => { frame.remove(); urls.forEach((u) => URL.revokeObjectURL(u)); }, 60000);
    } catch (e) {
      setPrinting(null);
      setError(e);
      urls.forEach((u) => URL.revokeObjectURL(u));
    }
  };

  const savePdf = () => {
    if (!("data" in source)) return;
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([source.data], { type: "application/pdf" }));
    a.download = pdfName ?? "document.pdf";
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 10000);
  };

  const submitPage = (e: FormEvent) => { e.preventDefault(); goPage(parseInt(pageInput) || 1); };
  const setZoomStep = (f: number) => setZoom(Math.max(0.25, Math.min(5, Math.round(scale * f * 100) / 100)));

  return (
    <>
      <Toolbar entry={entry}>
        {doc && (
          <>
            <form onSubmit={(e) => { e.preventDefault(); void find(); }} className="flex items-center gap-1">
              <div className="relative">
                <Search size={13} className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-muted" />
                <input ref={searchBox} value={query} onChange={(e) => setQuery(e.target.value)} placeholder={tr("Search")} className="input !h-8 !w-36 !pl-7 text-[12.5px]"
                  onKeyDown={(e) => { if (e.key === "Enter" && e.shiftKey) { e.preventDefault(); void find(true); } }} />
              </div>
              {search && <span className="text-[12px] text-muted">{search.busy ? "…" : search.pages.length ? tr("{0} page(s)", search.pages.length) : tr("Not found")}</span>}
              {search && search.pages.length > 0 && <>
                <Button size="sm" variant="ghost" icon={<ChevronUp size={14} />} title={tr("Previous result")} onClick={() => void find(true)} />
                <Button size="sm" variant="ghost" icon={<ChevronDown size={14} />} title={tr("Next result")} onClick={() => void find()} />
              </>}
            </form>
            <form onSubmit={submitPage} className="flex items-center gap-1 text-[12px] text-muted">
              <Input value={pageInput} onChange={(e) => setPageInput(e.target.value.replace(/\D/g, ""))} onBlur={submitPage} className="!h-8 !w-12 text-center text-[12.5px]" aria-label={tr("Page")} />
              <span>/ {doc.numPages}</span>
            </form>
            <Button size="sm" variant="ghost" icon={<Minus size={14} />} title={tr("Zoom out")} onClick={() => setZoomStep(1 / 1.2)} />
            <span className="w-11 text-center text-[12px] text-muted">{Math.round(scale * 100)} %</span>
            <Button size="sm" variant="ghost" icon={<Plus size={14} />} title={tr("Zoom in")} onClick={() => setZoomStep(1.2)} />
            <Button size="sm" variant={zoom === "fit" ? "secondary" : "ghost"} icon={<StretchHorizontal size={14} />} title={tr("Fit width")} onClick={() => setZoom("fit")} />
            <Button size="sm" variant="ghost" icon={<Printer size={14} />} title={tr("Print")} loading={!!printing} onClick={() => void print()} />
            {"data" in source && <Button size="sm" variant="ghost" icon={<FileDown size={14} />} title={tr("Download as PDF")} onClick={savePdf} />}
          </>
        )}
      </Toolbar>
      {printing && <div className="shrink-0 bg-panel-2 px-3 py-1 text-[12px] text-muted">{printing}</div>}
      {error ? <div className="p-4"><ErrorBox error={error} /></div> : !doc ? <Spinner /> : (
        <div ref={scroller} onScroll={onScroll} className="relative min-h-0 flex-1 overflow-auto bg-panel-2" style={{ overflowAnchor: "none" }}>
          <div style={{ height: (tops[tops.length - 1] ?? 0) + (sizes[sizes.length - 1]?.h ?? 0) * scale + 16, position: "relative", minWidth: maxW * scale + 32 }}>
            {sizes.map((s, i) => (
              <PageView key={i} doc={doc} n={i + 1} scale={scale} size={s} top={tops[i]} onSize={onSize} query={search?.q ?? ""} scroller={scroller} />
            ))}
          </div>
        </div>
      )}
      <Modal open={!!password} onClose={() => { setPassword(null); setError(new Error(tr("This PDF is protected by a password."))); }} title={tr("Password required")} footer={<>
        <Button variant="primary" onClick={() => { password?.cb(pw); setPassword(null); setPw(""); }}>{tr("Open")}</Button>
      </>}>
        <p className="mb-3 text-[13px]">{password?.wrong ? tr("Wrong password, try again.") : tr("This PDF is protected by a password.")}</p>
        <Input type="password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { password?.cb(pw); setPassword(null); setPw(""); } }} />
      </Modal>
    </>
  );
}

/** One page: drawn when it comes near the visible area, released far from it. */
const PageView = memo(function PageView({ doc, n, scale, size, top, onSize, query, scroller }: {
  doc: PDFDocumentProxy; n: number; scale: number; size: Size; top: number; onSize: (n: number, s: Size) => void; query: string; scroller: React.RefObject<HTMLDivElement | null>;
}) {
  const box = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const textDiv = useRef<HTMLDivElement>(null);
  const [near, setNear] = useState(false);
  const [drawn, setDrawn] = useState(0); // scale drawn, 0 = none

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const io = new IntersectionObserver(([e]) => setNear(e.isIntersecting), { root: scroller.current, rootMargin: "1200px 0px" });
    io.observe(el);
    return () => io.disconnect();
  }, [scroller]);

  useEffect(() => {
    if (!near) {
      if (canvas.current && drawn) { canvas.current.width = 0; canvas.current.height = 0; setDrawn(0); }
      if (textDiv.current) textDiv.current.replaceChildren();
      return;
    }
    if (drawn === scale) return;
    let cancelled = false;
    let task: ReturnType<PDFPageProxy["render"]> | null = null;
    let text: pdfjs.TextLayer | null = null;
    (async () => {
      const page = await doc.getPage(n);
      if (cancelled) return;
      const base = page.getViewport({ scale: 1 });
      onSize(n, { w: base.width, h: base.height });
      const vp = page.getViewport({ scale });
      const dpr = Math.min(window.devicePixelRatio || 1, 3);
      const c = canvas.current!;
      c.width = Math.floor(vp.width * dpr);
      c.height = Math.floor(vp.height * dpr);
      task = page.render({ canvas: c, viewport: vp, transform: dpr !== 1 ? [dpr, 0, 0, dpr, 0, 0] : undefined });
      try {
        await task.promise;
      } catch {
        return; // cancelled
      }
      if (cancelled) return;
      const div = textDiv.current!;
      div.replaceChildren();
      text = new pdfjs.TextLayer({ textContentSource: page.streamTextContent(), container: div, viewport: vp });
      await text.render().catch(() => {});
      if (!cancelled) setDrawn(scale);
    })();
    return () => { cancelled = true; task?.cancel(); text?.cancel(); };
  }, [near, scale, doc, n, drawn, onSize]);

  // Search hits: the text runs holding the words.
  useEffect(() => {
    const div = textDiv.current;
    if (!div) return;
    for (const s of div.querySelectorAll("span")) {
      s.classList.toggle("pdf-hit", !!query && !!s.textContent && s.textContent.toLowerCase().includes(query));
    }
  }, [query, drawn]);

  const style = { top, left: "50%", transform: "translateX(-50%)", position: "absolute" as const, width: size.w * scale, height: size.h * scale, "--scale-factor": scale, "--total-scale-factor": scale } as React.CSSProperties;
  return (
    <div ref={box} className="pdf-page" style={style} data-page={n}>
      <canvas ref={canvas} />
      <div ref={textDiv} className="textLayer" />
    </div>
  );
});
