import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Database, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { dateTime } from "../lib/format";
import { tr } from "../lib/i18n";
import { Badge, Button, ErrorBox, Field, Input, Modal, Notice, Spinner, useUI } from "./ui";

interface MyS3 {
  enabled: boolean;
  endpoint: string;
  domain?: string;
  region: string;
  keys: { access: string; name: string; created: string }[];
  buckets: { name: string; share: string; perm: string; readOnly?: boolean }[];
}

function Copyable({ value }: { value: string }) {
  const { toast } = useUI();
  return (
    <div className="flex items-center gap-2">
      <code className="mono min-w-0 flex-1 truncate rounded-lg bg-panel-2 px-2.5 py-1.5 text-[12.5px]">{value}</code>
      <Button size="sm" variant="ghost" title={tr("Copy")} icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(value); toast(tr("Copied")); }} />
    </div>
  );
}

/** The user's S3 access keys and the buckets they reach. */
export function S3KeysModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { confirm, toast } = useUI();
  const { data, error } = useQuery({ queryKey: ["my-s3"], queryFn: () => api.get<MyS3>("/api/me/s3"), enabled: open });
  const [name, setName] = useState("");
  const [created, setCreated] = useState<{ access: string; secret: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (!open) { setCreated(null); setName(""); setErr(null); } }, [open]);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try { await fn(); } catch (e) { setErr(e); } finally { setBusy(false); qc.invalidateQueries({ queryKey: ["my-s3"] }); }
  };
  const create = () => run(async () => {
    setCreated(await api.post<{ access: string; secret: string }>("/api/me/s3/keys", { name }));
    setName("");
  });
  const revoke = async (access: string) => {
    if (!(await confirm({ title: tr("Revoke the key {0}?", access), text: tr("Applications using it lose access at once."), danger: true }))) return;
    run(async () => { await api.del(`/api/me/s3/keys/${access}`); toast(tr("Key revoked")); });
  };
  return (
    <Modal open={open} onClose={onClose} title={tr("S3 access keys")} wide>
      {!data ? <><ErrorBox error={error} /><Spinner /></> : (
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("S3 keys let backup software, rclone, the aws command or an application read and write your shared folders as S3 buckets, with the same rights as over SMB. Give each application its own key.")}</p>
          {!data.enabled && <Notice tone="warn">{tr("The S3 service is turned off on this NAS: an administrator can turn it on in File services.")}</Notice>}
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={tr("Endpoint")}><Copyable value={data.endpoint} /></Field>
            <Field label={tr("Region")}><Copyable value={data.region} /></Field>
          </div>
          <div>
            <p className="mb-1 font-medium">{tr("Your buckets")}</p>
            {data.buckets.length === 0 ? <p className="text-muted text-[13px]">{tr("No shared folder is exposed to you over S3.")}</p> : (
              <div className="flex flex-wrap gap-1.5">
                {data.buckets.map((b) => <Badge key={b.name} tone={b.perm === "rw" ? "ok" : "neutral"} title={b.share}>{b.name} · {b.perm === "rw" ? tr("read-write") : tr("read-only")}</Badge>)}
              </div>
            )}
            <p className="mt-1 text-muted text-[12px]">{tr("Address the buckets by their name: listing all buckets shows none (they belong to the NAS).")}</p>
          </div>

          {created && (
            <div className="space-y-2 rounded-xl border border-border p-4">
              <p className="font-medium">{tr("New key: copy the secret now, it will not be shown again")}</p>
              <Field label={tr("Access key")}><Copyable value={created.access} /></Field>
              <Field label={tr("Secret key")}><Copyable value={created.secret} /></Field>
              <p className="text-muted text-[12px]">rclone: <code className="mono">rclone config create nas s3 provider=Other endpoint={data.endpoint} access_key_id={created.access} secret_access_key=… no_check_bucket=true</code></p>
            </div>
          )}

          <div className="divide-y divide-border rounded-xl border border-border">
            {data.keys.length === 0 && <p className="px-4 py-3 text-muted text-[13px]">{tr("No key yet.")}</p>}
            {data.keys.map((k) => (
              <div key={k.access} className="flex items-center gap-3 px-4 py-2.5">
                <Database size={16} className="text-accent" />
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{k.name}</div>
                  <div className="mono text-muted text-[12px]">{k.access} · {dateTime(k.created)}</div>
                </div>
                <Button size="sm" variant="ghost" className="text-danger" title={tr("Revoke")} icon={<Trash2 size={14} />} onClick={() => revoke(k.access)} />
              </div>
            ))}
          </div>
          <div className="flex items-end gap-2">
            <Field label={tr("Name of the new key")} className="flex-1"><Input value={name} maxLength={64} placeholder={tr("e.g. Backup laptop")} onChange={(e) => setName(e.target.value)} /></Field>
            <Button variant="primary" icon={<Plus size={15} />} loading={busy} onClick={create}>{tr("Create a key")}</Button>
          </div>
          <ErrorBox error={err} />
        </div>
      )}
    </Modal>
  );
}
