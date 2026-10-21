import { useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { Cast as CastIcon, Pause, Play, RefreshCcw, Square, Tv, Speaker, Volume2, X, Repeat } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { Badge, Button, Checkbox, Empty, ErrorBox, IconButton, Notice, Spinner } from "./ui";
import { srv, tr } from "../lib/i18n";

export type Receiver = { id: string; kind: "chromecast" | "dlna"; name: string; model?: string; addr: string; video: boolean };
type Status = { state: string; position: number; duration: number; volume: number };
export type CastSession = { id: string; user: string; path: string; name: string; kind: "video" | "audio" | "image"; started: string; receivers: { id: string; name: string; kind: string; error?: string; status: Status }[] };

const exts: Record<string, "video" | "audio" | "image"> = {};
for (const e of ["mp4", "m4v", "mov", "mkv", "webm", "avi", "ts", "m2ts", "mpg", "mpeg", "wmv"]) exts[e] = "video";
for (const e of ["mp3", "m4a", "aac", "flac", "ogg", "oga", "opus", "wav", "wma"]) exts[e] = "audio";
for (const e of ["jpg", "jpeg", "png", "gif", "webp", "bmp"]) exts[e] = "image";

/** The kind of media a file name can be cast as, or undefined. */
export const castKind = (name: string) => exts[name.split(".").pop()?.toLowerCase() ?? ""];

const clock = (s: number) => {
  s = Math.max(0, Math.floor(s));
  const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, x = s % 60;
  return (h ? `${h}:${String(m).padStart(2, "0")}` : String(m)) + ":" + String(x).padStart(2, "0");
};

const stateLabel = (s: string) => ({ PLAYING: tr("Playing"), PAUSED: tr("Paused"), BUFFERING: tr("Loading"), IDLE: tr("Stopped"), STOPPED: tr("Stopped") })[s] ?? s;

/** A dialog above everything, the photo viewer included. */
function Overlay({ title, onClose, children, footer }: { title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode }) {
  return createPortal(
    <div className="fixed inset-0 z-[9700] flex items-start justify-center overflow-y-auto bg-black/50 p-4 text-fg backdrop-blur-sm sm:items-center" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="card my-8 flex max-h-[90vh] w-full max-w-lg flex-col">
        <header className="flex items-center justify-between border-b border-line px-5 py-3.5">
          <h3 className="min-w-0 truncate font-semibold">{title}</h3>
          <IconButton title={tr("Close")} onClick={onClose}><X size={17} /></IconButton>
        </header>
        <div className="overflow-y-auto p-5">{children}</div>
        {footer && <footer className="flex justify-end gap-2 border-t border-line px-5 py-3.5">{footer}</footer>}
      </div>
    </div>,
    document.body,
  );
}

export function useReceivers() {
  const [refresh, setRefresh] = useState(0);
  const query = useQuery({ queryKey: ["cast-receivers", refresh], queryFn: () => api.get<{ receivers: Receiver[] }>(`/api/media/cast/receivers${refresh ? "?refresh=1" : ""}`), staleTime: 30000 });
  return { ...query, rescan: () => setRefresh((n) => n + 1) };
}

export function ReceiverIcon({ r }: { r: { kind: string; video?: boolean } }) {
  return r.video === false ? <Speaker size={15} className="text-muted" /> : <Tv size={15} className="text-muted" />;
}

/** Notes shown wherever casting starts. */
export function CastNotes() {
  return (
    <div className="space-y-2 text-[12.5px] text-muted">
      <div>{tr("The receiver plays the original file: it must support its format (no transcoding). To convert videos on the fly, use Jellyfin from the App Center.")}</div>
      <div>{tr("AirPlay video is not supported (closed protocol). To play music on AirPlay speakers, use Media → Music & speakers.")}</div>
    </div>
  );
}

function CastPicker({ path, name, onClose }: { path: string; name: string; onClose: () => void }) {
  const kind = castKind(name);
  const rec = useReceivers();
  const [sel, setSel] = useState<string[]>([]);
  const [session, setSession] = useState<string | null>(null);
  const start = useAction(() => api.post<CastSession>("/api/media/cast", { path, receivers: sel }), { onDone: (s) => setSession(s.id) });
  if (session) return <Overlay title={tr("Playing {0}", name)} onClose={onClose}><CastRemote id={session} onEnded={onClose} /></Overlay>;
  const list = rec.data?.receivers ?? [];
  return (
    <Overlay title={tr("Cast {0}", name)} onClose={onClose} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" icon={<CastIcon size={15} />} disabled={!sel.length} loading={start.isPending} onClick={() => start.mutate()}>{sel.length > 1 ? tr("Play on {0} receivers", sel.length) : tr("Play")}</Button></>}>
      <div className="space-y-4">
        <ErrorBox error={rec.error || start.error} />
        <div className="flex items-center justify-between">
          <div className="text-[13px] font-medium">{tr("Receivers on the local network")}</div>
          <Button size="sm" variant="ghost" icon={<RefreshCcw size={14} />} loading={rec.isFetching} onClick={rec.rescan}>{tr("Search again")}</Button>
        </div>
        {rec.isLoading ? <Spinner label={tr("Searching for TVs and speakers…")} /> : !list.length ? (
          <Empty icon={<CastIcon size={22} />} title={tr("No receiver found")} text={tr("Chromecast devices and DLNA TVs must be switched on and on the same network as the NAS.")} />
        ) : (
          <div className="space-y-1.5">
            {list.map((r) => {
              const off = !r.video && kind !== "audio";
              return (
                <div key={r.id} className="flex items-center gap-3 rounded-lg border border-line px-3 py-2">
                  <Checkbox checked={sel.includes(r.id)} disabled={off} onChange={(v) => setSel(v ? [...sel, r.id] : sel.filter((x) => x !== r.id))} />
                  <ReceiverIcon r={r} />
                  <div className="min-w-0 flex-1"><div className="truncate text-[13px] font-medium">{r.name}</div><div className="truncate text-[11.5px] text-muted">{[r.model, r.addr].filter(Boolean).join(" · ")}{off ? " · " + tr("audio only") : ""}</div></div>
                  <Badge tone="accent">{r.kind === "chromecast" ? "Chromecast" : "DLNA"}</Badge>
                </div>
              );
            })}
          </div>
        )}
        {sel.length > 1 && kind !== "image" && <Notice>{tr("Several receivers: playback starts on all of them, then their positions are aligned after a few seconds (best effort, a slight offset may remain).")}</Notice>}
        <CastNotes />
      </div>
    </Overlay>
  );
}

/** "Cast" button for a file; renders nothing for files that cannot be cast. */
export function CastButton({ path, name, dark }: { path: string; name: string; dark?: boolean }) {
  const [open, setOpen] = useState(false);
  if (!castKind(name)) return null;
  return (
    <>
      {dark ? (
        <button className="rounded-lg p-2 hover:bg-white/10" onClick={() => setOpen(true)} title={tr("Cast to a TV or speaker")}><CastIcon size={18} /></button>
      ) : (
        <Button size="sm" variant="ghost" icon={<CastIcon size={14} />} onClick={() => setOpen(true)} title={tr("Cast to a TV or speaker")} />
      )}
      {open && <CastPicker path={path} name={name} onClose={() => setOpen(false)} />}
    </>
  );
}

/** Remote control of one cast session: every receiver, or all at once. */
export function CastRemote({ id, onEnded }: { id: string; onEnded?: () => void }) {
  const { data, error } = useQuery({ queryKey: ["cast-sessions"], queryFn: () => api.get<{ sessions: CastSession[] }>("/api/media/cast/sessions"), refetchInterval: 2000 });
  const s = data?.sessions.find((x) => x.id === id);
  return s ? <SessionPanel s={s} onEnded={onEnded} /> : data ? <div className="text-[13px] text-muted">{tr("This cast has ended.")}</div> : <><ErrorBox error={error} /><Spinner /></>;
}

export function SessionPanel({ s, onEnded }: { s: CastSession; onEnded?: () => void }) {
  const ctl = useAction((a: { action: string; receiver?: string; value?: number }) => api.post(`/api/media/cast/sessions/${s.id}/control`, a));
  const stop = useAction(() => api.del(`/api/media/cast/sessions/${s.id}`), { onDone: () => onEnded?.() });
  const [seek, setSeek] = useState<Record<string, number>>({});
  const live = s.receivers.filter((r) => !r.error);
  const lead = live[0]?.status;
  const media = s.kind !== "image";
  return (
    <div className="space-y-3">
      <ErrorBox error={ctl.error || stop.error} />
      {live.length > 1 && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg bg-panel-2 px-3 py-2">
          <span className="mr-auto text-[13px] font-medium">{tr("All receivers")}</span>
          {media && <><IconButton title={tr("Play")} onClick={() => ctl.mutate({ action: "play" })}><Play size={16} /></IconButton>
            <IconButton title={tr("Pause")} onClick={() => ctl.mutate({ action: "pause" })}><Pause size={16} /></IconButton>
            <IconButton title={tr("Align positions")} onClick={() => ctl.mutate({ action: "sync" })}><Repeat size={16} /></IconButton></>}
        </div>
      )}
      {s.receivers.map((r) => {
        const st = r.status;
        const pos = seek[r.id] ?? st.position;
        return (
          <div key={r.id} className="space-y-2 rounded-lg border border-line px-3 py-2.5">
            <div className="flex items-center gap-2">
              <ReceiverIcon r={r} />
              <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{r.name}</span>
              <Badge tone={r.error ? "danger" : st.state === "PLAYING" ? "ok" : "neutral"}>{r.error ? tr("Error") : stateLabel(st.state)}</Badge>
            </div>
            {r.error ? <div className="text-[12px] text-danger">{srv(r.error)}</div> : (
              <>
                {media && st.duration > 0 && (
                  <div className="flex items-center gap-2 text-[11.5px] text-muted">
                    <span className="mono w-12 text-right">{clock(pos)}</span>
                    <input type="range" className="flex-1" min={0} max={Math.ceil(st.duration)} value={Math.min(pos, st.duration)} onChange={(e) => setSeek({ ...seek, [r.id]: +e.target.value })}
                      onMouseUp={() => { ctl.mutate({ action: "seek", receiver: r.id, value: pos }); setSeek({}); }} onTouchEnd={() => { ctl.mutate({ action: "seek", receiver: r.id, value: pos }); setSeek({}); }} />
                    <span className="mono w-12">{clock(st.duration)}</span>
                  </div>
                )}
                <div className="flex items-center gap-1">
                  {media && <IconButton title={st.state === "PLAYING" ? tr("Pause") : tr("Play")} onClick={() => ctl.mutate({ action: st.state === "PLAYING" ? "pause" : "play", receiver: r.id })}>{st.state === "PLAYING" ? <Pause size={16} /> : <Play size={16} />}</IconButton>}
                  <IconButton title={tr("Stop")} onClick={() => ctl.mutate({ action: "stop", receiver: r.id })}><Square size={15} /></IconButton>
                  {st.volume >= 0 && <><Volume2 size={15} className="ml-3 text-muted" /><input type="range" className="w-32" min={0} max={100} defaultValue={st.volume} onMouseUp={(e) => ctl.mutate({ action: "volume", receiver: r.id, value: +(e.target as HTMLInputElement).value })} onTouchEnd={(e) => ctl.mutate({ action: "volume", receiver: r.id, value: +(e.target as HTMLInputElement).value })} /></>}
                </div>
              </>
            )}
          </div>
        );
      })}
      {lead === undefined && <div className="text-[12.5px] text-muted">{tr("No receiver is playing.")}</div>}
      <div className="flex justify-end"><Button variant="danger" size="sm" icon={<Square size={14} />} loading={stop.isPending} onClick={() => stop.mutate()}>{tr("Stop casting")}</Button></div>
    </div>
  );
}
