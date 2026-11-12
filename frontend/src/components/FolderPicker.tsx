import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Folder, FolderOpen, FolderPlus, HardDrive, Share2 } from "lucide-react";
import { api, q } from "../lib/api";
import type { Me } from "../lib/types";
import { bytes, cx } from "../lib/format";
import { newShareDefaults } from "../lib/shares";
import { Button, ErrorBox, Field, Input, Modal, Select, Spinner, useUI } from "./ui";
import { tr, srv } from "../lib/i18n";

const ROOT = "/srv/strata";

type Entry = { name: string; path: string; dir: boolean; link?: boolean };
type Listing = { path: string; entries: Entry[] };
type Location = { path: string; label: string; free: number };
type Sources = { folders: { path: string; label: string }[] };

/** A folder name typed by the administrator: one path component. */
export function validFolderName(n: string): string | null {
  const v = n.trim();
  if (!v) return tr("Enter a name");
  if (v.includes("/") || v === "." || v === "..") return tr("A folder name cannot contain / or be . or ..");
  if (v.length > 255) return tr("Name too long");
  return null;
}

const parentOf = (p: string) => (p.lastIndexOf("/") > ROOT.length ? p.slice(0, p.lastIndexOf("/")) : ROOT);

/**
 * A folder of the NAS storage (/srv/strata/...): a path field, quick choices
 * (volumes, shares) that append suffix, and a browser where a new folder or a
 * new shared folder can be created without leaving the form.
 */
export function FolderPicker({ value, onChange, quick, quickLabel, suffix, allowShare = true, placeholder, label }: {
  value: string;
  onChange: (v: string) => void;
  /** Quick choices; suffix is appended to the chosen one (e.g. "backups"). */
  quick?: { path: string; label: string }[];
  suffix?: string;
  /** First entry of the quick choices (default "Volume…"). */
  quickLabel?: string;
  /** Offer "New shared folder" in the browser (not for Time Machine, which creates its own share). */
  allowShare?: boolean;
  placeholder?: string;
  /** Accessible name of the path field. */
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-wrap gap-2 sm:flex-nowrap">
      {quick && quick.length > 0 && (
        <Select aria-label={tr("Quick choice")} className="sm:!w-56 shrink-0" value="" onChange={(e) => e.target.value && onChange(suffix ? `${e.target.value}/${suffix}` : e.target.value)}>
          <option value="">{quickLabel ?? tr("Volume…")}</option>
          {quick.map((f) => <option key={f.path} value={f.path}>{srv(f.label)}</option>)}
        </Select>
      )}
      <Input aria-label={label ?? tr("Folder")} className="mono min-w-0 flex-1" value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
      <Button type="button" icon={<FolderOpen size={15} />} onClick={() => setOpen(true)}>{tr("Browse…")}</Button>
      {open && <FolderBrowser start={value} allowShare={allowShare} onPick={(p) => { onChange(p); setOpen(false); }} onClose={() => setOpen(false)} />}
    </div>
  );
}

export function FolderBrowser({ start, allowShare = true, onPick, onClose }: { start: string; allowShare?: boolean; onPick: (p: string) => void; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [path, setPath] = useState(() => (start.startsWith(ROOT + "/") ? start.replace(/\/+$/, "") : ""));
  const [mode, setMode] = useState<"" | "folder" | "share">("");
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [base, setBase] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const list = useQuery({ queryKey: ["folder-picker", path], queryFn: () => api.get<Listing>(`/api/files${q({ path })}`), enabled: path !== "", retry: false });
  const locations = useQuery({ queryKey: ["locations"], queryFn: () => api.get<Location[]>("/api/storage/locations") });
  const sources = useQuery({ queryKey: ["backup-sources"], queryFn: () => api.get<Sources>("/api/backups/sources") });
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me") });

  // A typed path that does not exist yet: start from its nearest existing parent.
  useEffect(() => {
    if (list.isError && path && path !== ROOT) setPath(parentOf(path));
  }, [list.isError, path]);
  // The only volume is preselected for a new share, as on the Shares page.
  useEffect(() => {
    if (!base && locations.data?.length === 1) setBase(locations.data[0].path);
  }, [base, locations.data]);
  // Escape closes this dialog only, not the form below it.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      if (mode) setMode("");
      else onClose();
    };
    window.addEventListener("keydown", h, true);
    return () => window.removeEventListener("keydown", h, true);
  }, [mode, onClose]);

  const nameErr = name ? validFolderName(name) : null;
  const go = (p: string) => { setPath(p); setMode(""); setErr(null); };
  const refresh = () => ["shares", "backup-sources", "locations", "folder-picker"].forEach((k) => qc.invalidateQueries({ queryKey: [k] }));

  const mkdir = async () => {
    if (validFolderName(name) || !path) return;
    const p = `${path}/${name.trim()}`;
    setBusy(true);
    setErr(null);
    try {
      await api.post("/api/files/mkdir", { path: p });
      refresh();
      toast(tr("Folder created"));
      setName("");
      go(p);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  const mkshare = async () => {
    if (validFolderName(name) || !base) return;
    const sh = { ...newShareDefaults(name.trim(), base, me.data?.username), description: desc.trim() };
    setBusy(true);
    setErr(null);
    try {
      await api.post("/api/shares", sh);
      refresh();
      toast(tr("Shared folder {0} created", sh.name));
      onPick(sh.path);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };

  const rel = path ? path.slice(ROOT.length).split("/").filter(Boolean) : [];
  const shares = (sources.data?.folders ?? []).filter((f) => f.label.startsWith("Share "));
  const dirs = (list.data?.entries ?? []).filter((e) => e.dir);
  const row = "flex w-full items-center gap-2 rounded-lg px-3 py-1.5 text-left text-[13px] hover:bg-panel-2 focus-visible:bg-panel-2 focus-visible:outline-none";

  return (
    <Modal open onClose={onClose} wide title={tr("Choose a folder")} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" disabled={!path || path === ROOT} onClick={() => onPick(path)}>{tr("Choose this folder")}</Button>
    </>}>
      <div className="space-y-3">
        <nav aria-label={tr("Location")} className="flex flex-wrap items-center gap-1 text-[13px]">
          <button type="button" className="rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => go("")}>{tr("NAS storage")}</button>
          {rel.map((p, i) => (
            <span key={i} className="flex items-center gap-1">
              <ChevronRight size={13} className="text-muted" aria-hidden />
              <button type="button" className="mono rounded px-1.5 py-0.5 hover:bg-panel-2" aria-current={i === rel.length - 1 ? "location" : undefined} onClick={() => go(`${ROOT}/${rel.slice(0, i + 1).join("/")}`)}>{p}</button>
            </span>
          ))}
        </nav>

        <div className="max-h-72 overflow-y-auto rounded-xl border border-line p-1">
          {path === "" ? (
            locations.isLoading || sources.isLoading ? <Spinner /> : (
              <>
                {shares.length > 0 && <div className="px-3 pb-1 pt-2 text-[11.5px] font-medium text-muted">{tr("Shared folders")}</div>}
                {shares.map((s) => <button type="button" key={s.path} className={row} onClick={() => go(s.path)}><Share2 size={15} className="shrink-0 text-accent" /><span className="truncate">{s.label.slice(6)}</span><span className="mono ml-auto truncate text-[11px] text-muted">{s.path}</span></button>)}
                <div className="px-3 pb-1 pt-2 text-[11.5px] font-medium text-muted">{tr("Volumes")}</div>
                {locations.data?.length === 0 && <div className="px-3 py-2 text-[13px] text-muted">{tr("First create a volume or a ZFS pool.")}</div>}
                {locations.data?.map((l) => <button type="button" key={l.path} className={row} onClick={() => go(l.path)}><HardDrive size={15} className="shrink-0 text-accent" /><span className="truncate">{l.label}</span><span className="ml-auto shrink-0 text-[11px] text-muted">{tr("{0} free", bytes(l.free))}</span></button>)}
              </>
            )
          ) : list.isLoading ? <Spinner /> : (
            <>
              {path !== ROOT && <button type="button" className={cx(row, "text-muted")} onClick={() => go(parentOf(path))}><Folder size={15} className="shrink-0" />..</button>}
              {dirs.length === 0 && <div className="px-3 py-2 text-[13px] text-muted">{tr("No subfolders")}</div>}
              {dirs.map((d) => <button type="button" key={d.name} className={row} onClick={() => go(`${path}/${d.name}`)}><Folder size={15} className="shrink-0 text-accent" /><span className="truncate">{d.name}</span></button>)}
            </>
          )}
        </div>
        {path && <div className="mono truncate text-[12px] text-muted" aria-live="polite">{path}</div>}

        <div className="flex flex-wrap gap-2">
          <Button size="sm" icon={<FolderPlus size={14} />} disabled={!path || path === ROOT} aria-expanded={mode === "folder"} onClick={() => { setMode(mode === "folder" ? "" : "folder"); setName(""); setErr(null); }}>{tr("New folder")}</Button>
          {allowShare && <Button size="sm" icon={<Share2 size={14} />} aria-expanded={mode === "share"} onClick={() => { setMode(mode === "share" ? "" : "share"); setName(""); setDesc(""); setErr(null); }}>{tr("New shared folder")}</Button>}
        </div>

        {mode === "folder" && (
          <form className="flex flex-wrap items-end gap-2 rounded-xl border border-line p-3" onSubmit={(e) => { e.preventDefault(); void mkdir(); }}>
            <Field label={tr("Name of the new folder in {0}", rel[rel.length - 1] ?? "")} hint={nameErr ?? undefined} className="min-w-0 flex-1"><Input autoFocus value={name} onChange={(e) => setName(e.target.value)} /></Field>
            <Button type="submit" variant="primary" loading={busy} disabled={!name || !!nameErr}>{tr("Create")}</Button>
          </form>
        )}
        {mode === "share" && (
          <form className="space-y-3 rounded-xl border border-line p-3" onSubmit={(e) => { e.preventDefault(); void mkshare(); }}>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={tr("Share name")} hint={nameErr ?? undefined}><Input autoFocus value={name} onChange={(e) => setName(e.target.value)} /></Field>
              <Field label={tr("Volume")}>
                <Select value={base} onChange={(e) => setBase(e.target.value)}>
                  <option value="">{tr("Choose a volume…")}</option>
                  {locations.data?.map((l) => <option key={l.path} value={l.path}>{l.label} — {tr("{0} free", bytes(l.free))}</option>)}
                </Select>
              </Field>
              <Field label={tr("Description")} className="sm:col-span-2"><Input value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
            </div>
            <p className="text-[12px] text-muted">{tr("Shared over SMB with a recycle bin; you can write to it, the other users can read it. Change this, or create an encrypted folder, in Shared folders.")}</p>
            <div className="flex justify-end"><Button type="submit" variant="primary" loading={busy} disabled={!name || !!nameErr || !base}>{tr("Create and choose")}</Button></div>
          </form>
        )}
        <ErrorBox error={err || (list.isError && path === ROOT ? list.error : null)} />
      </div>
    </Modal>
  );
}
