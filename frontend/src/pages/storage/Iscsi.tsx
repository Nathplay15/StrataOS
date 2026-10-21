import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Camera, Copy, HardDriveUpload, KeyRound, Pencil, Plug, Plus, ShieldAlert, Trash2, Users } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { bytes, parseSize } from "../../lib/format";
import { Badge, Button, Card, Checkbox, Empty, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr } from "../../lib/i18n";

type Kind = "fileio" | "lvm" | "zvol";
type Session = { initiator: string; address?: string };
type CHAP = { user?: string; secret?: string; mutualUser?: string; mutualSecret?: string };
type LUN = { lun: number; kind: Kind; path: string; size: number; writeCache?: boolean; readOnly?: boolean; serial: string; created?: boolean; device: string; snapshots: boolean; missing?: boolean };
type Target = { id: string; name: string; iqn: string; portalIp?: string; port?: number; allowAny?: boolean; initiators: string[]; chap: CHAP; luns: LUN[]; portal: string; live: boolean; sessions: Session[] };
type Data = { installed: boolean; supported: boolean; enabled: boolean; prefix: string; ports: number[]; targets: Target[] };
type Loc = { path: string; label: string; free: number };
type Blk = { path: string; size: number; busy?: string; target?: string };
type Backings = { volumes: Loc[]; lvs: Blk[]; vgs: Loc[]; zvols: Blk[]; pools: Loc[]; zfs: boolean };
type LunReq = { kind: Kind; path: string; size: number; writeCache: boolean; readOnly: boolean; create: boolean; sparse: boolean };

const MASK = "********";
const kindLabel = (k: Kind) => (k === "fileio" ? tr("Image file") : k === "lvm" ? tr("LVM logical volume") : tr("ZFS volume"));

function genSecret() {
  const a = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const b = new Uint32Array(16);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => a[x % a.length]).join("");
}

/* ---------- LUN backing ---------- */

type LunDraft = { kind: Kind; volume: string; file: string; size: string; existing: boolean; lv: string; vg: string; lvName: string; zvol: string; pool: string; zvolName: string; sparse: boolean; writeCache: boolean; readOnly: boolean };

const newDraft = (target: string, n: number, b?: Backings): LunDraft => ({
  kind: "fileio", volume: b?.volumes[0]?.path ?? "", file: `iscsi/${target || "target"}-lun${n}.img`, size: "100G", existing: true,
  lv: "", vg: b?.vgs[0]?.path ?? "", lvName: `${target || "iscsi"}-lun${n}`, zvol: "", pool: b?.pools[0]?.path ?? "", zvolName: `iscsi/${target || "target"}-lun${n}`, sparse: true, writeCache: false, readOnly: false,
});

function lunReq(d: LunDraft): LunReq {
  const size = parseSize(d.size);
  const base = { size: isNaN(size) ? 0 : size, writeCache: d.kind === "fileio" && d.writeCache, readOnly: d.readOnly, sparse: d.sparse };
  if (d.kind === "fileio") return { ...base, kind: "fileio", path: `${d.volume}/${d.file.replace(/^\/+/, "")}`, create: true };
  if (d.kind === "lvm") return d.existing ? { ...base, kind: "lvm", path: d.lv, create: false } : { ...base, kind: "lvm", path: `${d.vg}/${d.lvName}`, create: true };
  return d.existing ? { ...base, kind: "zvol", path: d.zvol, create: false } : { ...base, kind: "zvol", path: `${d.pool}/${d.zvolName}`, create: true };
}

function lunReady(d: LunDraft): boolean {
  const r = lunReq(d);
  if (!r.path || r.path.endsWith("/")) return false;
  if (r.create && r.kind !== "fileio" && !r.size) return false;
  return true;
}

function BlockOptions({ list, value, onChange }: { list: Blk[]; value: string; onChange: (v: string) => void }) {
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{tr("Choose…")}</option>
      {list.map((b) => (
        <option key={b.path} value={b.path} disabled={!!b.busy || !!b.target}>
          {b.path} · {bytes(b.size)}{b.target ? ` — ${tr("exported by {0}", b.target)}` : b.busy ? ` — ${b.busy}` : ""}
        </option>
      ))}
    </Select>
  );
}

function LunForm({ d, set, b }: { d: LunDraft; set: (d: LunDraft) => void; b: Backings }) {
  const up = (k: Partial<LunDraft>) => set({ ...d, ...k });
  return (
    <div className="space-y-4">
      <Field label={tr("Backing storage")}>
        <Select value={d.kind} onChange={(e) => up({ kind: e.target.value as Kind })}>
          <option value="fileio">{tr("Image file on a volume")}</option>
          <option value="lvm">{tr("LVM logical volume")}</option>
          {b.zfs && <option value="zvol">{tr("ZFS volume (zvol)")}</option>}
        </Select>
      </Field>
      {d.kind === "fileio" && (
        <>
          {!b.volumes.length && <Notice tone="warn">{tr("No mounted volume: create one first.")}</Notice>}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Volume")}>
              <Select value={d.volume} onChange={(e) => up({ volume: e.target.value })}>
                {b.volumes.map((v) => <option key={v.path} value={v.path}>{v.label} · {tr("{0} free", bytes(v.free))}</option>)}
              </Select>
            </Field>
            <Field label={tr("Size")} hint={tr("Sparse file: space is used as data is written. Ignored when the file already exists.")}><Input value={d.size} onChange={(e) => up({ size: e.target.value })} placeholder="100G" /></Field>
          </div>
          <Field label={tr("File")} hint={tr("Relative to the volume, outside shared folders. An existing image is reused.")}><Input className="mono" value={d.file} onChange={(e) => up({ file: e.target.value })} /></Field>
        </>
      )}
      {d.kind === "lvm" && (
        <>
          <div className="flex gap-4">
            <Checkbox checked={d.existing} onChange={() => up({ existing: true })} label={tr("Existing logical volume")} />
            <Checkbox checked={!d.existing} onChange={() => up({ existing: false })} label={tr("New logical volume")} />
          </div>
          {d.existing ? (
            <Field label={tr("Logical volume")} hint={tr("Mounted or used volumes cannot be exported.")}><BlockOptions list={b.lvs} value={d.lv} onChange={(v) => up({ lv: v })} /></Field>
          ) : (
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label={tr("Group")}><Select value={d.vg} onChange={(e) => up({ vg: e.target.value })}>{b.vgs.map((v) => <option key={v.path} value={v.path}>{v.label} · {tr("{0} free", bytes(v.free))}</option>)}</Select></Field>
              <Field label={tr("Name")}><Input value={d.lvName} onChange={(e) => up({ lvName: e.target.value })} /></Field>
              <Field label={tr("Size")}><Input value={d.size} onChange={(e) => up({ size: e.target.value })} placeholder="100G" /></Field>
            </div>
          )}
        </>
      )}
      {d.kind === "zvol" && (
        <>
          <div className="flex gap-4">
            <Checkbox checked={d.existing} onChange={() => up({ existing: true })} label={tr("Existing ZFS volume")} />
            <Checkbox checked={!d.existing} onChange={() => up({ existing: false })} label={tr("New ZFS volume")} />
          </div>
          {d.existing ? (
            <Field label={tr("ZFS volume")}><BlockOptions list={b.zvols} value={d.zvol} onChange={(v) => up({ zvol: v })} /></Field>
          ) : (
            <>
              <div className="grid gap-4 sm:grid-cols-3">
                <Field label={tr("Pool")}><Select value={d.pool} onChange={(e) => up({ pool: e.target.value })}>{b.pools.map((p) => <option key={p.path} value={p.path}>{p.label} · {tr("{0} free", bytes(p.free))}</option>)}</Select></Field>
                <Field label={tr("Name")}><Input className="mono" value={d.zvolName} onChange={(e) => up({ zvolName: e.target.value })} /></Field>
                <Field label={tr("Size")}><Input value={d.size} onChange={(e) => up({ size: e.target.value })} placeholder="100G" /></Field>
              </div>
              <Checkbox checked={d.sparse} onChange={(v) => up({ sparse: v })} label={tr("Thin provisioning (no space reserved)")} />
            </>
          )}
        </>
      )}
      <div className="space-y-2">
        {d.kind === "fileio" && <Checkbox checked={d.writeCache} onChange={(v) => up({ writeCache: v })} label={tr("Write cache (faster; recent writes are lost on a power cut)")} />}
        <Checkbox checked={d.readOnly} onChange={(v) => up({ readOnly: v })} label={tr("Read-only")} />
      </div>
    </div>
  );
}

/* ---------- Access ---------- */

type AccessDraft = { initiators: string; allowAny: boolean; chap: boolean; user: string; secret: string; mutual: boolean; mutualUser: string; mutualSecret: string };

const accessOf = (t?: Target, name = ""): AccessDraft => ({
  initiators: t?.initiators.join("\n") ?? "", allowAny: !!t?.allowAny, chap: !!t?.chap.user, user: t?.chap.user ?? name, secret: t?.chap.secret ?? "",
  mutual: !!t?.chap.mutualUser, mutualUser: t?.chap.mutualUser ?? "", mutualSecret: t?.chap.mutualSecret ?? "",
});

const accessReq = (a: AccessDraft) => ({
  allowAny: a.allowAny,
  initiators: a.initiators.split(/[\s,]+/).filter(Boolean),
  chap: a.chap ? { user: a.user, secret: a.secret, ...(a.mutual ? { mutualUser: a.mutualUser, mutualSecret: a.mutualSecret } : {}) } : {},
});

function SecretInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="flex gap-2">
      <Input type={value === MASK ? "password" : "text"} className="mono" value={value} onChange={(e) => onChange(e.target.value)} />
      <Button type="button" onClick={() => onChange(genSecret())}>{tr("Generate")}</Button>
    </div>
  );
}

function AccessForm({ a, set }: { a: AccessDraft; set: (a: AccessDraft) => void }) {
  const up = (k: Partial<AccessDraft>) => set({ ...a, ...k });
  return (
    <div className="space-y-4">
      {!a.allowAny && (
        <Field label={tr("Allowed initiators")} hint={tr("One initiator name (IQN) per line. Nobody can connect while the list is empty. Windows: iSCSI Initiator → Configuration; Linux: /etc/iscsi/initiatorname.iscsi; VMware: Storage adapters.")}>
          <textarea className="input mono min-h-[90px]" value={a.initiators} onChange={(e) => up({ initiators: e.target.value })} placeholder="iqn.1991-05.com.microsoft:pc-01" />
        </Field>
      )}
      <Switch checked={a.allowAny} onChange={(v) => up({ allowAny: v })} label={tr("Allow any initiator (unsafe)")} hint={tr("Every computer that reaches the NAS can use the disks. Only on an isolated storage network, ideally with CHAP.")} />
      {a.allowAny && <Notice tone="warn">{tr("Any initiator will be able to read and overwrite these LUNs. Two computers writing to the same disk without a cluster file system destroy it.")}</Notice>}
      <Switch checked={a.chap} onChange={(v) => up({ chap: v, secret: v && !a.secret ? genSecret() : a.secret })} label={tr("CHAP authentication")} hint={tr("The initiator must give this user name and secret")} />
      {a.chap && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("User name")}><Input value={a.user} onChange={(e) => up({ user: e.target.value })} /></Field>
          <Field label={tr("Secret")} hint={tr("12 to 16 characters for Windows")}><SecretInput value={a.secret} onChange={(v) => up({ secret: v })} /></Field>
        </div>
      )}
      {a.chap && <Switch checked={a.mutual} onChange={(v) => up({ mutual: v, mutualSecret: v && !a.mutualSecret ? genSecret() : a.mutualSecret })} label={tr("Mutual CHAP")} hint={tr("The NAS proves its identity to the initiator too, with a different secret")} />}
      {a.chap && a.mutual && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Target user name")}><Input value={a.mutualUser} onChange={(e) => up({ mutualUser: e.target.value })} /></Field>
          <Field label={tr("Target secret")}><SecretInput value={a.mutualSecret} onChange={(v) => up({ mutualSecret: v })} /></Field>
        </div>
      )}
    </div>
  );
}

/* ---------- Wizard ---------- */

function CreateTarget({ data, onClose }: { data: Data; onClose: () => void }) {
  const { data: b } = useQuery({ queryKey: ["iscsi-backings"], queryFn: () => api.get<Backings>("/api/iscsi/backings") });
  const [step, setStep] = useState(0);
  const [name, setName] = useState("");
  const [iqn, setIqn] = useState("");
  const [portalIp, setPortalIp] = useState("");
  const [port, setPort] = useState(3260);
  const [luns, setLuns] = useState<LunDraft[] | null>(null);
  const [access, setAccess] = useState<AccessDraft>(accessOf());
  const create = useAction(() => api.post("/api/iscsi/targets", { name, iqn: iqn || `${data.prefix}:${name}`, portalIp, port, luns: (luns ?? []).map(lunReq), ...accessReq(access) }), { success: tr("Target created"), onDone: onClose });
  const steps = [tr("Target"), tr("LUNs"), tr("Access"), tr("Summary")];
  const drafts = luns ?? (b ? [newDraft(name, 0, b)] : []);
  const okStep = [/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(name), drafts.length > 0 && drafts.every(lunReady), true, true][step];
  const next = () => { if (step === 0 && !luns && b) setLuns([newDraft(name, 0, b)]); setStep(step + 1); };
  return (
    <Modal open onClose={onClose} title={tr("New iSCSI target")} wide footer={<>
      {step > 0 && <Button onClick={() => setStep(step - 1)}>{tr("Back")}</Button>}
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      {step < steps.length - 1
        ? <Button variant="primary" disabled={!okStep || (step === 0 && !b)} onClick={next}>{tr("Next")}</Button>
        : <Button variant="primary" loading={create.isPending} onClick={() => create.mutate()}>{tr("Create")}</Button>}
    </>}>
      <div className="mb-5 flex flex-wrap gap-2">
        {steps.map((s, i) => <Badge key={s} tone={i === step ? "accent" : i < step ? "ok" : "neutral"}>{i + 1}. {s}</Badge>)}
      </div>
      <ErrorBox error={create.error} />
      {step === 0 && (
        <div className="space-y-4">
          <Field label={tr("Name")} hint={tr("Lower-case letters, digits and dashes")}><Input autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="vmware" /></Field>
          <Field label={tr("Target name (IQN)")} hint={tr("Generated from the NAS name; change it only if you need a precise name.")}><Input className="mono" value={iqn} onChange={(e) => setIqn(e.target.value)} placeholder={`${data.prefix}:${name || "…"}`} /></Field>
          <div className="grid gap-4 sm:grid-cols-[1fr_140px]">
            <Field label={tr("Portal address")} hint={tr("Empty: every address of the NAS")}><Input className="mono" value={portalIp} onChange={(e) => setPortalIp(e.target.value)} placeholder="0.0.0.0" /></Field>
            <Field label={tr("Port")}><Input type="number" value={port} onChange={(e) => setPort(+e.target.value)} /></Field>
          </div>
        </div>
      )}
      {step === 1 && b && (
        <div className="space-y-5">
          {drafts.map((d, i) => (
            <Card key={i} title={`LUN ${i}`} actions={drafts.length > 1 && <IconButton title={tr("Remove")} onClick={() => setLuns(drafts.filter((_, j) => j !== i))}><Trash2 size={15} /></IconButton>}>
              <LunForm d={d} b={b} set={(x) => setLuns(drafts.map((y, j) => (j === i ? x : y)))} />
            </Card>
          ))}
          <Button icon={<Plus size={15} />} onClick={() => setLuns([...drafts, newDraft(name, drafts.length, b)])}>{tr("Add a LUN")}</Button>
        </div>
      )}
      {step === 2 && <AccessForm a={access} set={setAccess} />}
      {step === 3 && (
        <div className="space-y-2 text-[13px]">
          <div><span className="text-muted">{tr("Target name (IQN)")}: </span><span className="mono">{iqn || `${data.prefix}:${name}`}</span></div>
          <div><span className="text-muted">{tr("Portal")}: </span><span className="mono">{portalIp || "0.0.0.0"}:{port}</span></div>
          {drafts.map((d, i) => { const r = lunReq(d); return <div key={i}><span className="text-muted">LUN {i}: </span>{kindLabel(r.kind)} <span className="mono">{r.path}</span>{r.create && r.size ? ` · ${bytes(r.size)}` : ""}{r.readOnly ? ` · ${tr("read-only")}` : ""}</div>; })}
          <div><span className="text-muted">{tr("Access")}: </span>{access.allowAny ? tr("any initiator (unsafe)") : tr("{0} initiator(s)", accessReq(access).initiators.length)}{access.chap ? ` · CHAP${access.mutual ? " " + tr("mutual") : ""}` : ""}</div>
          {!access.allowAny && !accessReq(access).initiators.length && <Notice tone="warn">{tr("No initiator is allowed yet: nobody will be able to connect.")}</Notice>}
        </div>
      )}
    </Modal>
  );
}

/* ---------- Dialogs ---------- */

function EditAccess({ t, onClose }: { t: Target; onClose: () => void }) {
  const [iqn, setIqn] = useState(t.iqn);
  const [portalIp, setPortalIp] = useState(t.portalIp ?? "");
  const [port, setPort] = useState(t.port || 3260);
  const [a, setA] = useState(accessOf(t));
  const save = useAction(() => api.put(`/api/iscsi/targets/${t.id}`, { iqn, portalIp, port, ...accessReq(a) }), { success: tr("Target updated"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("Target {0}", t.name)} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <ErrorBox error={save.error} />
        {t.sessions.length > 0 && <Notice tone="warn">{tr("Connected initiators are disconnected for a moment and reconnect by themselves.")}</Notice>}
        <Field label={tr("Target name (IQN)")} hint={tr("Initiators must be configured again after a change.")}><Input className="mono" value={iqn} onChange={(e) => setIqn(e.target.value)} /></Field>
        <div className="grid gap-4 sm:grid-cols-[1fr_140px]">
          <Field label={tr("Portal address")} hint={tr("Empty: every address of the NAS")}><Input className="mono" value={portalIp} onChange={(e) => setPortalIp(e.target.value)} placeholder="0.0.0.0" /></Field>
          <Field label={tr("Port")}><Input type="number" value={port} onChange={(e) => setPort(+e.target.value)} /></Field>
        </div>
        <AccessForm a={a} set={setA} />
      </div>
    </Modal>
  );
}

function AddLun({ t, onClose }: { t: Target; onClose: () => void }) {
  const { data: b } = useQuery({ queryKey: ["iscsi-backings"], queryFn: () => api.get<Backings>("/api/iscsi/backings") });
  const n = (() => { let i = 0; while (t.luns.some((l) => l.lun === i)) i++; return i; })();
  const [d, setD] = useState<LunDraft | null>(null);
  const draft = d ?? (b ? newDraft(t.name, n, b) : null);
  const add = useAction(() => api.post(`/api/iscsi/targets/${t.id}/luns`, lunReq(draft!)), { success: tr("LUN added"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("New LUN for {0}", t.name)} wide footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" disabled={!draft || !lunReady(draft)} loading={add.isPending} onClick={() => add.mutate()}>{tr("Add")}</Button>
    </>}>
      <ErrorBox error={add.error} />
      {!b || !draft ? <Spinner /> : <LunForm d={draft} b={b} set={setD} />}
    </Modal>
  );
}

function EditLun({ t, l, onClose }: { t: Target; l: LUN; onClose: () => void }) {
  const [wc, setWc] = useState(!!l.writeCache);
  const [ro, setRo] = useState(!!l.readOnly);
  const save = useAction(() => api.put(`/api/iscsi/targets/${t.id}/luns/${l.lun}`, { writeCache: wc, readOnly: ro }), { success: tr("LUN updated"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={tr("LUN {0} of {1}", l.lun, t.name)} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-3">
        <ErrorBox error={save.error} />
        {l.kind === "fileio"
          ? <Checkbox checked={wc} onChange={setWc} label={tr("Write cache (faster; recent writes are lost on a power cut)")} />
          : <div className="text-muted text-[12.5px]">{tr("The write cache of a block device follows the device itself (ZFS: the sync property).")}</div>}
        <Checkbox checked={ro} onChange={setRo} label={tr("Read-only")} />
        {t.sessions.length > 0 && <Notice tone="warn">{tr("Connected initiators are disconnected for a moment and reconnect by themselves.")}</Notice>}
      </div>
    </Modal>
  );
}

function DeleteDialog({ title, sessions, onClose, onDelete, pending }: { title: string; sessions: Session[]; onClose: () => void; onDelete: (backing: boolean, force: boolean) => void; pending: boolean }) {
  const [backing, setBacking] = useState(false);
  const [force, setForce] = useState(false);
  return (
    <Modal open onClose={onClose} title={title} footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="danger" loading={pending} disabled={sessions.length > 0 && !force} onClick={() => onDelete(backing, force)}>{tr("Delete")}</Button>
    </>}>
      <div className="space-y-4 text-[13.5px]">
        {sessions.length > 0 && (
          <>
            <Notice tone="warn">{tr("Initiators are connected: {0}. They lose the disk at once, with the risk of losing data being written.", sessions.map((s) => s.initiator).join(", "))}</Notice>
            <Checkbox checked={force} onChange={setForce} label={tr("Disconnect them")} />
          </>
        )}
        <Checkbox checked={backing} onChange={setBacking} label={tr("Also delete the backing storage (file, logical volume or ZFS volume): its data is lost")} />
      </div>
    </Modal>
  );
}

/* ---------- Page ---------- */

function TargetCard({ t, enabled }: { t: Target; enabled: boolean }) {
  const { toast } = useUI();
  const [edit, setEdit] = useState(false);
  const [add, setAdd] = useState(false);
  const [lun, setLun] = useState<LUN | null>(null);
  const [del, setDel] = useState(false);
  const [delLun, setDelLun] = useState<LUN | null>(null);
  const remove = useAction((p: { backing: boolean; force: boolean }) => api.del(`/api/iscsi/targets/${t.id}?backing=${p.backing ? 1 : 0}&force=${p.force ? 1 : 0}`), { success: tr("Target deleted"), onDone: () => setDel(false) });
  const removeLun = useAction((p: { lun: number; backing: boolean; force: boolean }) => api.del(`/api/iscsi/targets/${t.id}/luns/${p.lun}?backing=${p.backing ? 1 : 0}&force=${p.force ? 1 : 0}`), { success: tr("LUN removed"), onDone: () => setDelLun(null) });
  const snap = useAction((n: number) => api.post<{ snapshot: string }>(`/api/iscsi/targets/${t.id}/luns/${n}/snapshot`), { success: (r) => tr("Snapshot {0} created", r.snapshot) });
  return (
    <Card title={<span className="flex flex-wrap items-center gap-2">{t.name}
      {enabled && (t.live ? <Badge tone="ok" dot>{tr("active")}</Badge> : <Badge tone="danger" dot>{tr("not running")}</Badge>)}
      {t.allowAny ? <Badge tone="danger"><ShieldAlert size={11} className="mr-1 inline" />{tr("any initiator")}</Badge> : <Badge><Users size={11} className="mr-1 inline" />{tr("{0} initiator(s)", t.initiators.length)}</Badge>}
      {t.chap.user && <Badge tone="accent"><KeyRound size={11} className="mr-1 inline" />CHAP{t.chap.mutualUser ? " " + tr("mutual") : ""}</Badge>}
    </span>} actions={<>
      <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setAdd(true)}>LUN</Button>
      <Button size="sm" variant="ghost" icon={<Pencil size={14} />} onClick={() => setEdit(true)}>{tr("Access")}</Button>
      <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={() => setDel(true)} />
    </>}>
      <div className="mb-3 flex flex-wrap items-center gap-x-5 gap-y-1 text-[12.5px]">
        <span className="flex items-center gap-1"><span className="mono">{t.iqn}</span><IconButton title={tr("Copy")} onClick={() => { navigator.clipboard?.writeText(t.iqn); toast(tr("Name copied")); }}><Copy size={13} /></IconButton></span>
        <span className="text-muted">{tr("Portal")} <span className="mono text-fg">{t.portal}</span></span>
      </div>
      {!t.allowAny && !t.initiators.length && <div className="mb-3"><Notice tone="warn">{tr("No initiator is allowed: nobody can connect. Add initiator names under Access.")}</Notice></div>}
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="table">
          <thead><tr><th>LUN</th><th>{tr("Backing storage")}</th><th>{tr("Size")}</th><th>{tr("Options")}</th><th /></tr></thead>
          <tbody>
            {t.luns.map((l) => (
              <tr key={l.lun}>
                <td>{l.lun}</td>
                <td><div>{kindLabel(l.kind)}</div><div className="mono text-[12px] text-muted">{l.path}</div>{l.missing && <Badge tone="danger">{tr("missing")}</Badge>}</td>
                <td>{bytes(l.size)}</td>
                <td className="space-x-1">{l.readOnly && <Badge>{tr("read-only")}</Badge>}{l.writeCache && <Badge tone="warn">{tr("write cache")}</Badge>}</td>
                <td className="text-right whitespace-nowrap">
                  {l.snapshots && <IconButton title={tr("Snapshot")} onClick={() => snap.mutate(l.lun)}><Camera size={14} /></IconButton>}
                  <IconButton title={tr("Options")} onClick={() => setLun(l)}><Pencil size={14} /></IconButton>
                  <IconButton title={tr("Remove")} onClick={() => setDelLun(l)}><Trash2 size={14} /></IconButton>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-4">
        <div className="mb-1.5 text-[12.5px] font-medium text-muted">{tr("Connected initiators")}</div>
        {!enabled ? <div className="text-muted text-[12.5px]">{tr("The iSCSI service is off.")}</div> : !t.sessions.length ? <div className="text-muted text-[12.5px]">{tr("None")}</div> : (
          <div className="flex flex-wrap gap-1.5">{t.sessions.map((s, i) => <Badge key={i} tone="ok"><span className="mono">{s.initiator}</span>{s.address ? ` · ${s.address}` : ""}</Badge>)}</div>
        )}
      </div>
      {edit && <EditAccess t={t} onClose={() => setEdit(false)} />}
      {add && <AddLun t={t} onClose={() => setAdd(false)} />}
      {lun && <EditLun t={t} l={lun} onClose={() => setLun(null)} />}
      {del && <DeleteDialog title={tr("Delete {0}?", t.name)} sessions={t.sessions} pending={remove.isPending} onClose={() => setDel(false)} onDelete={(backing, force) => remove.mutate({ backing, force })} />}
      {delLun && <DeleteDialog title={tr("Remove LUN {0}?", delLun.lun)} sessions={t.sessions} pending={removeLun.isPending} onClose={() => setDelLun(null)} onDelete={(backing, force) => removeLun.mutate({ lun: delLun.lun, backing, force })} />}
    </Card>
  );
}

export default function Iscsi() {
  const { confirm } = useUI();
  const [create, setCreate] = useState(false);
  const { data, isLoading, error } = useQuery({ queryKey: ["iscsi"], queryFn: () => api.get<Data>("/api/iscsi"), refetchInterval: 10000 });
  const install = useAction(() => api.post("/api/system/components/iscsi/install"));
  const toggle = useAction((p: { enabled: boolean; force: boolean }) => api.put("/api/iscsi/settings", p), { success: tr("iSCSI service updated") });
  const header = <PageHeader title="iSCSI" subtitle={tr("Virtual disks for hypervisors, Windows and other servers")} icon={<HardDriveUpload size={20} />} actions={data?.installed && <Button variant="primary" icon={<Plus size={15} />} onClick={() => setCreate(true)}>{tr("Create a target")}</Button>} />;
  if (isLoading || !data) return <div>{header}<ErrorBox error={error} /><Spinner /></div>;
  const connected = data.targets.reduce((n, t) => n + t.sessions.length, 0);
  const setEnabled = async (v: boolean) => {
    if (!v && connected > 0 && !(await confirm({ title: tr("Stop the iSCSI service?"), text: tr("{0} initiator(s) are connected and lose their disks at once.", connected), danger: true }))) return;
    toggle.mutate({ enabled: v, force: !v });
  };
  return (
    <div>
      {header}
      <ErrorBox error={error} />
      {!data.supported && <div className="mb-4"><Notice tone="warn">{tr("This kernel has no iSCSI target module (iscsi_target_mod).")}</Notice></div>}
      {!data.installed ? (
        <Card>
          <Empty icon={<HardDriveUpload />} title={tr("The iSCSI target is not installed")} text={tr("It serves volumes as network disks (LUNs) to VMware, Proxmox, Hyper-V, Windows or Linux servers, with the Linux kernel target (LIO).")} action={<Button variant="primary" icon={<Plug size={15} />} loading={install.isPending} onClick={() => install.mutate()}>{tr("Install")}</Button>} />
        </Card>
      ) : (
        <div className="space-y-5">
          <Card>
            <Switch checked={data.enabled} disabled={toggle.isPending} onChange={setEnabled} label={tr("Enable the iSCSI service")} hint={data.enabled && data.ports.length ? tr("Listening on TCP port {0}. Address blocking (Control Panel) applies to it as to the other services.", data.ports.join(", ")) : tr("Nothing listens on the iSCSI port while the service is off.")} />
          </Card>
          {!data.targets.length ? (
            <Card><Empty icon={<HardDriveUpload />} title={tr("No targets")} text={tr("A target groups one or more LUNs (disks) that initiators connect to.")} action={<Button variant="primary" onClick={() => setCreate(true)}>{tr("Create a target")}</Button>} /></Card>
          ) : data.targets.map((t) => <TargetCard key={t.id} t={t} enabled={data.enabled} />)}
        </div>
      )}
      {create && <CreateTarget data={data} onClose={() => setCreate(false)} />}
    </div>
  );
}
