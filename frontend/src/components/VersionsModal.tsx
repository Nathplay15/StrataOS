import { useQuery } from "@tanstack/react-query";
import { Download, History, RotateCcw } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import { bytes, dateTime } from "../lib/format";
import { Button, Empty, ErrorBox, Modal, Spinner, useUI } from "./ui";
import { tr } from "../lib/i18n";

type Version = { snapshot: string; time: string; size: number; modTime: string };

/** Earlier versions of a file, kept in the snapshots of its volume. */
export function VersionsModal({ path, name, onClose }: { path: string; name: string; onClose: () => void }) {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["versions", path], queryFn: () => api.get<{ versions: Version[]; writable: boolean }>(`/api/files/versions${q({ path })}`) });
  const restore = useAction((v: { snapshot: string; replace: boolean }) => api.post<{ path: string }>("/api/files/versions/restore", { path, ...v }), { success: (r) => tr("Version restaurée : {0}", r.path.split("/").pop() ?? "") });
  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-2"><History size={17} />{tr("Versions de « {0} »", name)}</span>} footer={<Button onClick={onClose}>{tr("Fermer")}</Button>}>
      <ErrorBox error={error || restore.error} />
      {isLoading ? <Spinner /> : !data?.versions.length ? (
        <Empty icon={<History />} title={tr("Aucune version antérieure")} text={tr("Les versions viennent des snapshots du volume (Btrfs ou ZFS) : programmez des snapshots dans Tâches planifiées.")} />
      ) : (
        <table className="table">
          <thead><tr><th>{tr("Snapshot du")}</th><th>{tr("Modifié le")}</th><th>{tr("Taille")}</th><th /></tr></thead>
          <tbody>
            {data.versions.map((v) => (
              <tr key={v.snapshot}>
                <td>{dateTime(v.time)}</td>
                <td className="text-muted text-[12.5px]">{dateTime(v.modTime)}</td>
                <td className="text-muted">{bytes(v.size)}</td>
                <td className="whitespace-nowrap text-right">
                  <a href={`/api/files/versions/download${q({ path, snapshot: v.snapshot })}`} title={tr("Télécharger")}><Button size="sm" variant="ghost" icon={<Download size={14} />} /></a>
                  {data.writable && <>
                    <Button size="sm" variant="ghost" loading={restore.isPending && restore.variables?.snapshot === v.snapshot && !restore.variables.replace} onClick={() => restore.mutate({ snapshot: v.snapshot, replace: false })}>{tr("Restaurer une copie")}</Button>
                    <Button size="sm" variant="ghost" icon={<RotateCcw size={14} />} title={tr("Remplacer le fichier actuel")} onClick={async () => { if (await confirm({ title: tr("Remplacer le fichier ?"), text: tr("Le fichier actuel est remplacé par la version du {0}. Il reste disponible dans les snapshots plus récents.", dateTime(v.time)), danger: true })) restore.mutate({ snapshot: v.snapshot, replace: true }); }} />
                  </>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <p className="mt-3 text-muted text-[12px]">{tr("Sous Windows, ces versions apparaissent aussi dans Propriétés → Versions précédentes du partage.")}</p>
    </Modal>
  );
}
