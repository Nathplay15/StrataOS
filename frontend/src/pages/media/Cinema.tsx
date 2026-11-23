import { useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Clapperboard, KeyRound, Play, RefreshCw, Search, Star } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Spinner } from "../../components/ui";
import { tr } from "../../lib/i18n";
import { caps } from "../../components/files/MediaPlayer";

export type CinemaItem = {
  path: string; title: string; year?: number; kind: string; season?: number; episode?: number; size: number;
  tmdbId?: number; poster?: string; overview?: string; rating?: number; runtime?: number; genres?: string[]; cast?: string[];
};
type CinemaData = { keySet: boolean; running: boolean; checked?: string; scanned: number; items: CinemaItem[]; positions: Record<string, { at: number; dur: number; upd: string }> };
type Work = { path?: string; title: string; year?: number; kind: string; items: CinemaItem[]; tmdbId?: number; poster?: string; overview?: string; rating?: number; runtime?: number; genres?: string[]; cast?: string[] };

const epLabel = (i: CinemaItem) => `S${String(i.season ?? 0).padStart(2, "0")}E${String(i.episode ?? 0).padStart(2, "0")}`;

function Poster({ w, className }: { w: Work; className?: string }) {
  if (w.tmdbId) return <img src={`/api/cinema/poster/${w.tmdbId}`} alt="" loading="lazy" className={className} />;
  return <div className={`flex items-center justify-center bg-panel-2 text-muted ${className}`}><Clapperboard size={28} /></div>;
}

function WorkCard({ w, onClick }: { w: Work; onClick: () => void }) {
  return (
    <button onClick={onClick} className="group overflow-hidden rounded-xl border border-line bg-panel text-left transition hover:border-accent/50">
      <Poster w={w} className="aspect-[2/3] w-full object-cover" />
      <div className="space-y-1 p-3">
        <div className="truncate text-[13.5px] font-medium group-hover:text-accent">{w.title}</div>
        <div className="flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted">
          {w.year ? <span>{w.year}</span> : null}
          {w.rating ? <span className="flex items-center gap-0.5 text-warn"><Star size={11} />{w.rating.toFixed(1)}</span> : null}
          {w.runtime ? <span>{tr("{0} min", w.runtime)}</span> : null}
          {w.kind === "episode" && <span>{tr("{0} episodes", w.items.length)}</span>}
        </div>
        {!w.tmdbId && <Badge tone="neutral">{tr("not matched with TMDB")}</Badge>}
      </div>
    </button>
  );
}

export function CinemaTab({ admin }: { admin: boolean }) {
  const qc = useQueryClient();
  const lib = useQuery({ queryKey: ["cinema"], queryFn: () => api.get<CinemaData>("/api/cinema"), refetchInterval: (qq) => (qq.state.data?.running ? 5000 : 60000) });
  const scan = useAction(() => api.post("/api/cinema/scan", {}), { onDone: () => qc.invalidateQueries({ queryKey: ["cinema"] }) });
  const [key, setKey] = useState("");
  const saveKey = useAction(() => api.put("/api/cinema/settings", { tmdbKey: key }), { success: tr("Key saved"), onDone: () => { setKey(""); qc.invalidateQueries({ queryKey: ["cinema"] }); } });
  const [filter, setFilter] = useState("");
  const [sel, setSel] = useState<Work | null>(null);
  const [play, setPlay] = useState<{ item: CinemaItem; start: number } | null>(null);
  const lastSave = useRef(0);

  const d = lib.data;
  const items = d?.items ?? [];
  const f = filter.trim().toLowerCase();
  const shown = items.filter((i) => !f || i.title.toLowerCase().includes(f));
  const movies: Work[] = shown.filter((i) => i.kind !== "episode").map((i) => ({ ...i, items: [i] }));
  const seriesMap = new Map<string, CinemaItem[]>();
  for (const i of shown) {
    if (i.kind === "episode") {
      const a = seriesMap.get(i.title) ?? [];
      a.push(i);
      seriesMap.set(i.title, a);
    }
  }
  const series: Work[] = [...seriesMap.entries()].map(([title, eps]) => ({ ...eps[0], title, items: eps }));
  // Continue watching: the videos this account stopped, newest first.
  const cw = Object.entries(d?.positions ?? {})
    .map(([path, pos]) => ({ item: items.find((i) => i.path === path), pos }))
    .filter((x): x is { item: CinemaItem; pos: { at: number; dur: number; upd: string } } => !!x.item)
    .sort((a, b) => (b.pos.upd ?? "").localeCompare(a.pos.upd ?? ""))
    .slice(0, 12);

  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-center gap-3">
          <div className="relative min-w-48 flex-1">
            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <Input className="pl-8" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={tr("Search a title…")} />
          </div>
          {admin && <Button icon={<RefreshCw size={14} />} loading={scan.isPending || d?.running} disabled={d?.running} onClick={() => scan.mutate()}>{d?.running ? tr("Scanning…") : tr("Scan the library")}</Button>}
        </div>
        {d && <div className="mt-2 text-[12px] text-muted">{tr("Scanned {0} videos", d.scanned)}{d.checked && ` · ${tr("Last scan {0}", ago(d.checked))}`}</div>}
        <ErrorBox error={lib.error || scan.error} />
      </Card>

      {admin && !d?.keySet && (
        <Card title={<span className="flex items-center gap-2"><KeyRound size={16} />{tr("TMDB API key")}</span>}>
          <div className="space-y-3">
            <div className="text-muted text-[12.5px]">{tr("Get a free API key at themoviedb.org, then paste it here to fetch the posters, synopses and ratings automatically.")}</div>
            <div className="flex flex-wrap items-end gap-3">
              <Field label={tr("TMDB API key")} className="min-w-72"><Input className="mono" value={key} onChange={(e) => setKey(e.target.value)} /></Field>
              <Button variant="primary" loading={saveKey.isPending} disabled={!key.trim()} onClick={() => saveKey.mutate()}>{tr("Save")}</Button>
            </div>
            <ErrorBox error={saveKey.error} />
          </div>
        </Card>
      )}

      {lib.isLoading ? <Spinner /> : !items.length ? (
        <Card><Empty icon={<Clapperboard />} title={tr("Video library")} text={tr("No video found on the shares: add videos to a shared folder, then scan.")} /></Card>
      ) : (
        <>
          {!!cw.length && (
            <div>
              <div className="mb-2 text-[13px] font-medium text-muted">{tr("Continue watching")}</div>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">
                {cw.map(({ item, pos }) => {
                  const w: Work = item.kind === "episode"
                    ? { ...item, title: item.title, items: seriesMap.get(item.title) ?? [item] }
                    : { ...item, items: [item] };
                  const pct = Math.min(100, Math.round((pos.at / Math.max(1, pos.dur)) * 100));
                  return (
                    <button key={item.path} onClick={() => setPlay({ item, start: pos.at })} className="group overflow-hidden rounded-xl border border-line bg-panel text-left transition hover:border-accent/50">
                      <div className="relative">
                        <Poster w={w} className="aspect-video w-full object-cover" />
                        <span className="absolute inset-x-0 bottom-0 h-1 bg-black/50"><span className="block h-full bg-accent" style={{ width: `${pct}%` }} /></span>
                        <span className="absolute inset-0 flex items-center justify-center bg-black/30 opacity-0 transition group-hover:opacity-100"><Play size={28} className="text-white" /></span>
                      </div>
                      <div className="space-y-0.5 p-3">
                        <div className="truncate text-[13px] font-medium">{item.title}{item.kind === "episode" ? ` · ${epLabel(item)}` : ""}</div>
                        <div className="text-[11.5px] text-muted">{tr("Resume")} · {pct}%</div>
                      </div>
                    </button>
                  );
                })}
              </div>
            </div>
          )}
          {!!movies.length && (
            <div>
              <div className="mb-2 text-[13px] font-medium text-muted">{tr("Movies")}</div>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">{movies.map((w) => <WorkCard key={w.path} w={w} onClick={() => setSel(w)} />)}</div>
            </div>
          )}
          {!!series.length && (
            <div className="mt-6">
              <div className="mb-2 text-[13px] font-medium text-muted">{tr("Series")}</div>
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">{series.map((w) => <WorkCard key={w.title} w={w} onClick={() => setSel(w)} />)}</div>
            </div>
          )}
        </>
      )}

      <Modal open={!!sel} wide onClose={() => setSel(null)} title={sel ? `${sel.title}${sel.year ? ` (${sel.year})` : ""}` : ""}>
        {sel && (
          <div className="flex flex-col gap-4 sm:flex-row">
            <Poster w={sel} className="w-full max-w-40 rounded-lg border border-line object-cover sm:w-40" />
            <div className="min-w-0 flex-1 space-y-2">
              <div className="flex flex-wrap items-center gap-1.5 text-[12px] text-muted">
                {sel.rating ? <Badge tone="warn"><Star size={11} className="mr-0.5 inline" />{sel.rating.toFixed(1)}</Badge> : null}
                {sel.runtime ? <Badge>{tr("{0} min", sel.runtime)}</Badge> : null}
                {(sel.genres ?? []).map((g) => <Badge key={g}>{g}</Badge>)}
              </div>
              {sel.overview && <p className="text-[13px] text-[13.5px]">{sel.overview}</p>}
              {sel.cast?.length ? <div className="text-[12px] text-muted">{tr("Cast")}: {sel.cast.join(", ")}</div> : null}
              {sel.kind === "episode" ? (
                <div className="max-h-64 space-y-1 overflow-y-auto">
                  {sel.items.map((e) => (
                    <div key={e.path} className="flex items-center justify-between gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[12.5px]">
                      <span className="mono">{epLabel(e)}<span className="ml-2 text-muted">{bytes(e.size)}</span></span>
                      <Button size="sm" variant="ghost" icon={<Play size={13} />} onClick={() => setPlay({ item: e, start: 0 })}>{tr("Play")}</Button>
                    </div>
                  ))}
                </div>
              ) : (
                <div><Button variant="primary" icon={<Play size={14} />} onClick={() => setPlay({ item: sel.items[0], start: 0 })}>{tr("Play")}</Button></div>
              )}
            </div>
          </div>
        )}
      </Modal>

      <Modal open={!!play} wide onClose={() => setPlay(null)} title={play ? `${play.item.title}${play.item.kind === "episode" ? ` · ${epLabel(play.item)}` : ""}` : ""}>
        {play && (
          <video key={play.item.path} controls autoPlay className="w-full rounded-lg bg-black"
            src={`/api/files/play${q({ path: play.item.path, start: play.start || undefined, ...caps })}`}
            onTimeUpdate={(e) => {
              const v = e.currentTarget;
              const now = Date.now();
              if (now - lastSave.current > 5000) {
                lastSave.current = now;
                api.put("/api/cinema/position", { path: play.item.path, at: Math.floor(v.currentTime), duration: v.duration || 0 }).catch(() => {});
              }
            }}
            onEnded={(e) => {
              const v = e.currentTarget;
              api.put("/api/cinema/position", { path: play.item.path, at: v.duration || 0, duration: v.duration || 0 }).catch(() => {});
              qc.invalidateQueries({ queryKey: ["cinema"] });
            }} />
        )}
      </Modal>
    </div>
  );
}
