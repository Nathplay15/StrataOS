import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Check, Sparkles, ChevronLeft, ChevronRight, Copy, Download, FolderPlus, Images, Link2, Pencil, Play, Plug, Plus, RefreshCcw, Search, Settings2, Trash2, Users, X } from "lucide-react";
import { createPortal } from "react-dom";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Me, Share } from "../lib/types";
import { bytes, dateTime } from "../lib/format";
import { Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, PageHeader, Spinner, useUI } from "../components/ui";
import { ShareLinkModal } from "../components/ShareLinkModal";
import { PwaPhotoUpload } from "../components/PwaPhotoUpload";
import { CastButton } from "../components/Cast";
import { locale, tr } from "../lib/i18n";
import { tagLabel, tagOf } from "../lib/photoTags";
import { Memories, PeopleView, PersonTools, PlacesView, SmartProgress, SmartSettings, ThingsView, placeLabel, useSmart, type Filter, type Memory, type Person } from "./photos/Smart";

type Item = { path: string; date: number; mtime: number; size: number; video?: boolean };
type Album = { id: string; name: string; count: number; cover?: string; created: string; shared: boolean; from?: string; with?: string[] };
type Page = { installed: boolean; total: number; items: Item[]; running: boolean; updated: string; folders?: string[] };

const PAGE = 300;
const viewable = (p: string) => /\.(jpe?g|png|gif|webp)$/i.test(p);
const playable = (p: string) => /\.(mp4|webm|m4v)$/i.test(p);
const thumb = (p: string, size = "s") => `/api/photos/thumb${q({ path: p, size })}`;
const original = (p: string, inline = true) => `/api/files/download${q({ path: p, inline: inline ? 1 : undefined })}`;

function Viewer({ items, index, onIndex, onClose }: { items: Item[]; index: number; onIndex: (i: number) => void; onClose: () => void }) {
  const it = items[index];
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight" && index < items.length - 1) onIndex(index + 1);
      if (e.key === "ArrowLeft" && index > 0) onIndex(index - 1);
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [index, items.length, onClose, onIndex]);
  if (!it) return null;
  const name = it.path.split("/").pop();
  return createPortal(
    <div className="fixed inset-0 z-[9600] flex flex-col bg-black/95 text-white">
      <div className="flex items-center gap-3 px-4 py-3 text-[13px]">
        <div className="min-w-0 flex-1"><div className="truncate font-medium">{name}</div><div className="text-white/60">{dateTime(it.date * 1000)} · {bytes(it.size)}</div></div>
        <CastButton path={it.path} name={name ?? ""} dark />
        <a href={original(it.path, false)} className="rounded-lg p-2 hover:bg-white/10" title={tr("Download")}><Download size={18} /></a>
        <button className="rounded-lg p-2 hover:bg-white/10" onClick={onClose} title={tr("Close")}><X size={20} /></button>
      </div>
      <div className="relative flex min-h-0 flex-1 items-center justify-center px-14 pb-6">
        {it.video ? (
          playable(it.path) ? <video key={it.path} src={original(it.path)} controls autoPlay className="max-h-full max-w-full" /> : <div className="text-center"><img src={thumb(it.path, "l")} alt="" className="max-h-[70vh]" /><div className="mt-3 text-white/70 text-[13px]">{tr("Video format not playable in the browser: download it.")}</div></div>
        ) : (
          <img key={it.path} src={viewable(it.path) ? original(it.path) : thumb(it.path, "l")} alt="" className="max-h-full max-w-full object-contain" />
        )}
        {index > 0 && <button className="absolute left-2 rounded-full bg-white/10 p-2 hover:bg-white/20" onClick={() => onIndex(index - 1)}><ChevronLeft size={26} /></button>}
        {index < items.length - 1 && <button className="absolute right-2 rounded-full bg-white/10 p-2 hover:bg-white/20" onClick={() => onIndex(index + 1)}><ChevronRight size={26} /></button>}
      </div>
    </div>,
    document.body,
  );
}

function FoldersModal({ current, onClose }: { current: string[]; onClose: () => void }) {
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => api.get<Share[]>("/api/shares") });
  const [sel, setSel] = useState<string[]>(current);
  const save = useAction(() => api.put("/api/photos/settings", { folders: sel }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Photo folders")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save and index")}</Button></>}>
      <div className="space-y-3">
        <p className="text-muted text-[13px]">{tr("Photos and videos in these shared folders appear in Photos. Each user only sees those from shares they can access.")}</p>
        {shares.data?.map((s) => <Checkbox key={s.name} checked={sel.includes(s.path)} onChange={(v) => setSel(v ? [...sel, s.path] : sel.filter((x) => x !== s.path))} label={<span>{s.name} <span className="mono text-muted text-[12px]">{s.path}</span></span>} />)}
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function AddToAlbum({ items, onClose }: { items: string[]; onClose: () => void }) {
  const albums = useQuery({ queryKey: ["albums"], queryFn: () => api.get<Album[]>("/api/photos/albums") });
  const [target, setTarget] = useState<string>("new");
  const [name, setName] = useState("");
  const save = useAction(() => (target === "new" ? api.post("/api/photos/albums", { name, items }) : api.put(`/api/photos/albums/${target}`, { add: items })), { success: tr("{0} photo(s) added to the album", items.length), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Add to album")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={save.isPending} disabled={target === "new" && !name.trim()} onClick={() => save.mutate()}>{tr("Add")}</Button></>}>
      <div className="space-y-2">
        {albums.data?.map((a) => (
          <label key={a.id} className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 hover:bg-panel-2">
            <input type="radio" checked={target === a.id} onChange={() => setTarget(a.id)} />
            <span className="flex-1">{a.name}</span><span className="text-muted text-[12px]">{a.count}</span>
          </label>
        ))}
        <label className="flex cursor-pointer items-center gap-2.5 rounded-lg px-2 py-1.5 hover:bg-panel-2">
          <input type="radio" checked={target === "new"} onChange={() => setTarget("new")} />
          <span>{tr("New album")}</span>
        </label>
        {target === "new" && <Input autoFocus placeholder={tr("Album name")} value={name} onChange={(e) => setName(e.target.value)} />}
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

function Grid({ groups, selected, onToggle, onOpen }: { groups: { label: string; start: number; items: Item[] }[]; selected: string[] | null; onToggle: (p: string) => void; onOpen: (i: number) => void }) {
  return (
    <div className="space-y-6">
      {groups.map((g) => (
        <section key={g.label + g.start}>
          <h3 className="sticky top-0 z-10 -mx-1 mb-2 bg-bg/90 px-1 py-1.5 text-[14px] font-semibold capitalize backdrop-blur">{g.label}</h3>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(120px,1fr))] gap-1">
            {g.items.map((it, k) => {
              const on = selected?.includes(it.path);
              return (
                <button key={it.path} onClick={() => (selected ? onToggle(it.path) : onOpen(g.start + k))} className={`group relative aspect-square overflow-hidden rounded-md bg-panel-2 ${on ? "ring-3 ring-accent" : ""}`}>
                  <img src={thumb(it.path)} alt="" loading="lazy" className={`h-full w-full object-cover transition ${on ? "scale-90" : "group-hover:scale-105"}`} />
                  {it.video && <span className="absolute right-1.5 bottom-1.5 rounded-full bg-black/60 p-1 text-white"><Play size={12} /></span>}
                  {selected && <span className={`absolute top-1.5 left-1.5 flex h-5 w-5 items-center justify-center rounded-full border-2 ${on ? "border-accent bg-accent text-white" : "border-white/80 bg-black/30"}`}>{on && <Check size={12} />}</span>}
                </button>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
}

/** The photos visually similar to each other (perceptual hash), group by
 * group: select the copies to move to the trash. */
function DuplicatesView() {
  const { confirm } = useUI();
  const [selected, setSelected] = useState<string[]>([]);
  const [open, setOpen] = useState<number | null>(null);
  const dupes = useQuery({ queryKey: ["photos-duplicates"], queryFn: () => api.get<{ installed: boolean; groups: Item[][] }>("/api/photos/duplicates") });
  const trash = useAction((paths: string[]) => api.post("/api/files/trash", { paths }), { success: tr("Moved to the trash"), onDone: () => setSelected([]) });
  if (dupes.isLoading) return <Spinner />;
  const groups = dupes.data?.groups ?? [];
  const flat = groups.flat();
  let start = 0;
  const gridGroups = groups.map((g) => {
    const out = { label: tr("{0} similar photos", g.length), start, items: g };
    start += g.length;
    return out;
  });
  return (
    <div>
      {groups.length > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 text-[13px]">
          <span>{tr("{0} group(s) of similar photos", groups.length)}</span>
          <div className="flex-1" />
          {selected.length > 0 && <span>{tr("{0} selected", selected.length)}</span>}
          <Button size="sm" variant="ghost" onClick={() => setSelected(groups.flatMap((g) => g.slice(1).map((i) => i.path)))}>{tr("Select the older copies")}</Button>
          <Button size="sm" variant="danger" icon={<Trash2 size={14} />} disabled={!selected.length} loading={trash.isPending} onClick={async () => { if (await confirm({ title: tr("Move to the trash?"), text: tr("{0} photo(s) will be moved to the trash of their share.", selected.length), danger: true })) trash.mutate(selected); }}>{tr("Move to the trash")}</Button>
        </div>
      )}
      {groups.length === 0 && (
        <Card><Empty icon={<Copy />} title={dupes.data?.installed ? tr("No duplicates") : tr("Thumbnail tools are not installed")} text={dupes.data?.installed ? tr("Similar photos (bursts, resized copies…) are detected during indexing; reindex to analyze recent photos.") : tr("libvips (with the iPhone HEIC format) and ffmpeg for videos.")} /></Card>
      )}
      {groups.length > 0 && <Grid groups={gridGroups} selected={selected} onToggle={(p) => setSelected((s) => (s.includes(p) ? s.filter((x) => x !== p) : [...s, p]))} onOpen={(i) => setOpen(i)} />}
      {open !== null && <Viewer items={flat} index={open} onIndex={setOpen} onClose={() => setOpen(null)} />}
    </div>
  );
}

function AlbumsView({ onOpen }: { onOpen: (a: Album) => void }) {
  const { data, isLoading } = useQuery({ queryKey: ["albums"], queryFn: () => api.get<Album[]>("/api/photos/albums") });
  const [name, setName] = useState<string | null>(null);
  const [sharing, setSharing] = useState<Album | null>(null);
  const create = useAction(() => api.post("/api/photos/albums", { name, items: [] }), { onDone: () => setName(null) });
  if (isLoading) return <Spinner />;
  return (
    <div>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-4">
        {data?.map((a) => (
          <button key={a.id} onClick={() => onOpen(a)} className="group text-left">
            <div className="relative aspect-square overflow-hidden rounded-xl bg-panel-2">
              {a.cover ? <img src={thumb(a.cover)} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" /> : <div className="flex h-full items-center justify-center text-muted"><Images size={36} /></div>}
              {a.shared && <span className="absolute top-2 right-2 rounded-full bg-black/60 p-1.5 text-white" title={tr("Shared by link")}><Link2 size={13} /></span>}
              {a.from && <span className="absolute top-2 left-2 rounded-full bg-black/60 px-2 py-1 text-[11px] text-white" title={tr("Shared by {0}", a.from)}><Users size={11} className="mr-1 inline" />{a.from}</span>}
              {!a.from && !!a.with?.length && <span className="absolute top-2 left-2 rounded-full bg-black/60 p-1.5 text-white" title={tr("Shared with {0}", a.with.length)}><Users size={13} /></span>}
              {!a.from && <span role="button" tabIndex={0} className="absolute right-2 bottom-2 rounded-full bg-black/60 p-1.5 text-white opacity-0 transition group-hover:opacity-100 focus:opacity-100" title={tr("Share album")} onClick={(e) => { e.stopPropagation(); setSharing(a); }} onKeyDown={(e) => { if (e.key === "Enter") { e.stopPropagation(); setSharing(a); } }}><Users size={13} /></span>}
            </div>
            <div className="mt-2 truncate font-medium text-[13.5px]">{a.name}</div>
            <div className="text-muted text-[12px]">{tr("{0} item(s)", a.count)}</div>
          </button>
        ))}
        <button onClick={() => setName("")} className="flex aspect-square flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-line text-muted hover:border-accent hover:text-accent">
          <Plus size={28} /><span className="text-[13px]">{tr("New album")}</span>
        </button>
      </div>
      <Modal open={name !== null} onClose={() => setName(null)} title={tr("New album")} footer={<><Button onClick={() => setName(null)}>{tr("Cancel")}</Button><Button variant="primary" disabled={!name?.trim()} loading={create.isPending} onClick={() => create.mutate()}>{tr("Create")}</Button></>}>
        <Field label={tr("Album name")}><Input autoFocus value={name ?? ""} onChange={(e) => setName(e.target.value)} /></Field>
        <p className="mt-3 text-muted text-[12.5px]">{tr("Then add photos from the timeline with \"Select\".")}</p>
        <ErrorBox error={create.error} />
      </Modal>
      {sharing && <ShareAlbumModal album={sharing} onClose={() => setSharing(null)} />}
    </div>
  );
}

// Sharing an album with other accounts: they see its photos in their Photos
// page, read-only, without the rest of the library.
function ShareAlbumModal({ album, onClose }: { album: Album; onClose: () => void }) {
  const [names, setNames] = useState<string[]>(album.with ?? []);
  const [text, setText] = useState("");
  const save = useAction(() => api.put(`/api/photos/albums/${album.id}/share`, { with: names }), { success: tr("Saved"), onDone: onClose });
  const add = () => {
    const n = text.trim();
    if (!n || names.includes(n)) return setText("");
    setNames([...names, n]);
    setText("");
  };
  return (
    <Modal open onClose={onClose} title={tr("Share album")} footer={<><Button onClick={onClose}>{tr("Cancel")}</Button><Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button></>}>
      <p className="mb-3 text-muted text-[12.5px]">{tr("Accounts you share it with see this album in their Photos, read-only.")}</p>
      {names.length > 0 && <div className="mb-3 flex flex-wrap gap-2">{names.map((n) => <span key={n} className="flex items-center gap-1.5 rounded-full bg-panel-2 px-2.5 py-1 text-[12.5px]">{n}<button onClick={() => setNames(names.filter((x) => x !== n))} title={tr("Remove access")} className="text-muted hover:text-danger"><X size={13} /></button></span>)}</div>}
      <Field label={tr("Add account")}><div className="flex gap-2"><Input value={text} onChange={(e) => setText(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }} placeholder="marie" /><Button onClick={add}>{tr("Add")}</Button></div></Field>
      <ErrorBox error={save.error} />
    </Modal>
  );
}

export default function Photos({ me }: { me: Me }) {
  const admin = me.role === "admin";
  const { confirm } = useUI();
  const [tab, setTab] = useState<"all" | "albums" | "dupes" | "people" | "places" | "things">("all");
  const [album, setAlbum] = useState<Album | null>(null);
  // A person, a thing or a place chosen in the smart views; a memory.
  const [filter, setFilter] = useState<Filter | null>(null);
  const [person, setPerson] = useState<Person | null>(null);
  const [memory, setMemory] = useState<Memory | null>(null);
  const smart = useSmart();
  const [smartSettings, setSmartSettings] = useState(false);
  const smartSearch = !!smart.data?.objects && !album && !filter;
  const [search, setSearch] = useState("");
  const [qText, setQText] = useState("");
  useEffect(() => { const t = setTimeout(() => setQText(search.trim()), 350); return () => clearTimeout(t); }, [search]);
  const [limit, setLimit] = useState(PAGE);
  const showGrid = tab === "all" || !!album || !!filter || !!memory;
  const searching = smartSearch && !!qText && tab === "all" && !memory;
  const fq = filter ? { [filter.kind]: filter.id } : {};
  const timeline = useQuery({
    queryKey: ["photos", limit, album?.id ?? "", album || filter ? "" : qText, filter?.kind ?? "", filter?.id ?? ""],
    queryFn: () => api.get<Page>(`/api/photos${q({ offset: 0, limit, album: album?.id, q: album || filter ? undefined : qText, ...fq })}`),
    refetchInterval: (d) => (d.state.data?.running ? 4000 : 60000),
    enabled: showGrid && !searching && !memory,
  });
  // Search by description, tags and names (smart features on): best first.
  const found = useQuery({
    queryKey: ["photo-search", qText],
    queryFn: () => api.get<{ items: Item[] }>(`/api/photos/search${q({ q: tagOf(qText) ?? qText })}`),
    enabled: searching,
  });
  const data: Page | undefined = memory
    ? { installed: true, total: memory.items.length, items: memory.items, running: false, updated: "" }
    : searching
      ? found.data && { installed: true, total: found.data.items.length, items: found.data.items, running: false, updated: "", folders: timeline.data?.folders }
      : timeline.data;
  const isLoading = memory ? false : searching ? found.isLoading : timeline.isLoading;
  const error = searching ? found.error : timeline.error;
  const install = useAction(() => api.post("/api/system/components/photos/install"));
  const reindex = useAction(() => api.post("/api/photos/reindex"));
  const [folders, setFolders] = useState(false);
  const [open, setOpen] = useState<number | null>(null);
  const [selected, setSelected] = useState<string[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [sharing, setSharing] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const removeItems = useAction((items: string[]) => api.put(`/api/photos/albums/${album!.id}`, { remove: items }), { onDone: () => setSelected(null) });
  const rename = useAction(() => api.put(`/api/photos/albums/${album!.id}`, { name: renaming }), { onDone: () => { setAlbum({ ...album!, name: renaming!.trim() }); setRenaming(null); } });
  const delAlbum = useAction(() => api.del(`/api/photos/albums/${album!.id}`), { success: tr("Album deleted"), onDone: () => setAlbum(null) });
  const sentinel = useRef<HTMLDivElement>(null);
  const items = useMemo(() => data?.items ?? [], [data]);
  const more = data && items.length < data.total;

  useEffect(() => { setSelected(null); setLimit(PAGE); }, [tab, album?.id, qText, filter?.id, memory?.year]);
  const back = () => { setFilter(null); setPerson(null); setMemory(null); };
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !more) return;
    const o = new IntersectionObserver((e) => { if (e[0].isIntersecting) setLimit((l) => l + PAGE); }, { rootMargin: "600px" });
    o.observe(el);
    return () => o.disconnect();
  }, [more, items.length]);

  const groups = useMemo(() => {
    if (searching) return items.length ? [{ label: tr("Best matches"), start: 0, items }] : [];
    const fmt = new Intl.DateTimeFormat(locale, { month: "long", year: "numeric" });
    const out: { label: string; start: number; items: Item[] }[] = [];
    items.forEach((it, i) => {
      const label = fmt.format(new Date(it.date * 1000));
      if (!out.length || out[out.length - 1].label !== label) out.push({ label, start: i, items: [] });
      out[out.length - 1].items.push(it);
    });
    return out;
  }, [items, searching]);
  const onIndex = useCallback((i: number) => setOpen(i), []);
  const toggle = (p: string) => setSelected((s) => (s ? (s.includes(p) ? s.filter((x) => x !== p) : [...s, p]) : [p]));

  const title = album ? album.name : memory ? (memory.yearsAgo === 1 ? tr("1 year ago") : tr("{0} years ago", memory.yearsAgo)) : filter ? filter.label : tr("Photos");
  const header = <PageHeader title={title} subtitle={data && showGrid ? tr("{0} photos and videos", data.total) : undefined} icon={<Images size={20} />} actions={<>
    {album && <>
      <Button size="sm" icon={<ArrowLeft size={14} />} onClick={() => setAlbum(null)}>{tr("Albums")}</Button>
      <Button size="sm" icon={<Link2 size={14} />} onClick={() => setSharing(true)}>{tr("Share")}</Button>
      <Button size="sm" icon={<Pencil size={14} />} onClick={() => setRenaming(album.name)} title={tr("Rename")} />
      <Button size="sm" icon={<Trash2 size={14} />} title={tr("Delete album")} onClick={async () => { if (await confirm({ title: tr("Delete album?"), text: tr("Photos stay in their folders; only the album and its share links are deleted."), danger: true })) delAlbum.mutate(); }} />
    </>}
    {(filter || memory) && <>
      <Button size="sm" icon={<ArrowLeft size={14} />} onClick={back}>{tr("Back")}</Button>
      {person && <PersonTools person={person} onGone={() => { back(); }} />}
    </>}
    {!album && !filter && !memory && admin && (data || tab !== "all") && <Button size="sm" icon={<Sparkles size={14} />} onClick={() => setSmartSettings(true)}>{tr("Smart features")}</Button>}
    {!album && !filter && !memory && admin && data && tab === "all" && <>
      <Button size="sm" icon={<RefreshCcw size={14} />} loading={reindex.isPending || data.running} onClick={() => reindex.mutate()}>{data.running ? tr("Indexing…") : tr("Reindex")}</Button>
      <Button size="sm" icon={<Settings2 size={14} />} onClick={() => setFolders(true)}>{tr("Folders")}</Button>
    </>}
  </>} />;

  const tabList = (["all", "albums", ...(smart.data?.faces ? ["people" as const] : []), "places", ...(smart.data?.objects ? ["things" as const] : []), "dupes"] as const);
  const tabLabel = { all: tr("Timeline"), albums: tr("Albums"), people: tr("People"), places: tr("Places"), things: tr("Things"), dupes: tr("Duplicates") };
  const tabs = !album && !filter && !memory && (
    <div className="mb-4 flex flex-wrap items-center gap-2">
      <div className="flex flex-wrap rounded-lg border border-line p-0.5 text-[13px]">
        {tabList.map((t) => <button key={t} onClick={() => setTab(t)} className={`rounded-md px-3 py-1 ${tab === t ? "bg-panel-2 font-medium" : "text-muted"}`}>{tabLabel[t]}</button>)}
      </div>
      {tab === "all" && <div className="relative min-w-[180px] max-w-xs flex-1"><Search size={14} className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-muted" /><Input className="!pl-8" placeholder={smartSearch ? tr("Search: dog, beach, car, a folder…") : tr("Search for a folder or file")} value={search} onChange={(e) => setSearch(e.target.value)} /></div>}
    </div>
  );

  const selectBar = showGrid && data && data.total > 0 && (
    <div className="mb-3 flex flex-wrap items-center gap-2 text-[13px]">
      {selected ? <>
        <span>{tr("{0} selected", selected.length)}</span>
        <Button size="sm" variant="ghost" onClick={() => setSelected(items.map((i) => i.path))}>{tr("Select all")}</Button>
        <div className="flex-1" />
        {album ? <Button size="sm" icon={<X size={14} />} disabled={!selected.length} loading={removeItems.isPending} onClick={() => removeItems.mutate(selected)}>{tr("Remove from album")}</Button>
          : <Button size="sm" variant="primary" icon={<FolderPlus size={14} />} disabled={!selected.length} onClick={() => setAdding(true)}>{tr("Add to album")}</Button>}
        <Button size="sm" onClick={() => setSelected(null)}>{tr("Cancel")}</Button>
      </> : <><div className="flex-1" /><Button size="sm" icon={<Check size={14} />} onClick={() => setSelected([])}>{tr("Select")}</Button></>}
    </div>
  );

  const smartViews = !album && !filter && !memory && <>
    {tab === "people" && <PeopleView onOpen={(p) => { setPerson(p); setFilter({ kind: "person", id: String(p.id), label: p.name || tr("Unnamed person") }); }} />}
    {tab === "places" && <PlacesView onOpen={(p) => setFilter({ kind: "place", id: String(p.id), label: placeLabel(p) })} />}
    {tab === "things" && <ThingsView onOpen={(t) => setFilter({ kind: "tag", id: t.tag, label: tagLabel(t.tag) })} />}
  </>;
  if (!showGrid) return <div>{header}{tabs}{smart.data && <SmartProgress s={smart.data} />}{smartViews}{smartSettings && <SmartSettings onClose={() => setSmartSettings(false)} />}</div>;
  if (showGrid && (isLoading || !data)) return <div>{header}{tabs}<ErrorBox error={error} /><Spinner /></div>;
  if (showGrid && data && !data.installed && admin) return <div>{header}<Card><Empty icon={<Plug />} title={tr("Thumbnail tools are not installed")} text={tr("libvips (with the iPhone HEIC format) and ffmpeg for videos.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} /></Card></div>;

  return (
    <div>
      {header}
      {tabs}
      {tab === "all" && !album && !filter && !memory && !qText && <PwaPhotoUpload />}
      <ErrorBox error={error} />
      {tab === "albums" && !album && !filter && <AlbumsView onOpen={(a) => setAlbum(a)} />}
      {tab === "all" && !album && !filter && !memory && !qText && <>{smart.data && <SmartProgress s={smart.data} />}<Memories onOpen={setMemory} /></>}
      {tab === "dupes" && !album && !filter && <DuplicatesView />}
      {showGrid && data && <>
        {!album && admin && data.folders?.length === 0 && <Card><Empty icon={<Images />} title={tr("No photo folders")} text={tr("Choose the shared folders that contain your photos.")} action={<Button variant="primary" onClick={() => setFolders(true)}>{tr("Choose folders")}</Button>} /></Card>}
        {data.total === 0 && !(admin && data.folders?.length === 0 && !album) && <Card><Empty icon={<Images />} title={album ? tr("Empty album") : qText ? tr("No results") : data.running ? tr("Indexing…") : tr("No photos")} text={album ? tr("Add photos from the timeline with \"Select\".") : qText ? undefined : tr("Drop photos into a photo folder (Files, SMB, WebDAV…); they appear here after indexing.")} /></Card>}
        {selectBar}
        {data.total > 0 && <Grid groups={groups} selected={selected} onToggle={toggle} onOpen={setOpen} />}
        {more && <div ref={sentinel} className="py-6"><Spinner /></div>}
        {!admin && !album && data.total > 0 && <div className="mt-6"><Notice>{tr("To back up phone photos automatically, use a sync app (PhotoSync, FolderSync…) to WebDAV or SFTP, into a photo folder.")}</Notice></div>}
      </>}
      {open !== null && <Viewer items={items} index={open} onIndex={onIndex} onClose={() => setOpen(null)} />}
      {smartSettings && <SmartSettings onClose={() => setSmartSettings(false)} />}
      {folders && <FoldersModal current={data?.folders ?? []} onClose={() => setFolders(false)} />}
      {adding && selected && <AddToAlbum items={selected} onClose={() => { setAdding(false); setSelected(null); }} />}
      {sharing && album && <ShareLinkModal album={album.id} name={album.name} onClose={() => setSharing(false)} />}
      <Modal open={renaming !== null} onClose={() => setRenaming(null)} title={tr("Rename album")} footer={<><Button onClick={() => setRenaming(null)}>{tr("Cancel")}</Button><Button variant="primary" disabled={!renaming?.trim()} loading={rename.isPending} onClick={() => rename.mutate()}>{tr("Rename")}</Button></>}>
        <Input autoFocus value={renaming ?? ""} onChange={(e) => setRenaming(e.target.value)} />
        <ErrorBox error={rename.error} />
      </Modal>
    </div>
  );
}
