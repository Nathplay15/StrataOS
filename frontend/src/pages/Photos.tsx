import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Download, Images, Play, Plug, RefreshCcw, Settings2, X } from "lucide-react";
import { createPortal } from "react-dom";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Me, Share } from "../lib/types";
import { bytes, dateTime } from "../lib/format";
import { Button, Card, Checkbox, Empty, ErrorBox, Modal, Notice, PageHeader, Spinner } from "../components/ui";
import { locale, tr } from "../lib/i18n";

type Item = { path: string; date: number; mtime: number; size: number; video?: boolean };
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
        <a href={original(it.path, false)} className="rounded-lg p-2 hover:bg-white/10" title={tr("Télécharger")}><Download size={18} /></a>
        <button className="rounded-lg p-2 hover:bg-white/10" onClick={onClose} title={tr("Fermer")}><X size={20} /></button>
      </div>
      <div className="relative flex min-h-0 flex-1 items-center justify-center px-14 pb-6">
        {it.video ? (
          playable(it.path) ? <video key={it.path} src={original(it.path)} controls autoPlay className="max-h-full max-w-full" /> : <div className="text-center"><img src={thumb(it.path, "l")} alt="" className="max-h-[70vh]" /><div className="mt-3 text-white/70 text-[13px]">{tr("Format vidéo non lisible dans le navigateur : téléchargez-la.")}</div></div>
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
    <Modal open onClose={onClose} title={tr("Dossiers de photos")} footer={<><Button onClick={onClose}>{tr("Annuler")}</Button><Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Enregistrer et indexer")}</Button></>}>
      <div className="space-y-3">
        <p className="text-muted text-[13px]">{tr("Les photos et vidéos de ces dossiers partagés apparaissent dans Photos. Chaque utilisateur ne voit que celles des partages auxquels il a accès.")}</p>
        {shares.data?.map((s) => <Checkbox key={s.name} checked={sel.includes(s.path)} onChange={(v) => setSel(v ? [...sel, s.path] : sel.filter((x) => x !== s.path))} label={<span>{s.name} <span className="mono text-muted text-[12px]">{s.path}</span></span>} />)}
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  );
}

export default function Photos({ me }: { me: Me }) {
  const admin = me.role === "admin";
  const [limit, setLimit] = useState(PAGE);
  const { data, isLoading, error } = useQuery({ queryKey: ["photos", limit], queryFn: () => api.get<Page>(`/api/photos${q({ offset: 0, limit })}`), refetchInterval: (d) => (d.state.data?.running ? 4000 : 60000) });
  const install = useAction(() => api.post("/api/system/components/photos/install"));
  const reindex = useAction(() => api.post("/api/photos/reindex"));
  const [folders, setFolders] = useState(false);
  const [open, setOpen] = useState<number | null>(null);
  const sentinel = useRef<HTMLDivElement>(null);
  const items = useMemo(() => data?.items ?? [], [data]);
  const more = data && items.length < data.total;

  useEffect(() => {
    const el = sentinel.current;
    if (!el || !more) return;
    const o = new IntersectionObserver((e) => { if (e[0].isIntersecting) setLimit((l) => l + PAGE); }, { rootMargin: "600px" });
    o.observe(el);
    return () => o.disconnect();
  }, [more, items.length]);

  const groups = useMemo(() => {
    const fmt = new Intl.DateTimeFormat(locale, { month: "long", year: "numeric" });
    const out: { label: string; start: number; items: Item[] }[] = [];
    items.forEach((it, i) => {
      const label = fmt.format(new Date(it.date * 1000));
      if (!out.length || out[out.length - 1].label !== label) out.push({ label, start: i, items: [] });
      out[out.length - 1].items.push(it);
    });
    return out;
  }, [items]);
  const onIndex = useCallback((i: number) => setOpen(i), []);

  const header = <PageHeader title={tr("Photos")} subtitle={data ? tr("{0} photos et vidéos", data.total) : undefined} icon={<Images size={20} />} actions={admin && data && <>
    <Button size="sm" icon={<RefreshCcw size={14} />} loading={reindex.isPending || data.running} onClick={() => reindex.mutate()}>{data.running ? tr("Indexation…") : tr("Réindexer")}</Button>
    <Button size="sm" icon={<Settings2 size={14} />} onClick={() => setFolders(true)}>{tr("Dossiers")}</Button>
  </>} />;

  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  if (!data.installed && admin) return <div>{header}<Card><Empty icon={<Plug />} title={tr("Les outils de vignettes ne sont pas installés")} text={tr("libvips (avec le format HEIC des iPhone) et ffmpeg pour les vidéos.")} action={<Button variant="primary" loading={install.isPending} onClick={() => install.mutate()}>{tr("Installer")}</Button>} /></Card></div>;

  return (
    <div>
      {header}
      {admin && data.folders?.length === 0 && <Card><Empty icon={<Images />} title={tr("Aucun dossier de photos")} text={tr("Choisissez les dossiers partagés qui contiennent vos photos.")} action={<Button variant="primary" onClick={() => setFolders(true)}>{tr("Choisir les dossiers")}</Button>} /></Card>}
      {data.total === 0 && !(admin && data.folders?.length === 0) && <Card><Empty icon={<Images />} title={data.running ? tr("Indexation en cours…") : tr("Aucune photo")} text={tr("Déposez des photos dans un dossier de photos (Fichiers, SMB, WebDAV…) ; elles apparaissent ici après l'indexation.")} /></Card>}
      {data.total > 0 && (
        <div className="space-y-6">
          {groups.map((g) => (
            <section key={g.label + g.start}>
              <h3 className="sticky top-0 z-10 -mx-1 mb-2 bg-bg/90 px-1 py-1.5 text-[14px] font-semibold capitalize backdrop-blur">{g.label}</h3>
              <div className="grid grid-cols-[repeat(auto-fill,minmax(120px,1fr))] gap-1">
                {g.items.map((it, k) => (
                  <button key={it.path} onClick={() => setOpen(g.start + k)} className="group relative aspect-square overflow-hidden rounded-md bg-panel-2">
                    <img src={thumb(it.path)} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" />
                    {it.video && <span className="absolute right-1.5 bottom-1.5 rounded-full bg-black/60 p-1 text-white"><Play size={12} /></span>}
                  </button>
                ))}
              </div>
            </section>
          ))}
          {more && <div ref={sentinel} className="py-6"><Spinner /></div>}
        </div>
      )}
      {!admin && data.total > 0 && <div className="mt-6"><Notice>{tr("Pour sauvegarder automatiquement les photos du téléphone, utilisez une application de synchronisation (PhotoSync, FolderSync…) vers WebDAV ou SFTP, dans un dossier de photos.")}</Notice></div>}
      {open !== null && <Viewer items={items} index={open} onIndex={onIndex} onClose={() => setOpen(null)} />}
      {folders && <FoldersModal current={data.folders ?? []} onClose={() => setFolders(false)} />}
    </div>
  );
}
