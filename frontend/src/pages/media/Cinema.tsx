import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Clapperboard, KeyRound, ListPlus, Moon, Play, RefreshCw, Search, Star, Trash2, Tv, Users } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { ago, bytes, cx } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, IconButton, Input, Modal, Select, Spinner, Switch } from "../../components/ui";
import { locale, tr } from "../../lib/i18n";
import { caps } from "../../components/files/MediaPlayer";
import { TvRemoteModal } from "../../components/ApplianceConsole";

export type CinemaItem = {
  path: string; title: string; year?: number; kind: string; season?: number; episode?: number; size: number; mod?: string;
  tmdbId?: number; poster?: string; overview?: string; rating?: number; runtime?: number; genres?: string[]; cast?: string[];
  collection?: string; collectionId?: number;
};
type CinemaData = { keySet: boolean; running: boolean; checked?: string; scanned: number; items: CinemaItem[]; positions: Record<string, { at: number; dur: number; upd: string }>; remote?: boolean };
type Work = { path?: string; title: string; year?: number; kind: string; items: CinemaItem[]; mod?: string; collection?: string; collectionId?: number; tmdbId?: number; poster?: string; overview?: string; rating?: number; runtime?: number; genres?: string[]; cast?: string[] };

const epLabel = (i: CinemaItem) => `S${String(i.season ?? 0).padStart(2, "0")}E${String(i.episode ?? 0).padStart(2, "0")}`;

function Poster({ w, className }: { w: Work; className?: string }) {
  if (w.tmdbId) return <img src={`/api/cinema/poster/${w.tmdbId}`} alt="" loading="lazy" className={className} />;
  return <div className={`flex items-center justify-center bg-panel-2 text-muted ${className}`}><Clapperboard size={28} /></div>;
}

// PosterCard: 10-foot card — big poster that zooms on hover, the YouTube
// trailer plays over it (hover, fine pointers only), and a ring when the
// keyboard/gamepad selection sits on it.
function PosterCard({ w, onClick, selected, cardRef, aspect = "aspect-[2/3]", overlay, footer }: {
  w: Work; onClick: () => void; selected?: boolean; cardRef?: (el: HTMLButtonElement | null) => void;
  aspect?: string; overlay?: ReactNode; footer?: ReactNode;
}) {
  const [trailer, setTrailer] = useState<string | null>(null);
  const known = useRef<string | null>(null); // null = not asked yet, "" = no trailer
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const canHover = useMemo(() => typeof window !== "undefined" && !!window.matchMedia?.("(hover: hover) and (pointer: fine)").matches, []);
  const kind = w.kind === "episode" ? "tv" : "movie";
  const enter = () => {
    if (!canHover || !w.tmdbId) return;
    if (known.current !== null) { if (known.current) setTrailer(known.current); return; }
    timer.current = setTimeout(() => {
      api.get<{ key: string; name: string }>(`/api/cinema/trailer${q({ kind, id: w.tmdbId })}`)
        .then((r) => { known.current = r.key; if (r.key) setTrailer(r.key); })
        .catch(() => { known.current = ""; });
    }, 700);
  };
  const leave = () => { if (timer.current) clearTimeout(timer.current); setTrailer(null); };
  return (
    <button ref={cardRef} onClick={onClick} onMouseEnter={enter} onMouseLeave={leave}
      className={cx("group relative overflow-hidden rounded-xl border bg-panel text-left transition duration-300 hover:-translate-y-1 hover:shadow-2xl hover:shadow-black/50", selected ? "border-accent ring-2 ring-accent" : "border-line hover:border-accent/60")}>
      <div className="relative overflow-hidden">
        <Poster w={w} className={cx(aspect, "w-full object-cover transition duration-500 group-hover:scale-110")} />
        {trailer && (
          <iframe className="absolute inset-0 h-full w-full" title={tr("Trailer")}
            src={`https://www.youtube-nocookie.com/embed/${trailer}?autoplay=1&mute=1&controls=0&loop=1&playlist=${trailer}&rel=0&playsinline=1`}
            allow="autoplay; encrypted-media; picture-in-picture" sandbox="allow-scripts allow-same-origin allow-presentation" />
        )}
        {!trailer && <span className="absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition group-hover:opacity-100"><Play size={28} className="text-white" /></span>}
        {overlay}
      </div>
      <div className="space-y-1 p-3">
        {footer ?? (
          <>
            <div className="truncate text-[13.5px] font-medium group-hover:text-accent">{w.title}</div>
            <div className="flex flex-wrap items-center gap-1.5 text-[11.5px] text-muted">
              {w.year ? <span>{w.year}</span> : null}
              {w.rating ? <span className="flex items-center gap-0.5 text-warn"><Star size={11} />{w.rating.toFixed(1)}</span> : null}
              {w.runtime ? <span>{tr("{0} min", w.runtime)}</span> : null}
              {w.kind === "episode" && <span>{tr("{0} episodes", w.items.length)}</span>}
            </div>
          </>
        )}
        {!w.tmdbId && <Badge tone="neutral">{tr("not matched with TMDB")}</Badge>}
      </div>
    </button>
  );
}

type ShelfRow = { key: string; works: Work[]; pick: (w: Work) => void };

// Keyboard (arrows + Enter, L/R for the shelves) and gamepad (D-pad, A = ok,
// B = back, L/R = shelves) navigation across the poster shelves.
function useShelfNav(rows: ShelfRow[], modalOpen: boolean) {
  const [sel, setSel] = useState<{ row: number; col: number } | null>(null);
  const selRef = useRef(sel);
  selRef.current = sel;
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const refs = useRef(new Map<string, HTMLButtonElement>());
  const register = useCallback((row: number, col: number) => (el: HTMLButtonElement | null) => {
    const k = row + ":" + col;
    if (el) refs.current.set(k, el); else refs.current.delete(k);
  }, []);
  const clampSel = (s: { row: number; col: number } | null) => {
    const r = rowsRef.current;
    if (!r.length) return null;
    const row = Math.max(0, Math.min(r.length - 1, s?.row ?? 0));
    const col = Math.max(0, Math.min(Math.max(1, r[row].works.length) - 1, s?.col ?? 0));
    return { row, col };
  };
  const move = useCallback((dr: number, dc: number) => {
    setSel((s) => clampSel({ row: (clampSel(s)?.row ?? 0) + dr, col: (clampSel(s)?.col ?? 0) + dc }));
  }, []);
  const moveRow = useCallback((dr: number) => move(dr, 0), [move]);
  const activate = useCallback(() => {
    const s = clampSel(selRef.current);
    const row = s ? rowsRef.current[s.row] : undefined;
    const w = s ? row?.works[s.col] : undefined;
    if (s && row && w) row.pick(w);
  }, []);
  useEffect(() => {
    if (!sel) return;
    refs.current.get(sel.row + ":" + sel.col)?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [sel]);
  useEffect(() => {
    if (modalOpen) return;
    const typing = () => {
      const ae = document.activeElement;
      return !!ae && (ae.tagName === "INPUT" || ae.tagName === "TEXTAREA" || ae.tagName === "SELECT" || (ae as HTMLElement).isContentEditable);
    };
    const onKey = (e: KeyboardEvent) => {
      if (typing()) return;
      switch (e.key) {
        case "ArrowRight": e.preventDefault(); move(0, 1); break;
        case "ArrowLeft": e.preventDefault(); move(0, -1); break;
        case "ArrowDown": e.preventDefault(); move(1, 0); break;
        case "ArrowUp": e.preventDefault(); move(-1, 0); break;
        case "Enter": case " ": e.preventDefault(); activate(); break;
        case "l": case "L": moveRow(1); break;
        case "r": case "R": moveRow(-1); break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [modalOpen, move, moveRow, activate]);
  useEffect(() => {
    let raf = 0;
    const prev: boolean[] = [];
    const loop = () => {
      const gp = navigator.getGamepads?.()[0];
      if (gp && !modalOpen) {
        const b = gp.buttons.map((x) => x.pressed);
        const hit = (i: number) => !!b[i] && !prev[i];
        if (hit(15)) move(0, 1); else if (hit(14)) move(0, -1); // D-pad
        if (hit(13)) move(1, 0); else if (hit(12)) move(-1, 0);
        if (hit(0)) activate(); // A = ok
        if (hit(5)) moveRow(1); else if (hit(4)) moveRow(-1); // R / L = shelves
        prev.length = 0;
        prev.push(...b);
      }
      if (gp && modalOpen) {
        const b = gp.buttons.map((x) => x.pressed);
        if (b[1] && !prev[1]) window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); // B = back
        prev.length = 0;
        prev.push(...b);
      }
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, [modalOpen, move, moveRow, activate]);
  return { sel, register };
}

function Shelf({ label, row, works, onPick, navSel, register }: {
  label: string; row: number; works: Work[]; onPick: (w: Work) => void;
  navSel: { row: number; col: number } | null; register: (r: number, c: number) => (el: HTMLButtonElement | null) => void;
}) {
  return (
    <div className="mt-6 first:mt-0">
      <div className="mb-2 text-[13px] font-medium text-muted">{label}</div>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">
        {works.map((w, ci) => <PosterCard key={w.path ?? w.title} w={w} onClick={() => onPick(w)} selected={navSel?.row === row && navSel?.col === ci} cardRef={register(row, ci)} />)}
      </div>
    </div>
  );
}

// Screen saver after 5 idle minutes: a slow wall of posters with the clock.
function Screensaver({ items, disabled }: { items: CinemaItem[]; disabled: boolean }) {
  const [idle, setIdle] = useState(false);
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    if (disabled) { setIdle(false); return; }
    let timer: ReturnType<typeof setTimeout> | undefined;
    const events = ["mousemove", "mousedown", "keydown", "wheel", "touchstart", "pointerdown"];
    const poke = () => { setIdle(false); clearTimeout(timer); timer = setTimeout(() => setIdle(true), 5 * 60 * 1000); };
    const tick = setInterval(() => setNow(new Date()), 1000);
    poke();
    for (const ev of events) window.addEventListener(ev, poke, { passive: true });
    return () => { clearTimeout(timer); clearInterval(tick); for (const ev of events) window.removeEventListener(ev, poke); };
  }, [disabled]);
  const posters = useMemo(() => items.filter((i) => i.tmdbId), [items]);
  if (!idle || disabled || !posters.length) return null;
  return (
    <div className="fixed inset-0 z-[9000] overflow-hidden bg-black" role="dialog" aria-label={tr("Screensaver")}>
      <div className="screensaver-wall absolute -inset-[25%] grid grid-cols-4 content-start gap-3 sm:grid-cols-6" aria-hidden>
        {posters.slice(0, 36).map((i) => <img key={i.path} src={`/api/cinema/poster/${i.tmdbId}`} alt="" loading="lazy" className="aspect-[2/3] w-full rounded-lg object-cover opacity-80" />)}
      </div>
      <div className="absolute inset-0 flex flex-col items-center justify-center bg-black/45">
        <div className="text-6xl font-semibold tabular-nums text-white/95 sm:text-8xl">{now.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" })}</div>
        <div className="mt-3 text-lg capitalize text-white/75 sm:text-2xl">{now.toLocaleDateString(locale, { weekday: "long", day: "numeric", month: "long" })}</div>
      </div>
    </div>
  );
}

type WatchList = { id: string; name: string; owner: string; mine: boolean; with?: string[]; created: string; items: (CinemaItem & { path: string })[] };

function ListModal({ list, items, onClose, onSave, onDelete, onPlay, saving, error }: { list: WatchList | null; items: CinemaItem[]; onClose: () => void; onSave: (id: string, body: object) => void; onDelete: (id: string) => void; onPlay: (i: CinemaItem) => void; saving: boolean; error: unknown }) {
  const [name, setName] = useState("");
  const [withText, setWithText] = useState("");
  const [add, setAdd] = useState("");
  const qc = useQueryClient();
  useEffect(() => {
    if (list) { setName(list.name); setWithText((list.with ?? []).join(", ")); setAdd(""); }
  }, [list]);
  if (!list) return null;
  const paths = new Set(list.items.map((i) => i.path));
  const addable = items.filter((i) => !paths.has(i.path));
  return (
    <Modal open wide onClose={onClose} title={list.name}
      footer={<>
        <Button onClick={onClose}>{tr("Cancel")}</Button>
        {list.mine && <Button variant="danger" icon={<Trash2 size={14} />} disabled={saving} onClick={() => onDelete(list.id)}>{tr("Delete")}</Button>}
        <Button variant="primary" loading={saving} onClick={() => onSave(list.id, { name, items: list.items.map((i) => i.path), with: withText.split(",").map((x) => x.trim()).filter(Boolean) })}>{tr("Save")}</Button>
      </>}>
      <div className="space-y-4">
        <ErrorBox error={error} />
        {list.mine && <Field label={tr("List name")}><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>}
        {list.mine && <Field label={tr("Members (account names, comma separated)")}><Input value={withText} onChange={(e) => setWithText(e.target.value)} placeholder="alice, bob" /></Field>}
        {!list.mine && !!list.with?.length && <div className="text-[12px] text-muted">{tr("Shared with {0}", list.with.join(", "))}</div>}
        <Field label={tr("Add videos")}>
          <div className="flex gap-2">
            <Select value={add} onChange={(e) => setAdd(e.target.value)} className="min-w-0 flex-1">
              <option value="">{tr("Search a title…")}</option>
              {addable.map((i) => <option key={i.path} value={i.path}>{i.title}{i.year ? ` (${i.year})` : ""}</option>)}
            </Select>
            <Button size="sm" disabled={!add} onClick={() => { if (add) { onSave(list.id, { items: [...list.items.map((i) => i.path), add] }); qc.invalidateQueries({ queryKey: ["cinema-lists"] }); } }}>{tr("Add")}</Button>
          </div>
        </Field>
        <div className="max-h-72 space-y-1 overflow-y-auto">
          {list.items.map((i) => (
            <div key={i.path} className="flex items-center justify-between gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[12.5px]">
              <span className="truncate">{i.title}{i.kind === "episode" ? ` · ${epLabel(i)}` : ""}{i.year ? ` (${i.year})` : ""}</span>
              <span className="flex shrink-0 gap-1">
                <IconButton title={tr("Play")} onClick={() => { onPlay(i); onClose(); }}><Play size={14} /></IconButton>
                <IconButton title={tr("Remove")} onClick={() => onSave(list.id, { items: list.items.filter((x) => x.path !== i.path).map((x) => x.path) })}><Trash2 size={14} /></IconButton>
              </span>
            </div>
          ))}
          {!list.items.length && <div className="text-[12.5px] text-muted">{tr("No video yet")}</div>}
        </div>
      </div>
    </Modal>
  );
}

export function CinemaTab({ admin }: { admin: boolean }) {
  const qc = useQueryClient();
  const lib = useQuery({ queryKey: ["cinema"], queryFn: () => api.get<CinemaData>("/api/cinema"), refetchInterval: (qq) => (qq.state.data?.running ? 5000 : 60000) });
  const scan = useAction(() => api.post("/api/cinema/scan", {}), { onDone: () => qc.invalidateQueries({ queryKey: ["cinema"] }) });
  const [key, setKey] = useState("");
  const saveKey = useAction(() => api.put("/api/cinema/settings", { tmdbKey: key }), { success: tr("Key saved"), onDone: () => { setKey(""); qc.invalidateQueries({ queryKey: ["cinema"] }); } });
  const [filter, setFilter] = useState("");
  // Family profile: the library hides what rates above the profile's cap.
  const profiles = useQuery({ queryKey: ["cinema-profiles"], queryFn: () => api.get<{ id: string; name: string; maxRating?: number }[]>("/api/cinema/profiles"), staleTime: 60000 });
  const [profileId, setProfileId] = useState<string | null>(() => { try { return localStorage.getItem("strata-profile"); } catch { return null; } });
  useEffect(() => { try { if (profileId) localStorage.setItem("strata-profile", profileId); else localStorage.removeItem("strata-profile"); } catch { /* private mode */ } }, [profileId]);
  const [newProf, setNewProf] = useState<{ name: string; maxRating: string } | null>(null);
  const mkProf = useAction(() => api.post("/api/cinema/profiles", { name: newProf?.name ?? "", maxRating: parseFloat(newProf?.maxRating ?? "0") || 0 }), { success: tr("Profile created"), onDone: () => { setNewProf(null); qc.invalidateQueries({ queryKey: ["cinema-profiles"] }); } });
  const delProf = useAction((id: string) => api.del(`/api/cinema/profiles/${id}`), { success: tr("Profile deleted"), onDone: () => { qc.invalidateQueries({ queryKey: ["cinema-profiles"] }); } });
  const lists = useQuery({ queryKey: ["cinema-lists"], queryFn: () => api.get<WatchList[]>("/api/cinema/lists"), refetchInterval: 30000 });
  const [listSel, setListSel] = useState<WatchList | null>(null);
  const [newList, setNewList] = useState<string | null>("");
  const mkList = useAction(() => api.post("/api/cinema/lists", { name: newList ?? "" }), { success: tr("List created"), onDone: () => { setNewList(""); qc.invalidateQueries({ queryKey: ["cinema-lists"] }); } });
  const saveList = useAction((l: { id: string; body: object }) => api.put(`/api/cinema/lists/${l.id}`, l.body), { success: tr("List saved"), onDone: () => qc.invalidateQueries({ queryKey: ["cinema-lists"] }) });
  const delList = useAction((id: string) => api.del(`/api/cinema/lists/${id}`), { success: tr("List deleted"), onDone: () => { setListSel(null); qc.invalidateQueries({ queryKey: ["cinema-lists"] }); } });
  const night = useQuery({ queryKey: ["media-night"], queryFn: () => api.get<{ enabled: boolean; from: number; to: number; maxVolume: number; brightness: number }>("/api/media/night"), staleTime: 60000 });
  const [nightForm, setNightForm] = useState<{ enabled: boolean; from: string; to: string; maxVolume: string; brightness: string } | null>(null);
  const saveNight = useAction(() => api.put("/api/media/night", { enabled: !!nightForm?.enabled, from: parseInt(nightForm?.from ?? "22") || 0, to: parseInt(nightForm?.to ?? "6") || 0, maxVolume: parseInt(nightForm?.maxVolume ?? "30") || 30, brightness: parseInt(nightForm?.brightness ?? "80") || 80 }), { success: tr("Night mode saved"), onDone: () => { setNightForm(null); qc.invalidateQueries({ queryKey: ["media-night"] }); } });
  const [sel, setSel] = useState<Work | null>(null);
  const [play, setPlay] = useState<{ item: CinemaItem; start: number } | null>(null);
  const lastSave = useRef(0);
  const [tvRemote, setTvRemote] = useState(false);

  const d = lib.data;
  const allItems = d?.items ?? [];
  const prof = profiles.data?.find((p) => p.id === profileId);
  const items = prof && (prof.maxRating ?? 0) > 0 ? allItems.filter((i) => !i.rating || i.rating <= prof.maxRating!) : allItems;
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
  // Auto-collections: added this week, top rated, franchises (TMDB collections).
  const weekAgo = Date.now() - 7 * 864e5;
  const added = movies.filter((w) => w.mod && new Date(w.mod).getTime() >= weekAgo).sort((a, b) => new Date(b.mod!).getTime() - new Date(a.mod!).getTime()).slice(0, 12);
  const top = movies.filter((w) => (w.rating ?? 0) >= 7.5).sort((a, b) => (b.rating ?? 0) - (a.rating ?? 0)).slice(0, 12);
  const collMap = new Map<number, Work[]>();
  for (const w of movies) {
    if (w.collectionId) {
      const a = collMap.get(w.collectionId) ?? [];
      a.push(w);
      collMap.set(w.collectionId, a);
    }
  }
  const franchises = [...collMap.entries()].filter(([, ws]) => ws.length > 1).map(([id, ws]) => ({ ...ws[0], title: ws[0].collection ?? "", kind: "collection", items: ws.map((w) => w.items[0]), collectionId: id }));
  // Continue watching: the videos this account stopped, newest first.
  const cw = Object.entries(d?.positions ?? {})
    .map(([path, pos]) => ({ item: items.find((i) => i.path === path), pos }))
    .filter((x): x is { item: CinemaItem; pos: { at: number; dur: number; upd: string } } => !!x.item)
    .sort((a, b) => (b.pos.upd ?? "").localeCompare(a.pos.upd ?? ""))
    .slice(0, 12);

  // Shelves for the keyboard/gamepad navigation (arrows + Enter, L/R shelves).
  const rows: ShelfRow[] = [];
  if (cw.length) rows.push({ key: "cw", works: cw.map(({ item }) => ({ ...item, items: item.kind === "episode" ? seriesMap.get(item.title) ?? [item] : [item] })), pick: (w) => { const c = cw.find((x) => x.item.path === w.path); if (c) setPlay({ item: c.item, start: c.pos.at }); } });
  if (added.length) rows.push({ key: "added", works: added, pick: (w) => setSel(w) });
  if (top.length) rows.push({ key: "top", works: top, pick: (w) => setSel(w) });
  if (franchises.length) rows.push({ key: "franchises", works: franchises, pick: (w) => setSel(w) });
  if (movies.length) rows.push({ key: "movies", works: movies, pick: (w) => setSel(w) });
  if (series.length) rows.push({ key: "series", works: series, pick: (w) => setSel(w) });
  const rowOf = (key: string) => rows.findIndex((r) => r.key === key);
  const modalOpen = !!(sel || play || listSel || newList !== null);
  const { sel: navSel, register } = useShelfNav(rows, modalOpen);

  return (
    <div className="space-y-4">
      <TvRemoteModal open={tvRemote} onClose={() => setTvRemote(false)} />
      <Card>
        <div className="flex flex-wrap items-center gap-3">
          <div className="relative min-w-48 flex-1">
            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <Input className="pl-8" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={tr("Search a title…")} />
          </div>
          <Select value={profileId ?? ""} onChange={(e) => setProfileId(e.target.value || null)} className="!h-9 w-44 text-[12.5px]" aria-label={tr("Family profiles")}>
            <option value="">{tr("All content")}</option>
            {(profiles.data ?? []).map((p) => <option key={p.id} value={p.id}>{p.name}{p.maxRating ? ` (≤ ${p.maxRating})` : ""}</option>)}
          </Select>
          {d?.remote && <Button icon={<Tv size={14} />} onClick={() => setTvRemote(true)}>{tr("TV remote")}</Button>}
          {admin && <Button icon={<RefreshCw size={14} />} loading={scan.isPending || d?.running} disabled={d?.running} onClick={() => scan.mutate()}>{d?.running ? tr("Scanning…") : tr("Scan the library")}</Button>}
        </div>
        {d && <div className="mt-2 text-[12px] text-muted">{tr("Scanned {0} videos", d.scanned)}{d.checked && ` · ${tr("Last scan {0}", ago(d.checked))}`}</div>}
        <p className="mt-1 text-[11.5px] text-muted">{tr("Navigate with the arrow keys (Enter to open, L/R for the shelves); a gamepad works too (A = ok, B = back, L/R = shelves).")}</p>
        <ErrorBox error={lib.error || scan.error} />
      </Card>

      {admin && (
        <Card title={<span className="flex items-center gap-2"><Users size={16} />{tr("Family profiles")}</span>}
          actions={<Button size="sm" variant="ghost" icon={<ListPlus size={14} />} onClick={() => setNewProf({ name: "", maxRating: "6" })}>{tr("New profile")}</Button>}>
          <p className="text-muted text-[12.5px]">{tr("A profile hides the videos rated above its maximum on TMDB: a kid-only shelf.")}</p>
          <div className="mt-2 flex flex-wrap gap-2">
            {(profiles.data ?? []).map((p) => (
              <span key={p.id} className="flex items-center gap-1.5 rounded-lg border border-line bg-panel-2 px-2.5 py-1.5 text-[12.5px]">
                {p.name}{p.maxRating ? ` (≤ ${p.maxRating})` : ""}
                <IconButton title={tr("Delete")} onClick={() => { delProf.mutate(p.id); if (profileId === p.id) setProfileId(null); }}><Trash2 size={13} /></IconButton>
              </span>
            ))}
            {!profiles.data?.length && !newProf && <span className="text-[12.5px] text-muted">{tr("No profile yet")}</span>}
          </div>
          {newProf && (
            <div className="mt-3 flex flex-wrap items-end gap-3">
              <Field label={tr("Profile name")}><Input className="w-44" value={newProf.name} onChange={(e) => setNewProf({ ...newProf, name: e.target.value })} /></Field>
              <Field label={tr("Maximum rating (0 = no limit)")}><Input className="w-24" type="number" min={0} max={10} step={0.5} value={newProf.maxRating} onChange={(e) => setNewProf({ ...newProf, maxRating: e.target.value })} /></Field>
              <Button size="sm" variant="primary" loading={mkProf.isPending} disabled={!newProf.name.trim()} onClick={() => mkProf.mutate()}>{tr("Add")}</Button>
              <Button size="sm" variant="ghost" onClick={() => setNewProf(null)}>{tr("Cancel")}</Button>
            </div>
          )}
          <ErrorBox error={mkProf.error || delProf.error} />
        </Card>
      )}

      {admin && (
        <Card title={<span className="flex items-center gap-2"><Moon size={16} />{tr("Night mode")}</span>}
          actions={nightForm ? <Button size="sm" variant="primary" loading={saveNight.isPending} onClick={() => saveNight.mutate()}>{tr("Save")}</Button> : <Button size="sm" variant="ghost" onClick={() => setNightForm({ enabled: !!night.data?.enabled, from: String(night.data?.from ?? 22), to: String(night.data?.to ?? 6), maxVolume: String(night.data?.maxVolume ?? 30), brightness: String(night.data?.brightness ?? 80) })}>{tr("Edit")}</Button>}>
          <p className="text-muted text-[12.5px]">{tr("Limits the brightness and the volume of the players during these hours.")}</p>
          {nightForm ? (
            <div className="mt-3 space-y-3">
              <Switch checked={nightForm.enabled} onChange={(v) => setNightForm({ ...nightForm, enabled: v })} label={tr("Enabled")} />
              <div className="flex flex-wrap items-end gap-3">
                <Field label={tr("From (hour)")}><Input className="w-20" type="number" min={0} max={23} value={nightForm.from} onChange={(e) => setNightForm({ ...nightForm, from: e.target.value })} /></Field>
                <Field label={tr("To (hour)")}><Input className="w-20" type="number" min={0} max={23} value={nightForm.to} onChange={(e) => setNightForm({ ...nightForm, to: e.target.value })} /></Field>
                <Field label={tr("Maximum volume (%)")}><Input className="w-24" type="number" min={1} max={100} value={nightForm.maxVolume} onChange={(e) => setNightForm({ ...nightForm, maxVolume: e.target.value })} /></Field>
                <Field label={tr("Brightness (%)")}><Input className="w-24" type="number" min={20} max={100} value={nightForm.brightness} onChange={(e) => setNightForm({ ...nightForm, brightness: e.target.value })} /></Field>
              </div>
              <ErrorBox error={saveNight.error} />
            </div>
          ) : night.data?.enabled ? (
            <div className="mt-2 text-[12.5px] text-muted">{night.data.from}:00 – {night.data.to}:00 · {tr("Maximum volume (%)")} {night.data.maxVolume} · {tr("Brightness (%)")} {night.data.brightness}</div>
          ) : null}
        </Card>
      )}

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
                {cw.map(({ item, pos }, ci) => {
                  const w: Work = item.kind === "episode"
                    ? { ...item, title: item.title, items: seriesMap.get(item.title) ?? [item] }
                    : { ...item, items: [item] };
                  const pct = Math.min(100, Math.round((pos.at / Math.max(1, pos.dur)) * 100));
                  return (
                    <PosterCard key={item.path} w={w} aspect="aspect-video" onClick={() => setPlay({ item, start: pos.at })}
                      selected={navSel?.row === rowOf("cw") && navSel?.col === ci} cardRef={register(rowOf("cw"), ci)}
                      overlay={<span className="absolute inset-x-0 bottom-0 z-10 h-1 bg-black/50"><span className="block h-full bg-accent" style={{ width: `${pct}%` }} /></span>}
                      footer={<>
                        <div className="truncate text-[13px] font-medium">{item.title}{item.kind === "episode" ? ` · ${epLabel(item)}` : ""}</div>
                        <div className="text-[11.5px] text-muted">{tr("Resume")} · {pct}%</div>
                      </>} />
                  );
                })}
              </div>
            </div>
          )}
          {!!added.length && <Shelf label={tr("Added this week")} row={rowOf("added")} works={added} onPick={(w) => setSel(w)} navSel={navSel} register={register} />}
          {!!top.length && <Shelf label={tr("Top rated")} row={rowOf("top")} works={top} onPick={(w) => setSel(w)} navSel={navSel} register={register} />}
          {!!franchises.length && <Shelf label={tr("Collections")} row={rowOf("franchises")} works={franchises} onPick={(w) => setSel(w)} navSel={navSel} register={register} />}
          <div className="mt-6">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <span className="text-[13px] font-medium text-muted">{tr("Watch together")}</span>
              <Button size="sm" variant="ghost" icon={<ListPlus size={14} />} onClick={() => setNewList(" ")}>{tr("New list")}</Button>
            </div>
            {!lists.data?.length ? (
              <div className="text-[12.5px] text-muted">{tr("No list yet: create one and share it with other accounts.")}</div>
            ) : (
              <div className="flex flex-wrap gap-2">
                {lists.data.map((l) => (
                  <button key={l.id} onClick={() => setListSel(l)} className="flex items-center gap-2 rounded-xl border border-line bg-panel px-3 py-2 text-[12.5px] hover:border-accent/50">
                    <span className="font-medium">{l.name}</span>
                    <Badge>{tr("{0} video(s)", l.items.length)}</Badge>
                    {!!l.with?.length && <span className="text-muted" title={tr("Shared with {0}", l.with.length)}><Users size={13} /></span>}
                    {!l.mine && <Badge tone="accent">{l.owner}</Badge>}
                  </button>
                ))}
              </div>
            )}
          </div>
          {!!movies.length && <Shelf label={tr("Movies")} row={rowOf("movies")} works={movies} onPick={(w) => setSel(w)} navSel={navSel} register={register} />}
          {!!series.length && <Shelf label={tr("Series")} row={rowOf("series")} works={series} onPick={(w) => setSel(w)} navSel={navSel} register={register} />}
        </>
      )}

      <Screensaver items={allItems} disabled={modalOpen} />

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
              {sel.kind !== "movie" ? (
                <div className="max-h-64 space-y-1 overflow-y-auto">
                  {sel.items.map((e) => (
                    <div key={e.path} className="flex items-center justify-between gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[12.5px]">
                      <span className={sel.kind === "episode" ? "mono" : ""}>{sel.kind === "episode" ? epLabel(e) : `${e.title}${e.year ? ` (${e.year})` : ""}`}<span className="ml-2 text-muted">{bytes(e.size)}</span></span>
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

      <Modal open={newList !== null} onClose={() => setNewList(null)} title={tr("New list")} footer={<><Button onClick={() => setNewList(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={mkList.isPending} disabled={!(newList ?? "").trim()} onClick={() => mkList.mutate()}>{tr("Add")}</Button></>}>
        <Field label={tr("List name")}><Input value={newList ?? ""} onChange={(e) => setNewList(e.target.value)} placeholder={tr("Weekend")} /></Field>
        <ErrorBox error={mkList.error} />
      </Modal>

      <ListModal list={listSel} items={items} onClose={() => setListSel(null)} onSave={(id, body) => saveList.mutate({ id, body })} onDelete={(id) => delList.mutate(id)} onPlay={(i) => setPlay({ item: i, start: 0 })} saving={saveList.isPending} error={saveList.error || delList.error} />

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
