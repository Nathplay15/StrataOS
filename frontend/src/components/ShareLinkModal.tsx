import { useState } from "react";
import { Copy, Inbox, Link2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { ShareLink } from "../lib/types";
import { Button, ErrorBox, Field, Input, Modal, Notice, Select, Switch, Tabs, useUI } from "./ui";
import { tr } from "../lib/i18n";

export const linkURL = (id: string) => `${location.origin}/s/${id}`;

/** Options shared by creation and editing: limits and file request settings. */
export type LinkForm = {
  password: string; expires: string; maxDownloads: number;
  title: string; message: string; askGuest: "" | "optional" | "required"; guestFolders: boolean; maxFileMb: number; maxTotalMb: number; notify: boolean;
};

const day = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
/** Expiry date as typed (yyyy-mm-dd, end of that day locally) → RFC 3339, "" = never. */
export const expiresISO = (v: string) => (v ? new Date(`${v}T23:59:59`).toISOString() : "");
export const inDays = (n: number) => day(new Date(Date.now() + n * 86400e3));
export const dateInput = (iso?: string) => (iso ? day(new Date(iso)) : "");

export function LinkFields({ f, setF, upload, download, editing }: { f: LinkForm; setF: (f: LinkForm) => void; upload: boolean; download: boolean; editing?: boolean }) {
  return (
    <div className="space-y-4">
      {upload && (
        <div className="space-y-3 rounded-xl border border-line p-3">
          <Field label={tr("Title shown to visitors")}><Input value={f.title} maxLength={120} onChange={(e) => setF({ ...f, title: e.target.value })} placeholder={tr("Send me your holiday photos")} /></Field>
          <Field label={tr("Message (optional)")}><textarea className="input h-20" maxLength={2000} value={f.message} onChange={(e) => setF({ ...f, message: e.target.value })} /></Field>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label={tr("Name and email of the sender")}>
              <Select value={f.askGuest} onChange={(e) => setF({ ...f, askGuest: e.target.value as LinkForm["askGuest"] })}>
                <option value="">{tr("Not asked")}</option>
                <option value="optional">{tr("Asked (optional)")}</option>
                <option value="required">{tr("Required")}</option>
              </Select>
            </Field>
            <Field label={tr("Maximum size per file (MB)")} hint={tr("0 = unlimited")}><Input type="number" min={0} value={f.maxFileMb} onChange={(e) => setF({ ...f, maxFileMb: Math.max(0, +e.target.value) })} /></Field>
            <Field label={tr("Maximum in total (MB)")} hint={tr("0 = unlimited")}><Input type="number" min={0} value={f.maxTotalMb} onChange={(e) => setF({ ...f, maxTotalMb: Math.max(0, +e.target.value) })} /></Field>
          </div>
          {f.askGuest && <Switch checked={f.guestFolders} onChange={(v) => setF({ ...f, guestFolders: v })} label={tr("A sub-folder per sender")} hint={tr("Files of a sender who gives their name go into a folder with that name.")} />}
          <Switch checked={f.notify} onChange={(v) => setF({ ...f, notify: v })} label={tr("Notify me when files arrive")} hint={tr("Administrators: through the NAS notifications. Users: by email, when the NAS can send mail and your account has an address.")} />
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={tr("Expiration")} hint={tr("Empty = never")}><Input type="date" min={inDays(1)} value={f.expires} onChange={(e) => setF({ ...f, expires: e.target.value })} /></Field>
        {download && <Field label={tr("Maximum downloads")} hint={tr("0 = unlimited")}><Input type="number" min={0} value={f.maxDownloads} onChange={(e) => setF({ ...f, maxDownloads: Math.max(0, +e.target.value) })} /></Field>}
      </div>
      <Field label={editing ? tr("New password (optional)") : tr("Password (optional)")}><Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
    </div>
  );
}

export const linkPayload = (f: LinkForm) => ({
  expires: expiresISO(f.expires), maxDownloads: f.maxDownloads, title: f.title, message: f.message, askGuest: f.askGuest,
  guestFolders: f.guestFolders, maxFileMb: f.maxFileMb, maxTotalMb: f.maxTotalMb, notify: f.notify, ...(f.password ? { password: f.password } : {}),
});

/** Creates a public link to a file, a folder or a photo album, or a file request on a folder. */
export function ShareLinkModal({ path, album, dir, name, onClose }: { path?: string; album?: string; dir?: boolean; name: string; onClose: () => void }) {
  const { toast } = useUI();
  const [mode, setMode] = useState<"share" | "request">("share");
  const [access, setAccess] = useState({ download: true, upload: false });
  const [f, setF] = useState<LinkForm>({ password: "", expires: inDays(7), maxDownloads: 0, title: "", message: "", askGuest: "", guestFolders: false, maxFileMb: 0, maxTotalMb: 0, notify: false });
  const [made, setMade] = useState<ShareLink | null>(null);
  const req = mode === "request" ? { download: false, upload: true } : access;
  const create = useAction(() => api.post<ShareLink>("/api/links", { path, album, ...req, ...linkPayload(f) }), { onDone: (l) => setMade(l) });
  const copy = (u: string) => { navigator.clipboard?.writeText(u); toast(tr("Link copied")); };
  const pick = (m: "share" | "request") => { setMode(m); if (m === "request") setF({ ...f, notify: true, expires: inDays(30) }); };

  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2">{mode === "request" ? <Inbox size={17} /> : <Link2 size={17} />}{mode === "request" ? tr("Request files in \"{0}\"", name) : tr("Share \"{0}\"", name)}</span>} footer={made ? <Button variant="primary" onClick={onClose}>{tr("Completed")}</Button> : <>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={create.isPending} disabled={!req.download && !req.upload} onClick={() => create.mutate()}>{tr("Create link")}</Button>
    </>}>
      {made ? (
        <div className="space-y-3">
          <p className="text-muted text-[13px]">{mode === "request" ? tr("Anyone with this link can send you files, without an account and without seeing what the folder holds.") : tr("Anyone with this link can open it, without an account on the NAS.")}</p>
          <div className="flex gap-2"><Input readOnly className="mono" value={linkURL(made.id)} onFocus={(e) => e.target.select()} /><Button icon={<Copy size={15} />} onClick={() => copy(linkURL(made.id))}>{tr("Copy")}</Button></div>
          <Notice>{tr("To open it from the Internet, the NAS must be reachable from outside (see the assistant in Remote access). Find your links in Shared links.")}</Notice>
        </div>
      ) : (
        <div className="space-y-4">
          {dir && !album && <Tabs value={mode} onChange={pick} tabs={[{ id: "share", label: tr("Share") }, { id: "request", label: tr("Request files") }]} />}
          {mode === "share" && !album && <Switch checked={access.download} onChange={(v) => setAccess({ ...access, download: v })} label={dir ? tr("View and download content") : tr("Download file")} />}
          {mode === "share" && dir && <Switch checked={access.upload} onChange={(v) => setAccess({ ...access, upload: v })} label={tr("Upload files")} hint={tr("Visitors can upload files to this folder, without ever replacing existing ones. Without download, the content stays hidden (drop box).")} />}
          {mode === "request" && <p className="text-[13px] text-muted">{tr("Guests can only upload: they never see the content of the folder, and never replace a file.")}</p>}
          <LinkFields f={f} setF={setF} upload={req.upload} download={req.download} />
          <ErrorBox error={create.error} />
        </div>
      )}
    </Modal>
  );
}
