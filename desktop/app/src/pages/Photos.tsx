// Photos of the NAS: the timeline by month (newest first, loaded by pages),
// the albums, and a viewer with previous/next, video playback and download.
import { useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Download, Image as ImageIcon, Play } from "lucide-react";
import { Button, Card, Empty, ErrorBox, Modal, Spinner, Tabs } from "@nas/components/ui";
import { locale } from "@nas/lib/i18n";
import { nasURL } from "../lib/agent";
import { api, currentAccount, q } from "../lib/nasapi";
import { t } from "../lib/i18n";
import { PageHeader, WebButton, downloadToComputer } from "../components/kit";

type Photo = { path: string; date: number; mtime: number; size: number; video?: boolean };
type Page = { installed: boolean; total: number; items: Photo[]; running?: boolean };
type Album = { id: string; name: string; count: number; cover: string };
const PAGE = 300;

const thumb = (p: Photo | string, size: "s" | "l", mtime = 0) =>
  nasURL(currentAccount(), "/api/photos/thumb", { path: typeof p === "string" ? p : p.path, size, v: typeof p === "string" ? mtime : p.mtime });

export function PhotosPage() {
  const [tab, setTab] = useState<"timeline" | "albums">("timeline");
  const [album, setAlbum] = useState<Album | null>(null);
  return (
    <>
      <PageHeader title={album ? album.name : t("Photos")} actions={<>
        {album && <Button onClick={() => setAlbum(null)}>{t("All albums")}</Button>}
        <WebButton path="/photos" title={t("Photos")} />
      </>} />
      {!album && <Tabs value={tab} onChange={setTab} tabs={[{ id: "timeline", label: t("Timeline") }, { id: "albums", label: t("Albums") }]} />}
      <div className="mt-4">{album ? <Timeline album={album.id} /> : tab === "timeline" ? <Timeline album="" /> : <Albums onOpen={setAlbum} />}</div>
    </>
  );
}

function Timeline({ album }: { album: string }) {
  const [open, setOpen] = useState<number | null>(null);
  const pages = useInfiniteQuery({
    queryKey: ["photos", album],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.get<Page>(`/api/photos${q({ offset: pageParam, limit: PAGE, album })}`),
    getNextPageParam: (last, all) => {
      const n = all.reduce((s, p) => s + p.items.length, 0);
      return n < last.total ? n : undefined;
    },
  });
  const items = useMemo(() => pages.data?.pages.flatMap((p) => p.items) ?? [], [pages.data]);
  // Group by month, in the order of the timeline.
  const months = useMemo(() => {
    const out: { label: string; list: { p: Photo; i: number }[] }[] = [];
    items.forEach((p, i) => {
      const label = new Date(p.date).toLocaleDateString(locale, { month: "long", year: "numeric" });
      if (out[out.length - 1]?.label !== label) out.push({ label, list: [] });
      out[out.length - 1].list.push({ p, i });
    });
    return out;
  }, [items]);
  if (pages.isLoading) return <Spinner />;
  if (pages.error) return <ErrorBox error={pages.error} />;
  if (pages.data?.pages[0]?.installed === false) return <Card><Empty icon={<ImageIcon />} title={t("Photos is not set up on this NAS")} action={<WebButton path="/photos" />} /></Card>;
  if (!items.length) return <Card><Empty icon={<ImageIcon />} title={t("No photos")} text={pages.data?.pages[0]?.running ? t("The NAS is indexing the photos…") : undefined} /></Card>;
  return (
    <>
      {months.map((m) => (
        <section key={m.label} className="mb-5">
          <h2 className="mb-2 text-[13px] font-semibold capitalize">{m.label}</h2>
          <div className="grid grid-cols-[repeat(auto-fill,minmax(130px,1fr))] gap-1.5">
            {m.list.map(({ p, i }) => (
              <button key={p.path} onClick={() => setOpen(i)} className="relative aspect-square overflow-hidden rounded-md bg-panel-2">
                <img src={thumb(p, "s")} alt="" loading="lazy" className="h-full w-full object-cover" />
                {p.video && <Play size={18} className="absolute bottom-1.5 right-1.5 text-white drop-shadow" />}
              </button>
            ))}
          </div>
        </section>
      ))}
      {pages.hasNextPage && <div className="text-center"><Button loading={pages.isFetchingNextPage} onClick={() => void pages.fetchNextPage()}>{t("Show more")}</Button></div>}
      {open !== null && <Viewer items={items} index={open} setIndex={setOpen} onClose={() => setOpen(null)} />}
    </>
  );
}

function Viewer({ items, index, setIndex, onClose }: { items: Photo[]; index: number; setIndex: (i: number) => void; onClose: () => void }) {
  const p = items[index];
  const name = p.path.slice(p.path.lastIndexOf("/") + 1);
  return (
    <Modal open wide="xl" onClose={onClose} title={name} footer={<>
      <span className="mr-auto self-center text-[12px] text-muted">{new Date(p.date).toLocaleString(locale)}</span>
      <Button disabled={index <= 0} onClick={() => setIndex(index - 1)}>←</Button>
      <Button disabled={index >= items.length - 1} onClick={() => setIndex(index + 1)}>→</Button>
      <Button icon={<Download size={14} />} onClick={() => void downloadToComputer(p.path, name, false, p.size)}>{t("Download")}</Button>
    </>}>
      <div className="flex min-h-[300px] items-center justify-center rounded-lg bg-black/80">
        {p.video
          ? <video key={p.path} src={nasURL(currentAccount(), "/api/files/download", { path: p.path, inline: 1 })} poster={thumb(p, "l")} controls autoPlay className="max-h-[70vh] max-w-full" />
          : <img src={thumb(p, "l")} alt={name} className="max-h-[70vh] max-w-full object-contain" />}
      </div>
    </Modal>
  );
}

function Albums({ onOpen }: { onOpen: (a: Album) => void }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["albums"], queryFn: () => api.get<Album[]>("/api/photos/albums") });
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.length) return <Card><Empty icon={<ImageIcon />} title={t("No albums")} action={<WebButton path="/photos" label={t("Create an album")} />} /></Card>;
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3">
      {data.map((a) => (
        <button key={a.id} onClick={() => onOpen(a)} className="card overflow-hidden text-left">
          <div className="aspect-[4/3] bg-panel-2">{a.cover && <img src={thumb(a.cover, "s")} alt="" className="h-full w-full object-cover" />}</div>
          <div className="px-3 py-2"><div className="truncate font-medium">{a.name}</div><div className="text-[12px] text-muted">{t("{0} items", a.count)}</div></div>
        </button>
      ))}
    </div>
  );
}
