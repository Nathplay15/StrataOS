import { useState } from "react";
import { Copy, Link2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { ShareLink } from "../lib/types";
import { Button, ErrorBox, Field, Input, Modal, Notice, Select, Switch, useUI } from "./ui";
import { tr } from "../lib/i18n";

export const linkURL = (id: string) => `${location.origin}/s/${id}`;

/** Creates a public link to a file, a folder or a photo album. */
export function ShareLinkModal({ path, album, dir, name, onClose }: { path?: string; album?: string; dir?: boolean; name: string; onClose: () => void }) {
  const { toast } = useUI();
  const [f, setF] = useState({ download: true, upload: false, password: "", expiresDays: 7, maxDownloads: 0 });
  const [made, setMade] = useState<ShareLink | null>(null);
  const create = useAction(() => api.post<ShareLink>("/api/links", { path, album, ...f }), { onDone: (l) => setMade(l) });
  const copy = (u: string) => { navigator.clipboard?.writeText(u); toast(tr("Link copied")); };

  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><Link2 size={17} />{tr("Share \"{0}\"", name)}</span>} footer={made ? <Button variant="primary" onClick={onClose}>{tr("Completed")}</Button> : <>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={create.isPending} disabled={!f.download && !f.upload} onClick={() => create.mutate()}>{tr("Create link")}</Button>
    </>}>
      {made ? (
        <div className="space-y-3">
          <p className="text-muted text-[13px]">{tr("Anyone with this link can open it, without an account on the NAS.")}</p>
          <div className="flex gap-2"><Input readOnly className="mono" value={linkURL(made.id)} onFocus={(e) => e.target.select()} /><Button icon={<Copy size={15} />} onClick={() => copy(linkURL(made.id))}>{tr("Copy")}</Button></div>
          <Notice>{tr("To open it from the Internet, the NAS must be reachable from outside (port forwarding or dynamic DNS in Remote Access). Find your links in Shared links.")}</Notice>
        </div>
      ) : (
        <div className="space-y-4">
          {!album && <Switch checked={f.download} onChange={(v) => setF({ ...f, download: v })} label={dir ? tr("View and download content") : tr("Download file")} />}
          {dir && <Switch checked={f.upload} onChange={(v) => setF({ ...f, upload: v })} label={tr("Upload files")} hint={tr("Visitors can upload files to this folder, without ever replacing existing ones. Without download, the content stays hidden (drop box).")} />}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Expiration")}>
              <Select value={f.expiresDays} onChange={(e) => setF({ ...f, expiresDays: +e.target.value })}>
                <option value={1}>{tr("1 day")}</option>
                <option value={7}>{tr("7 days")}</option>
                <option value={30}>{tr("30 days")}</option>
                <option value={90}>{tr("90 days")}</option>
                <option value={0}>{tr("Never")}</option>
              </Select>
            </Field>
            <Field label={tr("Maximum downloads")} hint={tr("0 = unlimited")}><Input type="number" min={0} value={f.maxDownloads} onChange={(e) => setF({ ...f, maxDownloads: Math.max(0, +e.target.value) })} /></Field>
          </div>
          <Field label={tr("Password (optional)")}><Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
          <ErrorBox error={create.error} />
        </div>
      )}
    </Modal>
  );
}
