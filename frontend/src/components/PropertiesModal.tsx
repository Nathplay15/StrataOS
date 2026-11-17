import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Info } from "lucide-react";
import { api, q } from "../lib/api";
import { useAction } from "../lib/hooks";
import { bytes, dateTime } from "../lib/format";
import { fileKind } from "../lib/filetypes";
import { Button, ErrorBox, Input, Modal, Spinner, useUI } from "./ui";
import { tr } from "../lib/i18n";

export type FileProps = {
  name: string;
  path: string;
  dir: boolean;
  link: boolean;
  linkTarget?: string;
  size: number;
  files?: number;
  dirs?: number;
  totalSize?: number;
  truncated?: boolean;
  mode: string;
  octal: string;
  owner: string;
  group: string;
  modTime: string;
  hash?: string;
  hashError?: string;
};

const kindLabel = (name: string, dir: boolean) => {
  if (dir) return tr("Folder");
  switch (fileKind(name)) {
    case "image": return tr("Image");
    case "video": return tr("Video");
    case "audio": return tr("Audio");
    case "pdf": return "PDF";
    case "archive": return tr("Archive");
    case "docx": case "document": return tr("Document");
    case "sheet": return tr("Spreadsheet");
    case "text": case "markdown": return tr("Text");
  }
  return tr("Other");
};

const Row = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <div className="flex items-start gap-3 py-1.5 text-[13px]">
    <span className="w-36 shrink-0 text-muted">{label}</span>
    <span className="min-w-0 flex-1 break-all">{children}</span>
  </div>
);

/** Everything about one file or folder: sizes, owner, permissions, checksum. */
export function PropertiesModal({ path, writable, onClose }: { path: string; writable: boolean; onClose: () => void }) {
  const { toast } = useUI();
  const qc = useQueryClient();
  const [hashed, setHashed] = useState(false);
  const [octal, setOctal] = useState<string | null>(null);
  const { data, isLoading, error } = useQuery({
    queryKey: ["file-props", path, hashed],
    queryFn: () => api.get<FileProps>(`/api/files/properties${q({ path, hash: hashed ? 1 : undefined })}`),
  });
  const chmod = useAction((p: { path: string; mode: string }) => api.post("/api/files/chmod", p), {
    success: tr("Permissions updated"),
    onDone: () => qc.invalidateQueries({ queryKey: ["file-props"] }),
  });
  const p = data;
  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><Info size={17} />{tr("Properties")}</span>} footer={<Button onClick={onClose}>{tr("Close")}</Button>}>
      <ErrorBox error={error} />
      {isLoading || !p ? <Spinner /> : (
        <>
          <div className="mb-2 flex items-center gap-2.5 text-[15px] font-medium break-all">
            <Info size={18} className="text-accent" />{p.name}
          </div>
          <div className="divide-y divide-panel-2">
            <Row label={tr("Type")}>{kindLabel(p.name, p.dir)}{p.link ? ` (${tr("Symlink")})` : ""}</Row>
            <Row label={tr("Size")}>
              {p.dir
                ? <>{tr("{0} files, {1} folders", p.files ?? 0)}{p.totalSize ? <> · {bytes(p.totalSize)}</> : null}{p.truncated ? <> · {tr("Partial (too many items)")}</> : null}</>
                : bytes(p.size)}
            </Row>
            <Row label={tr("Location")}>{p.path.slice(0, p.path.length - p.name.length - 1) || "/"}</Row>
            <Row label={tr("Modified")}>{dateTime(p.modTime)}</Row>
            <Row label={tr("Owner")}>{p.owner}</Row>
            <Row label={tr("Group")}>{p.group}</Row>
            <Row label={tr("Permissions")}>
              {writable && !p.link ? (
                <span className="flex items-center gap-2">
                  <Input className="w-24 mono" value={octal ?? p.octal} onChange={(e) => setOctal(e.target.value)} onKeyDown={(e) => e.key === "Enter" && octal && octal !== p.octal && chmod.mutate({ path, mode: octal })} />
                  <Button size="sm" variant="ghost" loading={chmod.isPending} disabled={!octal || octal === p.octal} onClick={() => chmod.mutate({ path, mode: octal! })}>{tr("Save")}</Button>
                  <span className="mono text-muted text-[12px]">{p.mode}</span>
                </span>
              ) : <span className="mono">{p.octal} · {p.mode}</span>}
            </Row>
            {p.link && <Row label={tr("Symlink target")}>{p.linkTarget || "?"}</Row>}
            {!p.dir && !p.link && (
              <Row label={tr("SHA-256 checksum")}>
                {p.hash ? (
                  <span className="flex items-center gap-2">
                    <span className="mono text-[11.5px] break-all">{p.hash}</span>
                    <Button size="sm" variant="ghost" icon={<Copy size={14} />} title={tr("Copy")} onClick={() => { navigator.clipboard.writeText(p.hash!); toast(tr("Copied")); }} />
                  </span>
                ) : p.hashError ? <span className="text-muted">{p.hashError}</span> : (
                  <Button size="sm" variant="ghost" disabled={hashed} loading={hashed && !p.hash} onClick={() => setHashed(true)}>{tr("Compute")}</Button>
                )}
              </Row>
            )}
          </div>
        </>
      )}
    </Modal>
  );
}
