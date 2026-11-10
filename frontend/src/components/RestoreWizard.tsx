import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArchiveRestore, Camera, ChevronRight, Cloud, DatabaseBackup, Disc, Download, File, Folder, FolderOpen, HardDrive, Lock, Server } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import { ago, bytes, cx, dateTime } from "../lib/format";
import type { Job } from "../lib/types";
import { Badge, Button, Checkbox, Empty, ErrorBox, Field, Input, Modal, Notice, Spinner } from "./ui";
import { tr } from "../lib/i18n";
import { ImportModal, JobLog, type Doc } from "../pages/system/ConfigBackup";

type Info = { id: string; kind: "snapshot" | "backup" | "cloud" | "image"; name: string; detail: string; downloadDirs: boolean };
type DiskState = { state: "closed" | "opening" | "open" | "failed"; open?: number; max?: number; idleMinutes?: number; session?: { error?: string; size: number; roots: { path: string; fsType: string; label?: string }[]; skipped?: string[] } };
type Point = { id: string; time: string; label?: string; files?: number; bytes?: number; roots: string[] };
type Entry = { path: string; name: string; dir: boolean; link?: boolean; size: number; mtime: string };
type Item = { path: string; action: "overwrite" | "conflict"; size: number; curSize: number; mtime: string; curMtime: string };
type Preview = { files: number; dirs: number; bytes: number; new: number; newBytes: number; same: number; overwrite: number; conflicts: number; changedBytes: number; items: Item[]; truncated: boolean; sample: { path: string; size: number }[]; target: string; configFile?: string };
type Scope = "files" | "share" | "nas";
type Mode = "inplace" | "folder" | "download";
type Policy = "overwrite" | "skip" | "rename";

const kindIcon = { snapshot: <Camera size={16} />, backup: <DatabaseBackup size={16} />, cloud: <Cloud size={16} />, image: <Disc size={16} /> };
const dirname = (p: string) => p.slice(0, p.lastIndexOf("/")) || "/";

/** Points in time grouped by day, newest first: a simple timeline. */
function Timeline({ points, value, onPick, labels }: { points: Point[]; value: string; onPick: (p: Point) => void; labels?: boolean }) {
  const days = useMemo(() => {
    const m = new Map<string, Point[]>();
    for (const p of [...points].sort((a, b) => b.time.localeCompare(a.time))) {
      const d = new Date(p.time).toLocaleDateString();
      m.set(d, [...(m.get(d) ?? []), p]);
    }
    return [...m.entries()];
  }, [points]);
  return (
    <div className="max-h-80 space-y-3 overflow-y-auto pr-1">
      {days.map(([day, list]) => (
        <div key={day} className="relative border-l-2 border-line pl-4">
          <div className="absolute -left-[7px] top-1 h-3 w-3 rounded-full bg-accent" />
          <div className="mb-1 text-[12.5px] font-medium">{day}</div>
          <div className="flex flex-wrap gap-1.5">
            {list.map((p) => (
              <button key={p.id} type="button" onClick={() => onPick(p)} title={p.label} className={cx("rounded-lg px-2.5 py-1 text-[12px]", value === p.id ? "bg-accent text-white" : "bg-panel-2 hover:bg-accent-soft")}>
                {new Date(p.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
                {p.files ? <span className="ml-1 opacity-70">· {p.files}</span> : null}
                {labels && p.label ? <span className="mono ml-1 opacity-70">· {p.label}</span> : null}
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

/** A VM disk backup must be opened (read back, mounted read-only) before its files can be browsed. */
function DiskPanel({ source, point }: { source: string; point: Point }) {
  const st = useQuery({
    queryKey: ["restore-disk", source, point.id],
    queryFn: () => api.get<DiskState>(`/api/restore/disk${q({ source, point: point.id })}`),
    refetchInterval: (x) => (x.state.data?.state === "opening" ? 2000 : 30000),
  });
  const open = useAction(() => api.post("/api/restore/disk/open", { source, point: point.id }), { onDone: () => setTimeout(() => st.refetch(), 800) });
  const close = useAction(() => api.post("/api/restore/disk/close", { source, point: point.id }), { success: tr("Disk closed") });
  const d = st.data;
  if (!d) return <Spinner />;
  if (d.state === "open") {
    return (
      <Notice>
        <div className="flex flex-wrap items-center gap-2">
          <Lock size={14} />
          <span className="flex-1">{tr("Disk {0} open read-only: {1}. It closes by itself after {2} idle minutes.", point.label ?? "", d.session?.roots.map((r) => `${r.path.slice(1)} (${r.fsType}${r.label ? ` “${r.label}”` : ""})`).join(", ") ?? "", d.idleMinutes ?? 30)}</span>
          <Button size="sm" loading={close.isPending} onClick={() => close.mutate()}>{tr("Close the disk")}</Button>
        </div>
        {!!d.session?.skipped?.length && <div className="mt-1 text-muted text-[12px]">{tr("Not opened: {0}", d.session.skipped.join("; "))}</div>}
      </Notice>
    );
  }
  return (
    <div className="space-y-3 rounded-xl border border-line p-4">
      <div className="flex items-start gap-3">
        <Disc size={18} className="mt-0.5 text-accent" />
        <div className="flex-1 text-[13px]">
          <div className="font-medium">{tr("Disk {0} of {1}", point.label ?? "", dateTime(point.time))}{point.bytes ? ` · ${bytes(point.bytes)}` : ""}</div>
          <div className="text-muted text-[12.5px]">{tr("To browse it, the disk is read back from the backup onto a volume, then its partitions are mounted read-only (ext4, XFS, Btrfs, NTFS, FAT, LVM). The backup itself is never modified. This takes a while for a large disk.")}</div>
        </div>
      </div>
      {d.state === "opening" ? <Spinner label={tr("Opening the disk…")} /> : (
        <div className="flex items-center justify-end gap-2">
          {d.open !== undefined && d.max !== undefined && d.open >= d.max && <span className="text-warn text-[12px]">{tr("{0} disks are open already: close one first.", d.max)}</span>}
          <Button variant="primary" loading={open.isPending} onClick={() => open.mutate()}>{tr("Open the disk")}</Button>
        </div>
      )}
      <ErrorBox error={open.error || close.error} />
    </div>
  );
}

/**
 * Guided restore: source → point in time → content → destination and
 * preview → job. Opened from Backup, Scheduled tasks or the Files app.
 */
export function RestoreWizard({ onClose, initialPath, initialSource }: { onClose: () => void; initialPath?: string; initialSource?: string }) {
  const [step, setStep] = useState(initialSource ? 1 : 0);
  const [scope, setScope] = useState<Scope>("files");
  const [source, setSource] = useState(initialSource ?? "");
  const [point, setPoint] = useState<Point | null>(null);
  const [dir, setDir] = useState("");
  const [sel, setSel] = useState<Entry[]>([]);
  const [mode, setMode] = useState<Mode>("inplace");
  const [target, setTarget] = useState("");
  const [policy, setPolicy] = useState<Policy>("rename");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [job, setJob] = useState("");
  const [ended, setEnded] = useState<Job | null>(null);
  const [doc, setDoc] = useState<Doc | null>(null);

  const sources = useQuery({ queryKey: ["restore-sources", initialPath ?? ""], queryFn: () => api.get<Info[]>(`/api/restore/sources${q({ path: initialPath })}`) });
  const info = sources.data?.find((s) => s.id === source);
  const points = useQuery({ queryKey: ["restore-points", source], queryFn: () => api.get<Point[]>(`/api/restore/points${q({ source })}`), enabled: !!source && step >= 1 });
  const isImage = info?.kind === "image";
  const disk = useQuery({
    queryKey: ["restore-disk", source, point?.id],
    queryFn: () => api.get<DiskState>(`/api/restore/disk${q({ source, point: point?.id })}`),
    enabled: isImage && !!point && step === 2,
    refetchInterval: (x) => (x.state.data?.state === "opening" ? 2000 : 30000),
  });
  const diskOpen = !isImage || disk.data?.state === "open";
  const browse = useQuery({
    queryKey: ["restore-browse", source, point?.id, dir, diskOpen],
    queryFn: () => api.get<{ entries: Entry[] }>(`/api/restore/browse${q({ source, point: point?.id, path: dir })}`),
    enabled: !!point && step === 2 && diskOpen,
  });
  const body = () => ({ source, point: point?.id, paths: sel.map((e) => e.path), mode, target: mode === "folder" ? target : "", policy });
  const doPreview = useAction(() => api.post<Preview>("/api/restore/preview", body()), { onDone: setPreview });
  const run = useAction(() => api.post<Job>("/api/restore/run", body()), { onDone: (j) => { setJob(j.id); setStep(4); } });
  const loadConfig = useAction((path: string) => api.post<Doc>("/api/restore/config", { path }), { onDone: setDoc });

  // Opened on a path: start the browsing next to it, with it selected.
  const pickPoint = (p: Point) => {
    setPoint(p);
    setPreview(null);
    if (scope === "nas") {
      setSel(p.roots.map((r) => ({ path: r, name: r.split("/").pop() ?? r, dir: true, size: 0, mtime: p.time })));
      setDir("");
    } else if (initialPath && p.roots.some((r) => initialPath === r || initialPath.startsWith(r + "/"))) {
      setDir(p.roots.includes(initialPath) ? initialPath : dirname(initialPath));
      setSel([]);
    } else {
      setDir("");
      setSel([]);
    }
  };
  useEffect(() => {
    if (initialPath && browse.data && sel.length === 0) {
      const e = browse.data.entries.find((x) => x.path === initialPath);
      if (e) setSel([e]);
    }
  }, [browse.data]);
  useEffect(() => setPreview(null), [mode, target, sel, policy]);
  useEffect(() => { if (isImage && mode === "inplace") setMode("folder"); }, [isImage, mode]);

  const crumbs = useMemo(() => {
    if (!dir || !point) return [];
    const root = point.roots.find((r) => dir === r || dir.startsWith(r + "/")) ?? dir;
    const rest = dir.slice(root.length).split("/").filter(Boolean);
    const out = [{ label: root, path: root }];
    rest.forEach((n, i) => out.push({ label: n, path: root + "/" + rest.slice(0, i + 1).join("/") }));
    return out;
  }, [dir, point]);

  const toggle = (e: Entry, v: boolean) => setSel((s) => (v ? [...s.filter((x) => !x.path.startsWith(e.path + "/") && !e.path.startsWith(x.path + "/")), e] : s.filter((x) => x.path !== e.path)));
  const single = sel.length === 1 ? sel[0] : null;
  const canDownload = !!single && (!single.dir || !!info?.downloadDirs);
  const steps = [tr("Source"), tr("Point in time"), tr("Content"), tr("Restore"), tr("Progress")];
  const footer = (
    <>
      {step > 0 && step < 4 && <Button className="mr-auto" onClick={() => setStep(scope === "nas" && step === 3 ? 1 : step - 1)}>{tr("Back")}</Button>}
      <Button onClick={onClose}>{step === 4 ? tr("Close") : tr("Cancel")}</Button>
      {step === 0 && <Button variant="primary" disabled={!source} onClick={() => setStep(1)}>{tr("Next")}</Button>}
      {step === 1 && <Button variant="primary" disabled={!point} onClick={() => setStep(scope === "nas" ? 3 : 2)}>{tr("Next")}</Button>}
      {step === 2 && <Button variant="primary" disabled={!sel.length} onClick={() => setStep(3)}>{tr("Next")}</Button>}
      {step === 3 && mode === "download" && (
        canDownload ? <a href={`/api/restore/download${q({ source, point: point?.id, path: single!.path })}`}><Button variant="primary" icon={<Download size={15} />}>{tr("Download")}</Button></a> : null
      )}
      {step === 3 && mode !== "download" && <>
        <Button loading={doPreview.isPending} disabled={mode === "folder" && !target} onClick={() => doPreview.mutate()}>{tr("Preview")}</Button>
        <Button variant="primary" loading={run.isPending} disabled={!preview} onClick={() => run.mutate()}>{tr("Restore")}</Button>
      </>}
    </>
  );

  return (
    <Modal open onClose={onClose} wide="xl" title={<span className="flex items-center gap-2"><ArchiveRestore size={17} />{tr("Restore")}</span>} footer={footer}>
      <div className="mb-4 flex flex-wrap items-center gap-1 text-[12px]">
        {steps.map((s, i) => (
          <span key={s} className="flex items-center gap-1">
            {i > 0 && <ChevronRight size={12} className="text-muted" />}
            <span className={cx("rounded-md px-2 py-0.5", i === step ? "bg-accent text-white" : i < step ? "text-fg" : "text-muted")}>{s}</span>
          </span>
        ))}
      </div>

      {step === 0 && (
        <div className="space-y-4">
          {!initialPath && (
            <div className="grid gap-2 sm:grid-cols-3">
              {([["files", tr("Files or folders"), tr("Pick what to bring back")], ["share", tr("A shared folder"), tr("A whole shared folder as it was")], ["nas", tr("The whole NAS"), tr("Every folder of a backup, then its configuration")]] as [Scope, string, string][]).map(([k, l, sub]) => (
                <button key={k} type="button" onClick={() => setScope(k)} className={cx("rounded-xl border px-3 py-2.5 text-left", scope === k ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                  <div className="text-[13px] font-medium">{l}</div><div className="text-muted text-[11.5px]">{sub}</div>
                </button>
              ))}
            </div>
          )}
          {initialPath && <Notice>{tr("Versions of {0}", initialPath)}</Notice>}
          {scope === "nas" && <Notice>{tr("On a new or reinstalled NAS, import the configuration first (Settings → Configuration backup): it brings back the volumes, shared folders, users and backup tasks. Then restore the data here. A configuration export found in the backup is offered at the end.")}</Notice>}
          <ErrorBox error={sources.error} />
          {sources.isLoading ? <Spinner /> : !sources.data?.length ? (
            <Empty icon={<ArchiveRestore />} title={tr("Nothing to restore from")} text={tr("Restores read snapshots (Btrfs, ZFS, LVM thin), versioned backups, file copies to a local folder and cloud uploads.")} />
          ) : (
            <div className="space-y-1.5">
              {sources.data.filter((s) => (scope !== "nas" || s.kind !== "snapshot") && (scope === "files" || s.kind !== "image")).map((s) => (
                <button key={s.id} type="button" onClick={() => { setSource(s.id); setPoint(null); }} className={cx("flex w-full items-center gap-3 rounded-xl border px-3 py-2 text-left", source === s.id ? "border-accent bg-accent-soft" : "border-line hover:bg-panel-2")}>
                  <span className="text-accent">{kindIcon[s.kind]}</span>
                  <span className="min-w-0 flex-1"><span className="block text-[13px] font-medium">{s.name}</span><span className="mono block truncate text-muted text-[11.5px]">{s.detail}</span></span>
                  <Badge>{{ snapshot: tr("Snapshots"), backup: tr("Backup"), cloud: tr("Cloud copy"), image: tr("VM disks") }[s.kind]}</Badge>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {step === 1 && (
        <div className="space-y-3">
          <div className="text-[13px]"><span className="text-muted">{tr("Source:")}</span> <b>{info?.name}</b></div>
          <ErrorBox error={points.error} />
          {points.isLoading ? <Spinner label={tr("Reading the versions…")} /> : !points.data?.length ? <Empty icon={<ArchiveRestore />} title={tr("No versions")} text={tr("Run the backup or the snapshot task once first.")} /> : (
            <>
              <Timeline points={points.data} value={point?.id ?? ""} onPick={pickPoint} labels={isImage} />
              {point && <div className="text-[12.5px] text-muted">{tr("Version of {0} ({1})", dateTime(point.time), ago(point.time))}{point.bytes ? ` · ${bytes(point.bytes)}` : ""}</div>}
            </>
          )}
        </div>
      )}

      {step === 2 && isImage && point && <div className="mb-3"><DiskPanel source={source} point={point} /></div>}
      {step === 2 && diskOpen && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-1 text-[12.5px]">
            <button className="rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => setDir("")}><HardDrive size={14} /></button>
            {crumbs.map((c) => <span key={c.path} className="flex items-center gap-1"><ChevronRight size={12} className="text-muted" /><button className="mono rounded px-1.5 py-0.5 hover:bg-panel-2" onClick={() => setDir(c.path)}>{c.label}</button></span>)}
          </div>
          <ErrorBox error={browse.error} />
          {browse.isLoading ? <Spinner /> : (
            <div className="max-h-80 overflow-y-auto rounded-xl border border-line">
              <table className="table">
                <tbody>
                  {(browse.data?.entries ?? []).map((e) => (
                    <tr key={e.path}>
                      <td className="w-9"><Checkbox checked={sel.some((x) => x.path === e.path)} onChange={(v) => toggle(e, v)} /></td>
                      <td>
                        {e.dir ? <button className="flex items-center gap-2 hover:text-accent" onClick={() => setDir(e.path)}><Folder size={15} className="text-accent" />{e.name}</button>
                          : <span className="flex items-center gap-2"><File size={15} className="text-muted" />{e.name}</span>}
                      </td>
                      <td className="text-muted text-[12px]">{e.dir ? "—" : bytes(e.size)}</td>
                      <td className="text-muted text-[12px]">{e.mtime && !e.mtime.startsWith("0001") ? dateTime(e.mtime) : ""}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {browse.data && !browse.data.entries.length && <div className="p-4 text-muted text-[13px]">{tr("Empty folder")}</div>}
            </div>
          )}
          <div className="text-[12.5px] text-muted">{sel.length ? tr("{0} item(s) selected: {1}", sel.length, sel.map((e) => e.name).join(", ")) : scope === "share" ? tr("Select the shared folder to restore.") : tr("Select files or folders.")}</div>
        </div>
      )}

      {step === 3 && (
        <div className="space-y-4">
          <div className="text-[12.5px] text-muted">{tr("{0} item(s) from the version of {1}", sel.length, dateTime(point?.time ?? ""))}</div>
          <div className="grid gap-2 sm:grid-cols-3">
            {([["inplace", tr("In place"), tr("Where they were")], ["folder", tr("To another folder"), tr("Nothing existing is touched")], ["download", tr("Download"), tr("One file, or a folder as a zip")]] as [Mode, string, string][]).map(([k, l, sub]) => (
              <button key={k} type="button" disabled={(k === "download" && !canDownload) || (k === "inplace" && isImage)} onClick={() => setMode(k)} className={cx("rounded-xl border px-3 py-2.5 text-left disabled:opacity-50", mode === k ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                <div className="text-[13px] font-medium">{l}</div><div className="text-muted text-[11.5px]">{sub}</div>
              </button>
            ))}
          </div>
          {mode === "folder" && <Field label={tr("Restore to")} hint={tr("Each selected item is recreated under this folder with its name.")}><Input className="mono" value={target} placeholder={`${dirname(sel[0]?.path ?? "/srv/strata/x")}/restored-${new Date().toISOString().slice(0, 10)}`} onChange={(e) => setTarget(e.target.value)} /></Field>}
          {mode !== "download" && (
            <Field label={tr("Files that exist and differ")}>
              <div className="flex flex-wrap gap-1">
                {([["rename", tr("Keep both (restored copy renamed)")], ["skip", tr("Keep the current file")], ["overwrite", tr("Replace with the backup")]] as [Policy, string][]).map(([k, l]) => (
                  <button key={k} type="button" onClick={() => setPolicy(k)} className={cx("rounded-lg px-3 py-1.5 text-[12.5px]", policy === k ? "bg-accent text-white" : "bg-panel-2 text-muted hover:text-fg")}>{l}</button>
                ))}
              </div>
            </Field>
          )}
          {mode === "inplace" && policy === "overwrite" && <Notice tone="warn">{tr("On Btrfs and ZFS, a snapshot is taken first: the files replaced stay available in it.")}</Notice>}
          {mode === "download" && <Notice>{info?.kind === "cloud" ? tr("Folders of a cloud copy cannot be downloaded: restore them to a folder.") : tr("Select a single file or folder to download it.")}</Notice>}
          <ErrorBox error={doPreview.error || run.error} />
          {preview && (
            <div className="space-y-3 rounded-xl border border-line p-4">
              <div className="grid grid-cols-2 gap-3 text-[13px] sm:grid-cols-4">
                <div><div className="text-muted text-[11.5px]">{tr("Files")}</div><b>{preview.files}</b> <span className="text-muted">({bytes(preview.bytes)})</span></div>
                <div><div className="text-muted text-[11.5px]">{tr("New")}</div><b>{preview.new}</b> <span className="text-muted">({bytes(preview.newBytes)})</span></div>
                <div><div className="text-muted text-[11.5px]">{tr("Already identical")}</div><b>{preview.same}</b></div>
                <div><div className="text-muted text-[11.5px]">{tr("Overwritten / conflicts")}</div><b>{preview.overwrite}</b> / <b className={preview.conflicts ? "text-warn" : ""}>{preview.conflicts}</b></div>
              </div>
              {preview.truncated && <Notice tone="warn">{tr("The listing stopped at 200,000 entries: the counts are partial.")}</Notice>}
              {preview.conflicts > 0 && <Notice tone="warn"><span className="flex items-center gap-2"><AlertTriangle size={15} />{tr("{0} file(s) changed after this version: “{1}” applies to them.", preview.conflicts, { rename: tr("Keep both (restored copy renamed)"), skip: tr("Keep the current file"), overwrite: tr("Replace with the backup") }[policy])}</span></Notice>}
              {preview.items.length > 0 && (
                <div className="max-h-56 overflow-y-auto">
                  <table className="table">
                    <thead><tr><th>{tr("File")}</th><th>{tr("Now")}</th><th>{tr("In the backup")}</th></tr></thead>
                    <tbody>
                      {preview.items.map((it) => (
                        <tr key={it.path}>
                          <td className="mono text-[12px]">{it.action === "conflict" && <Badge tone="warn">{tr("changed since")}</Badge>} {it.path}</td>
                          <td className="whitespace-nowrap text-[12px] text-muted">{bytes(it.curSize)} · {dateTime(it.curMtime)}</td>
                          <td className="whitespace-nowrap text-[12px] text-muted">{bytes(it.size)} · {dateTime(it.mtime)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {preview.items.length === 0 && preview.sample.length > 0 && <div className="text-[12px] text-muted">{tr("Nothing is overwritten. For example:")} <span className="mono">{preview.sample.slice(0, 5).map((s) => s.path.split("/").pop()).join(", ")}</span></div>}
              {preview.configFile && <div className="text-[12.5px]"><FolderOpen size={14} className="mr-1 inline" />{tr("This backup holds a configuration export: it can be imported after the restore.")}</div>}
            </div>
          )}
        </div>
      )}

      {step === 4 && job && (
        <div className="space-y-3">
          <JobLog id={job} label={tr("Restoring…")} onEnd={setEnded} />
          {ended?.status === "success" && preview?.configFile && (
            <Notice>
              <div className="flex flex-wrap items-center gap-3">
                <span className="flex-1">{tr("Configuration export restored: {0}", preview.configFile)}</span>
                <Button size="sm" icon={<Server size={14} />} loading={loadConfig.isPending} onClick={() => loadConfig.mutate(preview.configFile!)}>{tr("Import this configuration")}</Button>
              </div>
            </Notice>
          )}
          <ErrorBox error={loadConfig.error} />
        </div>
      )}
      {doc && <ImportModal doc={doc} onClose={() => setDoc(null)} />}
    </Modal>
  );
}
