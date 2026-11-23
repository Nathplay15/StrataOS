import { useMemo, useRef, useState, type PointerEvent, type WheelEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, EyeOff, Eye, Merge, Minus, Plus, Sparkles, Split, UserRound, X } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, cx } from "../../lib/format";
import { locale, tr } from "../../lib/i18n";
import { LAND } from "../../lib/worldMap";
import { tagLabel } from "../../lib/photoTags";
import { LimitsFields, PAUSE_REASONS, type Limits } from "../../components/BackgroundLimits";
import { Button, Empty, ErrorBox, Field, Input, Modal, Notice, Progress, Spinner, Switch, useUI } from "../../components/ui";

// The smart features of Photos: people, places (offline world map),
// things, memories and the settings of the analysis. Everything is computed
// on the NAS; each user only sees what the pictures they can open contain.

export type SmartStatus = {
  installed: boolean; enabled: boolean; faces: boolean; objects: boolean;
  running: boolean; done: number; total: number; paused?: string; current?: string; error?: string;
  settings?: { enabled?: boolean; noFaces?: boolean; noObjects?: boolean; limits?: Limits };
  threads?: number; modelBytes?: number; dbBytes?: number; provider?: string;
  counts?: { photos: number; failed: number; faces: number };
};
export type Face = { id: number; path: string; box: [number, number, number, number]; score: number; person: number; manual?: boolean };
export type Person = { id: number; name: string; hidden: boolean; count: number; cover?: Face };
export type Place = { id: number; city: string; region?: string; country: string; lat: number; lon: number; count: number; cover: string };
export type TagView = { tag: string; count: number; cover: string };
export type Filter = { kind: "person" | "tag" | "place"; id: string; label: string };

const thumb = (p: string) => `/api/photos/thumb${q({ path: p, size: "s" })}`;
export const faceURL = (id: number) => `/api/photos/face${q({ id })}`;

export function useSmart() {
  return useQuery({ queryKey: ["photos-smart"], queryFn: () => api.get<SmartStatus>("/api/photos/smart"), refetchInterval: (d) => (d.state.data?.running ? 4000 : 60000) });
}

const countryName = (cc: string) => {
  try {
    return new Intl.DisplayNames([locale], { type: "region" }).of(cc) ?? cc;
  } catch {
    return cc;
  }
};

export const placeLabel = (p: Place) => `${p.city}, ${countryName(p.country)}`;

export function SmartProgress({ s }: { s: SmartStatus }) {
  if (!s.running || s.total === 0) return null;
  return (
    <div className="mb-4 rounded-lg border border-line px-4 py-2.5 text-[13px]">
      <div className="mb-1.5 flex justify-between gap-3">
        <span className="flex items-center gap-2"><Sparkles size={14} className="text-accent" />{s.paused ? PAUSE_REASONS[s.paused] ?? s.paused : tr("Analysing photos on the NAS…")}</span>
        <span className="text-muted">{s.done} / {s.total}</span>
      </div>
      <Progress value={(100 * s.done) / s.total} />
    </div>
  );
}

export function SmartSettings({ onClose }: { onClose: () => void }) {
  const { data: s } = useQuery({ queryKey: ["photos-smart"], queryFn: () => api.get<SmartStatus>("/api/photos/smart"), refetchInterval: 4000 });
  const [form, setForm] = useState<SmartStatus["settings"] | null>(null);
  const cur = form ?? s?.settings ?? {};
  const set = (p: Partial<NonNullable<SmartStatus["settings"]>>) => setForm({ ...cur, ...p });
  const install = useAction(() => api.post("/api/system/components/photoml/install"));
  const save = useAction(() => api.put("/api/photos/smart", cur), { success: tr("Saved"), onDone: onClose });
  const { confirm } = useUI();
  const reset = useAction(() => api.post("/api/photos/smart/reset"), { success: tr("Every photo will be analysed again") });
  return (
    <Modal open wide onClose={onClose} title={tr("Smart features")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" disabled={!s?.installed} loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button></>}>
      {!s ? <Spinner /> : (
        <div className="space-y-4">
          <p className="text-muted text-[13px]">{tr("Recognises faces to group them into people, things and scenes (dog, beach, car…) and lets you search photos by description. Everything is computed on the NAS: no photo leaves it. Each user only sees the people and things of the photos they can open, and names people for themselves.")}</p>
          {!s.installed ? (
            <Notice>
              <div className="flex flex-wrap items-center gap-3">
                <span className="flex-1">{tr("Needs the analysis runtime and its models (about 190 MB to download, 1 GB of free memory recommended).")}</span>
                <Button size="sm" variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>
              </div>
            </Notice>
          ) : <>
            <Switch checked={!!cur.enabled} onChange={(v) => set({ enabled: v })} label={tr("Analyse the photos")} />
            <Switch checked={!cur.noFaces} disabled={!cur.enabled} onChange={(v) => set({ noFaces: !v })} label={tr("People (face recognition)")} />
            <Switch checked={!cur.noObjects} disabled={!cur.enabled} onChange={(v) => set({ noObjects: !v })} label={tr("Things and search by description")} hint={tr("Descriptions work best in English (\"dog on a beach\"); the names of things are translated.")} />
            <LimitsFields value={cur.limits ?? {}} onChange={(l) => set({ limits: l })} />
            <div className="rounded-lg border border-line p-3 text-[13px]">
              <div className="flex flex-wrap gap-x-6 gap-y-1">
                <span>{tr("{0} photos analysed", s.counts?.photos ?? 0)}</span>
                <span>{tr("{0} faces", s.counts?.faces ?? 0)}</span>
                {!!s.counts?.failed && <span className="text-warn">{tr("{0} could not be read", s.counts.failed)}</span>}
                <span className="text-muted">{tr("Models: {0}", bytes(s.modelBytes ?? 0))}</span>
                <span className="text-muted">{tr("Database: {0}", bytes(s.dbBytes ?? 0))}</span>
                {s.provider && <span className="text-muted">{tr("Runs on: {0}", s.provider.replace("ExecutionProvider", ""))}</span>}
              </div>
              {s.running && <div className="mt-2"><div className="mb-1 truncate text-muted text-[12px]">{s.paused ? PAUSE_REASONS[s.paused] ?? s.paused : s.current}</div><Progress value={s.total ? (100 * s.done) / s.total : 0} /></div>}
              {s.error && <div className="mt-2 text-danger text-[12.5px]">{s.error}</div>}
              <div className="mt-3"><Button size="sm" loading={reset.isPending} onClick={async () => { if (await confirm({ title: tr("Analyse everything again?"), text: tr("Faces, people and their names, and things are computed again from the start."), danger: true })) reset.mutate(); }}>{tr("Start over")}</Button></div>
            </div>
          </>}
          <ErrorBox error={install.error ?? save.error ?? reset.error} />
        </div>
      )}
    </Modal>
  );
}

// ---- People ----

export function PeopleView({ onOpen }: { onOpen: (p: Person) => void }) {
  const [hidden, setHidden] = useState(false);
  const { data, isLoading, error } = useQuery({ queryKey: ["photo-people", hidden], queryFn: () => api.get<Person[]>(`/api/photos/people${q({ hidden: hidden ? 1 : undefined })}`) });
  const [merging, setMerging] = useState<number[] | null>(null);
  const [into, setInto] = useState<number | null>(null);
  const merge = useAction(() => api.post(`/api/photos/people/${into}/merge`, { from: merging!.filter((x) => x !== into) }), { success: tr("People merged"), onDone: () => { setMerging(null); setInto(null); } });
  if (isLoading) return <Spinner />;
  return (
    <div>
      <ErrorBox error={error} />
      <div className="mb-3 flex flex-wrap items-center gap-2 text-[13px]">
        {merging ? <>
          <span>{tr("Choose the people to merge, then the one to keep")}</span><div className="flex-1" />
          <Button size="sm" variant="primary" icon={<Merge size={14} />} disabled={merging.length < 2} onClick={() => setInto(merging[0])}>{tr("Merge")}</Button>
          <Button size="sm" onClick={() => setMerging(null)}>{tr("Cancel")}</Button>
        </> : <>
          <div className="flex-1" />
          <Button size="sm" variant="ghost" icon={hidden ? <EyeOff size={14} /> : <Eye size={14} />} onClick={() => setHidden(!hidden)}>{hidden ? tr("Hide hidden people") : tr("Show hidden people")}</Button>
          <Button size="sm" icon={<Merge size={14} />} onClick={() => setMerging([])}>{tr("Merge")}</Button>
        </>}
      </div>
      {!data?.length ? <Empty icon={<UserRound />} title={tr("No people yet")} text={tr("People appear once the same face is found in several photos.")} /> : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(120px,1fr))] gap-4">
          {data.map((p) => {
            const on = merging?.includes(p.id);
            return (
              <button key={p.id} onClick={() => (merging ? setMerging(on ? merging.filter((x) => x !== p.id) : [...merging, p.id]) : onOpen(p))} className={cx("group text-center", p.hidden && "opacity-50")}>
                <div className={cx("relative mx-auto aspect-square w-full overflow-hidden rounded-full bg-panel-2", on && "ring-3 ring-accent")}>
                  {p.cover ? <img src={faceURL(p.cover.id)} alt="" loading="lazy" className="h-full w-full object-cover" /> : <UserRound className="m-auto h-full text-muted" />}
                  {on && <span className="absolute inset-0 flex items-center justify-center bg-accent/30 text-white"><Check size={28} /></span>}
                </div>
                <div className={cx("mt-2 truncate text-[13px]", p.name ? "font-medium" : "text-muted")}>{p.name || tr("Add a name")}</div>
                <div className="text-muted text-[12px]">{tr("{0} photo(s)", p.count)}</div>
              </button>
            );
          })}
        </div>
      )}
      <Modal open={into !== null} onClose={() => setInto(null)} title={tr("Merge people")} footer={<><Button onClick={() => setInto(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={merge.isPending} onClick={() => merge.mutate()}>{tr("Merge")}</Button></>}>
        <p className="mb-3 text-[13px]">{tr("Keep the name of:")}</p>
        <div className="flex flex-wrap gap-3">
          {data?.filter((p) => merging?.includes(p.id)).map((p) => (
            <button key={p.id} onClick={() => setInto(p.id)} className={cx("w-24 text-center", into === p.id ? "font-semibold" : "opacity-70")}>
              <img src={p.cover ? faceURL(p.cover.id) : ""} alt="" className={cx("mx-auto h-20 w-20 rounded-full object-cover", into === p.id && "ring-3 ring-accent")} />
              <div className="mt-1 truncate text-[12.5px]">{p.name || tr("No name")}</div>
            </button>
          ))}
        </div>
        <ErrorBox error={merge.error} />
      </Modal>
    </div>
  );
}

/** Header of a person: name, hide, and the faces to split off. */
export function PersonTools({ person, onGone }: { person: Person; onGone: () => void }) {
  const [name, setName] = useState<string | null>(null);
  const [faces, setFaces] = useState(false);
  const rename = useAction(() => api.put(`/api/photos/people/${person.id}`, { name: name?.trim() }), { onDone: () => { person.name = name!.trim(); setName(null); } });
  const hide = useAction(() => api.put(`/api/photos/people/${person.id}`, { hidden: !person.hidden }), { success: person.hidden ? tr("Person shown again") : tr("Person hidden"), onDone: onGone });
  return (
    <>
      <Button size="sm" onClick={() => setName(person.name)}>{person.name ? tr("Rename") : tr("Add a name")}</Button>
      <Button size="sm" icon={<Split size={14} />} onClick={() => setFaces(true)}>{tr("Faces")}</Button>
      <Button size="sm" icon={person.hidden ? <Eye size={14} /> : <EyeOff size={14} />} loading={hide.isPending} onClick={() => hide.mutate()}>{person.hidden ? tr("Show") : tr("Hide")}</Button>
      <Modal open={name !== null} onClose={() => setName(null)} title={tr("Name")} footer={<><Button onClick={() => setName(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={rename.isPending} onClick={() => rename.mutate()}>{tr("Save")}</Button></>}>
        <Field label={tr("Name of this person")} hint={tr("Only you see the names you give.")}><Input autoFocus value={name ?? ""} maxLength={80} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && rename.mutate()} /></Field>
        <ErrorBox error={rename.error} />
      </Modal>
      {faces && <FacesModal person={person} onClose={() => setFaces(false)} onGone={onGone} />}
    </>
  );
}

function FacesModal({ person, onClose, onGone }: { person: Person; onClose: () => void; onGone: () => void }) {
  const { data, isLoading } = useQuery({ queryKey: ["person-faces", person.id], queryFn: () => api.get<Face[]>(`/api/photos/people/${person.id}/faces`) });
  const [sel, setSel] = useState<number[]>([]);
  const [newName, setNewName] = useState<string | null>(null);
  const move = useAction((body: { to: number; name?: string }) => api.post("/api/photos/faces/move", { faces: sel, ...body }), {
    success: tr("Faces moved"),
    onDone: () => { if (sel.length === data?.length) onGone(); setSel([]); setNewName(null); },
  });
  return (
    <Modal open wide onClose={onClose} title={person.name || tr("Faces")} footer={<>
      <span className="mr-auto text-muted text-[12.5px]">{tr("{0} selected", sel.length)}</span>
      <Button icon={<X size={14} />} disabled={!sel.length} loading={move.isPending} onClick={() => move.mutate({ to: -1 })}>{tr("Not this person")}</Button>
      <Button icon={<Split size={14} />} disabled={!sel.length} onClick={() => setNewName("")}>{tr("Another person")}</Button>
      <Button onClick={onClose}>{tr("Close")}</Button>
    </>}>
      <p className="mb-3 text-muted text-[13px]">{tr("Select the faces that are not this person: they can go to a new person, or to nobody. Grouping never changes your choices.")}</p>
      {isLoading ? <Spinner /> : (
        <div className="grid max-h-[55vh] grid-cols-[repeat(auto-fill,minmax(84px,1fr))] gap-2 overflow-y-auto">
          {data?.map((f) => {
            const on = sel.includes(f.id);
            return (
              <button key={f.id} onClick={() => setSel(on ? sel.filter((x) => x !== f.id) : [...sel, f.id])} className={cx("relative aspect-square overflow-hidden rounded-lg", on && "ring-3 ring-accent")}>
                <img src={faceURL(f.id)} alt="" loading="lazy" className="h-full w-full object-cover" />
                {on && <span className="absolute top-1 left-1 rounded-full bg-accent p-0.5 text-white"><Check size={12} /></span>}
              </button>
            );
          })}
        </div>
      )}
      <Modal open={newName !== null} onClose={() => setNewName(null)} title={tr("Another person")} footer={<><Button onClick={() => setNewName(null)}>{tr("Cancel")}</Button><Button variant="primary" loading={move.isPending} onClick={() => move.mutate({ to: 0, name: newName ?? "" })}>{tr("Create")}</Button></>}>
        <Field label={tr("Name (optional)")}><Input autoFocus value={newName ?? ""} maxLength={80} onChange={(e) => setNewName(e.target.value)} /></Field>
        <ErrorBox error={move.error} />
      </Modal>
      <ErrorBox error={move.error} />
    </Modal>
  );
}

// ---- Places: an offline world map (Natural Earth land, equirectangular) ----

export function PlacesView({ onOpen }: { onOpen: (p: Place) => void }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["photo-places"], queryFn: () => api.get<Place[]>("/api/photos/places") });
  const [view, setView] = useState({ x: 0, y: 0, k: 1 }); // translation (map units) and zoom
  const drag = useRef<{ x: number; y: number; vx: number; vy: number; w: number } | null>(null);
  const svg = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<Place | null>(null);
  const maxCount = useMemo(() => Math.max(1, ...(data ?? []).map((p) => p.count)), [data]);
  if (isLoading) return <Spinner />;
  const zoom = (f: number, cx = 180, cy = 90) => setView((v) => {
    const k = Math.min(40, Math.max(1, v.k * f));
    // Keep the point under the cursor in place.
    return { k, x: cx - ((cx - v.x) * k) / v.k, y: cy - ((cy - v.y) * k) / v.k };
  });
  const toMap = (e: { clientX: number; clientY: number }) => {
    const r = svg.current!.getBoundingClientRect();
    return { x: ((e.clientX - r.left) / r.width) * 360, y: ((e.clientY - r.top) / r.height) * 180 };
  };
  const onWheel = (e: WheelEvent) => { const p = toMap(e); zoom(e.deltaY < 0 ? 1.25 : 0.8, p.x, p.y); };
  const onDown = (e: PointerEvent) => { drag.current = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y, w: svg.current!.getBoundingClientRect().width }; (e.target as Element).setPointerCapture?.(e.pointerId); };
  const onMove = (e: PointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const s = 360 / d.w;
    setView((v) => ({ ...v, x: d.vx + (e.clientX - d.x) * s, y: d.vy + (e.clientY - d.y) * s }));
  };
  return (
    <div>
      <ErrorBox error={error} />
      {!data?.length ? <Empty icon={<Sparkles />} title={tr("No places")} text={tr("Photos with a GPS position (most phones add one) appear on the map, by town.")} /> : <>
        <div className="relative mb-4 overflow-hidden rounded-xl border border-line bg-[#cfe3f3] dark:bg-[#0f2233]">
          <svg ref={svg} viewBox="0 0 360 180" className="block h-auto w-full touch-none select-none" onWheel={onWheel} onPointerDown={onDown} onPointerMove={onMove} onPointerUp={() => (drag.current = null)} role="img" aria-label={tr("Map of the photos")}>
            <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
              <path d={LAND} className="fill-[#f4f1e8] stroke-[#b9b3a2] dark:fill-[#2a3340] dark:stroke-[#3d4a5c]" strokeWidth={0.3 / view.k} />
              {data.map((p) => (
                <circle key={p.id} cx={p.lon + 180} cy={90 - p.lat} r={(1.2 + 3 * Math.sqrt(p.count / maxCount)) / Math.sqrt(view.k)}
                  className="cursor-pointer fill-accent/70 stroke-white" strokeWidth={0.3 / view.k}
                  onPointerDown={(e) => e.stopPropagation()} onClick={() => onOpen(p)} onMouseEnter={() => setHover(p)} onMouseLeave={() => setHover(null)}>
                  <title>{placeLabel(p)} ({p.count})</title>
                </circle>
              ))}
            </g>
          </svg>
          <div className="absolute top-2 right-2 flex flex-col gap-1">
            <Button size="sm" icon={<Plus size={14} />} onClick={() => zoom(1.5)} title={tr("Zoom in")} />
            <Button size="sm" icon={<Minus size={14} />} onClick={() => zoom(1 / 1.5)} title={tr("Zoom out")} />
          </div>
          {hover && <div className="absolute bottom-2 left-2 rounded-md bg-black/70 px-2 py-1 text-white text-[12px]">{placeLabel(hover)} · {hover.count}</div>}
        </div>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-3">
          {data.map((p) => (
            <button key={p.id} onClick={() => onOpen(p)} className="group text-left">
              <div className="aspect-[4/3] overflow-hidden rounded-xl bg-panel-2"><img src={thumb(p.cover)} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" /></div>
              <div className="mt-1.5 truncate font-medium text-[13px]">{p.city}</div>
              <div className="truncate text-muted text-[12px]">{[p.region, countryName(p.country)].filter(Boolean).join(", ")} · {p.count}</div>
            </button>
          ))}
        </div>
        <p className="mt-4 text-muted text-[11.5px]">{tr("Towns: GeoNames (CC BY 4.0). Map: Natural Earth.")}</p>
      </>}
    </div>
  );
}

// ---- Things ----

export function ThingsView({ onOpen }: { onOpen: (t: TagView) => void }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["photo-tags"], queryFn: () => api.get<TagView[]>("/api/photos/tags") });
  if (isLoading) return <Spinner />;
  return (
    <div>
      <ErrorBox error={error} />
      {!data?.length ? <Empty icon={<Sparkles />} title={tr("Nothing recognised yet")} text={tr("Things and scenes appear as the photos are analysed.")} /> : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
          {data.map((t) => (
            <button key={t.tag} onClick={() => onOpen(t)} className="group relative aspect-square overflow-hidden rounded-xl bg-panel-2 text-left">
              <img src={thumb(t.cover)} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" />
              <span className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/70 to-transparent px-2.5 pt-6 pb-2 text-white">
                <span className="block truncate font-medium text-[13.5px] first-letter:uppercase">{tagLabel(t.tag)}</span>
                <span className="text-[11.5px] text-white/80">{t.count}</span>
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ---- Memories ----

export type Memory = { year: number; yearsAgo: number; count: number; items: { path: string; date: number; mtime: number; size: number; video?: boolean }[] };

export function Memories({ onOpen }: { onOpen: (m: Memory) => void }) {
  const { data } = useQuery({ queryKey: ["photo-memories", new Date().toDateString()], queryFn: () => api.get<Memory[]>("/api/photos/memories"), staleTime: 3600000 });
  if (!data?.length) return null;
  return (
    <div className="mb-5">
      <h3 className="mb-2 text-[14px] font-semibold">{tr("On this day")}</h3>
      <div className="flex gap-3 overflow-x-auto pb-1">
        {data.map((m) => (
          <button key={m.year} onClick={() => onOpen(m)} className="group relative h-40 w-32 shrink-0 overflow-hidden rounded-xl bg-panel-2 text-left">
            <img src={thumb(m.items[0].path)} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" />
            <span className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/70 to-transparent px-2.5 pt-8 pb-2 text-white">
              <span className="block font-semibold text-[13.5px]">{m.yearsAgo === 1 ? tr("1 year ago") : tr("{0} years ago", m.yearsAgo)}</span>
              <span className="text-[11.5px] text-white/80">{m.year} · {m.count}</span>
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

// ---- Monthly recaps ----

export type Recap = { year: number; month: number; count: number; items: { path: string; date: number; mtime: number; size: number; video?: boolean }[] };

/** One card per past month with a spread of its photos (roadmap 0.15, souvenirs). */
export function Recaps({ onOpen }: { onOpen: (r: Recap) => void }) {
  const { data } = useQuery({ queryKey: ["photo-recaps"], queryFn: () => api.get<Recap[]>("/api/photos/recaps"), staleTime: 3600000 });
  if (!data?.length) return null;
  const fmt = new Intl.DateTimeFormat(locale, { month: "long", year: "numeric" });
  return (
    <div className="mb-5">
      <h3 className="mb-2 text-[14px] font-semibold">{tr("Monthly recaps")}</h3>
      <div className="flex gap-3 overflow-x-auto pb-1">
        {data.map((r) => (
          <button key={`${r.year}-${r.month}`} onClick={() => onOpen(r)} className="group relative h-40 w-32 shrink-0 overflow-hidden rounded-xl bg-panel-2 text-left">
            <img src={thumb(r.items[0].path)} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" />
            <span className="absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/70 to-transparent px-2.5 pt-8 pb-2 text-white">
              <span className="block truncate font-semibold text-[13.5px] first-letter:uppercase">{fmt.format(new Date(r.year, r.month - 1, 1))}</span>
              <span className="text-[11.5px] text-white/80">{tr("{0} photos and videos", r.count)}</span>
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}
