import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Film, Search } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import { bytes, dateTime } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Notice, PageHeader, Progress, Select, Spinner, Stat } from "../../components/ui";
import { tr } from "../../lib/i18n";
import { kindLabel, type ScanTarget } from "./SpaceAnalyzer";

type RecodeFile = { path: string; old: number; new?: number; status: string; detail?: string };
type Report = { started: string; files: RecodeFile[]; saved: number };
type Data = { job?: string; ffmpeg: boolean; report?: Report };

const PAGE = 100;

export default function MediaOptimizer() {
  const targets = useQuery({ queryKey: ["st-targets"], queryFn: () => api.get<ScanTarget[]>(`/api/storagetools/targets`) });
  const data = useQuery({ queryKey: ["st-recode"], queryFn: () => api.get<Data>("/api/storagetools/recode") });
  const [root, setRoot] = useState("");
  const [minSize, setMinSize] = useState(100 << 20);
  const [shown, setShown] = useState(PAGE);
  const job = useJobs().jobs.find((j) => j.id === data.data?.job && j.status === "running");
  const start = useAction(() => api.post("/api/storagetools/recode", { path: root, minSize }), { onDone: () => setShown(PAGE) });
  const rep = data.data?.report;
  const counts = (s: string) => (rep?.files ?? []).filter((f) => f.status === s).length;

  return (
    <div>
      <PageHeader title={tr("Video compression")} subtitle={tr("Re-encode videos to H.265 and free space; the original is kept when the gain is too small")} icon={<Film size={20} />} />
      <ErrorBox error={targets.error || data.error || start.error} />
      {data.data && !data.data.ffmpeg && (
        <Notice tone="warn">{tr("The media tools component (ffmpeg) is not installed. Install it in Settings → Components.")}</Notice>
      )}
      <div className="space-y-5">
        <Card title={tr("Compression")} actions={<Button variant="primary" icon={<Search size={15} />} disabled={!root} loading={start.isPending || !!job} onClick={() => start.mutate()}>{tr("Compress videos")}</Button>}>
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Folder to compress")}>
                <Select value={root} onChange={(e) => setRoot(e.target.value)}>
                  <option value="">—</option>
                  {(targets.data ?? []).map((t) => <option key={t.path} value={t.path}>{t.label} · {kindLabel(t.kind)}</option>)}
                </Select>
              </Field>
              <Field label={tr("Ignore videos smaller than")}>
                <Select value={minSize} onChange={(e) => setMinSize(+e.target.value)}>
                  {[10 << 20, 100 << 20, 500 << 20, 1 << 30].map((n) => <option key={n} value={n}>{bytes(n)}</option>)}
                </Select>
              </Field>
            </div>
            <p className="text-muted text-[12px]">{tr("Videos are re-encoded one by one (H.265, CRF 28). A video is replaced only when the result is at least 20% smaller; snapshots and recycle bins are skipped. Long and CPU-heavy: run it when the NAS is idle.")}</p>
            {job && <div><div className="mb-2 text-[13px]">{tr("Compressing videos…")}</div><Progress value={job.progress} tone="accent" indeterminate={job.progress < 0} /></div>}
          </div>
        </Card>

        {data.isLoading ? <Spinner /> : !rep ? (!job && <Empty icon={<Film />} title={tr("No compression yet")} />) : (
          <>
            <div className="grid gap-4 sm:grid-cols-3">
              <Stat label={tr("Compressed")} value={counts("recoded").toLocaleString()} icon={<Film size={18} />} tone={counts("recoded") ? "ok" : "accent"} />
              <Stat label={tr("Kept")} value={counts("kept").toLocaleString()} icon={<Search size={18} />} />
              <Stat label={tr("Space saved")} value={bytes(rep.saved)} icon={<Film size={18} />} tone={rep.saved ? "ok" : "accent"} sub={tr("Search of {0}", dateTime(rep.started))} />
            </div>
            {rep.files.length === 0 ? <Empty icon={<Film />} title={tr("No video to compress")} /> : (
              <Card title={tr("Last compression")}>
                <div className="overflow-x-auto">
                  <table className="w-full text-[13px]">
                    <thead><tr className="border-b border-line text-left text-muted">
                      <th className="py-2 pr-4 font-medium">{tr("File")}</th>
                      <th className="py-2 pr-4 font-medium">{tr("Before")}</th>
                      <th className="py-2 pr-4 font-medium">{tr("After")}</th>
                      <th className="py-2 font-medium">{tr("Result")}</th>
                    </tr></thead>
                    <tbody>
                      {rep.files.slice(0, shown).map((f) => (
                        <tr key={f.path} className="border-b border-line last:border-0">
                          <td className="py-2 pr-4"><span className="mono break-all">{f.path}</span>{f.detail && <span className="block text-[11px] text-muted">{f.detail}</span>}</td>
                          <td className="py-2 pr-4 whitespace-nowrap">{bytes(f.old)}</td>
                          <td className="py-2 pr-4 whitespace-nowrap">{f.new ? bytes(f.new) : "—"}</td>
                          <td className="py-2 whitespace-nowrap"><Badge tone={f.status === "recoded" ? "ok" : f.status === "kept" ? "neutral" : "danger"}>{f.status === "recoded" ? tr("Compressed") : f.status === "kept" ? tr("Kept") : tr("Failed")}</Badge></td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                {rep.files.length > shown && <div className="mt-3"><Button onClick={() => setShown(shown + PAGE)}>{tr("Show more ({0} left)", rep.files.length - shown)}</Button></div>}
              </Card>
            )}
          </>
        )}
      </div>
    </div>
  );
}
