import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, Plug, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { dateTime } from "../lib/format";
import { openAccountDialog } from "../lib/intent";
import { srv, tr } from "../lib/i18n";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, Select, Spinner, Switch } from "./ui";

type Bucket = { name: string; share: string; path?: string; issue?: string; readOnly?: boolean; keys: number };
type S3 = { installed: boolean; enabled: boolean; running: boolean; port: number; domain: string; buckets: Bucket[]; shares: string[]; endpoint: string };
type Entry = { time: string; ip: string; access: string; user?: string; op: string; bucket: string; key: string; status: number; error?: string };

const bucketName = (share: string) => share.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/-+/g, "-").replace(/^-|-$/g, "").slice(0, 63).padEnd(3, "0");

/** File services card: the built-in S3 service (versitygw). */
export function S3Service() {
  const qc = useQueryClient();
  const { data, error } = useQuery({ queryKey: ["s3"], queryFn: () => api.get<S3>("/api/s3") });
  const [f, setF] = useState<{ enabled: boolean; port: number; domain: string; buckets: { name: string; share: string }[] } | null>(null);
  const [log, setLog] = useState(false);
  useEffect(() => { if (data) setF({ enabled: data.enabled, port: data.port, domain: data.domain, buckets: data.buckets.map((b) => ({ name: b.name, share: b.share })) }); }, [data]);
  const save = useAction(() => api.put("/api/s3", f), { success: tr("S3 service applied"), onDone: () => qc.invalidateQueries({ queryKey: ["s3"] }) });
  const install = useAction(() => api.post("/api/system/components/s3/install"), { onDone: () => qc.invalidateQueries({ queryKey: ["s3"] }) });
  const title = <span className="flex items-center gap-2"><Database size={16} className="text-accent" />{tr("S3 (built-in)")}</span>;
  if (!data || !f) return <Card title={title}><ErrorBox error={error} /><Spinner /></Card>;
  const free = data.shares.filter((s) => !f.buckets.some((b) => b.share === s));
  const dirty = JSON.stringify(f) !== JSON.stringify({ enabled: data.enabled, port: data.port, domain: data.domain, buckets: data.buckets.map((b) => ({ name: b.name, share: b.share })) });
  return (
    <Card title={title} actions={data.installed && <Button size="sm" variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate()}>{tr("Apply")}</Button>}>
      <div className="space-y-4">
        <p className="text-[13px]">{tr("Exposes shared folders as S3 buckets for backup software, rclone, the aws command or applications. The objects are the folder's own files, also visible over SMB and in Files; each user creates access keys, whose rights are those of the shared folder (read or read-write; read-only over quota).")}</p>
        <Notice>{tr("Differs from the MinIO app of the App Center: MinIO keeps its own storage in a folder, with its own users and buckets, and its own console. The built-in service has no storage of its own and follows the NAS accounts and shared folder permissions. Use MinIO for an object store separate from your files.")}</Notice>
        {!data.installed ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="text-muted text-[13px]">{tr("The S3 service (versitygw, a single program checked by its checksum) is not installed.")}</div>
            <Button icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>
          </div>
        ) : (
          <>
            <Switch checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} label={tr("Enable the S3 service")} hint={data.enabled ? (data.running ? tr("Running") : tr("Not running: see the event log")) : undefined} />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={tr("Port")} hint={tr("HTTPS with the NAS certificate")}><Input type="number" min={1024} max={65535} value={f.port} onChange={(e) => setF({ ...f, port: +e.target.value })} /></Field>
              <Field label={tr("Domain for virtual-host style (optional)")} hint={tr("bucket.domain needs a DNS entry (wildcard) to the NAS; the path style (endpoint/bucket) always works")}><Input value={f.domain} placeholder="s3.nas.example.com" onChange={(e) => setF({ ...f, domain: e.target.value })} /></Field>
            </div>
            {data.enabled && <Field label={tr("Endpoint")}><code className="mono block truncate rounded-lg bg-panel-2 px-2.5 py-1.5 text-[12.5px]">{data.endpoint}</code></Field>}
            <div>
              <p className="mb-2 font-medium">{tr("Buckets")}</p>
              {f.buckets.length === 0 && <p className="text-muted text-[13px]">{tr("No shared folder exposed yet.")}</p>}
              <div className="space-y-2">
                {f.buckets.map((b, i) => {
                  const live = data.buckets.find((x) => x.share === b.share);
                  return (
                    <div key={b.share} className="flex flex-wrap items-center gap-2">
                      <Input className="mono max-w-60" value={b.name} onChange={(e) => setF({ ...f, buckets: f.buckets.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)) })} />
                      <span className="text-muted text-[13px]">→ {b.share}</span>
                      {live && <Badge>{tr("{0} key(s) with access", live.keys)}</Badge>}
                      {live?.readOnly && <Badge tone="warn">{tr("Read-only (quota)")}</Badge>}
                      {live?.issue && <Badge tone="danger">{srv(live.issue)}</Badge>}
                      <Button size="sm" variant="ghost" className="text-danger" title={tr("Remove")} icon={<Trash2 size={14} />} onClick={() => setF({ ...f, buckets: f.buckets.filter((_, j) => j !== i) })} />
                    </div>
                  );
                })}
              </div>
              {free.length > 0 && (
                <div className="mt-2 flex items-center gap-2">
                  <Select className="max-w-60" value="" onChange={(e) => e.target.value && setF({ ...f, buckets: [...f.buckets, { share: e.target.value, name: bucketName(e.target.value) }] })}>
                    <option value="">{tr("Expose a shared folder…")}</option>
                    {free.map((s) => <option key={s} value={s}>{s}</option>)}
                  </Select>
                  <Plus size={15} className="text-muted" />
                </div>
              )}
            </div>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={() => openAccountDialog("s3")}>{tr("My S3 access keys")}</Button>
              <Button size="sm" onClick={() => setLog(true)}>{tr("Recent requests")}</Button>
            </div>
            <p className="text-muted text-[12px]">{tr("Not supported: versioning, object lock, encrypted shared folders. Uploads in progress use a hidden .sgwtmp folder inside the shared folder. Listing all buckets shows none: clients address a bucket by its name.")}</p>
            <ErrorBox error={save.error || install.error} />
          </>
        )}
      </div>
      {log && <S3Log onClose={() => setLog(false)} />}
    </Card>
  );
}

function S3Log({ onClose }: { onClose: () => void }) {
  const { data, error } = useQuery({ queryKey: ["s3-log"], queryFn: () => api.get<Entry[]>("/api/s3/log?lines=300"), refetchInterval: 10000 });
  return (
    <Modal open onClose={onClose} title={tr("S3 requests")} wide="xl">
      {!data ? <><ErrorBox error={error} /><Spinner /></> : data.length === 0 ? <p className="text-muted text-[13px]">{tr("No request yet.")}</p> : (
        <div className="max-h-[60vh] overflow-auto">
          <table className="table text-[12.5px]">
            <thead><tr><th>{tr("Time")}</th><th>{tr("User")}</th><th>{tr("Operation")}</th><th>{tr("Bucket")}</th><th>{tr("Object")}</th><th>{tr("Result")}</th><th>{tr("Address")}</th></tr></thead>
            <tbody>
              {data.map((e, i) => (
                <tr key={i}>
                  <td className="whitespace-nowrap">{dateTime(e.time)}</td>
                  <td>{e.user ?? <span className="mono text-muted">{e.access || "-"}</span>}</td>
                  <td>{e.op}</td>
                  <td className="mono">{e.bucket}</td>
                  <td className="mono max-w-64 truncate">{e.key}</td>
                  <td><Badge tone={e.status < 300 ? "ok" : e.status === 403 ? "danger" : "warn"}>{e.status}{e.error ? ` ${e.error}` : ""}</Badge></td>
                  <td className="mono">{e.ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Modal>
  );
}
