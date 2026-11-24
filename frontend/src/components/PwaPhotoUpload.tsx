import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Images, Send } from "lucide-react";
import { api, q } from "../lib/api";
import type { Share } from "../lib/types";
import { tr } from "../lib/i18n";
import { Button, Card, ErrorBox, Field, Select, Switch, useUI } from "./ui";

const KEY = "strata-pwa-upload";
const DB = "strata-pwa";
const SYNC_TAG = "strata-photo-upload";

type Rule = { dir: string; target: string; paused: boolean };
type Cfg = { enabled: boolean; target: string; rules: Rule[] };

// The old single-target settings become one rule.
const load = (): Cfg => {
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) ?? "{}");
    const old = raw as { enabled?: boolean; target?: string; paused?: boolean };
    const rules: Rule[] = Array.isArray(raw.rules)
      ? raw.rules
      : old.target
        ? [{ dir: "", target: old.target, paused: !!old.paused }]
        : [];
    return { enabled: !!old.enabled, target: old.target ?? "", rules };
  } catch {
    return { enabled: false, target: "", rules: [] };
  }
};

const mediaRe = /\.(jpe?g|png|gif|webp|heic|heif|mp4|mov|webm|mkv|m4v)$/i;

type DirHandle = FileSystemDirectoryHandle & { queryPermission?: (o: { mode: string }) => Promise<string>; requestPermission?: (o: { mode: string }) => Promise<string> };

function db(): Promise<IDBDatabase> {
  return new Promise((res, rej) => {
    const rq = indexedDB.open(DB, 1);
    rq.onupgradeneeded = () => {
      rq.result.createObjectStore("handles");
      rq.result.createObjectStore("cfg");
    };
    rq.onsuccess = () => res(rq.result);
    rq.onerror = () => rej(rq.error);
  });
}
const idbPut = (d: IDBDatabase, store: string, key: string, val: unknown) =>
  new Promise<void>((res, rej) => {
    const rq = d.transaction(store, "readwrite").objectStore(store).put(val, key);
    rq.onsuccess = () => res();
    rq.onerror = () => rej(rq.error);
  });

/** Sends the new photos and videos of this phone to chosen shared folders
 *  (roadmap 0.15, PWA upload; only shown when the interface runs as an app).
 *  With a picked folder, the service worker uploads in the background
 *  (periodic sync); otherwise the files are picked by hand. */
export function PwaPhotoUpload() {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [cfg, setCfg] = useState<Cfg>(load);
  const [busy, setBusy] = useState(false);
  const [pct, setPct] = useState(0);
  const [err, setErr] = useState<unknown>(null);
  const [syncOk, setSyncOk] = useState<boolean | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const standalone = window.matchMedia?.("(display-mode: standalone)").matches || (navigator as unknown as { standalone?: boolean }).standalone === true;

  const save = (c: Cfg) => {
    setCfg(c);
    localStorage.setItem(KEY, JSON.stringify(c));
  };

  // Register the periodic background sync while the upload is enabled.
  useEffect(() => {
    if (!cfg.enabled) return;
    let on = true;
    navigator.serviceWorker?.ready
      .then((reg) => (reg as unknown as { periodicSync?: { register: (tag: string, o: { minInterval: number }) => Promise<void> } }).periodicSync?.register(SYNC_TAG, { minInterval: 12 * 3600 * 1000 }).then(() => on && setSyncOk(true)).catch(() => on && setSyncOk(false)))
      .catch(() => on && setSyncOk(false));
    return () => { on = false; };
  }, [cfg.enabled]);

  // Answer of the service worker after a "send now" run.
  useEffect(() => {
    const on = (e: MessageEvent) => {
      const d = e.data as { type?: string; sent?: number; failed?: number } | undefined;
      if (d?.type !== "upload-done") return;
      setBusy(false);
      toast(d.sent ? tr("{0} new photo(s)/video(s) sent", d.sent) : tr("Nothing new to send"));
      if (d.failed) setErr(new Error(tr("{0} upload(s) failed", d.failed)));
      qc.invalidateQueries({ queryKey: ["files"] });
      qc.invalidateQueries({ queryKey: ["photos"] });
    };
    navigator.serviceWorker?.addEventListener("message", on);
    return () => navigator.serviceWorker?.removeEventListener("message", on);
  }, [qc, toast]);

  // Pick a photo folder of the phone and store its persistent handle.
  const addFolder = async () => {
    setErr(null);
    try {
      const w = window as unknown as { showDirectoryPicker?: (o: { id: string; mode: string }) => Promise<DirHandle> };
      if (!w.showDirectoryPicker) throw new Error(tr("This browser cannot pick a folder: use the Send button to choose the photos by hand."));
      const h = await w.showDirectoryPicker({ id: "strata-photos", mode: "read" });
      await h.requestPermission?.({ mode: "read" });
      const d = await db();
      await idbPut(d, "handles", h.name, h);
      d.close();
      if (!cfg.rules.some((r) => r.dir === h.name)) save({ ...cfg, rules: [...cfg.rules, { dir: h.name, target: cfg.target, paused: false }] });
    } catch (e) {
      if ((e as Error).name !== "AbortError") setErr(e);
    }
  };

  const sendNow = async () => {
    if (!cfg.target) return;
    if (cfg.rules.length) {
      // A folder is known: the service worker enumerates it and uploads.
      setBusy(true);
      setErr(null);
      navigator.serviceWorker?.controller?.postMessage({ type: "upload-now" });
      return;
    }
    fileRef.current?.click();
  };

  // Manual pick (no folder handle, e.g. iOS): upload the chosen files.
  const send = async (files: File[]) => {
    if (!cfg.target) return;
    const todo = files.filter((f) => mediaRe.test(f.name));
    if (!todo.length) return;
    setBusy(true);
    setErr(null);
    setPct(0);
    try {
      const list = await api.get<{ entries: { name: string; size: number }[] }>(`/api/files${q({ path: cfg.target })}`);
      const there = new Set((list.entries ?? []).map((e) => `${e.name}:${e.size}`));
      const fresh = todo.filter((f) => !there.has(`${f.name}:${f.size}`));
      if (!fresh.length) {
        toast(tr("Nothing new to send"));
        return;
      }
      const fd = new FormData();
      for (const f of fresh) fd.append("file", f, f.name);
      await new Promise<void>((res, rej) => {
        const xhr = new XMLHttpRequest();
        xhr.open("POST", `/api/files/upload${q({ path: cfg.target, keep: 1 })}`); // never overwrite
        xhr.setRequestHeader("X-Strata", "1");
        xhr.upload.onprogress = (e) => e.lengthComputable && setPct(Math.round((e.loaded / e.total) * 100));
        xhr.onload = () => (xhr.status < 300 ? res() : rej(new Error(JSON.parse(xhr.responseText || "{}").error ?? "upload failed")));
        xhr.onerror = () => rej(new Error("network error"));
        xhr.send(fd);
      });
      setPct(0);
      toast(tr("{0} new photo(s)/video(s) sent", fresh.length));
      qc.invalidateQueries({ queryKey: ["files"] });
      qc.invalidateQueries({ queryKey: ["photos"] });
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };

  if (!standalone) return null;
  const paused = cfg.rules.some((r) => r.paused);

  return (
    <Card title={<span className="flex items-center gap-2"><Images size={16} />{tr("Send the photos of this phone")}</span>}
      actions={cfg.enabled && cfg.target && !paused && <Button size="sm" variant="primary" loading={busy} icon={<Send size={14} />} onClick={() => void sendNow()}>{tr("Send new photos now")}</Button>}>
      <input ref={fileRef} type="file" accept="image/*,video/*" multiple className="hidden" onChange={(e) => { const f = Array.from(e.target.files ?? []); e.target.value = ""; void send(f); }} />
      <div className="space-y-3">
        <Switch checked={cfg.enabled} onChange={(v) => save({ ...cfg, enabled: v })} label={tr("Automatic photo upload from this device")} hint={tr("New photos and videos of this phone are sent to the chosen folder, skipping what is already there.")} />
        {cfg.enabled && <>
          <Field label={tr("Destination folder")}>
            <Select value={cfg.target} onChange={(e) => save({ ...cfg, target: e.target.value })}>
              <option value="">{tr("Choose…")}</option>
              {shares.data?.map((s) => <option key={s.path} value={s.path}>{s.name} ({s.path})</option>)}
            </Select>
          </Field>
          <div className="space-y-2">
            {cfg.rules.map((r) => (
              <div key={r.dir || "manual"} className="flex flex-wrap items-center gap-2 rounded-xl border border-line bg-panel-2 px-3 py-2 text-[12.5px]">
                <span className="font-medium">{r.dir || tr("Manual selection")}</span>
                <span className="text-muted">→</span>
                <Select className="!w-auto text-[12px]" value={r.target} onChange={(e) => save({ ...cfg, rules: cfg.rules.map((x) => (x === r ? { ...x, target: e.target.value } : x)) })}>
                  <option value="">{tr("Choose…")}</option>
                  {shares.data?.map((s) => <option key={s.path} value={s.path}>{s.name}</option>)}
                </Select>
                <Switch checked={!r.paused} onChange={(v) => save({ ...cfg, rules: cfg.rules.map((x) => (x === r ? { ...x, paused: !v } : x)) })} label={r.paused ? tr("Paused") : tr("Active")} />
                {r.dir && <Button size="sm" variant="ghost" onClick={() => save({ ...cfg, rules: cfg.rules.filter((x) => x !== r) })}>{tr("Remove")}</Button>}
              </div>
            ))}
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" variant="secondary" onClick={() => void addFolder()}>{tr("Add a photo folder")}</Button>
              {syncOk === false && <span className="text-[12px] text-warn">{tr("Background sync is not available here: use the Send button.")}</span>}
              {syncOk === true && <span className="text-[12px] text-muted">{tr("Also uploaded in the background while the app is closed.")}</span>}
            </div>
            {!cfg.rules.length && <div className="text-[12px] text-muted">{tr("No folder chosen yet: add the photo folder of this phone (for example DCIM/Camera) to upload automatically, or use the Send button to pick files by hand.")}</div>}
          </div>
        </>}
        {busy && !cfg.rules.length && <div className="text-muted text-[12px]">{tr("Sending… {0}%", pct)}</div>}
        <ErrorBox error={err} />
      </div>
    </Card>
  );
}
