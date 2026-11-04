import { useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowDownToLine, Check, ExternalLink, Library, RefreshCw, ShieldAlert, Trash2, Users } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { AppVar } from "../../lib/types";
import { Badge, Button, Card, Checkbox, ErrorBox, Field, Health, Input, Modal, Notice, Spinner } from "../../components/ui";
import { tr } from "../../lib/i18n";
import { cx } from "../../lib/format";

// Community catalogs ("sources") of the App Center: apps maintained by
// others, never verified by StrataOS. Before an install or an update the NAS
// tells exactly what the app gets, and risky grants need a ticked box each.

export interface CatalogSource {
  id: string;
  url: string;
  kind: "https" | "git";
  name: string;
  publicKey?: string;
  sha256: string;
  commit?: string;
  addedAt: string;
  refreshed?: string;
  lastError?: string;
  pending?: string;
  trust: "signed" | "pinned";
  apps: number;
  invalid: { id: string; error: string }[];
}

export interface CommunityApp {
  id: string;
  name: string;
  version: string;
  maintainer: string;
  description?: string;
  category?: string;
  icon?: string;
  homepage?: string;
  webPort?: string;
  vars?: AppVar[];
  compose: string;
  source: string;
  sourceName: string;
}

export interface CommunityInstalled {
  stack: string;
  source: string;
  sourceName: string;
  app: string;
  name: string;
  version: string;
  icon?: string;
  status: string;
  latest?: string;
  update: boolean;
  port?: string;
  orphaned?: boolean;
}

interface CommunityData {
  sources: CatalogSource[];
  apps: CommunityApp[];
  installed: CommunityInstalled[];
}

interface Mount { service: string; source: string; target: string; readOnly?: boolean }
interface Grants {
  images: string[];
  ports: { service: string; hostIp?: string; published?: string; target: string; protocol: string }[];
  folders: Mount[];
  hostPaths: Mount[];
  volumes: string[];
  privileged: string[];
  hostNetwork: string[];
  hostNamespaces: string[];
  devices: string[];
  capabilities: string[];
  securityOpts: string[];
  dockerSocket: boolean;
}
interface Approval {
  hostPaths?: string[];
  privileged?: boolean;
  hostNetwork?: boolean;
  hostNamespaces?: boolean;
  devices?: string[];
  capabilities?: string[];
  securityOpts?: string[];
}
interface Check { grants: Grants; required: Approval; previous?: Approval; version: string; from?: string }

export function useCommunity() {
  return useQuery({ queryKey: ["apps", "community"], queryFn: () => api.get<CommunityData>("/api/apps/community") });
}

function randomSecret(length = 18) {
  const a = new Uint8Array(length);
  crypto.getRandomValues(a);
  return Array.from(a, (b) => "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"[b % 57]).join("");
}

export function CommunityBadge() {
  return <Badge tone="warn" title={tr("Maintained by a third party: StrataOS did not review this app.")}>{tr("Community · not verified by StrataOS")}</Badge>;
}

function Icon({ name, icon, size = 48 }: { name: string; icon?: string; size?: number }) {
  const [err, setErr] = useState(false);
  return err || !icon ? (
    <div className="flex shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-warn to-accent font-semibold text-white" style={{ width: size, height: size }}>{(name[0] ?? "?").toUpperCase()}</div>
  ) : (
    <img src={icon} alt="" referrerPolicy="no-referrer" onError={() => setErr(true)} className="shrink-0 rounded-xl bg-panel-2 p-1.5" style={{ width: size, height: size }} />
  );
}

/** One risky grant, with the box that approves it. */
function Risk({ danger, checked, onChange, children }: { danger?: boolean; checked: boolean; onChange: (v: boolean) => void; children: ReactNode }) {
  return (
    <div className={cx("rounded-xl border px-4 py-3 text-[13px]", danger ? "border-danger/40 bg-danger/10 text-danger" : "border-warn/30 bg-warn/10 text-warn")}>
      <Checkbox checked={checked} onChange={onChange} label={<span className="font-medium">{children}</span>} />
    </div>
  );
}

function List({ title, items }: { title: string; items: ReactNode[] }) {
  if (items.length === 0) return null;
  return (
    <div>
      <div className="mb-1 text-[12.5px] font-medium text-muted">{title}</div>
      <ul className="mono space-y-0.5 text-[12.5px]">{items.map((x, i) => <li key={i}>{x}</li>)}</ul>
    </div>
  );
}

const isNew = (prev: Approval | undefined, key: keyof Approval, item?: string) => {
  if (!prev) return false;
  const v = prev[key];
  return item === undefined ? !v : !(Array.isArray(v) && v.includes(item));
};

/** What the app gets, and the approvals the risky parts need. */
function Permissions({ check, approve, setApprove }: { check: Check; approve: Approval; setApprove: (a: Approval) => void }) {
  const g = check.grants;
  const req = check.required;
  const prev = check.previous;
  const toggle = (key: "hostPaths" | "devices" | "capabilities" | "securityOpts", item: string, on: boolean) => {
    const cur = new Set(approve[key] ?? []);
    if (on) cur.add(item); else cur.delete(item);
    setApprove({ ...approve, [key]: [...cur] });
  };
  const newTag = (b: boolean) => (b ? <> <Badge tone="danger">{tr("new")}</Badge></> : null);
  const port = (p: Grants["ports"][number]) => `${p.hostIp ? p.hostIp + ":" : ""}${p.published || tr("random port")} → ${p.service}:${p.target}/${p.protocol}`;
  const mount = (m: Mount) => `${m.source} → ${m.service}:${m.target}${m.readOnly ? " (" + tr("read-only") + ")" : ""}`;
  return (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <List title={tr("Images")} items={g.images} />
        <List title={tr("Ports of the NAS")} items={g.ports.map(port)} />
        <List title={tr("Folders of the NAS")} items={g.folders.map(mount)} />
        <List title={tr("Docker volumes")} items={g.volumes} />
      </div>
      {g.ports.length === 0 && g.folders.length === 0 && g.hostPaths.length === 0 && <p className="text-[12.5px] text-muted">{tr("No port and no folder of the NAS.")}</p>}
      {req.privileged && (
        <Risk danger checked={!!approve.privileged} onChange={(v) => setApprove({ ...approve, privileged: v })}>
          <ShieldAlert size={14} className="mr-1 inline" />{tr("Privileged mode ({0}): the app has full control of the NAS, like root.", g.privileged.join(", "))}{newTag(isNew(prev, "privileged"))}
        </Risk>
      )}
      {g.dockerSocket && <Notice tone="warn">{tr("This app reaches the Docker socket: it can start any container, so it controls the whole NAS.")}</Notice>}
      {(req.hostPaths ?? []).map((p) => (
        <Risk key={p} danger={g.dockerSocket} checked={!!approve.hostPaths?.includes(p)} onChange={(v) => toggle("hostPaths", p, v)}>
          {tr("Access to {0}, outside the data volumes", p)}{newTag(isNew(prev, "hostPaths", p))}
        </Risk>
      ))}
      {req.hostNetwork && (
        <Risk checked={!!approve.hostNetwork} onChange={(v) => setApprove({ ...approve, hostNetwork: v })}>
          {tr("Host network ({0}): the app uses the NAS's network directly and can open any port.", g.hostNetwork.join(", "))}{newTag(isNew(prev, "hostNetwork"))}
        </Risk>
      )}
      {req.hostNamespaces && (
        <Risk danger checked={!!approve.hostNamespaces} onChange={(v) => setApprove({ ...approve, hostNamespaces: v })}>
          {tr("Shares system namespaces with the NAS: {0}", g.hostNamespaces.join(", "))}{newTag(isNew(prev, "hostNamespaces"))}
        </Risk>
      )}
      {(req.devices ?? []).map((d) => (
        <Risk key={d} checked={!!approve.devices?.includes(d)} onChange={(v) => toggle("devices", d, v)}>{tr("Device {0}", d)}{newTag(isNew(prev, "devices", d))}</Risk>
      ))}
      {(req.capabilities ?? []).map((c) => (
        <Risk key={c} danger={c === "ALL" || c.endsWith("SYS_ADMIN")} checked={!!approve.capabilities?.includes(c)} onChange={(v) => toggle("capabilities", c, v)}>{tr("Capability {0}", c)}{newTag(isNew(prev, "capabilities", c))}</Risk>
      ))}
      {(req.securityOpts ?? []).map((o) => (
        <Risk key={o} danger checked={!!approve.securityOpts?.includes(o)} onChange={(v) => toggle("securityOpts", o, v)}>{tr("Security option {0}", o)}{newTag(isNew(prev, "securityOpts", o))}</Risk>
      ))}
    </div>
  );
}

const covered = (req: Approval, a: Approval) =>
  (!req.privileged || !!a.privileged) && (!req.hostNetwork || !!a.hostNetwork) && (!req.hostNamespaces || !!a.hostNamespaces) &&
  (["hostPaths", "devices", "capabilities", "securityOpts"] as const).every((k) => (req[k] ?? []).every((x) => (a[k] ?? []).includes(x)));

export function CommunityInstallModal({ app, onClose }: { app: CommunityApp; onClose: () => void }) {
  const [name, setName] = useState(app.id);
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries((app.vars ?? []).map((v) => [v.key, v.type === "password" ? randomSecret(v.length || 18) : v.default])));
  const [check, setCheck] = useState<Check | null>(null);
  const [approve, setApprove] = useState<Approval>({});
  const [showCompose, setShowCompose] = useState(false);
  const review = useAction(() => api.post<Check>(`/api/apps/community/${app.source}/${app.id}/check`, { name, values }), { onDone: (c) => { setCheck(c); setApprove({}); } });
  const install = useAction(() => api.post(`/api/apps/community/${app.source}/${app.id}/install`, { name, values, approve }), { onDone: onClose });
  return (
    <Modal open onClose={onClose} wide title={<span className="flex items-center gap-3"><Icon name={app.name} icon={app.icon} size={30} />{tr("Install")}{" "}{app.name}</span>} footer={<>
      {check && <Button className="mr-auto" onClick={() => setCheck(null)}>{tr("Back")}</Button>}
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      {!check ? <Button variant="primary" loading={review.isPending} onClick={() => review.mutate()}>{tr("Review permissions")}</Button>
        : <Button variant={check.required.privileged ? "danger" : "primary"} loading={install.isPending} disabled={!covered(check.required, approve)} onClick={() => install.mutate()}>{tr("Install")}</Button>}
    </>}>
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-1.5"><CommunityBadge /><Badge>{app.sourceName}</Badge><Badge>{tr("Version {0}", app.version)}</Badge></div>
        <p className="text-muted text-[13px]">{app.description} {" "}<span className="text-[12px]">{tr("Maintained by {0}.", app.maintainer)}</span>{" "}
          {app.homepage && <a className="text-accent" href={app.homepage} target="_blank" rel="noreferrer noopener">{tr("Website")} <ExternalLink size={11} className="inline" /></a>}</p>
        {!check ? <>
          <Field label={tr("Installation name (stack)")}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} /></Field>
          <div className="grid gap-4 sm:grid-cols-2">
            {(app.vars ?? []).map((v) => (
              <Field key={v.key} label={v.label} className={v.type === "path" || v.type === "device" ? "sm:col-span-2" : ""} hint={v.type === "path" ? tr("Created automatically under /srv/strata. Anywhere else needs your approval on the next step.") : undefined}>
                <Input className={v.type !== "port" && v.type !== "text" ? "mono" : ""} value={values[v.key] ?? ""} onChange={(e) => setValues({ ...values, [v.key]: e.target.value })} />
              </Field>
            ))}
          </div>
          <button className="text-accent text-[12.5px]" onClick={() => setShowCompose(!showCompose)}>{showCompose ? tr("Hide") : tr("View")} {" "}{tr("the compose file")}</button>
          {showCompose && <pre className="mono max-h-72 overflow-auto rounded-xl bg-[#0b0f1c] p-4 text-[#cbd3e6]">{app.compose}</pre>}
          <ErrorBox error={review.error} />
        </> : <>
          <div className="font-medium">{tr("What this app gets on your NAS")}</div>
          <Permissions check={check} approve={approve} setApprove={setApprove} />
          {!covered(check.required, approve) && <p className="text-[12.5px] text-muted">{tr("Tick every item above to install.")}</p>}
          <ErrorBox error={install.error} />
        </>}
      </div>
    </Modal>
  );
}

export function CommunityUpdateModal({ inst, onClose }: { inst: CommunityInstalled; onClose: () => void }) {
  const [approve, setApprove] = useState<Approval>({});
  const check = useQuery({ queryKey: ["apps", "community", "check", inst.stack], queryFn: () => api.post<Check>(`/api/apps/community/stacks/${inst.stack}/check`, {}) });
  const update = useAction(() => api.post(`/api/apps/community/stacks/${inst.stack}/update`, { approve }), { onDone: onClose });
  const c = check.data;
  return (
    <Modal open onClose={onClose} wide title={tr("Update {0}", inst.name)} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant={c?.required.privileged ? "danger" : "primary"} loading={update.isPending} disabled={!c || !covered(c.required, approve)} onClick={() => update.mutate()}>{tr("Update")}</Button>
    </>}>
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-1.5"><CommunityBadge /><Badge>{inst.sourceName}</Badge></div>
        {check.isLoading ? <Spinner /> : check.error ? <ErrorBox error={check.error} /> : c && <>
          <p className="text-[13px]">{tr("From version {0} to {1}. Items marked new were not approved before.", c.from ?? "", c.version)}</p>
          <Permissions check={c} approve={approve} setApprove={setApprove} />
        </>}
        <ErrorBox error={update.error} />
      </div>
    </Modal>
  );
}

/** Community apps as App Center cards (installed ones first). */
export function CommunityCards({ data, filter, mineOnly, onInstall, onUpdate, onRemove }: {
  data?: CommunityData; filter: (s: string) => boolean; mineOnly: boolean;
  onInstall: (a: CommunityApp) => void; onUpdate: (i: CommunityInstalled) => void; onRemove: (stack: string, name: string) => void;
}) {
  if (!data) return null;
  const host = location.hostname;
  const installed = data.installed.filter((i) => filter([i.name, i.sourceName].join(" ")));
  const taken = new Set(data.installed.map((i) => i.source + "/" + i.app));
  const apps = mineOnly ? [] : data.apps.filter((a) => !taken.has(a.source + "/" + a.id) && filter([a.name, a.description, a.category, a.sourceName].join(" ")));
  return <>
    {installed.map((i) => (
      <Card key={"ci-" + i.stack} pad={false} className="flex flex-col">
        <div className="flex flex-1 gap-4 p-5">
          <Icon name={i.name} icon={i.icon} />
          <div className="min-w-0">
            <div className="font-semibold">{i.name} <span className="text-[12px] font-normal text-muted">{i.version}</span></div>
            <span className="flex flex-wrap items-center gap-1.5"><CommunityBadge />{i.status && <Health value={i.status} />}</span>
            <p className="mt-2 text-[12.5px] text-muted">{i.orphaned ? tr("Its catalog no longer lists it: no updates.") : tr("From {0} (stack {1})", i.sourceName, i.stack)}</p>
          </div>
        </div>
        <div className="flex items-center justify-end gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
          {i.update ? <Button size="sm" variant="primary" className="mr-auto" icon={<ArrowDownToLine size={13} />} onClick={() => onUpdate(i)}>{tr("Update to {0}", i.latest ?? "")}</Button>
            : <span className="mr-auto flex items-center gap-1 text-ok text-[12.5px]"><Check size={14} /> {" "}{tr("Installed")}</span>}
          {i.port && <a href={`http://${host}:${i.port}`} target="_blank" rel="noreferrer"><Button size="sm" icon={<ExternalLink size={13} />}>{tr("Open")}</Button></a>}
          <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} title={tr("Uninstall")} onClick={() => onRemove(i.stack, i.name)} />
        </div>
      </Card>
    ))}
    {apps.map((a) => (
      <Card key={"ca-" + a.source + a.id} pad={false} className="flex flex-col">
        <div className="flex flex-1 gap-4 p-5">
          <Icon name={a.name} icon={a.icon} />
          <div className="min-w-0">
            <div className="font-semibold">{a.name} <span className="text-[12px] font-normal text-muted">{a.version}</span></div>
            <CommunityBadge />
            <p className="mt-2 text-[12.5px] text-muted">{a.description}</p>
            <p className="mt-1 text-[11.5px] text-muted">{tr("{0} · by {1}", a.sourceName, a.maintainer)}</p>
          </div>
        </div>
        <div className="flex items-center justify-end gap-2 border-t border-line bg-panel-2/50 px-4 py-2.5">
          <Button size="sm" onClick={() => onInstall(a)}>{tr("Install")}</Button>
        </div>
      </Card>
    ))}
  </>;
}

function short(sha: string) {
  return sha.slice(0, 16) + "…";
}

/** The community catalogs: add (signed or pinned), refresh, accept a change, remove. */
export function SourcesModal({ onClose }: { onClose: () => void }) {
  const q = useQuery({ queryKey: ["apps", "sources"], queryFn: () => api.get<CatalogSource[]>("/api/apps/sources") });
  const [url, setUrl] = useState("");
  const [key, setKey] = useState("");
  const [confirm, setConfirm] = useState<{ sha256: string; name: string; apps: number; signed: boolean; maintainer?: string; invalid: { id: string; error: string }[] } | null>(null);
  const add = useAction((sha?: string) => api.post<{ confirm?: typeof confirm } & Partial<CatalogSource>>("/api/apps/sources", { url, publicKey: key, sha256: sha ?? "" }), {
    onDone: (r) => {
      if (r.confirm) setConfirm(r.confirm);
      else { setConfirm(null); setUrl(""); setKey(""); }
    },
  });
  const refresh = useAction((id: string) => api.post(`/api/apps/sources/${id}/refresh`, {}), { success: tr("Catalog refreshed") });
  const accept = useAction((s: CatalogSource) => api.post(`/api/apps/sources/${s.id}/accept`, { sha256: s.pending }), { success: tr("New version accepted") });
  const remove = useAction((id: string) => api.del(`/api/apps/sources/${id}`), { success: tr("Catalog removed") });
  return (
    <Modal open onClose={onClose} wide="xl" title={<span className="flex items-center gap-2"><Library size={18} />{tr("Community catalogs")}</span>} footer={<Button onClick={onClose}>{tr("Close")}</Button>}>
      <div className="space-y-4">
        <Notice tone="warn">{tr("Community catalogs are maintained by third parties and not verified by StrataOS. Apps run as containers with the rights you approve: only add catalogs you trust.")}</Notice>
        {q.isLoading ? <Spinner /> : (q.data ?? []).length === 0 ? <p className="text-muted text-[13px]">{tr("No community catalog yet.")}</p> : (
          <div className="space-y-2">
            {(q.data ?? []).map((s) => (
              <div key={s.id} className="rounded-xl border border-line p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-semibold">{s.name}</span>
                  <Badge tone={s.trust === "signed" ? "ok" : "neutral"}>{s.trust === "signed" ? tr("Signed") : tr("Pinned by checksum")}</Badge>
                  <Badge>{tr("{0} apps", s.apps)}</Badge>
                  <span className="ml-auto flex gap-1">
                    <Button size="sm" variant="ghost" icon={<RefreshCw size={13} />} loading={refresh.isPending && refresh.variables === s.id} onClick={() => refresh.mutate(s.id)}>{tr("Refresh")}</Button>
                    <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={13} />} title={tr("Remove")} onClick={() => remove.mutate(s.id)} />
                  </span>
                </div>
                <div className="mono mt-1 break-all text-[12px] text-muted">{s.url}</div>
                <div className="mono text-[11.5px] text-muted">{s.trust === "signed" ? s.publicKey : "sha256 " + s.sha256}{s.commit ? " · " + s.commit.slice(0, 12) : ""}</div>
                {s.lastError && <div className="mt-1 text-[12.5px] text-danger">{s.lastError}</div>}
                {s.invalid.length > 0 && <details className="mt-1 text-[12.5px] text-warn"><summary>{tr("{0} apps left out (failed the checks)", s.invalid.length)}</summary>
                  <ul className="mono">{s.invalid.map((x) => <li key={x.id}>{x.id}: {x.error}</li>)}</ul></details>}
                {s.pending && (
                  <div className="mt-2 flex flex-wrap items-center gap-2 rounded-lg bg-warn/10 px-3 py-2 text-[12.5px] text-warn">
                    <AlertTriangle size={14} />{tr("The catalog changed (sha256 {0}). Review it at its address, then accept it.", short(s.pending))}
                    <Button size="sm" className="ml-auto" loading={accept.isPending} onClick={() => accept.mutate(s)}>{tr("Accept the new version")}</Button>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
        <div className="rounded-xl border border-line p-4 space-y-3">
          <div className="font-medium flex items-center gap-2"><Users size={15} />{tr("Add a catalog")}</div>
          <Field label={tr("Address")} hint={tr("https://…/catalog.json, or a Git repository (https://…/repo.git, git+https://…#branch).")}>
            <Input className="mono" value={url} placeholder="https://example.org/catalog.json" onChange={(e) => { setUrl(e.target.value); setConfirm(null); }} />
          </Field>
          <Field label={tr("Public key of the maintainer (optional)")} hint={tr("ed25519:… With a key, every update must be signed by it. Without one, the catalog is pinned to its current checksum and each change waits for your approval.")}>
            <Input className="mono" value={key} placeholder="ed25519:…" onChange={(e) => { setKey(e.target.value); setConfirm(null); }} />
          </Field>
          {confirm ? (
            <div className="space-y-2 rounded-lg bg-panel-2 p-3 text-[13px]">
              <div>{tr("Catalog {0}: {1} apps.", confirm.name, confirm.apps)} {confirm.maintainer && tr("Maintained by {0}.", confirm.maintainer)}</div>
              {confirm.invalid.length > 0 && <div className="text-warn">{tr("{0} apps left out (failed the checks)", confirm.invalid.length)}</div>}
              <div>{tr("Pin it at this checksum? Compare it with the one the maintainer publishes.")}</div>
              <div className="mono break-all text-[12px]">sha256 {confirm.sha256}</div>
              {confirm.signed && <div className="text-muted text-[12.5px]">{tr("This catalog is signed: add its public key instead to get its updates without approving each one.")}</div>}
              <div className="flex gap-2"><Button variant="primary" loading={add.isPending} onClick={() => add.mutate(confirm.sha256)}>{tr("Pin and add")}</Button><Button onClick={() => setConfirm(null)}>{tr("Cancel")}</Button></div>
            </div>
          ) : <Button variant="primary" loading={add.isPending} disabled={!url.trim()} onClick={() => add.mutate(undefined)}>{tr("Add")}</Button>}
          <ErrorBox error={add.error} />
        </div>
      </div>
    </Modal>
  );
}
