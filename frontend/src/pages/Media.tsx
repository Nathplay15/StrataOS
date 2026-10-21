import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AppWindow, Cast as CastIcon, Clapperboard, Disc3, ListMusic, Music, Pause, Play, Plug, RefreshCcw, Search, SkipBack, SkipForward, Speaker, Trash2, Tv, Volume2 } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Me, Share } from "../lib/types";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Notice, PageHeader, Select, Spinner, Switch, Tabs } from "../components/ui";
import { AppLink } from "../components/AppLink";
import { CastNotes, ReceiverIcon, SessionPanel, useReceivers, type CastSession } from "../components/Cast";
import { tr } from "../lib/i18n";
import { cx } from "../lib/format";

type Tab = "dlna" | "music" | "cast";

export default function Media({ me }: { me: Me }) {
  const admin = me.role === "admin";
  const [tab, setTab] = useState<Tab>(admin ? "dlna" : "music");
  const tabs: { id: Tab; label: string }[] = [
    ...(admin ? [{ id: "dlna" as Tab, label: tr("DLNA server") }] : []),
    { id: "music", label: tr("Music & speakers") },
    { id: "cast", label: tr("Cast") },
  ];
  return (
    <div>
      <PageHeader title={tr("Media")} subtitle={tr("Stream videos, music and photos to the TVs and speakers of your home")} icon={<Clapperboard size={20} />} />
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {tab === "dlna" && admin && <DLNATab />}
      {tab === "music" && <MusicTab admin={admin} />}
      {tab === "cast" && <CastTab />}
    </div>
  );
}

/* ---------- DLNA server ---------- */

type DLNAFolder = { share: string; type: "" | "video" | "audio" | "pictures" };
type DLNAConfig = { enabled: boolean; name: string; interface: string; folders: DLNAFolder[] };
type DLNAData = { config: DLNAConfig; status: { installed: boolean; service: string; audio: number; video: number; images: number }; interfaces: string[]; hostname: string; port: number };

function DLNATab() {
  const { data, error } = useQuery({ queryKey: ["media-dlna"], queryFn: () => api.get<DLNAData>("/api/media/dlna"), refetchInterval: 5000 });
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const [c, setC] = useState<DLNAConfig | null>(null);
  useEffect(() => { if (data && !c) setC(data.config); }, [data, c]);
  const install = useAction(() => api.post("/api/system/components/dlna/install"));
  const save = useAction(() => api.put("/api/media/dlna", c), { success: tr("DLNA server saved") });
  const rescan = useAction(() => api.post("/api/media/dlna/rescan"), { success: tr("Rescan started") });
  if (!data || !c) return <><ErrorBox error={error} /><Spinner /></>;
  const st = data.status;
  const typeOf = (share: string) => c.folders.find((f) => f.share === share);
  const setType = (share: string, v: string) => {
    const rest = c.folders.filter((f) => f.share !== share);
    setC({ ...c, folders: v === "off" ? rest : [...rest, { share, type: v as DLNAFolder["type"] }] });
  };
  if (!st.installed) {
    return (
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="text-[13px] text-muted">{tr("The DLNA server (ReadyMedia) is not installed. Once installed, TVs, game consoles and media players of the local network can browse the chosen folders.")}</div>
          <Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>
        </div>
      </Card>
    );
  }
  return (
    <div className="space-y-5">
      <ErrorBox error={save.error || rescan.error} />
      <Card title={<span className="flex items-center gap-2"><Tv size={16} className="text-accent" />{tr("DLNA / UPnP media server")}</span>} actions={<Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
        <div className="space-y-4">
          <Switch checked={c.enabled} onChange={(v) => setC({ ...c, enabled: v })} label={tr("Enable the DLNA server")} hint={tr("TVs and consoles find it by themselves on the local network.")} />
          <Notice tone="warn">{tr("DLNA has no authentication: every device on the local network can browse and play the chosen folders, even if the shared folder is private. Only publish what everyone at home may see.")}</Notice>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Name shown on TVs")}><Input value={c.name} placeholder={data.hostname} onChange={(e) => setC({ ...c, name: e.target.value })} /></Field>
            <Field label={tr("Network interface")}>
              <Select value={c.interface} onChange={(e) => setC({ ...c, interface: e.target.value })}>
                <option value="">{tr("All interfaces")}</option>
                {data.interfaces.map((i) => <option key={i} value={i}>{i}</option>)}
              </Select>
            </Field>
          </div>
          <div>
            <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Published folders")}</div>
            {!shares.data?.length ? <div className="text-[13px] text-muted">{tr("No shared folder yet.")}</div> : (
              <div className="space-y-1.5">
                {shares.data.map((sh) => {
                  const f = typeOf(sh.name);
                  return (
                    <div key={sh.name} className="flex flex-wrap items-center gap-3 rounded-lg border border-line px-3 py-2">
                      <div className="min-w-0 flex-1"><div className="text-[13px] font-medium">{sh.name}</div><div className="mono truncate text-[11.5px] text-muted">{sh.path}</div></div>
                      <Select className="w-44" value={f ? f.type : "off"} onChange={(e) => setType(sh.name, e.target.value)}>
                        <option value="off">{tr("Not published")}</option>
                        <option value="">{tr("All media")}</option>
                        <option value="video">{tr("Videos")}</option>
                        <option value="audio">{tr("Music")}</option>
                        <option value="pictures">{tr("Pictures")}</option>
                      </Select>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>
      </Card>
      <Card title={tr("State")} actions={<Button size="sm" icon={<RefreshCcw size={14} />} disabled={!data.config.enabled} loading={rescan.isPending} onClick={() => rescan.mutate()}>{tr("Rescan")}</Button>}>
        <div className="flex flex-wrap items-center gap-3 text-[13px]">
          <Badge tone={st.service === "active" ? "ok" : "neutral"} dot>{st.service === "active" ? tr("Running") : tr("Stopped")}</Badge>
          {st.service === "active" && <span className="text-muted">{tr("{0} videos · {1} music files · {2} pictures", st.video, st.audio, st.images)}</span>}
          {st.service === "active" && <span className="mono text-muted">{tr("port {0}", data.port)}</span>}
        </div>
        <div className="mt-3 text-[12.5px] text-muted">{tr("New files are picked up automatically; a rescan rebuilds the whole index (useful after moving many files).")}</div>
      </Card>
    </div>
  );
}

/* ---------- Music & speakers (OwnTone) ---------- */

const M = (p: string) => `/api/media/music/proxy/${p}`;
type MusicData = { installed: boolean; stack: string; allowed: boolean; config: { shares: string[]; users: boolean } };
type Output = { id: string; name: string; type: string; selected: boolean; volume: number; requires_auth?: boolean };
type Player = { state: "play" | "pause" | "stop"; volume: number; item_id: number; item_length_ms: number; item_progress_ms: number };
type QItem = { id: number; track_id: number; title: string; artist: string; album: string; length_ms: number };
type Album = { id: string; name: string; artist: string; uri: string; track_count?: number };
type Artist = { id: string; name: string; uri: string; album_count?: number };
type Playlist = { id: number; name: string; uri: string };
type Track = { id: number; title: string; artist: string; album: string; uri: string; length_ms: number };
type Items<T> = { items: T[] };

const mmss = (ms: number) => { const s = Math.floor(ms / 1000); return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`; };

function MusicSettings({ data }: { data: MusicData }) {
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const [c, setC] = useState(data.config);
  const save = useAction(() => api.put("/api/media/music", c), { success: tr("Music settings saved") });
  return (
    <Card title={tr("Music server settings")} actions={<Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>}>
      <ErrorBox error={save.error} />
      <div className="space-y-4">
        <div>
          <div className="mb-2 text-[12.5px] font-medium text-muted">{tr("Music folders")}</div>
          <div className="flex flex-wrap gap-2">
            {(shares.data ?? []).map((sh) => {
              const on = c.shares.includes(sh.name);
              return <button key={sh.name} onClick={() => setC({ ...c, shares: on ? c.shares.filter((x) => x !== sh.name) : [...c.shares, sh.name] })} className={cx("rounded-lg border px-3 py-1.5 text-[13px]", on ? "border-accent bg-accent/10 text-accent" : "border-line text-muted hover:text-fg")}>{sh.name}</button>;
            })}
          </div>
          <div className="mt-1.5 text-[11.5px] text-muted">{tr("OwnTone reads these shared folders (read-only) and scans them again when you save.")}</div>
        </div>
        <Switch checked={c.users} onChange={(v) => setC({ ...c, users: v })} label={tr("Let every user control the music")} hint={tr("Otherwise only administrators can play music and choose speakers.")} />
      </div>
    </Card>
  );
}

function MusicTab({ admin }: { admin: boolean }) {
  const { data, error } = useQuery({ queryKey: ["media-music"], queryFn: () => api.get<MusicData>("/api/media/music") });
  if (!data) return <><ErrorBox error={error} /><Spinner /></>;
  if (!data.installed) {
    return (
      <Card>
        <Empty icon={<Music size={22} />} title={tr("The music server is not installed")} text={tr("Multi-room music uses OwnTone: it plays your library on AirPlay and Chromecast speakers, several at once and in sync.")}
          action={admin ? <AppLink to="/apps"><Button variant="primary" icon={<AppWindow size={15} />}>{tr("Install OwnTone from the App Center")}</Button></AppLink> : <div className="text-[13px] text-muted">{tr("Ask an administrator to install it.")}</div>} />
      </Card>
    );
  }
  return (
    <div className="space-y-5">
      {data.allowed ? <MusicPlayer /> : <Notice>{tr("Music control is reserved to administrators.")}</Notice>}
      {admin && <MusicSettings data={data} />}
    </div>
  );
}

function MusicPlayer() {
  const qc = useQueryClient();
  const player = useQuery({ queryKey: ["ot-player"], queryFn: () => api.get<Player>(M("api/player")), refetchInterval: 2000 });
  const outputs = useQuery({ queryKey: ["ot-outputs"], queryFn: () => api.get<{ outputs: Output[] }>(M("api/outputs")), refetchInterval: 5000 });
  const queue = useQuery({ queryKey: ["ot-queue"], queryFn: () => api.get<{ items: QItem[] }>(M("api/queue")), refetchInterval: 4000 });
  const lib = useQuery({ queryKey: ["ot-library"], queryFn: () => api.get<{ songs: number; artists: number; albums: number; updating: boolean }>(M("api/library")), refetchInterval: 10000 });
  const refresh = () => { qc.invalidateQueries({ queryKey: ["ot-player"] }); qc.invalidateQueries({ queryKey: ["ot-queue"] }); qc.invalidateQueries({ queryKey: ["ot-outputs"] }); };
  const cmd = useAction((p: string) => api.put(M(p)), { onDone: refresh });
  const setOut = useAction((o: { id: string; selected?: boolean; volume?: number }) => api.put(M(`api/outputs/${o.id}`), { selected: o.selected, volume: o.volume }), { onDone: refresh });
  const p = player.data;
  const now = queue.data?.items.find((i) => i.id === p?.item_id);
  const err = player.error || outputs.error || cmd.error || setOut.error;
  return (
    <>
      <ErrorBox error={err} />
      <div className="grid gap-5 lg:grid-cols-[1fr_340px]">
        <Card title={<span className="flex items-center gap-2"><Disc3 size={16} className="text-accent" />{tr("Now playing")}</span>}>
          <div className="flex flex-wrap items-center gap-5">
            <img key={p?.item_id} src={`${M("artwork/nowplaying")}?maxwidth=300&i=${p?.item_id ?? 0}`} alt="" onError={(e) => (e.currentTarget.style.visibility = "hidden")} className="h-32 w-32 rounded-xl bg-panel-2 object-cover" />
            <div className="min-w-0 flex-1 space-y-3">
              <div>
                <div className="truncate text-lg font-semibold">{now?.title ?? tr("Nothing is playing")}</div>
                <div className="truncate text-[13px] text-muted">{[now?.artist, now?.album].filter(Boolean).join(" — ")}</div>
              </div>
              {p && p.item_length_ms > 0 && (
                <div className="flex items-center gap-2 text-[11.5px] text-muted">
                  <span className="mono">{mmss(p.item_progress_ms)}</span>
                  <input type="range" className="flex-1" min={0} max={p.item_length_ms} defaultValue={p.item_progress_ms} key={`${p.item_id}-${Math.floor(p.item_progress_ms / 5000)}`} onMouseUp={(e) => cmd.mutate(`api/player/seek${q({ position_ms: (e.target as HTMLInputElement).value })}`)} />
                  <span className="mono">{mmss(p.item_length_ms)}</span>
                </div>
              )}
              <div className="flex items-center gap-1">
                <IconButton title={tr("Previous")} onClick={() => cmd.mutate("api/player/previous")}><SkipBack size={18} /></IconButton>
                <IconButton title={p?.state === "play" ? tr("Pause") : tr("Play")} onClick={() => cmd.mutate(p?.state === "play" ? "api/player/pause" : "api/player/play")}>{p?.state === "play" ? <Pause size={20} /> : <Play size={20} />}</IconButton>
                <IconButton title={tr("Next")} onClick={() => cmd.mutate("api/player/next")}><SkipForward size={18} /></IconButton>
                {p && <><Volume2 size={15} className="ml-4 text-muted" /><input type="range" className="w-36" min={0} max={100} defaultValue={p.volume} key={`v${p.volume}`} onMouseUp={(e) => cmd.mutate(`api/player/volume${q({ volume: (e.target as HTMLInputElement).value })}`)} title={tr("Volume of all speakers")} /></>}
              </div>
            </div>
          </div>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Speaker size={16} className="text-accent" />{tr("Speakers")}</span>}>
          {!outputs.data ? <Spinner /> : !outputs.data.outputs.length ? <div className="text-[13px] text-muted">{tr("No speaker found. AirPlay and Chromecast speakers appear here when they are on the same network.")}</div> : (
            <div className="space-y-2">
              {outputs.data.outputs.map((o) => (
                <div key={o.id} className="space-y-1.5 rounded-lg border border-line px-3 py-2">
                  <div className="flex items-center gap-2">
                    <Switch checked={o.selected} onChange={(v) => setOut.mutate({ id: o.id, selected: v })} />
                    <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{o.name}</span>
                    <Badge>{o.type}</Badge>
                  </div>
                  {o.selected && <input type="range" className="w-full" min={0} max={100} defaultValue={o.volume} key={`${o.id}-${o.volume}`} onMouseUp={(e) => setOut.mutate({ id: o.id, volume: +(e.target as HTMLInputElement).value })} />}
                  {o.requires_auth && <div className="text-[11.5px] text-warn">{tr("This speaker asks for a PIN code: pair it from the OwnTone settings.")}</div>}
                </div>
              ))}
            </div>
          )}
          <div className="mt-3 text-[11.5px] text-muted">{tr("Turn on several speakers to play in every room at once, in sync.")}</div>
        </Card>
      </div>
      <Library onPlayed={refresh} stats={lib.data} />
      <Card title={<span className="flex items-center gap-2"><ListMusic size={16} className="text-accent" />{tr("Queue")}</span>} actions={!!queue.data?.items.length && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} onClick={() => cmd.mutate("api/queue/clear")}>{tr("Clear")}</Button>}>
        {!queue.data?.items.length ? <div className="text-[13px] text-muted">{tr("The queue is empty: choose an album, a playlist or a song above.")}</div> : (
          <div className="divide-y divide-line">
            {queue.data.items.map((i) => (
              <div key={i.id} className={cx("flex items-center gap-3 py-1.5 text-[13px]", i.id === p?.item_id && "text-accent")}>
                <button className="min-w-0 flex-1 truncate text-left" onClick={() => cmd.mutate(`api/player/play${q({ item_id: i.id })}`)}>{i.title} <span className="text-muted">— {i.artist}</span></button>
                <span className="mono text-[11.5px] text-muted">{mmss(i.length_ms)}</span>
                <IconButton title={tr("Remove")} onClick={() => api.del(M(`api/queue/items/${i.id}`)).then(refresh)}><Trash2 size={14} /></IconButton>
              </div>
            ))}
          </div>
        )}
      </Card>
    </>
  );
}

type LibTab = "albums" | "artists" | "playlists";

function Library({ onPlayed, stats }: { onPlayed: () => void; stats?: { songs: number; artists: number; albums: number; updating: boolean } }) {
  const [tab, setTab] = useState<LibTab>("albums");
  const [artist, setArtist] = useState<Artist | null>(null);
  const [search, setSearch] = useState("");
  const [term, setTerm] = useState("");
  useEffect(() => { const t = setTimeout(() => setTerm(search.trim()), 350); return () => clearTimeout(t); }, [search]);
  const albums = useQuery({ queryKey: ["ot-albums", artist?.id], queryFn: () => api.get<Items<Album>>(M(artist ? `api/library/artists/${artist.id}/albums` : "api/library/albums")), enabled: tab === "albums" && !term });
  const artists = useQuery({ queryKey: ["ot-artists"], queryFn: () => api.get<Items<Artist>>(M("api/library/artists")), enabled: tab === "artists" && !term });
  const playlists = useQuery({ queryKey: ["ot-playlists"], queryFn: () => api.get<Items<Playlist>>(M("api/library/playlists")), enabled: tab === "playlists" && !term });
  const found = useQuery({ queryKey: ["ot-search", term], queryFn: () => api.get<{ tracks?: Items<Track>; albums?: Items<Album>; artists?: Items<Artist> }>(M(`api/search${q({ type: "tracks,albums,artists", query: term, limit: 30 })}`)), enabled: !!term });
  const play = useAction((uri: string) => api.post(M(`api/queue/items/add${q({ uris: uri, clear: "true", playback: "start" })}`)), { onDone: onPlayed });
  const enqueue = useAction((uri: string) => api.post(M(`api/queue/items/add${q({ uris: uri })}`)), { success: tr("Added to the queue"), onDone: onPlayed });
  const albumGrid = (list: Album[]) => (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-4">
      {list.map((a) => (
        <div key={a.id} className="group">
          <button className="relative block aspect-square w-full overflow-hidden rounded-xl bg-panel-2" onClick={() => play.mutate(a.uri)} title={tr("Play")}>
            <img src={`${M(`artwork/group/${a.id}`)}?maxwidth=300`} alt="" loading="lazy" onError={(e) => (e.currentTarget.style.visibility = "hidden")} className="h-full w-full object-cover" />
            <span className="absolute inset-0 hidden items-center justify-center bg-black/40 text-white group-hover:flex"><Play size={30} /></span>
          </button>
          <div className="mt-1.5 flex items-start gap-1">
            <div className="min-w-0 flex-1"><div className="truncate text-[13px] font-medium">{a.name}</div><div className="truncate text-[11.5px] text-muted">{a.artist}</div></div>
            <button className="text-[11px] text-muted hover:text-accent" onClick={() => enqueue.mutate(a.uri)} title={tr("Add to the queue")}>+</button>
          </div>
        </div>
      ))}
    </div>
  );
  const loading = <Spinner />;
  let body;
  if (term) {
    const f = found.data;
    body = !f ? loading : (
      <div className="space-y-4">
        {!!f.tracks?.items.length && <div className="divide-y divide-line">{f.tracks.items.map((t) => (
          <button key={t.id} className="flex w-full items-center gap-3 py-1.5 text-left text-[13px] hover:text-accent" onClick={() => play.mutate(t.uri)}><Play size={13} /><span className="min-w-0 flex-1 truncate">{t.title} <span className="text-muted">— {t.artist} · {t.album}</span></span><span className="mono text-[11.5px] text-muted">{mmss(t.length_ms)}</span></button>
        ))}</div>}
        {!!f.albums?.items.length && albumGrid(f.albums.items)}
        {!f.tracks?.items.length && !f.albums?.items.length && <div className="text-[13px] text-muted">{tr("Nothing found.")}</div>}
      </div>
    );
  } else if (tab === "albums") {
    body = !albums.data ? loading : !albums.data.items.length ? <div className="text-[13px] text-muted">{tr("No album in the library yet.")}</div> : albumGrid(albums.data.items);
  } else if (tab === "artists") {
    body = !artists.data ? loading : (
      <div className="grid gap-1 sm:grid-cols-2 lg:grid-cols-3">
        {artists.data.items.map((a) => <button key={a.id} className="truncate rounded-lg px-3 py-2 text-left text-[13px] hover:bg-panel-2" onClick={() => { setArtist(a); setTab("albums"); }}>{a.name} <span className="text-muted">· {a.album_count ?? 0}</span></button>)}
      </div>
    );
  } else {
    body = !playlists.data ? loading : !playlists.data.items.length ? <div className="text-[13px] text-muted">{tr("No playlist.")}</div> : (
      <div className="divide-y divide-line">
        {playlists.data.items.map((p) => <button key={p.id} className="flex w-full items-center gap-2 py-2 text-left text-[13px] hover:text-accent" onClick={() => play.mutate(p.uri)}><Play size={13} />{p.name}</button>)}
      </div>
    );
  }
  return (
    <Card title={<span className="flex items-center gap-2"><Music size={16} className="text-accent" />{tr("Library")}{stats && <span className="text-[12px] font-normal text-muted">{tr("{0} songs · {1} albums · {2} artists", stats.songs, stats.albums, stats.artists)}{stats.updating ? " · " + tr("scanning…") : ""}</span>}</span>}
      actions={<div className="relative"><Search size={14} className="absolute left-2.5 top-2.5 text-muted" /><Input className="w-52 pl-8" placeholder={tr("Search")} value={search} onChange={(e) => setSearch(e.target.value)} /></div>}>
      <ErrorBox error={albums.error || artists.error || playlists.error || found.error || play.error} />
      {!term && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          {(["albums", "artists", "playlists"] as LibTab[]).map((t) => (
            <button key={t} onClick={() => { setTab(t); setArtist(null); }} className={cx("rounded-lg px-3 py-1 text-[13px]", tab === t ? "bg-accent text-white" : "text-muted hover:bg-panel-2")}>{{ albums: tr("Albums"), artists: tr("Artists"), playlists: tr("Playlists") }[t]}</button>
          ))}
          {artist && <Badge tone="accent">{artist.name}</Badge>}
        </div>
      )}
      {body}
    </Card>
  );
}

/* ---------- Cast ---------- */

function CastTab() {
  const sessions = useQuery({ queryKey: ["cast-sessions"], queryFn: () => api.get<{ sessions: CastSession[] }>("/api/media/cast/sessions"), refetchInterval: 3000 });
  const rec = useReceivers();
  return (
    <div className="space-y-5">
      <Card title={<span className="flex items-center gap-2"><CastIcon size={16} className="text-accent" />{tr("Playing now")}</span>}>
        <ErrorBox error={sessions.error} />
        {!sessions.data ? <Spinner /> : !sessions.data.sessions.length ? (
          <Empty icon={<CastIcon size={22} />} title={tr("Nothing is being cast")} text={tr("In Files or Photos, use the Cast button of a video, a song or a picture to play it on one or several TVs.")} />
        ) : (
          <div className="space-y-5">
            {sessions.data.sessions.map((s) => (
              <div key={s.id}>
                <div className="mb-2 flex items-center gap-2 text-[13px]"><span className="font-medium">{s.name}</span><span className="mono truncate text-[11.5px] text-muted">{s.path}</span></div>
                <SessionPanel s={s} />
              </div>
            ))}
          </div>
        )}
      </Card>
      <Card title={tr("Receivers on the local network")} actions={<Button size="sm" variant="ghost" icon={<RefreshCcw size={14} />} loading={rec.isFetching} onClick={rec.rescan}>{tr("Search again")}</Button>}>
        <ErrorBox error={rec.error} />
        {!rec.data ? <Spinner label={tr("Searching for TVs and speakers…")} /> : !rec.data.receivers.length ? (
          <div className="text-[13px] text-muted">{tr("No receiver found. Chromecast devices and DLNA TVs must be switched on and on the same network as the NAS.")}</div>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2">
            {rec.data.receivers.map((r) => (
              <div key={r.id} className="flex items-center gap-3 rounded-lg border border-line px-3 py-2">
                <ReceiverIcon r={r} />
                <div className="min-w-0 flex-1"><div className="truncate text-[13px] font-medium">{r.name}</div><div className="truncate text-[11.5px] text-muted">{[r.model, r.addr].filter(Boolean).join(" · ")}</div></div>
                <Badge tone="accent">{r.kind === "chromecast" ? "Chromecast" : "DLNA"}</Badge>
              </div>
            ))}
          </div>
        )}
        <div className="mt-4"><CastNotes /></div>
      </Card>
    </div>
  );
}
