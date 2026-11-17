import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Download, File, Folder, History, RotateCcw } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import { bytes, dateTime } from "../lib/format";
import { Button, Checkbox, Empty, ErrorBox, Field, Modal, Notice, Select, Spinner, useUI } from "./ui";
import { tr } from "../lib/i18n";

type SnapPoint = { snapshot: string; time: string };
type SnapEntry = { name: string; dir: boolean; size?: number; modTime: string };
type RestoreResult = { restored: string[]; skipped: string[] };

/** Browses the snapshots of a folder and restores files or folders from them
 * (self-service, h_snapshots.go): deleted or overwritten items come back
 * without an administrator. */
export function SnapshotsBrowser({ path, name, onClose }: { path: string; name: string; onClose: () => void }) {
  const { confirm } = useUI();
  const qc = useQueryClient();
  const [snap, setSnap] = useState("");
  const [cur, setCur] = useState(path);
  const [sel, setSel] = useState<string[]>([]);
  const [result, setResult] = useState<RestoreResult | null>(null);
  const timeline = useQuery({ queryKey: ["snapshots", path], queryFn: () => api.get<{ points: SnapPoint[]; writable: boolean }>(`/api/files/snapshots${q({ path })}`) });
  const points = timeline.data?.points ?? [];
  const active = snap || points[0]?.snapshot || "";
  const activeTime = points.find((p) => p.snapshot === active)?.time ?? "";
  const browse = useQuery({
    queryKey: ["snapshot-browse", cur, active],
    queryFn: () => api.get<{ entries: SnapEntry[]; exists: boolean }>(`/api/files/snapshots/browse${q({ path: cur, snapshot: active })}`),
    enabled: !!active,
  });
  const entries = browse.data?.entries ?? [];
  const writable = !!timeline.data?.writable;
  const exists = browse.data?.exists ?? true;
  const restore = useAction((v: { names: string[]; replace: boolean }) =>
    api.post<RestoreResult>("/api/files/snapshots/restore", { path: cur, snapshot: active, ...v }), {
    success: (r) => tr("Restored: {0}", r.restored.join(", ")),
    onDone: (r) => { setResult(r); setSel([]); qc.invalidateQueries({ queryKey: ["files"] }); },
  });
  const doRestore = async (names: string[], replace: boolean) => {
    setResult(null);
    if (replace && !(await confirm({
      title: tr("Replace the file?"),
      text: tr("The current file is replaced by the version of {0}. It remains available in more recent snapshots.", dateTime(activeTime)),
      danger: true,
    }))) return;
    restore.mutate({ names, replace });
  };
  const below = cur.slice(path.length).split("/").filter(Boolean);
  const go = (p: string) => { setCur(p); setSel([]); setResult(null); };
  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-2"><History size={17} />{tr("Snapshots of {0}", name)}</span>}
      footer={<>
        <Button onClick={onClose}>{tr("Close")}</Button>
        <Button variant="primary" icon={<RotateCcw size={14} />} disabled={!sel.length || !writable || !exists} loading={restore.isPending} onClick={() => doRestore(sel, false)}>{sel.length ? tr("Restore {0} item(s)", sel.length) : tr("Restore")}</Button>
      </>}>
      <ErrorBox error={restore.error} />
      {result && result.skipped.length > 0 && <div className="mb-3"><Notice tone="warn">{tr("Skipped: {0}", result.skipped.join(", "))}</Notice></div>}
      {timeline.isLoading ? <Spinner /> : !points.length ? (
        <Empty icon={<History />} title={tr("No snapshots")} text={tr("Versions come from volume snapshots (Btrfs or ZFS): schedule snapshots in Scheduled Tasks.")} />
      ) : (
        <>
          <Field label={tr("Snapshot")}>
            <Select value={active} onChange={(e) => { setSnap(e.target.value); setResult(null); }}>
              {points.map((p) => <option key={p.snapshot} value={p.snapshot}>{dateTime(p.time)}</option>)}
            </Select>
          </Field>
          <div className="mb-2 flex flex-wrap items-center gap-1 text-[13px]">
            <button onClick={() => go(path)} className="flex items-center gap-1.5 rounded-lg px-2 py-1 hover:bg-panel-2"><History size={15} /> {name}</button>
            {below.map((c, i) => (
              <span key={i} className="flex items-center gap-1">
                <span className="text-muted">/</span>
                <button onClick={() => go(`${path}/${below.slice(0, i + 1).join("/")}`)} className="rounded-lg px-2 py-1 hover:bg-panel-2">{c}</button>
              </span>
            ))}
          </div>
          <ErrorBox error={browse.error} />
          {!exists && <div className="mb-3"><Notice tone="warn">{tr("This folder no longer exists in the share. Go back and restore it from the snapshot.")}</Notice></div>}
          {browse.isLoading ? <Spinner /> : browse.data && entries.length === 0 ? (
            <Empty icon={<Folder />} title={tr("Empty folder")} />
          ) : (
            <div className="overflow-x-auto">
              <table className="table">
                <thead><tr><th className="w-10"><Checkbox checked={entries.length > 0 && sel.length === entries.length} onChange={(v) => setSel(v ? entries.map((e) => e.name) : [])} /></th><th>{tr("Name")}</th><th>{tr("Size")}</th><th>{tr("Modified on")}</th><th /></tr></thead>
                <tbody>
                  {entries.map((e) => (
                    <tr key={e.name} onDoubleClick={() => e.dir && go(`${cur}/${e.name}`)}>
                      <td><Checkbox checked={sel.includes(e.name)} onChange={(v) => setSel(v ? [...sel, e.name] : sel.filter((x) => x !== e.name))} /></td>
                      <td>
                        <button className="flex items-center gap-2.5 text-left" onClick={() => e.dir && go(`${cur}/${e.name}`)}>
                          {e.dir ? <Folder size={18} className="text-accent" fill="currentColor" fillOpacity={0.15} /> : <File size={18} className="text-muted" />}
                          <span className="hover:text-accent">{e.name}</span>
                        </button>
                      </td>
                      <td className="text-muted">{e.dir ? "—" : bytes(e.size ?? 0)}</td>
                      <td className="text-muted text-[12.5px]">{dateTime(e.modTime)}</td>
                      <td className="text-right whitespace-nowrap">
                        <a href={`/api/files/snapshots/download${q({ path: cur, snapshot: active, name: e.name })}`} title={tr("Download")}><Button size="sm" variant="ghost" icon={<Download size={14} />} /></a>
                        {writable && exists && <Button size="sm" variant="ghost" loading={restore.isPending && restore.variables?.names[0] === e.name && !restore.variables.replace} onClick={() => doRestore([e.name], false)}>{tr("Restore a copy")}</Button>}
                        {writable && exists && !e.dir && <Button size="sm" variant="ghost" icon={<RotateCcw size={14} />} title={tr("Replace the current file")} onClick={() => doRestore([e.name], true)} />}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <p className="mt-3 text-muted text-[12px]">{tr("Folders are always restored as a copy.")}</p>
        </>
      )}
    </Modal>
  );
}
