import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Clock, FileStack, Folder, PieChart, RefreshCw } from "lucide-react";
import { api, q } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import { bytes, dateTime, pct } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Notice, PageHeader, Progress, Select, Spinner, Stat } from "../../components/ui";
import { locale, tr } from "../../lib/i18n";

export type ScanTarget = { path: string; label: string; kind: "volume" | "dataset" | "share"; scanned?: string; size?: number; job?: string };
type FileEntry = { path: string; size: number; modTime: string };
type DirNode = { name: string; path: string; size: number; files: number; children?: DirNode[]; more?: boolean };
type Report = {
  root: string; started: string; finished: string; size: number; apparent: number; files: number; dirs: number; errors: number;
  largest: FileEntry[]; old: FileEntry[]; oldSize: number; oldFiles: number; types: { category: string; size: number; files: number }[] | null;
};
type Analysis = { job?: string; report?: Report; tree?: DirNode };

export const kindLabel = (k: string) => ({ volume: tr("Volume"), dataset: tr("ZFS dataset"), share: tr("Shared folder") }[k] ?? k);

const catLabel = (c: string) => ({
  video: tr("Videos"), photos: tr("Photos"), music: tr("Music"), documents: tr("Documents"),
  archives: tr("Archives"), diskimages: tr("Disk images"), other: tr("Other"),
}[c] ?? c);
const catColor: Record<string, string> = { video: "#6366f1", photos: "#ec4899", music: "#22c55e", documents: "#f59e0b", archives: "#06b6d4", diskimages: "#8b5cf6", other: "#94a3b8" };

function FileTable({ files, root }: { files: FileEntry[]; root: string }) {
  if (!files.length) return <div className="text-muted text-[13px]">{tr("None")}</div>;
  return (
    <div className="overflow-x-auto">
      <table className="table">
        <thead><tr><th>{tr("File")}</th><th className="text-right">{tr("Size")}</th><th>{tr("Modified")}</th></tr></thead>
        <tbody>
          {files.map((f) => (
            <tr key={f.path}>
              <td className="mono max-w-[420px] truncate text-[12px]" title={`${root}/${f.path}`}>{f.path}</td>
              <td className="text-right whitespace-nowrap">{bytes(f.size)}</td>
              <td className="whitespace-nowrap text-muted">{new Date(f.modTime).toLocaleDateString(locale)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export default function SpaceAnalyzer() {
  const { jobs } = useJobs();
  const targets = useQuery({ queryKey: ["st-targets"], queryFn: () => api.get<ScanTarget[]>("/api/storagetools/targets") });
  const [path, setPath] = useState("");
  const [dir, setDir] = useState("");
  useEffect(() => { if (!path && targets.data?.length) setPath(targets.data[0].path); }, [targets.data, path]);
  const data = useQuery({
    queryKey: ["st-analysis", path, dir],
    queryFn: () => api.get<Analysis>(`/api/storagetools/analyzer${q({ path, dir })}`),
    enabled: !!path,
  });
  const job = jobs.find((j) => j.id === data.data?.job && j.status === "running");
  const scan = useAction(() => api.post("/api/storagetools/analyzer", { path }));
  const rep = data.data?.report;
  const tree = data.data?.tree;
  const target = targets.data?.find((t) => t.path === path);
  const crumbs = dir ? dir.split("/") : [];

  return (
    <div>
      <PageHeader title={tr("Space analyzer")} subtitle={tr("What takes up space on your volumes and shared folders")} icon={<PieChart size={20} />}
        actions={path && <Button variant="primary" icon={<RefreshCw size={15} />} loading={scan.isPending || !!job} onClick={() => scan.mutate()}>{rep ? tr("Rescan") : tr("Analyze")}</Button>} />
      <ErrorBox error={targets.error || data.error || scan.error} />
      {targets.isLoading ? <Spinner /> : !targets.data?.length ? <Empty icon={<PieChart />} title={tr("No volume to analyze")} /> : (
        <div className="space-y-5">
          <div className="flex flex-wrap items-center gap-3">
            <Select value={path} onChange={(e) => { setPath(e.target.value); setDir(""); }} className="max-w-md">
              {targets.data.map((t) => <option key={t.path} value={t.path}>{t.label} — {kindLabel(t.kind)} ({t.path})</option>)}
            </Select>
            {rep && <span className="flex items-center gap-1.5 text-muted text-[12.5px]"><Clock size={14} />{tr("Analysis of {0}", dateTime(rep.finished))}</span>}
          </div>
          <p className="text-muted text-[12px]">{tr("Reserved to administrators: the analysis reads every folder, whatever the share permissions. It runs in the background at low priority and its result is kept until the next scan.")}</p>
          {job && (
            <Card>
              <div className="mb-2 text-[13px]">{tr("Analysis in progress…")}</div>
              <Progress value={job.progress} indeterminate={job.progress < 0} />
            </Card>
          )}
          {data.isLoading ? <Spinner /> : !rep || !tree ? (!job && <Empty icon={<PieChart />} title={tr("Not analyzed yet")} text={tr("Start an analysis of {0}.", target?.label ?? path)} />) : <>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <Stat label={tr("Space used")} value={bytes(rep.size)} sub={tr("{0} files", rep.files.toLocaleString(locale))} icon={<PieChart size={18} />} />
              <Stat label={tr("Folders")} value={rep.dirs.toLocaleString(locale)} icon={<Folder size={18} />} />
              <Stat label={tr("Not modified for a year")} value={bytes(rep.oldSize)} sub={tr("{0} files", rep.oldFiles.toLocaleString(locale))} icon={<Clock size={18} />} tone="warn" />
              <Stat label={tr("Unreadable entries")} value={rep.errors.toLocaleString(locale)} icon={<FileStack size={18} />} tone={rep.errors ? "warn" : "ok"} />
            </div>

            <Card title={tr("Folders")}>
              <div className="mb-3 flex flex-wrap items-center gap-1 text-[13px]">
                <button className="text-accent hover:underline" onClick={() => setDir("")}>{target?.label ?? path}</button>
                {crumbs.map((c, i) => (
                  <span key={i} className="flex items-center gap-1"><ChevronRight size={13} className="text-muted" />
                    <button className="text-accent hover:underline" onClick={() => setDir(crumbs.slice(0, i + 1).join("/"))}>{c}</button>
                  </span>
                ))}
                <span className="ml-auto text-muted">{bytes(tree.size)} · {tr("{0} files", tree.files.toLocaleString(locale))}</span>
              </div>
              {!tree.children?.length ? <div className="text-muted text-[13px]">{tree.more ? tr("Sub-folders are deeper than the analysis keeps.") : tr("No sub-folder.")}</div> : (
                <div className="space-y-1.5">
                  {tree.children.map((c) => {
                    const drill = !!c.children?.length || c.more;
                    return (
                      <div key={c.path} className="grid grid-cols-[minmax(0,1fr)_90px] items-center gap-3 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_90px]">
                        <button disabled={!drill} onClick={() => setDir(c.path)} className={`flex min-w-0 items-center gap-2 text-left text-[13px] ${drill ? "hover:text-accent" : ""}`}>
                          <Folder size={15} className="shrink-0 text-accent" /><span className="truncate">{c.name}</span>{drill && <ChevronRight size={13} className="shrink-0 text-muted" />}
                        </button>
                        <Progress className="hidden sm:block" value={pct(c.size, tree.size)} tone="accent" />
                        <span className="text-right text-[12.5px] tabular-nums">{bytes(c.size)}</span>
                      </div>
                    );
                  })}
                  {tree.more && <div className="text-muted text-[12px]">{tr("Smaller folders are not listed.")}</div>}
                </div>
              )}
            </Card>

            <Card title={tr("File types")}>
              <div className="mb-3 flex h-3 overflow-hidden rounded-full border border-line">
                {(rep.types ?? []).map((t) => <div key={t.category} style={{ width: `${pct(t.size, rep.size)}%`, background: catColor[t.category] }} title={catLabel(t.category)} />)}
              </div>
              <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
                {(rep.types ?? []).map((t) => (
                  <div key={t.category} className="flex items-center gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[13px]">
                    <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ background: catColor[t.category] }} />
                    <span className="flex-1">{catLabel(t.category)}</span>
                    <span className="text-right"><b>{bytes(t.size)}</b><span className="block text-muted text-[11.5px]">{tr("{0} files", t.files.toLocaleString(locale))}</span></span>
                  </div>
                ))}
              </div>
            </Card>

            <div className="grid gap-5 xl:grid-cols-2">
              <Card title={tr("Largest files")}><FileTable files={rep.largest ?? []} root={rep.root} /></Card>
              <Card title={<span className="flex items-center gap-2">{tr("Large files not modified for over a year")} <Badge tone="warn">{bytes(rep.oldSize)}</Badge></span>}>
                <FileTable files={rep.old ?? []} root={rep.root} />
              </Card>
            </div>
            <Notice>{tr("Space is counted once per file (hard links included) and does not include snapshots. Other file systems mounted inside are left out.")}</Notice>
          </>}
        </div>
      )}
    </div>
  );
}
