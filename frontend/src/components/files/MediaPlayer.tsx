// Audio and video player. Files the browser plays are read directly (it seeks
// with Range requests); others are remuxed or transcoded by the NAS into a
// stream that starts at a given time, so seeking restarts the stream and the
// timeline below is drawn by the player itself.
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Captions, Music, Pause, Play, Volume2, VolumeX } from "lucide-react";
import { ApiError, api, q } from "../../lib/api";
import type { FileEntry } from "../../lib/types";
import { cx } from "../../lib/format";
import { srv, tr } from "../../lib/i18n";
import { Badge, Button, ErrorBox, Notice, Select, Spinner } from "../ui";
import { Toolbar, ViewerCard, getPos, inlineURL, setPos, useViewerCaps } from "./common";
import { useAction } from "../../lib/hooks";

interface Track { index: number; codec: string; lang?: string; title?: string; channels?: number; default?: boolean }
interface Probe {
  installed: boolean;
  plan?: { mode: "native" | "remux" | "transcode"; audioOnly: boolean; type: string; copyVideo: boolean; copyAudio: boolean; video?: { codec: string; width: number; height: number }; audio?: { codec: string } };
  duration?: number;
  audioTracks?: Track[];
  subtitles?: Track[];
  vaapi?: boolean;
}

/** Optional codecs this browser decodes (sent to the NAS for its decision). */
export const caps = (() => {
  const ms = (globalThis as { MediaSource?: { isTypeSupported(t: string): boolean } }).MediaSource;
  const ok = (t: string) => { try { return !!ms?.isTypeSupported(t); } catch { return false; } };
  return { hevc: ok('video/mp4; codecs="hvc1.1.6.L93.B0"') ? 1 : undefined, av1: ok('video/mp4; codecs="av01.0.05M.08"') ? 1 : undefined };
})();

const clock = (t: number) => {
  if (!isFinite(t) || t < 0) t = 0;
  const h = Math.floor(t / 3600), m = Math.floor((t % 3600) / 60), s = Math.floor(t % 60);
  return (h ? `${h}:${String(m).padStart(2, "0")}` : String(m)) + ":" + String(s).padStart(2, "0");
};
const trackName = (t: Track) => [t.title, t.lang?.toUpperCase(), t.codec, t.channels ? `${t.channels} ch` : ""].filter(Boolean).join(" · ");

export default function MediaPlayer({ entry, audioOnly }: { entry: FileEntry; audioOnly: boolean }) {
  const [audio, setAudio] = useState<number | undefined>(undefined);
  const probe = useQuery({
    queryKey: ["probe", entry.path, entry.modTime, audio],
    queryFn: () => api.get<Probe>(`/api/files/probe${q({ path: entry.path, audio, ...caps })}`),
    retry: false,
    staleTime: Infinity,
  });
  if (probe.isLoading) return <><Toolbar entry={entry} /><Spinner /></>;
  const p = probe.data;
  // No ffmpeg, or the NAS cannot read it: let the browser try the file itself.
  if (!p?.installed || !p.plan || p.plan.mode === "native") {
    return <Native entry={entry} audioOnly={audioOnly || !!p?.plan?.audioOnly} missingTools={p?.installed === false} probeError={probe.error} />;
  }
  return <Streamed entry={entry} probe={p} audio={audio} setAudio={setAudio} />;
}

function Native({ entry, audioOnly, missingTools, probeError }: { entry: FileEntry; audioOnly: boolean; missingTools: boolean; probeError: unknown }) {
  const ref = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const [failed, setFailed] = useState(false);
  const key = `media:${entry.path}`;
  const restore = () => { const t = getPos(key); if (t && ref.current && t < (ref.current.duration || Infinity) - 3) ref.current.currentTime = t; };
  const save = () => { if (ref.current) setPos(key, ref.current.currentTime); };
  const El = audioOnly ? "audio" : "video";
  return (
    <>
      <Toolbar entry={entry} />
      {failed ? <CannotPlay missingTools={missingTools} error={probeError} /> : (
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-4 bg-panel-2 p-3">
          {audioOnly && <AudioArt name={entry.name} />}
          <El ref={ref} src={inlineURL(entry.path)} controls autoPlay preload="metadata" onLoadedMetadata={restore} onTimeUpdate={save} onError={() => setFailed(true)}
            className={audioOnly ? "w-full max-w-xl" : "max-h-full max-w-full rounded-lg bg-black"} />
        </div>
      )}
    </>
  );
}

function AudioArt({ name }: { name: string }) {
  return (
    <div className="flex flex-col items-center gap-3 text-muted">
      <div className="flex h-36 w-36 items-center justify-center rounded-2xl bg-panel shadow"><Music size={56} /></div>
      <div className="max-w-xl truncate text-[14px] font-medium text-fg">{name}</div>
    </div>
  );
}

function CannotPlay({ missingTools, error }: { missingTools: boolean; error?: unknown }) {
  const caps = useViewerCaps();
  const install = useAction(() => api.post("/api/system/components/photos/install"));
  return (
    <ViewerCard>
      <div className="font-semibold">{tr("This file cannot be played in the browser")}</div>
      <ErrorBox error={error} />
      {missingTools && (caps.data?.admin ? (
        <>
          <p className="text-muted">{tr("Install the media tools (ffmpeg, about 250 MB) so the NAS converts any video or audio format on the fly.")}</p>
          <Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install media tools")}</Button>
        </>
      ) : <p className="text-muted">{tr("Ask your administrator to install the media tools (Settings → Components) to play any format.")}</p>)}
    </ViewerCard>
  );
}

function Streamed({ entry, probe, audio, setAudio }: { entry: FileEntry; probe: Probe; audio?: number; setAudio: (a: number | undefined) => void }) {
  const plan = probe.plan!;
  const duration = probe.duration ?? 0;
  const key = `media:${entry.path}`;
  const ref = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const [offset, setOffset] = useState(() => { const t = getPos(key) ?? 0; return t < duration - 3 ? Math.floor(t) : 0; });
  const [current, setCurrent] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [drag, setDrag] = useState<number | null>(null);
  const [muted, setMuted] = useState(false);
  const [volume, setVolume] = useState(1);
  const [sub, setSub] = useState<number | null>(null);
  const [error, setError] = useState<unknown>(null);
  const src = `/api/files/play${q({ path: entry.path, start: offset || undefined, audio, ...caps })}`;
  const pos = offset + current;

  useEffect(() => { setCurrent(0); setError(null); }, [src]);
  useEffect(() => { if (ref.current) { ref.current.volume = volume; ref.current.muted = muted; } }, [volume, muted, src]);

  const seek = (t: number) => { setOffset(Math.max(0, Math.min(duration - 1, Math.floor(t)))); setPos(key, t); };
  const toggle = () => { const v = ref.current; if (!v) return; if (v.paused) void v.play(); else v.pause(); };
  // The stream answers JSON when it cannot start (busy, unreadable…).
  const onError = async () => {
    try {
      const ac = new AbortController();
      const res = await fetch(src, { headers: { "X-Strata": "1" }, credentials: "same-origin", signal: ac.signal });
      const data = res.ok ? {} : await res.json().catch(() => ({}));
      ac.abort(); // a working stream: stop it, only its status mattered
      setError(new ApiError(res.status, data.error ?? tr("This file cannot be played in the browser")));
    } catch (e) {
      setError(e);
    }
  };
  const El = plan.audioOnly ? "audio" : "video";
  const what = plan.mode === "remux" ? tr("Converted container (no quality loss)") : plan.audioOnly ? tr("Converted to AAC") : probe.vaapi && !plan.copyVideo ? tr("Converted to H.264 (hardware)") : tr("Converted to H.264");
  const subs = probe.subtitles ?? [];
  const audios = probe.audioTracks ?? [];
  const subURL = useMemo(() => (sub === null ? "" : `/api/files/subtitle${q({ path: entry.path, index: sub, start: offset || undefined })}`), [sub, offset, entry.path]);
  return (
    <>
      <Toolbar entry={entry}>
        <Badge tone="accent">{what}</Badge>
        {audios.length > 1 && (
          <Select value={audio ?? ""} onChange={(e) => { setAudio(e.target.value === "" ? undefined : +e.target.value); }} className="!h-8 max-w-[180px] text-[12.5px]" aria-label={tr("Audio track")}>
            <option value="">{tr("Default audio")}</option>
            {audios.map((a) => <option key={a.index} value={a.index}>{trackName(a)}</option>)}
          </Select>
        )}
        {!plan.audioOnly && subs.length > 0 && (
          <span className="flex items-center gap-1">
            <Captions size={14} className="text-muted" />
            <Select value={sub ?? ""} onChange={(e) => setSub(e.target.value === "" ? null : +e.target.value)} className="!h-8 max-w-[160px] text-[12.5px]" aria-label={tr("Subtitles")}>
              <option value="">{tr("No subtitles")}</option>
              {subs.map((s) => <option key={s.index} value={s.index}>{trackName(s)}</option>)}
            </Select>
          </span>
        )}
      </Toolbar>
      <div className="flex min-h-0 flex-1 flex-col bg-black">
        <div className={cx("flex min-h-0 flex-1 items-center justify-center", plan.audioOnly && "bg-panel-2")} onClick={toggle}>
          {plan.audioOnly && <AudioArt name={entry.name} />}
          <El key={src} ref={ref} src={src} autoPlay preload="auto" playsInline
            onTimeUpdate={(e) => { setCurrent(e.currentTarget.currentTime); setPos(key, offset + e.currentTarget.currentTime); }}
            onPlay={() => setPlaying(true)} onPause={() => setPlaying(false)} onError={() => void onError()}
            className={plan.audioOnly ? "hidden" : "max-h-full max-w-full"}>
            {subURL && <track key={subURL} kind="subtitles" src={subURL} default label={tr("Subtitles")} />}
          </El>
        </div>
        {error ? <div className="bg-panel p-3"><ErrorBox error={error} /><Button size="sm" onClick={() => { setError(null); seek(pos); }}>{tr("Retry")}</Button></div> : null}
        <div className="flex shrink-0 items-center gap-2 bg-panel px-3 py-2 text-[12px]">
          <Button size="sm" variant="ghost" icon={playing ? <Pause size={15} /> : <Play size={15} />} title={playing ? tr("Pause") : tr("Play")} onClick={toggle} />
          <span className="mono w-14 text-right text-muted">{clock(drag ?? pos)}</span>
          <input type="range" min={0} max={Math.max(1, duration)} step={1} value={drag ?? pos} aria-label={tr("Position")} className="min-w-0 flex-1 accent-[var(--accent)]"
            onChange={(e) => setDrag(+e.target.value)}
            onPointerUp={() => { if (drag !== null) { seek(drag); setDrag(null); } }}
            onKeyUp={() => { if (drag !== null) { seek(drag); setDrag(null); } }} />
          <span className="mono w-14 text-muted">{duration ? clock(duration) : "—"}</span>
          <Button size="sm" variant="ghost" icon={muted || volume === 0 ? <VolumeX size={15} /> : <Volume2 size={15} />} title={tr("Mute")} onClick={() => setMuted(!muted)} />
          <input type="range" min={0} max={1} step={0.05} value={muted ? 0 : volume} aria-label={tr("Volume")} className="w-20 accent-[var(--accent)] max-sm:hidden" onChange={(e) => { setVolume(+e.target.value); setMuted(false); }} />
        </div>
      </div>
      {error instanceof ApiError && error.status === 503 && <Notice tone="warn">{srv(error.message)}</Notice>}
    </>
  );
}
