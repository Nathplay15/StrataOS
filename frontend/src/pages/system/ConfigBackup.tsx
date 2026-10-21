import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArchiveRestore, Download, Play, Upload } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import type { Job } from "../../lib/types";
import { Badge, Button, Card, Checkbox, ErrorBox, Field, Input, Modal, Notice, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr, srv, locale } from "../../lib/i18n";
import { TrialModal, type Pending } from "./Network";

type Item = { key: string; name: string; action: "create" | "update" | "same" | "skip" | "report"; detail?: string; changes?: string[] };
type Section = { id: string; items: Item[] };
type VolumeChoice = { name: string; kind: string; mountPoint: string; found: boolean };
type Preview = { strataos: string; version: number; created: string; hostname: string; encrypted: boolean; secrets: boolean; volumes: VolumeChoice[]; targets: VolumeChoice[]; sections: Section[] };
type AutoConfig = { enabled: boolean; folder: string; keep: number; hour: number; secrets?: boolean; passphrase?: string; lastRun?: string; lastResult?: string; lastFile?: string };
type Auto = { config: AutoConfig; files: { name: string; size: number; modified: string }[] };
type Doc = { hostname: string; secrets?: unknown; config: { network?: { interfaces?: { mode: string; addresses?: string[] }[]; bonds?: { mode: string; addresses?: string[] }[]; bridges?: { mode: string; addresses?: string[] }[]; vlans?: { mode: string; addresses?: string[] }[] } } };

const sectionNames: Record<string, string> = {
  system: tr("Name and time zone"),
  settings: tr("General settings and notifications"),
  groups: tr("Groups"),
  users: tr("Users"),
  quotas: tr("User quotas"),
  volumes: tr("Volumes (never recreated)"),
  shares: tr("Shared folders"),
  schedules: tr("Scheduled tasks"),
  backups: tr("Backup tasks"),
  cloudSyncs: tr("Cloud sync"),
  stacks: tr("Docker apps and stacks"),
  network: tr("Network"),
  fileServices: tr("File services (WebDAV, FTP, SFTP)"),
  ransomware: tr("Ransomware protection"),
  vpn: tr("VPN server and devices"),
  ddns: tr("Dynamic DNS"),
  energy: tr("Energy"),
  guard: tr("Address blocking"),
  ups: tr("UPS"),
  photos: tr("Photo folders"),
  vaults: tr("Backup vaults"),
  fleet: tr("Watched NAS"),
  configBackup: tr("Automatic configuration backups"),
  ha: tr("High availability (reported only)"),
  directory: tr("Directory (reported only)"),
};

const actionBadge = (a: Item["action"]) => {
  switch (a) {
    case "create": return <Badge tone="ok">{tr("Create")}</Badge>;
    case "update": return <Badge tone="accent">{tr("Change")}</Badge>;
    case "skip": return <Badge tone="warn">{tr("Skipped")}</Badge>;
    case "report": return <Badge>{tr("Info")}</Badge>;
    default: return <Badge>{tr("Unchanged")}</Badge>;
  }
};

const todo = (s: Section) => s.items.some((i) => i.action === "create" || i.action === "update");

function download(name: string, data: unknown) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

function ExportModal({ onClose }: { onClose: () => void }) {
  const [secrets, setSecrets] = useState(false);
  const [pass, setPass] = useState("");
  const [pass2, setPass2] = useState("");
  const exp = useAction(() => api.post<Doc & { created: string }>("/api/config/export", { secrets, passphrase: secrets ? pass : "" }), {
    success: tr("Configuration exported"),
    onDone: (d) => {
      download(`strataos-config-${d.hostname}-${d.created.slice(0, 10)}.json`, d);
      onClose();
    },
  });
  const bad = secrets && (pass.length < 8 || pass !== pass2);
  return (
    <Modal open onClose={onClose} title={tr("Export the configuration")} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" icon={<Download size={15} />} disabled={bad} loading={exp.isPending} onClick={() => exp.mutate()}>{tr("Download")}</Button>
    </>}>
      <div className="space-y-4">
        <p className="text-[13px] text-muted">{tr("A JSON file with the settings, accounts, shared folders, tasks, apps, network and security settings. Files, snapshots, events and container data are not included.")}</p>
        <Switch checked={secrets} onChange={setSecrets} label={tr("Include the secrets")} hint={tr("Passwords of the accounts (to sign in again without a reset), notification tokens, backup and cloud credentials, VPN keys. They are encrypted with a passphrase (AES-256).")} />
        {secrets && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={tr("Passphrase")} hint={tr("at least 8 characters")}><Input type="password" autoComplete="new-password" value={pass} onChange={(e) => setPass(e.target.value)} /></Field>
            <Field label={tr("Confirm the passphrase")}><Input type="password" autoComplete="new-password" value={pass2} onChange={(e) => setPass2(e.target.value)} /></Field>
            <div className="sm:col-span-2"><Notice tone="warn">{tr("Without this passphrase, the secrets of the file cannot be recovered.")}</Notice></div>
          </div>
        )}
        {!secrets && <Notice>{tr("Without secrets, the file is readable and can be compared between two exports. Imported accounts are then created disabled until a password is set.")}</Notice>}
        <ErrorBox error={exp.error} />
      </div>
    </Modal>
  );
}

function JobLog({ id, onEnd }: { id: string; onEnd: (j: Job) => void }) {
  const { data } = useQuery({ queryKey: ["job", id], queryFn: () => api.get<Job>(`/api/jobs/${id}`), refetchInterval: (q) => (q.state.data?.status === "running" ? 1500 : false) });
  const ended = useRef(false);
  useEffect(() => {
    if (data && data.status !== "running" && !ended.current) {
      ended.current = true;
      onEnd(data);
    }
  }, [data, onEnd]);
  if (!data) return <Spinner />;
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-[13px]">
        {data.status === "running" ? <Spinner label={tr("Importing…")} /> : data.status === "success" ? <Badge tone="ok">{tr("Completed")}</Badge> : <Badge tone="danger">{srv(data.error) || tr("failed")}</Badge>}
      </div>
      <pre className="mono max-h-80 overflow-auto whitespace-pre-wrap rounded-lg bg-panel-2 p-3 text-[11.5px]">{data.log ?? ""}</pre>
    </div>
  );
}

function ImportModal({ doc, onClose }: { doc: Doc; onClose: () => void }) {
  const { confirm } = useUI();
  const { track } = useJobs();
  const [pass, setPass] = useState("");
  const [pathMap, setPathMap] = useState<Record<string, string>>({});
  const [chosen, setChosen] = useState<Set<string> | null>(null);
  const [jobId, setJobId] = useState("");
  const [trial, setTrial] = useState<{ p: Pending; urls: string[] } | null>(null);
  const [open, setOpen] = useState<Set<string>>(new Set());
  const preview = useAction((m: Record<string, string>) => api.post<Preview>("/api/config/preview", { document: doc, passphrase: pass, pathMap: m }), {
    onDone: (p) => { if (!chosen) setChosen(new Set(p.sections.filter(todo).map((s) => s.id))); },
  });
  const run = useAction(() => api.post<Job>("/api/config/import", { document: doc, passphrase: pass, pathMap, sections: [...(chosen ?? [])] }), { onDone: (j) => setJobId(j.id) });
  const applyNet = useAction(() => api.put<Pending>("/api/system/network/config", { config: doc.config.network, timeout: 120 }), {
    onDone: (p) => {
      const n = doc.config.network ?? {};
      const port = location.port ? `:${location.port}` : "";
      const addrs = [...(n.interfaces ?? []), ...(n.bonds ?? []), ...(n.bridges ?? []), ...(n.vlans ?? [])].filter((x) => x.mode === "static").flatMap((x) => x.addresses ?? []).map((a) => a.split("/")[0]).filter((a) => a !== location.hostname);
      setTrial({ p, urls: addrs.map((a) => `${location.protocol}//${a}${port}/?netconfirm=${p.token}`) });
    },
  });
  const needPass = !!doc.secrets;
  useEffect(() => { if (!needPass) preview.mutate({}); }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const p = preview.data;
  const remap = (from: string, to: string) => {
    const m = { ...pathMap };
    if (to) m[from] = to;
    else delete m[from];
    setPathMap(m);
    preview.mutate(m);
  };
  const toggle = (id: string, on: boolean) => {
    const c = new Set(chosen ?? []);
    if (on) c.add(id);
    else c.delete(id);
    setChosen(c);
  };
  const start = async () => {
    if (await confirm({ title: tr("Import the chosen sections?"), text: tr("Items of the file are created or updated; nothing that exists only on this NAS is removed. Disks and volumes are never touched.") })) run.mutate();
  };
  const onEnd = (j: Job) => {
    track(j);
    if (chosen?.has("network") && p?.sections.find((s) => s.id === "network" && todo(s)) && !applyNet.isPending && !applyNet.data) applyNet.mutate();
  };
  const unmapped = p?.volumes.filter((v) => !v.found) ?? [];

  return (
    <Modal open onClose={onClose} wide="xl" title={tr("Import a configuration")} footer={jobId ? <Button variant="primary" onClick={onClose}>{tr("Close")}</Button> : <>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      {p && <Button variant="primary" icon={<Upload size={15} />} disabled={!chosen?.size} loading={run.isPending} onClick={start}>{tr("Import")}</Button>}
    </>}>
      {jobId ? (
        <div className="space-y-3">
          <JobLog id={jobId} onEnd={onEnd} />
          <ErrorBox error={applyNet.error} />
          {trial && <TrialModal pending={trial.p} newURLs={trial.urls} onClose={() => setTrial(null)} />}
        </div>
      ) : (
        <div className="space-y-4">
          {(needPass && !p) || preview.error ? (
            <div className="space-y-3">
              {needPass && <>
                <Notice>{tr("This file holds encrypted secrets. Enter its passphrase to restore the passwords, or leave it empty to import without secrets (accounts are then created disabled).")}</Notice>
                <div className="flex items-end gap-2">
                  <Field label={tr("Passphrase")} className="flex-1"><Input type="password" value={pass} onChange={(e) => setPass(e.target.value)} onKeyDown={(e) => e.key === "Enter" && preview.mutate(pathMap)} /></Field>
                  <Button variant="primary" loading={preview.isPending} onClick={() => preview.mutate(pathMap)}>{tr("Preview")}</Button>
                </div>
              </>}
              <ErrorBox error={preview.error} />
            </div>
          ) : null}
          {!p && !preview.error && preview.isPending && <Spinner />}
          {p && (
            <>
              <div className="flex flex-wrap items-center gap-2 text-[13px]">
                <span>{tr("From {0}, StrataOS {1}, exported on {2}", p.hostname, p.strataos, new Date(p.created).toLocaleString(locale))}</span>
                {p.secrets ? <Badge tone="ok">{tr("Secrets included")}</Badge> : <Badge tone="warn">{tr("Without secrets")}</Badge>}
                {preview.isPending && <Spinner />}
              </div>
              {(unmapped.length > 0 || Object.keys(pathMap).length > 0) && (
                <Card title={tr("Volumes")}>
                  <p className="mb-3 text-[12.5px] text-muted">{tr("Volumes are never created by an import. Map a volume of the file to a volume of this NAS to bring back what it held (shared folders, tasks, apps).")}</p>
                  <div className="space-y-2">
                    {p.volumes.filter((v) => !v.found || pathMap[v.mountPoint]).map((v) => (
                      <div key={v.mountPoint} className="flex flex-wrap items-center gap-3">
                        <span className="mono min-w-[200px] text-[12.5px]">{v.name} ({v.mountPoint})</span>
                        <span>→</span>
                        <Select value={pathMap[v.mountPoint] ?? ""} onChange={(e) => remap(v.mountPoint, e.target.value)}>
                          <option value="">{tr("Not mapped (skipped)")}</option>
                          {p.targets.map((t) => <option key={t.mountPoint} value={t.mountPoint}>{t.name} ({t.mountPoint})</option>)}
                        </Select>
                      </div>
                    ))}
                  </div>
                </Card>
              )}
              <div className="space-y-2">
                {p.sections.map((s) => {
                  const n = (a: string) => s.items.filter((i) => i.action === a).length;
                  const reportOnly = s.items.every((i) => i.action === "report");
                  return (
                    <div key={s.id} className="rounded-xl border border-line">
                      <div className="flex flex-wrap items-center gap-3 px-4 py-2.5">
                        {reportOnly ? <span className="w-4" /> : <Checkbox checked={!!chosen?.has(s.id)} disabled={!todo(s)} onChange={(v) => toggle(s.id, v)} />}
                        <button type="button" className="flex-1 text-left font-medium" onClick={() => { const o = new Set(open); if (o.has(s.id)) o.delete(s.id); else o.add(s.id); setOpen(o); }}>{sectionNames[s.id] ?? s.id}</button>
                        {n("create") > 0 && <Badge tone="ok">{tr("{0} to create", n("create"))}</Badge>}
                        {n("update") > 0 && <Badge tone="accent">{tr("{0} to change", n("update"))}</Badge>}
                        {n("same") > 0 && <Badge>{tr("{0} unchanged", n("same"))}</Badge>}
                        {n("skip") > 0 && <Badge tone="warn">{tr("{0} skipped", n("skip"))}</Badge>}
                      </div>
                      {(open.has(s.id) || reportOnly || n("skip") > 0) && (
                        <div className="space-y-1 border-t border-line px-4 py-2">
                          {s.items.filter((i) => open.has(s.id) || i.action === "skip" || i.action === "report").map((i) => (
                            <div key={i.key} className="flex flex-wrap items-center gap-2 text-[12.5px]">
                              {actionBadge(i.action)}
                              <span className="font-medium">{i.name}</span>
                              {i.detail && <span className="text-muted">{srv(i.detail)}</span>}
                              {i.changes && i.changes.length > 0 && <span className="mono text-muted text-[11.5px]">{i.changes.join(", ")}</span>}
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
              {chosen?.has("network") && <Notice tone="warn">{tr("The network configuration is applied last, on trial: it is rolled back unless this browser still reaches the NAS.")}</Notice>}
              <ErrorBox error={run.error} />
            </>
          )}
        </div>
      )}
    </Modal>
  );
}

function AutoBackup() {
  const { data } = useQuery({ queryKey: ["config-auto"], queryFn: () => api.get<Auto>("/api/config/auto") });
  const [c, setC] = useState<AutoConfig | null>(null);
  useEffect(() => { if (data) setC(data.config); }, [data]);
  const save = useAction(() => api.put("/api/config/auto", c), { success: tr("Automatic backups saved") });
  const runNow = useAction(() => api.post("/api/config/auto/run"));
  if (!c || !data) return <Spinner />;
  const set = <K extends keyof AutoConfig>(k: K, v: AutoConfig[K]) => setC({ ...c, [k]: v });
  return (
    <div className="space-y-4">
      <Switch checked={c.enabled} onChange={(v) => set("enabled", v)} label={tr("Daily automatic backup")} hint={tr("A copy of the configuration every day in a folder of a volume (e.g. a shared folder that is itself backed up).")} />
      <div className="grid gap-3 sm:grid-cols-4">
        <Field label={tr("Folder")} className="sm:col-span-2"><Input className="mono" value={c.folder} placeholder="/srv/strata/main/Config" onChange={(e) => set("folder", e.target.value)} /></Field>
        <Field label={tr("Copies kept")}><Input type="number" min={1} max={365} value={c.keep} onChange={(e) => set("keep", +e.target.value)} /></Field>
        <Field label={tr("Hour")}><Select value={c.hour} onChange={(e) => set("hour", +e.target.value)}>{Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{`${h}:00`}</option>)}</Select></Field>
      </div>
      <Switch checked={!!c.secrets} onChange={(v) => set("secrets", v)} label={tr("Include the secrets (encrypted)")} hint={tr("The passphrase is kept on this NAS to encrypt each copy; keep it somewhere else too.")} />
      {c.secrets && <Field label={tr("Passphrase")} hint={tr("at least 8 characters")}><Input type="password" autoComplete="new-password" value={c.passphrase ?? ""} onChange={(e) => set("passphrase", e.target.value)} /></Field>}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
        <Button icon={<Play size={14} />} disabled={!data.config.folder} loading={runNow.isPending} onClick={() => runNow.mutate()}>{tr("Back up now")}</Button>
        {data.config.lastRun && <span className="text-[12.5px] text-muted">{tr("Last backup: {0}", new Date(data.config.lastRun).toLocaleString(locale))} — {data.config.lastResult === "OK" ? tr("OK") : srv(data.config.lastResult)}</span>}
      </div>
      <ErrorBox error={save.error} />
      {data.files.length > 0 && (
        <div className="space-y-1">
          {data.files.map((f) => (
            <div key={f.name} className="flex items-center gap-3 rounded-lg bg-panel-2 px-3 py-1.5 text-[12.5px]">
              <span className="mono flex-1 truncate">{f.name}</span>
              <span className="text-muted">{new Date(f.modified).toLocaleString(locale)}</span>
              <a className="text-accent underline" href={`/api/config/auto/files/${encodeURIComponent(f.name)}`}>{tr("Download")}</a>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/** Settings → Configuration backup: export, import with preview, automatic copies. */
export function ConfigBackupCard() {
  const { toast } = useUI();
  const [exporting, setExporting] = useState(false);
  const [doc, setDoc] = useState<Doc | null>(null);
  const file = useRef<HTMLInputElement>(null);
  const pick = (f: File | undefined) => f?.text().then((t) => {
    try {
      setDoc(JSON.parse(t));
    } catch {
      toast(tr("This is not a StrataOS configuration file"), "error");
    }
  });
  return (
    <Card title={<span className="flex items-center gap-2"><ArchiveRestore size={16} /> {" "}{tr("Configuration backup")}</span>} className="xl:col-span-2" actions={<>
      <Button size="sm" icon={<Download size={14} />} onClick={() => setExporting(true)}>{tr("Export")}</Button>
      <Button size="sm" icon={<Upload size={14} />} onClick={() => file.current?.click()}>{tr("Import")}</Button>
      <input ref={file} type="file" accept=".json,application/json" hidden onChange={(e) => { pick(e.target.files?.[0]); e.target.value = ""; }} />
    </>}>
      <p className="mb-4 text-[12.5px] text-muted">{tr("To move this NAS to new hardware or restore it after a reinstall: export the configuration, then import it on the new system and choose what to bring back.")}</p>
      <AutoBackup />
      {exporting && <ExportModal onClose={() => setExporting(false)} />}
      {doc && <ImportModal doc={doc} onClose={() => setDoc(null)} />}
    </Card>
  );
}
