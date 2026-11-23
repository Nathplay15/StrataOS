import { useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Cctv, Download, ExternalLink, FileCode, Pencil, Play, Plug, Plus, Scissors, Trash2, Video } from "lucide-react";
import { api, ApiError, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import { bytes, cx, dateTime } from "../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, Tabs, useUI } from "../components/ui";
import { srv, tr } from "../lib/i18n";
import { caps } from "../components/files/MediaPlayer";

type Status = { state: string; since?: string; error?: string; restarts: number };
type Camera = {
  id: string; name: string; url: string; subUrl?: string; username?: string; password?: string; enabled: boolean; engine?: string; audio?: boolean; motion?: boolean; zones?: string;
  keepDays?: number; maxGb?: number; status: Status; size: number; segments: number; oldest?: string; frigateName?: string; motionAt?: string;
};
type Frigate = {
  installed: boolean; docker: boolean; detector: string; resolved: string; intelGpu: string; coral: boolean; objects: string[]; allObjects: string[];
  notify: boolean; notifyMinutes: number; cameras: number; running?: boolean; webPort?: string; mediaDir?: string; error?: string;
};
type Vol = { path: string; label: string; free: number };
type Data = { installed: boolean; folder: string; folderError: string; alertMinutes: number; segmentSeconds: number; clipPre: number; clipPost: number; clipKeepDays: number; cameras: Camera[]; volumes: Vol[] | null; frigate: Frigate };
type Clip = { name: string; at: string; size: number };
type Segment = { name: string; start: string; end: string; size: number; open?: boolean };
type Recs = { days: string[]; day: string; segments: Segment[] };
type SegInfo = { video: string; audio?: string; duration: number; mode: "native" | "remux" | "transcode"; direct: boolean };
type FrigateEvent = { id: string; label: string; score: number; start: string; end?: string };
type FrigateEvents = { installed: boolean; day: string; webPort?: string; error?: string; events: FrigateEvent[] };

const MASK = "********";

const stateLabels: Record<string, [string, "ok" | "warn" | "danger" | "neutral" | "accent"]> = {
  recording: [tr("Recording"), "ok"],
  connecting: [tr("Connecting"), "warn"],
  offline: [tr("Offline"), "danger"],
  stopped: [tr("Not recording"), "neutral"],
  disabled: [tr("Disabled"), "neutral"],
  frigate: [tr("Recorded by Frigate"), "accent"],
};

function StateBadge({ s }: { s: Status }) {
  const [label, tone] = stateLabels[s.state] ?? [s.state, "neutral"];
  return <span title={s.error ? srv(s.error) : undefined}><Badge tone={tone} dot>{label}</Badge></span>;
}

/* ---------- Camera dialog ---------- */

type Draft = { name: string; url: string; subUrl: string; username: string; password: string; enabled: boolean; engine: string; audio: boolean; motion: boolean; zones: string; keepDays: string; maxGb: string };

const draftOf = (c?: Camera): Draft => ({
  name: c?.name ?? "", url: c?.url ?? "", subUrl: c?.subUrl ?? "", username: c?.username ?? "", password: c?.password ?? "",
  enabled: c?.enabled ?? true, engine: c?.engine ?? "", audio: c?.audio ?? false, motion: c?.motion ?? false, zones: c?.zones ?? "", keepDays: String(c?.keepDays ?? 14), maxGb: c?.maxGb ? String(c.maxGb) : "",
});

const bodyOf = (d: Draft) => ({
  name: d.name, url: d.url, subUrl: d.subUrl, username: d.username, password: d.password, enabled: d.enabled, engine: d.engine, audio: d.audio, motion: d.motion, zones: d.zones,
  keepDays: parseInt(d.keepDays) || 0, maxGb: parseFloat(d.maxGb) || 0,
});

/* ---------- Detection zones ---------- */

type ZoneRect = { x: number; y: number; w: number; h: number };

const parseZones = (s: string): ZoneRect[] => {
  try {
    const z = JSON.parse(s || "[]");
    return Array.isArray(z) ? z.filter((r) => r && typeof r.x === "number" && typeof r.y === "number" && typeof r.w === "number" && typeof r.h === "number") : [];
  } catch { return []; }
};

const round4 = (v: number) => Math.round(v * 10000) / 10000;

function ZoneEditor({ camId, img, value, onChange }: { camId?: string; img: string | null; value: string; onChange: (v: string) => void }) {
  const [zones, setZones] = useState<ZoneRect[]>(() => parseZones(value));
  const [notice, setNotice] = useState("");
  const box = useRef<HTMLDivElement | null>(null);
  const drag = useRef<{ x0: number; y0: number; x1: number; y1: number } | null>(null);
  const [cur, setCur] = useState<ZoneRect | null>(null);
  const src = camId ? `/api/nvr/cameras/${camId}/live.jpg` : img;
  const commit = (z: ZoneRect[]) => { setZones(z); setNotice(""); onChange(z.length ? JSON.stringify(z) : ""); };
  const frac = (e: MouseEvent<HTMLDivElement>) => {
    const r = box.current?.getBoundingClientRect();
    if (!r || !r.width || !r.height) return { x: 0, y: 0 };
    return { x: Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)), y: Math.min(1, Math.max(0, (e.clientY - r.top) / r.height)) };
  };
  const norm = (d: { x0: number; y0: number; x1: number; y1: number }): ZoneRect => ({
    x: Math.min(d.x0, d.x1), y: Math.min(d.y0, d.y1), w: Math.abs(d.x1 - d.x0), h: Math.abs(d.y1 - d.y0),
  });
  const endDrag = () => {
    const d = drag.current;
    drag.current = null;
    if (!d) return;
    const r = norm(d);
    if (r.w >= 0.02 && r.h >= 0.02) {
      if (zones.length >= 4) setNotice(tr("At most {0} zones", 4));
      else commit([...zones, { x: round4(r.x), y: round4(r.y), w: round4(r.w), h: round4(r.h) }]);
    }
    setCur(null);
  };
  const rectStyle = (z: ZoneRect) => ({ left: `${z.x * 100}%`, top: `${z.y * 100}%`, width: `${z.w * 100}%`, height: `${z.h * 100}%` });
  return (
    <div className="space-y-2">
      <div className="text-[12.5px] font-medium">{tr("Detection zones")}</div>
      <div className="text-muted text-[12px]">{tr("Draw the rectangles that matter on the picture; the rest of the frame is ignored.")}</div>
      {src ? (
        <div ref={box} className="relative inline-block max-w-full select-none">
          <img src={src} alt="" draggable={false} className="block max-h-72 max-w-full rounded-lg border border-line" />
          <div className="absolute inset-0 cursor-crosshair rounded-lg" style={{ touchAction: "none" }}
            onMouseDown={(e) => { if (e.button !== 0) return; const p = frac(e); drag.current = { x0: p.x, y0: p.y, x1: p.x, y1: p.y }; setCur({ x: p.x, y: p.y, w: 0, h: 0 }); }}
            onMouseMove={(e) => { if (!drag.current) return; const p = frac(e); drag.current.x1 = p.x; drag.current.y1 = p.y; setCur(norm(drag.current)); }}
            onMouseUp={endDrag}
            onMouseLeave={() => { if (drag.current) endDrag(); }}
          >
            {zones.map((z, i) => (
              <div key={i} className="absolute rounded border-2 border-accent bg-accent-soft" style={rectStyle(z)} onMouseDown={(e) => e.stopPropagation()}>
                <button type="button" title={tr("Remove")} onClick={(e) => { e.stopPropagation(); commit(zones.filter((_, j) => j !== i)); }}
                  className="absolute -right-2 -top-2 flex h-4 w-4 items-center justify-center rounded-full bg-accent text-[10px] leading-none text-white">×</button>
              </div>
            ))}
            {cur && cur.w > 0 && cur.h > 0 && <div className="absolute rounded border-2 border-dashed border-accent" style={rectStyle(cur)} />}
          </div>
        </div>
      ) : (
        <div className="text-muted text-[12px]">{tr("Test the connection to see the picture and draw the zones.")}</div>
      )}
      {src && !zones.length && <div className="text-muted text-[12px]">{tr("No zones: the whole picture is watched")}</div>}
      {!!zones.length && (
        <div className="flex flex-wrap items-center gap-1.5">
          {zones.map((_, i) => <Badge key={i} tone="accent">{tr("Zone {0}", i + 1)}</Badge>)}
          <Button size="sm" variant="ghost" onClick={() => commit([])}>{tr("Clear the zones")}</Button>
        </div>
      )}
      {notice && <div className="text-[12px] text-danger">{notice}</div>}
    </div>
  );
}

function CameraModal({ cam, frigate, onClose }: { cam?: Camera; frigate: Frigate; onClose: () => void }) {
  const [d, setD] = useState<Draft>(() => draftOf(cam));
  const up = (k: Partial<Draft>) => setD({ ...d, ...k });
  const [img, setImg] = useState<string | null>(null);
  const [testErr, setTestErr] = useState<string | null>(null);
  const [testing, setTesting] = useState(false);
  useEffect(() => () => { if (img) URL.revokeObjectURL(img); }, [img]);
  const save = useAction(() => (cam ? api.put(`/api/nvr/cameras/${cam.id}`, bodyOf(d)) : api.post("/api/nvr/cameras", bodyOf(d))), { success: cam ? tr("Camera updated") : tr("Camera added"), onDone: onClose });
  const test = async (stream: "main" | "sub") => {
    setTesting(true);
    setTestErr(null);
    setImg(null);
    try {
      const res = await fetch("/api/nvr/test", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Strata": "1" }, body: JSON.stringify({ ...bodyOf(d), id: cam?.id, stream }) });
      if (!res.ok) {
        const j = await res.json().catch(() => ({ error: res.statusText }));
        throw new ApiError(res.status, j.error ?? res.statusText);
      }
      setImg(URL.createObjectURL(await res.blob()));
    } catch (e) {
      setTestErr(e instanceof Error ? e.message : String(e));
    } finally {
      setTesting(false);
    }
  };
  return (
    <Modal open wide onClose={onClose} title={cam ? tr("Edit {0}", cam.name) : tr("Add a camera")} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!d.name || !d.url} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <ErrorBox error={save.error} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Name")}><Input value={d.name} onChange={(e) => up({ name: e.target.value })} placeholder={tr("Front door")} /></Field>
          <Field label={tr("Recorded by")}>
            <Select value={d.engine} onChange={(e) => up({ engine: e.target.value })}>
              <option value="">{tr("Built-in recorder")}</option>
              <option value="frigate">{tr("Frigate (object detection)")}{frigate.installed ? "" : ` — ${tr("not installed")}`}</option>
            </Select>
          </Field>
        </div>
        <Field label={tr("Main stream URL")} hint={tr("RTSP (rtsp://address:554/path) or HTTP (MJPEG). See the camera manual for the path. The main stream is recorded.")}>
          <Input className="mono" value={d.url} onChange={(e) => up({ url: e.target.value })} placeholder="rtsp://192.168.1.20:554/stream1" />
        </Field>
        <Field label={tr("Sub-stream URL (optional)")} hint={tr("A low resolution stream of the same camera, used for the live view (and for detection by Frigate): much lighter for the NAS.")}>
          <Input className="mono" value={d.subUrl} onChange={(e) => up({ subUrl: e.target.value })} placeholder="rtsp://192.168.1.20:554/stream2" />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("User name")}><Input autoComplete="off" value={d.username} onChange={(e) => up({ username: e.target.value })} /></Field>
          <Field label={tr("Password")} hint={cam?.password ? tr("Leave as is to keep the saved password.") : undefined}>
            <Input type="password" autoComplete="new-password" value={d.password} onFocus={() => d.password === MASK && up({ password: "" })} onChange={(e) => up({ password: e.target.value })} />
          </Field>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" icon={<Plug size={14} />} loading={testing} disabled={!d.url} onClick={() => test("main")}>{tr("Test connection")}</Button>
          {d.subUrl && <Button size="sm" variant="ghost" disabled={testing} onClick={() => test("sub")}>{tr("Test the sub-stream")}</Button>}
        </div>
        {testErr && <ErrorBox error={testErr} />}
        {img && <img src={img} alt="" className="max-h-64 rounded-lg border border-line" />}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Keep (days)")} hint={tr("0 = no limit")}><Input type="number" min={0} value={d.keepDays} onChange={(e) => up({ keepDays: e.target.value })} /></Field>
          {d.engine === "" ? (
            <Field label={tr("Maximum size (GB)")} hint={tr("The oldest recordings are deleted first. Empty = no limit.")}><Input type="number" min={0} value={d.maxGb} onChange={(e) => up({ maxGb: e.target.value })} /></Field>
          ) : (
            <div className="text-muted text-[12.5px] sm:pt-6">{tr("Frigate keeps recordings by days only; it deletes the oldest ones itself when the disk is nearly full.")}</div>
          )}
        </div>
        <div className="space-y-2">
          <Checkbox checked={d.enabled} onChange={(v) => up({ enabled: v })} label={tr("Enabled")} />
          {d.engine === "" && <Checkbox checked={d.audio} onChange={(v) => up({ audio: v })} label={tr("Record the sound (converted to AAC)")} />}
          {d.engine === "" && <div>
            <Checkbox checked={d.motion} onChange={(v) => up({ motion: v })} label={tr("Detect movement and warn me")} />
            {d.motion && <div className="ml-7 text-muted text-[12px]">{tr("A picture and a short clip are kept, and a notification carrying both is sent each time something moves (at most every 90 seconds).")}</div>}
            {d.motion && <div className="ml-7 mt-2"><ZoneEditor camId={cam?.id} img={img} value={d.zones} onChange={(v) => up({ zones: v })} /></div>}
          </div>}
        </div>
      </div>
    </Modal>
  );
}

/* ---------- Cameras ---------- */

function Cameras({ data }: { data: Data }) {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<Camera | "new" | null>(null);
  const del = useAction((p: { id: string; recs: boolean }) => api.del(`/api/nvr/cameras/${p.id}${p.recs ? "?recordings=1" : ""}`), { success: tr("Camera deleted") });
  const remove = async (c: Camera) => {
    if (!(await confirm({ title: tr("Delete {0}?", c.name), text: tr("Recording stops. Its recordings stay in the recordings folder unless you also delete them below."), danger: true, confirm: tr("Delete") }))) return;
    const recs = c.segments > 0 && (await confirm({ title: tr("Also delete the recordings?"), text: tr("{0} recordings ({1}) of this camera.", c.segments, bytes(c.size)), danger: true, confirm: tr("Delete the recordings") }));
    del.mutate({ id: c.id, recs });
  };
  return (
    <Card title={tr("Cameras")} actions={<Button size="sm" variant="primary" icon={<Plus size={14} />} onClick={() => setEdit("new")}>{tr("Add a camera")}</Button>} pad={!data.cameras.length}>
      {!data.cameras.length ? (
        <Empty icon={<Cctv />} title={tr("No cameras")} text={tr("Add a network camera (RTSP or MJPEG): StrataOS records it continuously, keeps the recordings as long as you choose and shows it live.")} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table">
            <thead><tr><th>{tr("Name")}</th><th>{tr("State")}</th><th>{tr("Recorded by")}</th><th>{tr("Recordings")}</th><th>{tr("Retention")}</th><th /></tr></thead>
            <tbody>
              {data.cameras.map((c) => (
                <tr key={c.id}>
                  <td><div className="font-medium">{c.name}</div><div className="mono text-[11.5px] text-muted">{c.url}</div></td>
                  <td><StateBadge s={c.status} />{c.status.state === "offline" && c.status.error && <div className="mt-1 max-w-xs truncate text-[11.5px] text-danger" title={srv(c.status.error)}>{srv(c.status.error)}</div>}</td>
                  <td>{c.engine === "frigate" ? <Badge tone="accent">Frigate{c.frigateName ? ` · ${c.frigateName}` : ""}</Badge> : <Badge>{tr("Built-in")}</Badge>}</td>
                  <td className="whitespace-nowrap">{c.segments ? <>{bytes(c.size)}<div className="text-[11.5px] text-muted">{tr("since {0}", dateTime(c.oldest))}</div></> : "—"}</td>
                  <td className="whitespace-nowrap text-[12.5px]">{[c.keepDays ? tr("{0} days", c.keepDays) : "", c.maxGb && c.engine !== "frigate" ? `${c.maxGb} GB` : ""].filter(Boolean).join(" · ") || tr("No limit")}{c.motion && <div className="text-[11.5px] text-accent">{c.motionAt ? tr("Movement detected at {0}", dateTime(c.motionAt)) : tr("Movement detection on")}{parseZones(c.zones ?? "").length > 0 && ` \u00b7 ${tr("{0} zones", parseZones(c.zones ?? "").length)}`}</div>}</td>
                  <td className="text-right whitespace-nowrap">
                    <IconButton title={tr("Edit")} onClick={() => setEdit(c)}><Pencil size={14} /></IconButton>
                    <IconButton title={tr("Delete")} onClick={() => remove(c)}><Trash2 size={14} /></IconButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {edit && <CameraModal cam={edit === "new" ? undefined : edit} frigate={data.frigate} onClose={() => setEdit(null)} />}
    </Card>
  );
}

/* ---------- Live ---------- */

function LiveTile({ cam, big, onClick }: { cam: Camera; big: boolean; onClick: () => void }) {
  const [tick, setTick] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  // The next picture is asked once the previous one is shown (about one per second).
  const next = (delay: number) => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setTick((t) => t + 1), delay);
  };
  const onError = async () => {
    try {
      const r = await fetch(`/api/nvr/cameras/${cam.id}/live.jpg`, { credentials: "same-origin" });
      const j = await r.json().catch(() => ({}));
      setErr(j.error ?? r.statusText);
    } catch {
      setErr(tr("No picture"));
    }
    next(5000);
  };
  // Enlarged, an RTSP camera plays as live video (main stream remuxed by the
  // NAS); a codec the browser cannot decode falls back to the pictures.
  const [videoFailed, setVideoFailed] = useState(false);
  const video = big && /^rtsps?:/.test(cam.url) && !videoFailed;
  // Stay close to live: skip ahead when the browser has buffered too much.
  const catchUp = (v: HTMLVideoElement) => {
    if (v.buffered.length && v.buffered.end(v.buffered.length - 1) - v.currentTime > 2) v.currentTime = v.buffered.end(v.buffered.length - 1) - 0.3;
  };
  return (
    <button onClick={onClick} className={cx("group relative aspect-video overflow-hidden rounded-xl border border-line bg-black text-left", big && "ring-1 ring-accent/40")}>
      {video ? (
        <video src={`/api/nvr/cameras/${cam.id}/live.mp4?stream=main`} autoPlay muted playsInline className="h-full w-full object-contain" onError={() => setVideoFailed(true)} onTimeUpdate={(e) => catchUp(e.currentTarget)} />
      ) : (
        <img src={`/api/nvr/cameras/${cam.id}/live.jpg?t=${tick}`} alt="" className="h-full w-full object-contain" onLoad={() => { setErr(null); next(1000); }} onError={onError} />
      )}
      {err && <div className="absolute inset-0 flex items-center justify-center p-4 text-center text-[12.5px] text-white/80">{srv(err)}</div>}
      <div className="absolute inset-x-0 bottom-0 flex items-center justify-between bg-gradient-to-t from-black/70 to-transparent px-3 py-2 text-[12.5px] text-white">
        <span className="font-medium">{cam.name}</span>
        {cam.status.state === "recording" && <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-red-500" />REC</span>}
      </div>
    </button>
  );
}

function Live({ data }: { data: Data }) {
  const list = data.cameras.filter((c) => c.enabled);
  const [cols, setCols] = useState(() => (list.length <= 1 ? 1 : list.length <= 4 ? 2 : 3));
  const [solo, setSolo] = useState<string | null>(null);
  if (!list.length) return <Card><Empty icon={<Video />} title={tr("No enabled camera")} /></Card>;
  const shown = solo ? list.filter((c) => c.id === solo) : list;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-muted text-[12.5px]">{tr("One picture per second, from the sub-stream when there is one. Click a camera to enlarge it and watch its live video.")}</div>
        {!solo && (
          <div className="flex gap-1">{[1, 2, 3, 4].map((n) => <Button key={n} size="sm" variant={cols === n ? "primary" : "ghost"} onClick={() => setCols(n)}>{n}×</Button>)}</div>
        )}
        {solo && <Button size="sm" onClick={() => setSolo(null)}>{tr("Back to all cameras")}</Button>}
      </div>
      <div className="grid gap-3" style={{ gridTemplateColumns: `repeat(${solo ? 1 : cols}, minmax(0, 1fr))` }}>
        {shown.map((c) => <LiveTile key={c.id} cam={c} big={!!solo} onClick={() => setSolo(solo ? null : c.id)} />)}
      </div>
    </div>
  );
}

/* ---------- Recordings ---------- */

const pctOfDay = (t: Date, day: string) => {
  const start = new Date(`${day}T00:00:00`).getTime();
  return Math.max(0, Math.min(100, ((t.getTime() - start) / 864e5) * 100));
};
const hm = (s: string) => new Date(s).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

// Plays a segment: as it is when the browser can (H.264 in MP4), else
// converted on the fly by the NAS (H.265, MJPEG…), started at the chosen second.
function SegmentPlayer({ base, seg }: { base: string; seg: Segment }) {
  const info = useQuery({ queryKey: ["nvr-seg", base, seg.name], queryFn: () => api.get<SegInfo>(`${base}/${seg.name}/info${q(caps)}`), staleTime: Infinity, retry: false });
  const [start, setStart] = useState(0);
  const [from, setFrom] = useState(0);
  if (info.isLoading) return <Spinner />;
  const i = info.data;
  if (!i || i.direct) return <video key={seg.name} controls autoPlay className="w-full rounded-lg bg-black" src={`${base}/${seg.name}`} />;
  const dur = Math.max(1, Math.floor(i.duration || 0));
  return (
    <div className="space-y-2">
      <video key={`${seg.name}-${from}`} controls autoPlay className="w-full rounded-lg bg-black" src={`${base}/${seg.name}/play${q({ ...caps, start: from ? String(from) : "" })}`} />
      <div className="flex flex-wrap items-center gap-3 text-[12.5px]">
        <span className="text-muted">{tr("Start at")}</span>
        <input type="range" min={0} max={dur} value={start} onChange={(e) => setStart(parseInt(e.target.value))} onMouseUp={() => setFrom(start)} onTouchEnd={() => setFrom(start)} onKeyUp={() => setFrom(start)} className="min-w-48 flex-1" />
        <span className="mono w-20">{Math.floor(start / 60)}:{String(start % 60).padStart(2, "0")}</span>
      </div>
      <div className="text-[11.5px] text-muted">{i.mode === "remux" ? tr("Repackaged by the NAS for the browser. Seeking restarts from the chosen second.") : tr("{0} video converted to H.264 by the NAS for the browser. Seeking restarts the conversion from the chosen second.", i.video.toUpperCase())}</div>
    </div>
  );
}

const today = () => {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};

// The recordings Frigate made of a camera: its events with a clip.
function FrigateRecordings({ cam }: { cam: Camera }) {
  const [day, setDay] = useState(today);
  const [play, setPlay] = useState<FrigateEvent | null>(null);
  const evs = useQuery({ queryKey: ["nvr-frigate", cam.id, day], queryFn: () => api.get<FrigateEvents>(`/api/nvr/cameras/${cam.id}/frigate/events${q({ day })}`), refetchInterval: 30000 });
  const base = `/api/nvr/cameras/${cam.id}/frigate/events`;
  const d = evs.data;
  const link = d?.webPort ? `https://${location.hostname}:${d.webPort}/review` : null;
  return (
    <Card title={tr("Frigate recordings")} actions={<div className="flex items-center gap-2">
      <Input type="date" className="w-40" value={day} max={today()} onChange={(e) => { setDay(e.target.value || today()); setPlay(null); }} />
      {link && <a href={link} target="_blank" rel="noreferrer"><Button size="sm" icon={<ExternalLink size={14} />}>{tr("Open Frigate")}</Button></a>}
    </div>}>
      <div className="space-y-4">
        <ErrorBox error={evs.error} />
        {d && !d.installed && <Notice tone="warn">{tr("Frigate is not installed: install it in the Settings tab.")}</Notice>}
        {d?.error && <Notice tone="warn">{srv(d.error)}</Notice>}
        <div className="text-[12.5px] text-muted">{tr("Events detected by Frigate that have a clip. The continuous recordings and the full timeline are in Frigate's interface.")}</div>
        {play && (
          <div className="space-y-2">
            <video key={play.id} controls autoPlay className="w-full rounded-lg bg-black" src={`${base}/${play.id}/clip.mp4`} />
            <div className="flex gap-2">
              <a href={`${base}/${play.id}/clip.mp4?download=1`}><Button size="sm" icon={<Download size={14} />}>{tr("Download the clip")}</Button></a>
              <Button size="sm" variant="ghost" onClick={() => setPlay(null)}>{tr("Close")}</Button>
            </div>
          </div>
        )}
        {evs.isLoading ? <Spinner /> : !d?.events.length ? <Empty title={tr("No event with a clip this day")} /> : (
          <div className="max-h-[28rem] overflow-y-auto">
            <table className="table">
              <thead><tr><th /><th>{tr("Object")}</th><th>{tr("Start")}</th><th>{tr("End")}</th><th /></tr></thead>
              <tbody>
                {d.events.map((e) => (
                  <tr key={e.id} className={cx(play?.id === e.id && "bg-panel-2")}>
                    <td><img src={`${base}/${e.id}/thumbnail.jpg`} alt="" className="h-10 w-16 rounded object-cover" loading="lazy" /></td>
                    <td>{e.label} <span className="text-muted text-[11.5px]">{Math.round(e.score * 100)}%</span></td>
                    <td>{hm(e.start)}</td>
                    <td>{e.end ? hm(e.end) : <Badge tone="ok" dot>{tr("in progress")}</Badge>}</td>
                    <td className="text-right whitespace-nowrap">
                      {e.end && <IconButton title={tr("Play")} onClick={() => setPlay(e)}><Play size={14} /></IconButton>}
                      {e.end && <a href={`${base}/${e.id}/clip.mp4?download=1`}><IconButton title={tr("Download")}><Download size={14} /></IconButton></a>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Card>
  );
}

// The event clips cut around each motion event of a built-in camera.
function EventClips({ camId, keepDays }: { camId: string; keepDays: number }) {
  const { confirm } = useUI();
  const qc = useQueryClient();
  const clips = useQuery({ queryKey: ["nvr-clips", camId], queryFn: () => api.get<{ clips: Clip[] }>(`/api/nvr/cameras/${camId}/clips`), refetchInterval: 30000 });
  const [play, setPlay] = useState<string | null>(null);
  const del = useAction((name: string) => api.del(`/api/nvr/cameras/${camId}/clips/${encodeURIComponent(name)}`), {
    onDone: () => qc.invalidateQueries({ queryKey: ["nvr-clips", camId] }),
  });
  const remove = async (c: Clip) => {
    if (!(await confirm({ title: tr("Delete {0}?", c.name), text: tr("The video of this event will be removed."), danger: true, confirm: tr("Delete") }))) return;
    del.mutate(c.name);
  };
  const base = `/api/nvr/cameras/${camId}/clips`;
  const list = clips.data?.clips ?? [];
  return (
    <Card title={tr("Event clips")} actions={play ? <Button size="sm" variant="ghost" onClick={() => setPlay(null)}>{tr("Close")}</Button> : undefined}>
      <div className="mb-3 text-[12.5px] text-muted">{tr("A clip is cut around each movement event and kept {0} days.", keepDays)}</div>
      <ErrorBox error={clips.error || del.error} />
      {play && <video key={play} controls autoPlay className="mb-3 w-full rounded-lg bg-black" src={`${base}/${encodeURIComponent(play)}`} />}
      {clips.isLoading ? <Spinner /> : !list.length ? <Empty title={tr("No clip yet")} /> : (
        <div className="max-h-80 overflow-y-auto">
          <table className="table">
            <thead><tr><th>{tr("Time")}</th><th>{tr("Size")}</th><th /></tr></thead>
            <tbody>
              {list.map((c) => (
                <tr key={c.name} className={cx(play === c.name && "bg-panel-2")}>
                  <td className="whitespace-nowrap">{dateTime(c.at)}</td>
                  <td>{bytes(c.size)}</td>
                  <td className="text-right whitespace-nowrap">
                    <IconButton title={tr("Play")} onClick={() => setPlay(c.name)}><Play size={14} /></IconButton>
                    <a href={`${base}/${encodeURIComponent(c.name)}?download=1`}><IconButton title={tr("Download")}><Download size={14} /></IconButton></a>
                    <IconButton title={tr("Delete")} onClick={() => remove(c)}><Trash2 size={14} /></IconButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function Recordings({ data }: { data: Data }) {
  const cams = data.cameras;
  const [camId, setCamId] = useState(cams[0]?.id ?? "");
  const [day, setDay] = useState("");
  const [play, setPlay] = useState<Segment | null>(null);
  const [clip, setClip] = useState({ from: "0", to: "60" });
  const cam = cams.find((c) => c.id === camId);
  const recs = useQuery({ queryKey: ["nvr-recs", camId, day], queryFn: () => api.get<Recs>(`/api/nvr/cameras/${camId}/recordings${q({ day })}`), enabled: !!camId, refetchInterval: 30000 });
  const segs = recs.data?.segments ?? [];
  const base = `/api/nvr/cameras/${camId}/recordings`;
  const hours = useMemo(() => Array.from({ length: 25 }, (_, i) => i), []);
  const curDay = recs.data?.day ?? day;
  const [clipPlay, setClipPlay] = useState<Clip | null>(null);
  const dayClips = useQuery({ queryKey: ["nvr-clips-day", camId, curDay], queryFn: () => api.get<{ clips: Clip[] }>(`/api/nvr/cameras/${camId}/clips${q({ day: curDay })}`), enabled: !!camId && !!curDay && cam?.engine !== "frigate", refetchInterval: 30000 });
  const dayClipList = dayClips.data?.clips ?? [];
  if (!cams.length) return <Card><Empty icon={<Video />} title={tr("No cameras")} /></Card>;
  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <Field label={tr("Camera")} className="min-w-48">
            <Select value={camId} onChange={(e) => { setCamId(e.target.value); setDay(""); setPlay(null); setClipPlay(null); }}>
              {cams.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
          </Field>
          <Field label={tr("Day")} className="min-w-40">
            <Select value={curDay} onChange={(e) => { setDay(e.target.value); setPlay(null); setClipPlay(null); }}>
              {(recs.data?.days.length ? recs.data.days : [curDay]).map((d) => <option key={d} value={d}>{new Date(`${d}T12:00:00`).toLocaleDateString()}</option>)}
            </Select>
          </Field>
        </div>
        {cam?.engine === "frigate" && (
          <div className="mt-4"><Notice>{tr("This camera is recorded by Frigate: its clips are listed below. The timeline shows what the built-in recorder recorded before.")}</Notice></div>
        )}
        <div className="mt-5">
          <div className="relative h-12 rounded-lg border border-line bg-panel-2">
            {segs.map((s) => (
              <button key={s.name} title={`${hm(s.start)} – ${hm(s.end)}`} onClick={() => { if (!s.open) { setPlay(s); setClipPlay(null); } }}
                className={cx("absolute top-4 bottom-1 rounded-sm", play?.name === s.name ? "bg-accent-2" : s.open ? "bg-ok/60" : "bg-accent hover:brightness-125")}
                style={{ left: `${pctOfDay(new Date(s.start), curDay)}%`, width: `max(2px, ${pctOfDay(new Date(s.end), curDay) - pctOfDay(new Date(s.start), curDay)}%)` }} />
            ))}
            {dayClipList.map((c) => (
              <button key={c.name} title={`${hm(c.at)} · ${tr("Movement events")}`} onClick={() => { setClipPlay(c); setPlay(null); }}
                className={cx("absolute top-1 h-2 w-1.5 -translate-x-1/2 rounded-sm bg-red-500 hover:brightness-125", clipPlay?.name === c.name && "ring-1 ring-white")}
                style={{ left: `${pctOfDay(new Date(c.at), curDay)}%` }} />
            ))}
          </div>
          <div className="relative mt-1 h-4 text-[10.5px] text-muted">
            {hours.filter((h) => h % 3 === 0).map((h) => <span key={h} className="absolute -translate-x-1/2" style={{ left: `${(h / 24) * 100}%` }}>{h}h</span>)}
          </div>
          <div className="mt-1 flex items-center gap-4 text-[10.5px] text-muted">
            <span className="flex items-center gap-1.5"><span className="inline-block h-2.5 w-3 rounded-sm bg-accent" />{tr("Recordings")}</span>
            <span className="flex items-center gap-1.5"><span className="inline-block h-2 w-1.5 rounded-sm bg-red-500" />{tr("Movement events")}</span>
          </div>
        </div>
      </Card>
      {cam?.engine === "frigate" && <FrigateRecordings key={cam.id} cam={cam} />}
      {cam && cam.engine !== "frigate" && <EventClips key={cam.id} camId={cam.id} keepDays={data.clipKeepDays || 30} />}
      {clipPlay && (
        <Card title={<span>{cam?.name} · {hm(clipPlay.at)}</span>} actions={<Button size="sm" variant="ghost" onClick={() => setClipPlay(null)}>{tr("Close")}</Button>}>
          <video key={clipPlay.name} controls autoPlay className="w-full rounded-lg bg-black" src={`/api/nvr/cameras/${camId}/clips/${encodeURIComponent(clipPlay.name)}`} />
          <div className="mt-3">
            <a href={`/api/nvr/cameras/${camId}/clips/${encodeURIComponent(clipPlay.name)}?download=1`}><Button size="sm" icon={<Download size={14} />}>{tr("Download the clip")}</Button></a>
          </div>
        </Card>
      )}
      {play && (
        <Card title={<span>{cam?.name} · {hm(play.start)} – {hm(play.end)}</span>} actions={<Button size="sm" variant="ghost" onClick={() => setPlay(null)}>{tr("Close")}</Button>}>
          <SegmentPlayer key={play.name} base={base} seg={play} />
          <div className="mt-3 flex flex-wrap items-end gap-3">
            <a href={`${base}/${play.name}?download=1`}><Button size="sm" icon={<Download size={14} />}>{tr("Download the segment")}</Button></a>
            <Field label={tr("Clip from (s)")}><Input className="w-24" type="number" min={0} value={clip.from} onChange={(e) => setClip({ ...clip, from: e.target.value })} /></Field>
            <Field label={tr("to (s)")}><Input className="w-24" type="number" min={1} value={clip.to} onChange={(e) => setClip({ ...clip, to: e.target.value })} /></Field>
            <a href={`${base}/${play.name}/clip${q({ from: clip.from, to: clip.to })}`}><Button size="sm" icon={<Scissors size={14} />}>{tr("Download the clip")}</Button></a>
          </div>
        </Card>
      )}
      <Card title={tr("Recordings of the day")} pad={false}>
        {recs.isLoading ? <Spinner /> : !segs.length ? <Empty title={tr("Nothing recorded this day")} /> : (
          <div className="max-h-96 overflow-y-auto">
            <table className="table">
              <thead><tr><th>{tr("Start")}</th><th>{tr("End")}</th><th>{tr("Size")}</th><th /></tr></thead>
              <tbody>
                {[...segs].reverse().map((s) => (
                  <tr key={s.name}>
                    <td>{hm(s.start)}</td>
                    <td>{s.open ? <Badge tone="ok" dot>{tr("recording")}</Badge> : hm(s.end)}</td>
                    <td>{bytes(s.size)}</td>
                    <td className="text-right whitespace-nowrap">
                      {!s.open && <IconButton title={tr("Play")} onClick={() => setPlay(s)}><Play size={14} /></IconButton>}
                      {!s.open && <a href={`${base}/${s.name}?download=1`}><IconButton title={tr("Download")}><Download size={14} /></IconButton></a>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <div className="text-muted text-[12px]">{tr("The built-in recorder does not detect motion or objects: use Frigate for that (Settings).")}</div>
    </div>
  );
}

/* ---------- Settings ---------- */

function FrigateCard({ data }: { data: Data }) {
  const f = data.frigate;
  const [opts, setOpts] = useState({ detector: f.detector || "", objects: f.objects, notify: f.notify, notifyMinutes: String(f.notifyMinutes || 10) });
  const [cfg, setCfg] = useState<string | null>(null);
  const install = useAction(() => api.post("/api/nvr/frigate/install"));
  const save = useAction(() => api.put("/api/nvr/frigate", { ...opts, notifyMinutes: parseInt(opts.notifyMinutes) || 0 }), { success: tr("Frigate options saved") });
  const showCfg = useAction(() => api.get<{ config: string }>("/api/nvr/frigate/config"), { onDone: (r) => setCfg(r.config) });
  const toggle = (o: string) => setOpts({ ...opts, objects: opts.objects.includes(o) ? opts.objects.filter((x) => x !== o) : [...opts.objects, o] });
  const detectors: Record<string, string> = { "": tr("Automatic"), cpu: tr("Processor (CPU)"), openvino: tr("Intel GPU (OpenVINO)"), edgetpu: tr("Coral USB") };
  return (
    <Card title={<span className="flex items-center gap-2">Frigate {f.installed ? (f.running ? <Badge tone="ok" dot>{tr("running")}</Badge> : <Badge tone="warn" dot>{tr("stopped")}</Badge>) : <Badge>{tr("not installed")}</Badge>}</span>}
      actions={<>
        <Button size="sm" variant="ghost" icon={<FileCode size={14} />} loading={showCfg.isPending} onClick={() => showCfg.mutate()}>{tr("Generated configuration")}</Button>
        {f.installed && f.webPort && <a href={`https://${location.hostname}:${f.webPort}`} target="_blank" rel="noreferrer"><Button size="sm" icon={<ExternalLink size={14} />}>{tr("Open Frigate")}</Button></a>}
      </>}>
      <div className="space-y-4">
        <div className="text-[13px] text-muted">{tr("Frigate records the cameras you hand to it (\"Recorded by: Frigate\") and detects people, cars and animals with AI. StrataOS writes its configuration from the cameras defined here and notifies its events. Each camera is recorded by one engine at a time.")}</div>
        {f.error && <ErrorBox error={f.error} />}
        <div className="flex flex-wrap gap-2 text-[12.5px]">
          <Badge tone={f.intelGpu ? "ok" : "neutral"}>{f.intelGpu ? tr("Intel GPU found: {0}", f.intelGpu) : tr("No Intel GPU")}</Badge>
          <Badge tone={f.coral ? "ok" : "neutral"}>{f.coral ? tr("Coral USB found") : tr("No Coral USB")}</Badge>
          <Badge tone="accent">{tr("Detector in use: {0}", detectors[f.resolved] ?? f.resolved)}</Badge>
        </div>
        {!f.installed && (
          !f.docker ? <Notice tone="warn">{tr("Frigate runs in Docker: install Docker first (Settings → Components).")}</Notice> : (
            <div className="flex flex-wrap items-center gap-3">
              <Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} disabled={!data.folder || !!data.folderError} onClick={() => install.mutate()}>{tr("Install Frigate")}</Button>
              <span className="text-[12px] text-muted">{tr("Large download (about 2 GB). Its recordings go to {0}/frigate.", data.folder || "…")}</span>
            </div>
          )
        )}
        {f.installed && <div className="text-[12px] text-muted">{tr("Sign in to Frigate with the admin password it prints in its logs at first start (Docker → Containers → frigate → Logs). Remove it from the App Center.")}</div>}
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Detector")}>
            <Select value={opts.detector} onChange={(e) => setOpts({ ...opts, detector: e.target.value })}>
              {Object.entries(detectors).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
            </Select>
          </Field>
          <Field label={tr("Notify at most every (minutes, per camera)")}><Input type="number" min={1} value={opts.notifyMinutes} onChange={(e) => setOpts({ ...opts, notifyMinutes: e.target.value })} /></Field>
        </div>
        <Field label={tr("Objects to detect")}>
          <div className="flex flex-wrap gap-x-4 gap-y-2">{f.allObjects.map((o) => <Checkbox key={o} checked={opts.objects.includes(o)} onChange={() => toggle(o)} label={o} />)}</div>
        </Field>
        <Switch checked={opts.notify} onChange={(v) => setOpts({ ...opts, notify: v })} label={tr("Notify Frigate events")} hint={tr("A person is notified as a warning, other objects as information.")} />
        <div className="flex justify-end"><Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button></div>
      </div>
      <Modal open={cfg !== null} wide onClose={() => setCfg(null)} title="config.yml">
        <div className="mb-2 text-[12px] text-muted">{tr("Camera credentials are not in this file: Frigate reads them from secret files.")}</div>
        <pre className="mono max-h-[60vh] overflow-auto rounded-lg bg-panel-2 p-3 text-[12px]">{cfg}</pre>
      </Modal>
    </Card>
  );
}

function Settings({ data }: { data: Data }) {
  const vols = data.volumes ?? [];
  const initVol = vols.find((v) => data.folder.startsWith(v.path + "/"))?.path ?? vols[0]?.path ?? "";
  const [vol, setVol] = useState(initVol);
  const [sub, setSub] = useState(data.folder && initVol ? data.folder.slice(initVol.length + 1) : "surveillance");
  const [alert, setAlert] = useState(String(data.alertMinutes || 5));
  const [clipPre, setClipPre] = useState(String(data.clipPre || 0));
  const [clipPost, setClipPost] = useState(String(data.clipPost || 0));
  const [clipKeep, setClipKeep] = useState(String(data.clipKeepDays || 0));
  const save = useAction(() => api.put("/api/nvr/settings", {
    folder: `${vol}/${sub.replace(/^\/+/, "")}`, alertMinutes: parseInt(alert) || 0, segmentSeconds: data.segmentSeconds,
    clipPre: parseInt(clipPre) || 0, clipPost: parseInt(clipPost) || 0, clipKeepDays: parseInt(clipKeep) || 0,
  }), { success: tr("Settings saved") });
  return (
    <div className="space-y-5">
      <Card title={tr("Recordings")}>
        <div className="space-y-4">
          <ErrorBox error={save.error} />
          {data.folderError && <Notice tone="warn">{srv(data.folderError)}</Notice>}
          {!vols.length && <Notice tone="warn">{tr("No mounted volume: create one first.")}</Notice>}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Volume")}>
              <Select value={vol} onChange={(e) => setVol(e.target.value)}>{vols.map((v) => <option key={v.path} value={v.path}>{v.label} · {tr("{0} free", bytes(v.free))}</option>)}</Select>
            </Field>
            <Field label={tr("Folder")} hint={tr("One subfolder per camera, 10-minute files named by their start time. Changing the folder leaves the existing recordings where they are.")}>
              <Input className="mono" value={sub} onChange={(e) => setSub(e.target.value)} />
            </Field>
          </div>
          <Field label={tr("Alert when a camera is unreachable for (minutes)")}><Input className="w-32" type="number" min={1} value={alert} onChange={(e) => setAlert(e.target.value)} /></Field>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label={tr("Before the event (seconds)")} hint={tr("0 = 10 s")}><Input type="number" min={0} max={120} value={clipPre} onChange={(e) => setClipPre(e.target.value)} /></Field>
            <Field label={tr("After the event (seconds)")} hint={tr("0 = 10 s")}><Input type="number" min={0} max={120} value={clipPost} onChange={(e) => setClipPost(e.target.value)} /></Field>
            <Field label={tr("Keep the clips (days)")} hint={tr("0 = 30 days")}><Input type="number" min={0} value={clipKeep} onChange={(e) => setClipKeep(e.target.value)} /></Field>
          </div>
          <div className="text-[12.5px] text-muted">{tr("A clip is cut around each movement event and kept {0} days.", parseInt(clipKeep) || 30)}</div>
          <div className="flex justify-end"><Button variant="primary" loading={save.isPending} disabled={!vol || !sub} onClick={() => save.mutate()}>{tr("Save")}</Button></div>
        </div>
      </Card>
      <FrigateCard data={data} />
    </div>
  );
}

/* ---------- Page ---------- */

type Tab = "cameras" | "live" | "recordings" | "settings";

export default function Surveillance() {
  const [tab, setTab] = useState<Tab>("cameras");
  const { data, isLoading, error } = useQuery({ queryKey: ["nvr"], queryFn: () => api.get<Data>("/api/nvr"), refetchInterval: 10000 });
  const install = useAction(() => api.post("/api/system/components/nvr/install"));
  const header = <PageHeader title={tr("Surveillance")} subtitle={tr("Network cameras: continuous recording, live view, playback")} icon={<Cctv size={20} />} />;
  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed) {
    return (
      <div>
        {header}
        <Card><Empty icon={<Cctv />} title={tr("The recorder is not installed")} text={tr("The built-in recorder uses ffmpeg (also used by Photos). Nothing runs until you add a camera.")} action={<Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card>
      </div>
    );
  }
  return (
    <div>
      {header}
      {(!data.folder || data.folderError) && tab !== "settings" && (
        <div className="mb-4"><Notice tone="warn">{data.folderError ? srv(data.folderError) : tr("Choose the recordings folder in Settings: nothing is recorded until then.")}</Notice></div>
      )}
      <Tabs<Tab> value={tab} onChange={setTab} tabs={[
        { id: "cameras", label: tr("Cameras") }, { id: "live", label: tr("Live") }, { id: "recordings", label: tr("Recordings") }, { id: "settings", label: tr("Settings") },
      ]} />
      {tab === "cameras" && <Cameras data={data} />}
      {tab === "live" && <Live data={data} />}
      {tab === "recordings" && <Recordings data={data} />}
      {tab === "settings" && <Settings data={data} />}
    </div>
  );
}
