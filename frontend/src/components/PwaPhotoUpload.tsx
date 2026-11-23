import { useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Images, Send } from "lucide-react";
import { api, q } from "../lib/api";
import type { Share } from "../lib/types";
import { tr } from "../lib/i18n";
import { Button, Card, ErrorBox, Field, Select, Switch, useUI } from "./ui";

const KEY = "strata-pwa-upload";
type Cfg = { enabled: boolean; target: string; paused: boolean };

const load = (): Cfg => {
  try {
    return { enabled: false, target: "", paused: false, ...JSON.parse(localStorage.getItem(KEY) ?? "{}") };
  } catch {
    return { enabled: false, target: "", paused: false };
  }
};

const mediaRe = /\.(jpe?g|png|gif|webp|heic|heif|mp4|mov|webm|mkv|m4v)$/i;

/** Sends the new photos and videos of this phone to a chosen shared folder
 *  (roadmap 0.15, PWA upload; only shown when the interface runs as an app). */
export function PwaPhotoUpload() {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [cfg, setCfg] = useState<Cfg>(load);
  const [busy, setBusy] = useState(false);
  const [pct, setPct] = useState(0);
  const [err, setErr] = useState<unknown>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const standalone = window.matchMedia?.("(display-mode: standalone)").matches || (navigator as unknown as { standalone?: boolean }).standalone === true;
  if (!standalone) return null;

  const save = (c: Cfg) => {
    setCfg(c);
    localStorage.setItem(KEY, JSON.stringify(c));
  };

  const send = async (files: File[]) => {
    if (!cfg.target || cfg.paused) return;
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
        xhr.open("POST", `/api/files/upload${q({ path: cfg.target })}`);
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

  return (
    <Card title={<span className="flex items-center gap-2"><Images size={16} />{tr("Send the photos of this phone")}</span>}
      actions={cfg.enabled && cfg.target && !cfg.paused && <Button size="sm" variant="primary" loading={busy} icon={<Send size={14} />} onClick={() => fileRef.current?.click()}>{tr("Send new photos now")}</Button>}>
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
          <Switch checked={cfg.paused} onChange={(v) => save({ ...cfg, paused: v })} label={cfg.paused ? tr("Resume") : tr("Pause")} />
        </>}
        {busy && <div className="text-muted text-[12px]">{tr("Sending… {0}%", pct)}</div>}
        <ErrorBox error={err} />
      </div>
    </Card>
  );
}
