import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, Plus, Search, Trash2, X } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import { bytes, dateTime } from "../../lib/format";
import { useUI, Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, Input, Notice, PageHeader, Progress, Select, Spinner, Stat } from "../../components/ui";
import { locale, tr } from "../../lib/i18n";
import { kindLabel, type ScanTarget } from "./SpaceAnalyzer";

type DupFile = { path: string; modTime: string; links?: number };
type Group = { id: string; size: number; wasted: number; files: DupFile[] };
type Report = { roots: string[]; minSize: number; started: string; finished: string; scanned: number; groups: Group[]; wasted: number; truncated?: boolean };
type Data = { job?: string; report?: Report; recycleBins: string[] };

const PAGE = 100;

export default function Duplicates() {
  const { confirm, toast } = useUI();
  const { jobs } = useJobs();
  const targets = useQuery({ queryKey: ["st-targets"], queryFn: () => api.get<ScanTarget[]>("/api/storagetools/targets") });
  const data = useQuery({ queryKey: ["st-dups"], queryFn: () => api.get<Data>("/api/storagetools/duplicates") });
  const [roots, setRoots] = useState<string[]>([]);
  const [extra, setExtra] = useState("");
  const [minSize, setMinSize] = useState(1 << 20);
  const [sel, setSel] = useState<Set<string>>(new Set());
  const [shown, setShown] = useState(PAGE);
  const job = jobs.find((j) => j.id === data.data?.job && j.status === "running");
  const start = useAction(() => api.post("/api/storagetools/duplicates", { paths: roots, minSize }), { onDone: () => setSel(new Set()) });
  const del = useAction((paths: string[]) => api.post<{ recycled: number; deleted: number; freed: number; skipped: string[] }>("/api/storagetools/duplicates/delete", { paths }), {
    onDone: (r) => {
      setSel(new Set());
      toast(tr("{0} file(s) moved to the recycle bin, {1} deleted", r.recycled, r.deleted));
      if (r.skipped.length) toast(tr("{0} file(s) skipped: changed since the search, or no other copy left", r.skipped.length), "error");
    },
  });
  const rep = data.data?.report;
  const bins = data.data?.recycleBins ?? [];
  const inBin = (p: string) => bins.some((b) => p.startsWith(b + "/"));

  const selectedSize = useMemo(() => {
    let n = 0;
    for (const g of rep?.groups ?? []) for (const f of g.files) if (sel.has(f.path)) n += g.size;
    return n;
  }, [rep, sel]);

  const toggle = (g: Group, p: string, on: boolean) => {
    const next = new Set(sel);
    if (on) {
      // Never every copy of a group.
      if (g.files.filter((f) => f.path !== p).every((f) => next.has(f.path))) return;
      next.add(p);
    } else next.delete(p);
    setSel(next);
  };
  // Keeps the oldest copy of each group (the original, most likely) and selects the others.
  const selectAllButOldest = () => {
    const next = new Set<string>();
    for (const g of rep?.groups ?? []) {
      const keep = [...g.files].sort((a, b) => a.modTime.localeCompare(b.modTime) || a.path.localeCompare(b.path))[0];
      for (const f of g.files) if (f.path !== keep.path) next.add(f.path);
    }
    setSel(next);
  };
  const doDelete = async () => {
    const paths = [...sel];
    const toBin = paths.filter(inBin).length;
    const ok = await confirm({
      title: tr("Delete {0} duplicate file(s)?", paths.length),
      text: <>{toBin > 0 && <p>{tr("{0} file(s) go to the recycle bin of their shared folder.", toBin)}</p>}{paths.length - toBin > 0 && <p><b>{tr("{0} file(s) have no recycle bin: they are deleted permanently.", paths.length - toBin)}</b></p>}<p className="mt-2 text-muted">{tr("At least one copy of each group is always kept.")}</p></>,
      danger: paths.length - toBin > 0, confirm: tr("Delete"),
    });
    if (ok) del.mutate(paths);
  };
  const addExtra = () => {
    const p = extra.trim().replace(/\/+$/, "");
    if (p && !roots.includes(p)) setRoots([...roots, p]);
    setExtra("");
  };

  return (
    <div>
      <PageHeader title={tr("Duplicates")} subtitle={tr("Find identical files and free the space they waste")} icon={<Copy size={20} />} />
      <ErrorBox error={targets.error || data.error} />
      <div className="space-y-5">
        <Card title={tr("Search")} actions={<Button variant="primary" icon={<Search size={15} />} disabled={!roots.length} loading={start.isPending || !!job} onClick={() => start.mutate()}>{tr("Search duplicates")}</Button>}>
          <div className="space-y-4">
            <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {(targets.data ?? []).map((t) => (
                <Checkbox key={t.path} checked={roots.includes(t.path)} onChange={(v) => setRoots(v ? [...roots, t.path] : roots.filter((r) => r !== t.path))}
                  label={<span>{t.label} <span className="text-muted text-[12px]">· {kindLabel(t.kind)}</span></span>} />
              ))}
            </div>
            {roots.filter((r) => !targets.data?.some((t) => t.path === r)).map((r) => (
              <div key={r} className="flex items-center gap-2 text-[13px]"><span className="mono">{r}</span><Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => setRoots(roots.filter((x) => x !== r))} /></div>
            ))}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Other folder")} hint={tr("A folder inside a volume, e.g. /srv/strata/main/Photos/2024")}>
                <div className="flex gap-2"><Input className="mono" value={extra} placeholder="/srv/strata/…" onChange={(e) => setExtra(e.target.value)} onKeyDown={(e) => e.key === "Enter" && addExtra()} /><Button icon={<Plus size={15} />} disabled={!extra.trim()} onClick={addExtra} /></div>
              </Field>
              <Field label={tr("Ignore files smaller than")}>
                <Select value={minSize} onChange={(e) => setMinSize(+e.target.value)}>
                  {[100 << 10, 1 << 20, 10 << 20, 100 << 20].map((n) => <option key={n} value={n}>{bytes(n)}</option>)}
                </Select>
              </Field>
            </div>
            <p className="text-muted text-[12px]">{tr("Files are compared by size, then by content (BLAKE2b hash). Hard links are the same file and are not counted. Recycle bins and snapshots are skipped. Reserved to administrators.")}</p>
          </div>
        </Card>

        {job && <Card><div className="mb-2 text-[13px]">{tr("Comparing files…")}</div><Progress value={job.progress} indeterminate={job.progress < 0} /></Card>}

        {data.isLoading ? <Spinner /> : !rep ? (!job && <Empty icon={<Copy />} title={tr("No search yet")} />) : <>
          <div className="grid gap-4 sm:grid-cols-3">
            <Stat label={tr("Wasted space")} value={bytes(rep.wasted)} sub={tr("Search of {0}", dateTime(rep.finished))} icon={<Copy size={18} />} tone={rep.wasted ? "warn" : "ok"} />
            <Stat label={tr("Groups of identical files")} value={rep.groups.length.toLocaleString(locale)} sub={rep.roots.join(", ")} icon={<Search size={18} />} />
            <Stat label={tr("Files compared")} value={rep.scanned.toLocaleString(locale)} sub={tr("{0} and more", bytes(rep.minSize))} icon={<Search size={18} />} />
          </div>
          {rep.truncated && <Notice tone="warn">{tr("Only the groups wasting the most space are listed.")}</Notice>}
          {rep.groups.length === 0 ? <Empty icon={<Copy />} title={tr("No duplicate found")} /> : (
            <Card title={tr("Identical files")} actions={<div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={selectAllButOldest}>{tr("Select all but the oldest")}</Button>
              {sel.size > 0 && <Button size="sm" onClick={() => setSel(new Set())}>{tr("Clear selection")}</Button>}
              <Button size="sm" variant="danger" icon={<Trash2 size={14} />} disabled={!sel.size} loading={del.isPending} onClick={doDelete}>{sel.size ? tr("Delete {0} ({1})", sel.size, bytes(selectedSize)) : tr("Delete")}</Button>
            </div>}>
              <div className="space-y-3">
                {rep.groups.slice(0, shown).map((g) => (
                  <div key={g.id} className="rounded-xl border border-line">
                    <div className="flex items-center justify-between border-b border-line bg-panel-2/50 px-3 py-2 text-[12.5px]">
                      <span>{tr("{0} copies of {1}", g.files.length, bytes(g.size))}</span>
                      <Badge tone="warn">{tr("{0} wasted", bytes(g.wasted))}</Badge>
                    </div>
                    <div className="space-y-1 px-3 py-2">
                      {g.files.map((f) => (
                        <div key={f.path} className="flex items-center gap-2 text-[12.5px]">
                          <Checkbox checked={sel.has(f.path)} onChange={(v) => toggle(g, f.path, v)} label={<span className="mono break-all">{f.path}</span>} />
                          <span className="ml-auto shrink-0 text-muted">{new Date(f.modTime).toLocaleDateString(locale)}</span>
                          {inBin(f.path) ? <Badge>{tr("Recycle bin")}</Badge> : <Badge tone="danger">{tr("Permanent")}</Badge>}
                          {!!f.links && <Badge tone="accent">{tr("{0} hard links", f.links)}</Badge>}
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
                {rep.groups.length > shown && <Button onClick={() => setShown(shown + PAGE)}>{tr("Show more ({0} left)", rep.groups.length - shown)}</Button>}
              </div>
            </Card>
          )}
        </>}
      </div>
    </div>
  );
}
