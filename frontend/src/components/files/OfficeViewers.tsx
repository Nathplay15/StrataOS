// Office documents read in the browser, without OnlyOffice: Word documents
// with docx-preview, spreadsheets with SheetJS (read only). Both are lazy.
// The Word rendering happens in an inert document and is shown in a frame
// with no script and no network: nothing of the file runs on this origin.
import { useEffect, useMemo, useState } from "react";
import { renderAsync } from "docx-preview";
import * as XLSX from "xlsx";
import { q } from "../../lib/api";
import type { FileEntry } from "../../lib/types";
import { cx } from "../../lib/format";
import { tr } from "../../lib/i18n";
import { ErrorBox, Notice, Spinner } from "../ui";
import { Toolbar, fetchBytes, getPos, setPos } from "./common";

const raw = (p: string) => `/api/files/download${q({ path: p })}`;
export const officeMaxBytes = 50 << 20;

export function DocxViewer({ entry }: { entry: FileEntry }) {
  const [html, setHtml] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    const ac = new AbortController();
    setHtml(null);
    setError(null);
    (async () => {
      const data = await fetchBytes(raw(entry.path), ac.signal);
      const inert = document.implementation.createHTMLDocument("");
      const body = inert.createElement("div");
      const styles = inert.createElement("div");
      await renderAsync(data, body, styles, { inWrapper: true, ignoreLastRenderedPageBreak: true, useBase64URL: true, renderAltChunks: false, renderComments: false, experimental: false });
      if (!ac.signal.aborted) setHtml(styles.innerHTML + body.innerHTML);
    })().catch((e) => { if (!ac.signal.aborted) setError(e); });
    return () => ac.abort();
  }, [entry.path, entry.modTime]);
  const doc = html === null ? "" : `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:"><style>body{margin:0;background:#e8e8ea}.docx-wrapper{background:#e8e8ea!important;padding:16px!important}.docx-wrapper>section.docx{box-shadow:0 1px 4px rgb(0 0 0/.25);margin-bottom:16px!important}@media(max-width:800px){.docx-wrapper>section.docx{transform-origin:0 0}}</style></head><body>${html}</body></html>`;
  return (
    <>
      <Toolbar entry={entry} />
      {error ? <div className="p-4"><ErrorBox error={error} /></div> : html === null ? <Spinner /> : (
        <iframe title={entry.name} sandbox="" srcDoc={doc} className="min-h-0 w-full flex-1 border-0 bg-panel-2" />
      )}
    </>
  );
}

const MAX_ROWS = 5000;
const MAX_COLS = 200;

export function SheetViewer({ entry }: { entry: FileEntry }) {
  const [book, setBook] = useState<XLSX.WorkBook | null>(null);
  const [error, setError] = useState<unknown>(null);
  const posKey = `sheet:${entry.path}`;
  const [sheet, setSheet] = useState(() => getPos(posKey) ?? 0);
  useEffect(() => {
    const ac = new AbortController();
    setBook(null);
    setError(null);
    (async () => {
      const data = await fetchBytes(raw(entry.path), ac.signal);
      // sheetRows: big sheets are only read up to what is shown.
      const wb = XLSX.read(data, { type: "array", sheetRows: MAX_ROWS + 1, cellDates: true, cellFormula: false, cellHTML: false, cellStyles: false, bookVBA: false, WTF: false });
      if (!ac.signal.aborted) setBook(wb);
    })().catch((e) => { if (!ac.signal.aborted) setError(e); });
    return () => ac.abort();
  }, [entry.path, entry.modTime]);
  const names = book?.SheetNames ?? [];
  const cur = Math.min(sheet, Math.max(0, names.length - 1));
  const rows = useMemo(() => {
    const ws = book?.Sheets[names[cur]];
    if (!ws) return [] as string[][];
    return XLSX.utils.sheet_to_json<string[]>(ws, { header: 1, raw: false, defval: "", blankrows: true });
  }, [book, names, cur]);
  const cols = Math.min(MAX_COLS, rows.reduce((m, r) => Math.max(m, r.length), 0));
  const cut = rows.length > MAX_ROWS || rows.some((r) => r.length > MAX_COLS);
  return (
    <>
      <Toolbar entry={entry} />
      {error ? <div className="p-4"><ErrorBox error={error} /></div> : !book ? <Spinner /> : (
        <>
          {cut && <Notice tone="warn">{tr("Large sheet: only the first {0} rows and {1} columns are shown. Download the file to see everything.", MAX_ROWS, MAX_COLS)}</Notice>}
          <div className="min-h-0 flex-1 overflow-auto bg-panel">
            <table className="border-collapse text-[12.5px]">
              <thead className="sticky top-0 z-10 bg-panel-2">
                <tr>
                  <th className="sticky left-0 z-20 min-w-10 border border-line bg-panel-2 px-1 text-muted" />
                  {Array.from({ length: cols }, (_, i) => <th key={i} className="min-w-16 border border-line px-2 py-0.5 font-medium text-muted">{XLSX.utils.encode_col(i)}</th>)}
                </tr>
              </thead>
              <tbody>
                {rows.slice(0, MAX_ROWS).map((r, i) => (
                  <tr key={i}>
                    <td className="sticky left-0 border border-line bg-panel-2 px-1.5 text-right text-muted">{i + 1}</td>
                    {Array.from({ length: cols }, (_, j) => <td key={j} className="max-w-[320px] truncate border border-line px-2 py-0.5 whitespace-pre" title={r[j] ? String(r[j]) : undefined}>{r[j] ?? ""}</td>)}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {names.length > 1 && (
            <div className="flex shrink-0 gap-1 overflow-x-auto border-t border-line bg-panel-2 px-2 py-1" role="tablist">
              {names.map((n, i) => (
                <button key={n} role="tab" aria-selected={i === cur} onClick={() => { setSheet(i); setPos(posKey, i); }}
                  className={cx("shrink-0 rounded px-3 py-1 text-[12.5px]", i === cur ? "bg-panel font-medium text-accent shadow-sm" : "text-muted hover:bg-panel")}>{n}</button>
              ))}
            </div>
          )}
        </>
      )}
    </>
  );
}
