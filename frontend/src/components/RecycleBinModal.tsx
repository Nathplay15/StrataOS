import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArchiveRestore, File, Folder, History, Trash2 } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import { bytes, dateTime } from "../lib/format";
import { Button, Checkbox, Empty, ErrorBox, Modal, Spinner, useUI } from "./ui";
import { tr } from "../lib/i18n";

/** A deleted file (or empty folder), at any depth of the bin: it comes back at its original place. */
type RecycleItem = { path: string; origin: string; name: string; dir: boolean; size: number; deleted: string };

/** The recycle bin of a share: what SMB and the web deleted, restorable. */
export function RecycleBinModal({ share, onClose }: { share: string; onClose: () => void }) {
  const { confirm } = useUI();
  const [sel, setSel] = useState<string[]>([]);
  const { data, isLoading, error } = useQuery({
    queryKey: ["recycle", share],
    queryFn: () => api.get<{ share: string; items: RecycleItem[] }>(`/api/files/recycle${q({ share })}`),
  });
  const restore = useAction((paths: string[]) => api.post<{ restored: string[] }>("/api/files/recycle/restore", { paths }), {
    success: (r: { restored: string[] }) => tr("{0} item(s) restored", String(r.restored.length)),
  });
  const empty = useAction((paths: string[]) => api.post<{ deleted: number }>("/api/files/recycle/empty", { share, paths }), {
    success: () => tr("Recycle bin emptied"),
  });
  const items = data?.items ?? [];
  const del = async (paths: string[]) => {
    if (await confirm({ title: tr("Delete permanently?"), text: tr("{0} item(s) will be deleted permanently. This action is irreversible.", String(paths.length)), danger: true })) empty.mutate(paths);
  };
  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-2"><History size={17} />{tr("Recycle bin")}</span>} footer={<>
      {items.length > 0 && <Button variant="danger" icon={<Trash2 size={14} />} onClick={() => del([])}>{tr("Empty the recycle bin")}</Button>}
      <Button onClick={onClose}>{tr("Close")}</Button>
    </>}>
      <ErrorBox error={error || restore.error || empty.error} />
      {isLoading ? <Spinner /> : items.length === 0 ? (
        <Empty icon={<History />} title={tr("No deleted files")} text={tr("Deleted files from this share are kept here until they are purged automatically.")} />
      ) : (
        <>
          {sel.length > 0 && (
            <div className="card mb-3 flex items-center gap-3 px-4 py-2.5 text-[13px]">
              <span>{sel.length} {" "}{tr("item(s) selected")}</span>
              <div className="flex-1" />
              <Button size="sm" variant="ghost" icon={<ArchiveRestore size={14} />} onClick={() => { restore.mutate(sel); setSel([]); }}>{tr("Restore")}</Button>
              <Button size="sm" variant="danger" icon={<Trash2 size={14} />} onClick={() => del(sel)}>{tr("Delete")}</Button>
            </div>
          )}
          <table className="table">
            <thead>
              <tr>
                <th className="w-10"><Checkbox checked={sel.length === items.length} onChange={(v) => setSel(v ? items.map((i) => i.path) : [])} /></th>
                <th>{tr("Name")}</th><th>{tr("Original location")}</th><th>{tr("Size")}</th><th>{tr("Deleted on")}</th><th />
              </tr>
            </thead>
            <tbody>
              {items.map((i) => (
                <tr key={i.path}>
                  <td><Checkbox checked={sel.includes(i.path)} onChange={(v) => setSel(v ? [...sel, i.path] : sel.filter((x) => x !== i.path))} /></td>
                  <td>
                    <span className="flex items-center gap-2.5">
                      {i.dir ? <Folder size={18} className="text-accent" fill="currentColor" fillOpacity={0.15} /> : <File size={18} className="text-muted" />}
                      <span>{i.name}</span>
                    </span>
                  </td>
                  <td className="text-muted text-[12.5px]">{i.origin.slice(0, i.origin.length - i.name.length - 1) || "/"}</td>
                  <td className="text-muted">{i.dir ? tr("Empty folder") : bytes(i.size)}</td>
                  <td className="text-muted text-[12.5px]">{dateTime(i.deleted)}</td>
                  <td className="whitespace-nowrap text-right">
                    <Button size="sm" variant="ghost" icon={<ArchiveRestore size={14} />} title={tr("Restore")} loading={restore.isPending && restore.variables?.includes(i.path)} onClick={() => restore.mutate([i.path])} />
                    <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} title={tr("Delete")} onClick={() => del([i.path])} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </Modal>
  );
}
