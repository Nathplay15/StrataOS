import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Download, File as FileIcon, Folder, HardDrive, Inbox, Lock, Upload, X } from "lucide-react";
import { createPortal } from "react-dom";
import { api, ApiError, q } from "../lib/api";
import { bytes, dateTime } from "../lib/format";
import { Button, Card, Empty, ErrorBox, Input, Progress, Spinner } from "../components/ui";
import { LangSwitch } from "../components/LangSwitch";
import { srv, tr } from "../lib/i18n";
import logo from "../logo.svg";

type Info = { name: string; dir: boolean; album?: boolean; download: boolean; upload: boolean; password: boolean; unlocked: boolean; owner: string; size?: number; expires?: string; downloadsLeft?: number };
type Entry = { name: string; path: string; dir: boolean; size: number; modTime: string };
type Photo = { path: string; name: string; date: number; size: number; video?: boolean };

const image = (n: string) => /\.(jpe?g|png|gif|webp)$/i.test(n);

function Viewer({ id, photos, index, onIndex, onClose }: { id: string; photos: Photo[]; index: number; onIndex: (i: number) => void; onClose: () => void }) {
  const p = photos[index];
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowRight" && index < photos.length - 1) onIndex(index + 1);
      if (e.key === "ArrowLeft" && index > 0) onIndex(index - 1);
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [index, photos.length, onClose, onIndex]);
  if (!p) return null;
  return createPortal(
    <div className="fixed inset-0 z-[9600] flex flex-col bg-black/95 text-white">
      <div className="flex items-center gap-3 px-4 py-3 text-[13px]">
        <div className="min-w-0 flex-1"><div className="truncate font-medium">{p.name}</div><div className="text-white/60">{dateTime(p.date * 1000)} · {bytes(p.size)}</div></div>
        <a href={`/api/s/${id}/download${q({ path: p.path })}`} className="rounded-lg p-2 hover:bg-white/10" title={tr("Télécharger")}><Download size={18} /></a>
        <button className="rounded-lg p-2 hover:bg-white/10" onClick={onClose} title={tr("Fermer")}><X size={20} /></button>
      </div>
      <div className="relative flex min-h-0 flex-1 items-center justify-center px-14 pb-6">
        <img key={p.path} src={`/api/s/${id}/thumb${q({ path: p.path, size: "l" })}`} alt="" className="max-h-full max-w-full object-contain" />
        {index > 0 && <button className="absolute left-2 rounded-full bg-white/10 p-2 hover:bg-white/20" onClick={() => onIndex(index - 1)}><ChevronLeft size={26} /></button>}
        {index < photos.length - 1 && <button className="absolute right-2 rounded-full bg-white/10 p-2 hover:bg-white/20" onClick={() => onIndex(index + 1)}><ChevronRight size={26} /></button>}
      </div>
    </div>,
    document.body,
  );
}

function DropZone({ id }: { id: string }) {
  const [up, setUp] = useState<{ pct: number } | null>(null);
  const [done, setDone] = useState<string[]>([]);
  const [err, setErr] = useState("");
  const [drag, setDrag] = useState(false);
  const qc = useQueryClient();
  const send = (files: FileList | File[]) => {
    const list = Array.from(files);
    if (!list.length) return;
    const fd = new FormData();
    list.forEach((f) => fd.append("file", f, f.name));
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/s/${id}/upload`);
    xhr.setRequestHeader("X-Strata", "1");
    xhr.upload.onprogress = (e) => e.lengthComputable && setUp({ pct: (e.loaded / e.total) * 100 });
    xhr.onload = () => {
      setUp(null);
      const r = JSON.parse(xhr.responseText || "{}");
      if (xhr.status >= 300) setErr(srv(r.error) || tr("Échec de l'envoi"));
      else { setErr(""); setDone((d) => [...(r.names ?? []), ...d]); qc.invalidateQueries({ queryKey: ["pub-files"] }); }
    };
    xhr.onerror = () => { setUp(null); setErr(tr("Échec de l'envoi")); };
    setUp({ pct: 0 });
    xhr.send(fd);
  };
  return (
    <Card>
      <div onDragOver={(e) => { e.preventDefault(); setDrag(true); }} onDragLeave={() => setDrag(false)} onDrop={(e) => { e.preventDefault(); setDrag(false); send(e.dataTransfer.files); }}
        className={`flex flex-col items-center gap-3 rounded-xl border-2 border-dashed p-8 text-center ${drag ? "border-accent bg-accent-soft" : "border-line"}`}>
        <Inbox size={32} className="text-accent" />
        <div className="font-medium">{tr("Déposez vos fichiers ici")}</div>
        <div className="text-muted text-[13px]">{tr("Ils sont ajoutés au dossier partagé, sans remplacer de fichier existant.")}</div>
        <label className="cursor-pointer"><span className="inline-flex items-center gap-2 rounded-lg bg-accent px-3 py-2 text-[13px] font-medium text-white"><Upload size={15} />{tr("Choisir des fichiers")}</span><input type="file" multiple hidden onChange={(e) => { if (e.target.files) send(e.target.files); e.target.value = ""; }} /></label>
        {up && <div className="w-full max-w-sm"><Progress value={up.pct} /><div className="mt-1 text-muted text-[12px]">{up.pct.toFixed(0)} %</div></div>}
        {err && <div className="text-danger text-[13px]">{err}</div>}
        {done.length > 0 && <div className="text-ok text-[13px]">{tr("{0} fichier(s) reçu(s) :", done.length)} {done.slice(0, 8).join(", ")}{done.length > 8 ? "…" : ""}</div>}
      </div>
    </Card>
  );
}

function FolderView({ id, info }: { id: string; info: Info }) {
  const [path, setPath] = useState("/");
  const { data, isLoading, error } = useQuery({ queryKey: ["pub-files", id, path], queryFn: () => api.get<{ entries: Entry[] }>(`/api/s/${id}/files${q({ path })}`) });
  const crumbs = path === "/" ? [] : path.slice(1).split("/");
  const dl = (p: string, inline = false) => `/api/s/${id}/download${q({ path: p, inline: inline ? 1 : undefined })}`;
  return (
    <Card pad={false}>
      <div className="flex flex-wrap items-center gap-1 border-b border-line px-4 py-2.5 text-[13px]">
        <button onClick={() => setPath("/")} className="flex items-center gap-1.5 rounded-lg px-2 py-1 hover:bg-panel-2"><HardDrive size={14} />{info.name}</button>
        {crumbs.map((c, i) => <span key={i} className="flex items-center gap-1"><ChevronRight size={13} className="text-muted" /><button onClick={() => setPath("/" + crumbs.slice(0, i + 1).join("/"))} className="rounded-lg px-2 py-1 hover:bg-panel-2">{c}</button></span>)}
        <div className="flex-1" />
        <a href={dl(path)}><Button size="sm" icon={<Download size={14} />}>{tr("Tout télécharger (ZIP)")}</Button></a>
      </div>
      <ErrorBox error={error} />
      {isLoading ? <Spinner /> : !data?.entries.length ? <Empty icon={<Folder />} title={tr("Dossier vide")} /> : (
        <table className="table">
          <tbody>
            {data.entries.map((e) => (
              <tr key={e.path}>
                <td>
                  {e.dir ? (
                    <button className="flex items-center gap-2.5 text-left hover:text-accent" onClick={() => setPath("/" + e.path)}><Folder size={16} className="text-accent" />{e.name}</button>
                  ) : (
                    <a className="flex items-center gap-2.5 hover:text-accent" href={dl(e.path, image(e.name))} target={image(e.name) ? "_blank" : undefined} rel="noreferrer"><FileIcon size={16} className="text-muted" />{e.name}</a>
                  )}
                </td>
                <td className="text-muted">{e.dir ? "—" : bytes(e.size)}</td>
                <td className="hidden text-muted text-[12.5px] sm:table-cell">{dateTime(e.modTime)}</td>
                <td className="text-right"><a href={dl(e.path)} title={tr("Télécharger")}><Button size="sm" variant="ghost" icon={<Download size={14} />} /></a></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function AlbumView({ id }: { id: string }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["pub-album", id], queryFn: () => api.get<{ photos: Photo[] }>(`/api/s/${id}/files`) });
  const [open, setOpen] = useState<number | null>(null);
  if (isLoading) return <Spinner />;
  const photos = data?.photos ?? [];
  return (
    <div>
      <ErrorBox error={error} />
      {!photos.length ? <Card><Empty title={tr("Album vide")} /></Card> : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-1">
          {photos.map((p, i) => (
            <button key={p.path} onClick={() => setOpen(i)} className="group relative aspect-square overflow-hidden rounded-md bg-panel-2">
              <img src={`/api/s/${id}/thumb${q({ path: p.path, size: "s" })}`} alt="" loading="lazy" className="h-full w-full object-cover transition group-hover:scale-105" />
            </button>
          ))}
        </div>
      )}
      {open !== null && <Viewer id={id} photos={photos} index={open} onIndex={setOpen} onClose={() => setOpen(null)} />}
    </div>
  );
}

/** Page opened by a share link (/s/<id>), without an account. */
export default function PublicLink({ id }: { id: string }) {
  const qc = useQueryClient();
  const { data: info, isLoading, error } = useQuery({ queryKey: ["pub-info", id], queryFn: () => api.get<Info>(`/api/s/${id}`), retry: false });
  const [pw, setPw] = useState("");
  const [pwErr, setPwErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (info?.name) document.title = info.name; }, [info?.name]);

  const unlock = async () => {
    setBusy(true);
    try {
      await api.post(`/api/s/${id}/unlock`, { password: pw });
      setPwErr(null);
      qc.invalidateQueries();
    } catch (e) {
      setPwErr(e);
    } finally {
      setBusy(false);
    }
  };

  let body;
  if (isLoading) body = <Spinner />;
  else if (error || !info) body = <Card><Empty icon={<X />} title={error instanceof ApiError && error.status === 410 ? tr("Ce lien a expiré ou a été supprimé") : tr("Lien introuvable")} text={tr("Demandez un nouveau lien à la personne qui vous l'a envoyé.")} /></Card>;
  else if (info.password && !info.unlocked) {
    body = (
      <Card>
        <form className="mx-auto max-w-sm space-y-3 py-6 text-center" onSubmit={(e) => { e.preventDefault(); unlock(); }}>
          <Lock size={28} className="mx-auto text-accent" />
          <div className="font-medium">{tr("Ce partage est protégé par un mot de passe")}</div>
          <Input type="password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} placeholder={tr("Mot de passe")} />
          <ErrorBox error={pwErr} />
          <Button variant="primary" type="submit" loading={busy} disabled={!pw} className="w-full justify-center">{tr("Ouvrir")}</Button>
        </form>
      </Card>
    );
  } else {
    body = (
      <div className="space-y-4">
        {info.upload && <DropZone id={id} />}
        {info.download && (info.album ? <AlbumView id={id} /> : info.dir ? <FolderView id={id} info={info} /> : (
          <Card>
            <div className="flex flex-col items-center gap-3 py-6 text-center">
              {image(info.name) ? <img src={`/api/s/${id}/download?inline=1`} alt="" className="max-h-[50vh] rounded-lg" /> : <FileIcon size={40} className="text-accent" />}
              <div className="font-medium break-all">{info.name}</div>
              {info.size !== undefined && <div className="text-muted text-[13px]">{bytes(info.size)}</div>}
              <a href={`/api/s/${id}/download`}><Button variant="primary" icon={<Download size={15} />} disabled={info.downloadsLeft === 0}>{tr("Télécharger")}</Button></a>
            </div>
          </Card>
        ))}
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-bg">
      <header className="flex items-center gap-3 border-b border-line px-5 py-3">
        <img src={logo} alt="" className="h-7 w-7" />
        <div className="min-w-0 flex-1">
          <div className="truncate font-semibold">{info?.name ?? "StrataOS"}</div>
          {info && <div className="text-muted text-[12px]">{tr("Partagé par {0}", info.owner)}{info.expires ? ` · ${tr("jusqu'au {0}", dateTime(info.expires))}` : ""}{info.downloadsLeft !== undefined ? ` · ${tr("{0} téléchargement(s) restant(s)", info.downloadsLeft)}` : ""}</div>}
        </div>
        <LangSwitch className="flex items-center gap-1.5 text-muted text-[12.5px]" />
      </header>
      <main className="mx-auto max-w-5xl p-4 sm:p-6">{body}</main>
    </div>
  );
}
