// Folders of this computer synchronized with the NAS shown: their state,
// the deletion brake, conflicts and skipped items, adding and changing them
// (end-to-end encrypted folders included).
import { useState } from "react";
import { open, save } from "@tauri-apps/plugin-dialog";
import { invoke } from "@tauri-apps/api/core";
import { AlertTriangle, ArrowLeftRight, FolderOpen, FolderPlus, KeyRound, Lock, Pause, Play, RefreshCw, Trash2 } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Field, Input, Modal, Notice, Progress, Switch, useUI } from "@nas/components/ui";
import { dateTime } from "@nas/lib/format";
import { agent } from "../lib/agent";
import { useApp } from "../lib/state";
import { t, tk } from "../lib/i18n";
import { PageHeader } from "../components/kit";
import { RemoteFolderPicker } from "../components/RemoteFolderPicker";
import type { PairStatus, SyncPair } from "../lib/types";

export function SyncPage() {
  const { account, accountId, refresh } = useApp();
  const { toast, confirm } = useUI();
  const [adding, setAdding] = useState(false);
  const base = `/v1/accounts/${encodeURIComponent(accountId)}`;
  if (!account) return null;
  const st = account.status;
  const pairs = account.config.pairs ?? [];
  const status = new Map((st.pairs ?? []).map((p) => [p.id, p]));
  const act = async (f: () => Promise<unknown>) => {
    try {
      await f();
      refresh();
    } catch (e) {
      toast(String((e as Error).message ?? e), "error");
    }
  };
  return (
    <>
      <PageHeader title={t("Folder sync")} subtitle={t("Folders of this computer kept identical with folders of {0}", account.config.nasName ?? "")} actions={<>
        {pairs.length > 0 && <Button icon={st.paused ? <Play size={15} /> : <Pause size={15} />} onClick={() => act(() => agent.post(`${base}/pause`, { on: !st.paused }))}>{tk(st.paused ? "action.resumeall" : "action.pauseall")}</Button>}
        {pairs.length > 0 && <Button icon={<RefreshCw size={15} />} disabled={st.paused} onClick={() => act(() => agent.post(`${base}/sync-now`, {}))}>{tk("action.syncnow")}</Button>}
        <Button variant="primary" icon={<FolderPlus size={15} />} onClick={() => setAdding(true)}>{tk("folders.add")}</Button>
      </>} />
      {st.signedOut && <div className="mb-4"><Notice tone="warn">{tk("status.signedout")}</Notice></div>}
      {!pairs.length ? (
        <Card><Empty icon={<ArrowLeftRight />} title={t("No folder synchronized yet")} text={tk("folders.localhint")} action={<Button variant="primary" icon={<FolderPlus size={15} />} onClick={() => setAdding(true)}>{tk("folders.add")}</Button>} /></Card>
      ) : (
        <div className="space-y-3">
          {pairs.map((p) => <PairCard key={p.id} pair={p} status={status.get(p.id)} base={base} act={act} confirm={confirm} />)}
        </div>
      )}
      {adding && <AddPair onClose={() => setAdding(false)} />}
    </>
  );
}

function StateBadge({ s, paused }: { s?: PairStatus; paused?: boolean }) {
  if (paused || s?.state === "paused") return <Badge>{tk("state.paused")}</Badge>;
  switch (s?.state) {
    case "syncing": return <Badge tone="accent" dot>{tk("state.syncing")}</Badge>;
    case "error": return <Badge tone="danger">{tk("state.error")}</Badge>;
    case "brake": return <Badge tone="warn">{tk("state.brake")}</Badge>;
  }
  return <Badge tone="ok">{tk("state.uptodate")}</Badge>;
}

type Confirm = ReturnType<typeof useUI>["confirm"];

function PairCard({ pair: p, status: s, base, act, confirm }: { pair: SyncPair; status?: PairStatus; base: string; act: (f: () => Promise<unknown>) => Promise<void>; confirm: Confirm }) {
  const [exclude, setExclude] = useState<string | null>(null);
  const [recovery, setRecovery] = useState("");
  const [pass, setPass] = useState(false);
  const pb = `${base}/pairs/${p.id}`;
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge s={s} paused={p.paused} />
        {p.encrypted && <Badge tone="ok"><Lock size={11} className="mr-1 inline" />{tk("folders.encrypted")}</Badge>}
        <span className="mono selectable truncate text-[13px]">{p.local}</span>
        <ArrowLeftRight size={14} className="text-muted" />
        <span className="mono selectable truncate text-[13px] text-muted">{p.remote}</span>
      </div>
      {s?.state === "syncing" && s.pending > 0 && <div className="mt-2 text-[12.5px] text-muted">{tk("status.progress", s.done, s.pending)}</div>}
      {s?.current && <div className="mt-1.5 space-y-1"><div className="mono truncate text-[12px] text-muted">{s.current}</div><Progress value={Math.round((s.progress ?? 0) * 100)} /></div>}
      {s?.error && <div className="mt-2"><ErrorBox error={s.error} /></div>}
      {s?.brake && (
        <div className="mt-3 space-y-2 rounded-xl border border-warn/40 bg-warn/10 p-3 text-[13px]">
          <div className="flex items-center gap-2 font-medium"><AlertTriangle size={16} className="text-warn" />{tk("brake.text", s.brake.localDeletes + s.brake.remoteDeletes, s.brake.files)}</div>
          {s.brake.localDeletes > 0 && <p className="text-[12.5px]">{tk("brake.local", s.brake.localDeletes)}</p>}
          {s.brake.remoteDeletes > 0 && <p className="text-[12.5px]">{tk("brake.remote", s.brake.remoteDeletes)}</p>}
          <div className="flex gap-2">
            <Button size="sm" variant="danger" onClick={() => act(() => agent.post(`${base}/brake`, { pair: p.id, apply: true }))}>{tk("brake.apply")}</Button>
            <Button size="sm" onClick={() => act(() => agent.post(`${base}/brake`, { pair: p.id, apply: false }))}>{tk("brake.restore")}</Button>
          </div>
        </div>
      )}
      {s && (
        <div className="mt-2 text-[12px] text-muted">
          {tk("status.files", s.files)} · {tk("status.last", s.lastSync ? dateTime(s.lastSync) : "—")}{s.conflicts ? ` · ${tk("status.conflicts", s.conflicts)}` : ""}
        </div>
      )}
      {s && ((s.skipped?.length ?? 0) > 0 || (s.links ?? 0) > 0) && (
        <details className="mt-2 text-[12.5px]">
          <summary className="cursor-pointer text-muted">{tk("status.skipped", (s.skipped?.length ?? 0) + (s.links ?? 0))}</summary>
          <ul className="mt-1.5 space-y-0.5 pl-4">
            {(s.links ?? 0) > 0 && <li>{tk("status.links", s.links)}</li>}
            {s.skipped?.map((x) => <li key={x.path}><span className="mono">{x.path}</span>: {x.reason}</li>)}
          </ul>
        </details>
      )}
      <div className="mt-3 flex flex-wrap gap-2 border-t border-line pt-3">
        <Button size="sm" icon={<FolderOpen size={14} />} onClick={() => act(() => agent.post("/v1/open", { account: base.split("/")[3], pair: p.id }))}>{tk("action.openfolder")}</Button>
        <Button size="sm" icon={p.paused ? <Play size={14} /> : <Pause size={14} />} onClick={() => act(() => agent.put(pb, { paused: !p.paused }))}>{tk(p.paused ? "action.resume" : "action.pause")}</Button>
        <Button size="sm" onClick={() => setExclude((p.exclude ?? []).join("\n"))}>{t("Excluded subfolders")}</Button>
        {p.encrypted && <Button size="sm" icon={<KeyRound size={14} />} onClick={() => act(async () => setRecovery((await agent.get<{ recoveryKey: string }>(`${pb}/recovery`)).recoveryKey))}>{tk("folders.recovery")}</Button>}
        {p.encrypted && <Button size="sm" onClick={() => setPass(true)}>{tk("folders.changepass")}</Button>}
        <Button size="sm" variant="ghost" className="ml-auto text-danger" icon={<Trash2 size={14} />} onClick={async () => {
          if (await confirm({ title: tk("folders.remove"), text: tk("folders.removeconfirm"), danger: true })) await act(() => agent.del(pb));
        }}>{tk("folders.remove")}</Button>
      </div>
      {exclude !== null && (
        <Modal open onClose={() => setExclude(null)} title={t("Excluded subfolders")} footer={<>
          <Button onClick={() => setExclude(null)}>{t("Cancel")}</Button>
          <Button variant="primary" onClick={() => act(() => agent.put(pb, { exclude: exclude.split("\n").map((x) => x.trim()).filter(Boolean) })).then(() => setExclude(null))}>{tk("action.save")}</Button>
        </>}>
          <Field label={tk("folders.exclude")}><textarea className="input mono h-40" spellCheck={false} value={exclude} onChange={(e) => setExclude(e.target.value)} /></Field>
        </Modal>
      )}
      {recovery && <RecoveryModal keyText={recovery} folder={p.local} onClose={() => setRecovery("")} />}
      {pass && <PassphraseModal url={`${pb}/passphrase`} onClose={() => setPass(false)} />}
    </Card>
  );
}

export function RecoveryModal({ keyText, folder, onClose }: { keyText: string; folder: string; onClose: () => void }) {
  const { toast } = useUI();
  return (
    <Modal open onClose={onClose} title={tk("folders.recovery")} footer={<>
      <Button onClick={async () => {
        const dest = await save({ defaultPath: "strataos-recovery-key.txt" });
        if (dest) {
          try {
            await invoke("save_text", { path: dest, text: `${tk("folders.recoverytext", folder)}\n\n${keyText}\n` });
            toast(t("Saved"));
          } catch (e) {
            toast(String(e), "error");
          }
        }
      }}>{tk("folders.recoverysave")}</Button>
      <Button variant="primary" onClick={onClose}>{t("Close")}</Button>
    </>}>
      <p className="mb-3 text-[13px]">{tk("folders.recoverytext", folder)}</p>
      <div className="mono selectable break-all rounded-xl border border-line bg-panel-2 p-3 text-[13px]">{keyText}</div>
    </Modal>
  );
}

function PassphraseModal({ url, onClose }: { url: string; onClose: () => void }) {
  const { toast } = useUI();
  const [a, setA] = useState("");
  const [b, setB] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <Modal open onClose={onClose} title={tk("folders.changepass")} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={busy} disabled={a.length < 10} onClick={async () => {
        if (a !== b) return setError(new Error(tk("folders.passmismatch")));
        setBusy(true);
        try {
          await agent.post(url, { passphrase: a });
          toast(tk("folders.passchanged"));
          onClose();
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}>{tk("action.save")}</Button>
    </>}>
      <div className="space-y-3">
        <Field label={tk("folders.newpass")}><Input type="password" value={a} onChange={(e) => setA(e.target.value)} autoComplete="new-password" /></Field>
        <Field label={tk("folders.passphrase2")}><Input type="password" value={b} onChange={(e) => setB(e.target.value)} autoComplete="new-password" /></Field>
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}

function AddPair({ onClose }: { onClose: () => void }) {
  const { accountId, state, refresh } = useApp();
  const [local, setLocal] = useState("");
  const [remote, setRemote] = useState("");
  const [encrypt, setEncrypt] = useState(false);
  const [locked, setLocked] = useState("");
  const [pass, setPass] = useState("");
  const [pass2, setPass2] = useState("");
  const [rec, setRec] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [recovery, setRecovery] = useState("");

  async function add() {
    setError(null);
    if (encrypt && pass !== pass2) return setError(new Error(tk("folders.passmismatch")));
    setBusy(true);
    try {
      const r = await agent.post<{ locked?: boolean; message?: string; recoveryKey?: string }>(`/v1/accounts/${encodeURIComponent(accountId)}/pairs`, {
        local, remote, encrypt, passphrase: pass, recoveryKey: rec,
      });
      if (r.locked) {
        setLocked(r.message ?? "");
        return;
      }
      refresh();
      if (r.recoveryKey) setRecovery(r.recoveryKey);
      else onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  if (recovery) return <RecoveryModal keyText={recovery} folder={local} onClose={onClose} />;
  const sep = state?.sep ?? "/";
  return (
    <Modal open wide onClose={onClose} title={tk("folders.add")} footer={<>
      <Button onClick={onClose}>{t("Cancel")}</Button>
      <Button variant="primary" loading={busy} disabled={!local || !remote || (encrypt && pass.length < 10) || (!!locked && !pass && !rec)} onClick={() => void add()}>{tk("folders.addbutton")}</Button>
    </>}>
      <div className="space-y-4">
        <Field label={tk("folders.local")} hint={tk("folders.localhint")}>
          <div className="flex gap-2">
            <Input value={local} onChange={(e) => setLocal(e.target.value)} placeholder={`${state?.home ?? ""}${sep}StrataOS`} className="mono" />
            <Button type="button" onClick={async () => {
              const d = await open({ directory: true, title: tk("folders.local"), defaultPath: state?.home });
              if (typeof d === "string") setLocal(d);
            }}>{tk("action.browse")}</Button>
          </div>
        </Field>
        <Field label={tk("folders.remote")}>
          <RemoteFolderPicker account={accountId} value={remote} onChange={(p) => { setRemote(p); setLocked(""); }} />
        </Field>
        {remote && <div className="mono text-[12px] text-muted">{remote}</div>}
        {locked ? (
          <div className="space-y-3 rounded-xl border border-line p-3">
            <p className="text-[13px]">{locked}</p>
            <Field label={tk("folders.passphrase")}><Input type="password" value={pass} onChange={(e) => setPass(e.target.value)} /></Field>
            <Field label={tk("folders.recoverykey")}><Input value={rec} onChange={(e) => setRec(e.target.value)} className="mono" /></Field>
          </div>
        ) : (
          <>
            <Switch checked={encrypt} onChange={setEncrypt} label={tk("folders.encrypt")} hint={tk("folders.e2ehint")} />
            {encrypt && (
              <div className="grid grid-cols-2 gap-3">
                <Field label={tk("folders.newpass")}><Input type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="new-password" /></Field>
                <Field label={tk("folders.passphrase2")}><Input type="password" value={pass2} onChange={(e) => setPass2(e.target.value)} autoComplete="new-password" /></Field>
              </div>
            )}
          </>
        )}
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}
