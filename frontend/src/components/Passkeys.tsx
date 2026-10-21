import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Edit3, FingerprintPattern, Plus, Trash2, X } from "lucide-react";
import { api } from "../lib/api";
import { dateTime } from "../lib/format";
import { srv, tr } from "../lib/i18n";
import { cancelled, createPasskey, passkeysSupported } from "../lib/webauthn";
import { Badge, Button, ErrorBox, Field, Input, Modal, Notice, Spinner, useUI } from "./ui";

interface Passkey {
  id: string;
  name: string;
  rpId: string;
  here: boolean;
  synced: boolean;
  transports: string[];
  createdAt: string;
  lastUsed?: string;
}

interface PasskeyList {
  available: boolean;
  rpId: string;
  reason?: string;
  passkeys: Passkey[];
}

/** Passkeys of the current user: list, add (named, after the password), rename, delete. */
export function PasskeysModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast, confirm } = useUI();
  const list = useQuery({ queryKey: ["passkeys"], queryFn: () => api.get<PasskeyList>("/api/me/passkeys"), enabled: open });
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [edit, setEdit] = useState<{ id: string; name: string } | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setAdding(false); setName(""); setPassword(""); setEdit(null); setErr(null); }
  }, [open]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try { await fn(); } catch (e) { if (!cancelled(e)) setErr(e); } finally { setBusy(false); qc.invalidateQueries({ queryKey: ["passkeys"] }); }
  };
  const add = () => run(async () => {
    const b = await api.post<{ flow: string; publicKey: unknown }>("/api/me/passkeys/begin", { password, name });
    setPassword("");
    const credential = await createPasskey(b.publicKey);
    await api.post("/api/me/passkeys/finish", { flow: b.flow, credential });
    toast(tr("Passkey added"));
    setAdding(false);
    setName("");
  });
  const rename = () => run(async () => {
    await api.put(`/api/me/passkeys/${edit!.id}`, { name: edit!.name });
    setEdit(null);
  });
  const remove = async (k: Passkey) => {
    if (!(await confirm({ title: tr("Delete the passkey {0}?", k.name), text: tr("You will no longer be able to sign in with it. Your password keeps working. Also remove it from your device or password manager."), danger: true }))) return;
    run(async () => {
      await api.del(`/api/me/passkeys/${k.id}`);
      toast(tr("Passkey deleted"));
    });
  };

  const d = list.data;
  const browser = passkeysSupported();
  return (
    <Modal open={open} onClose={onClose} title={tr("Passkeys")} wide>
      {list.isLoading || !d ? <Spinner /> : (
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("Sign in without a password with your fingerprint, your face, your device PIN (Windows Hello, Touch ID, Android) or a security key. A passkey replaces both the password and the two-factor code.")}</p>
          {!d.available ? <Notice tone="warn">{srv(d.reason)}</Notice>
            : !browser && <Notice tone="warn">{tr("This browser does not support passkeys.")}</Notice>}
          {d.available && <p className="text-muted text-[12.5px]">{tr("A passkey only works on the name it was created on. This page is open on {0}.", d.rpId)}</p>}

          {d.passkeys.length === 0 ? <p className="text-muted text-[13px]">{tr("No passkey yet.")}</p> : (
            <div className="divide-y divide-border rounded-xl border border-border">
              {d.passkeys.map((k) => (
                <div key={k.id} className="flex items-center gap-3 px-4 py-3">
                  <FingerprintPattern size={18} className={k.here ? "text-accent" : "text-muted"} />
                  <div className="min-w-0 flex-1">
                    {edit?.id === k.id ? (
                      <div className="flex items-center gap-1">
                        <Input autoFocus value={edit.name} maxLength={64} onChange={(e) => setEdit({ ...edit, name: e.target.value })} onKeyDown={(e) => { if (e.key === "Enter") rename(); if (e.key === "Escape") setEdit(null); }} />
                        <Button size="sm" variant="ghost" icon={<Check size={14} />} loading={busy} onClick={rename} />
                        <Button size="sm" variant="ghost" icon={<X size={14} />} onClick={() => setEdit(null)} />
                      </div>
                    ) : (
                      <div className="flex flex-wrap items-center gap-1.5 font-medium">
                        <span className="truncate">{k.name}</span>
                        <Badge tone={k.here ? "accent" : "neutral"}>{k.rpId}</Badge>
                        {k.synced && <Badge>{tr("Synced")}</Badge>}
                      </div>
                    )}
                    <div className="text-muted text-[12px]">
                      {tr("Added {0}", dateTime(k.createdAt))} · {k.lastUsed ? tr("last used {0}", dateTime(k.lastUsed)) : tr("never used")}
                      {!k.here && <> · {tr("usable on {0} only", k.rpId)}</>}
                    </div>
                  </div>
                  {edit?.id !== k.id && <>
                    <Button size="sm" variant="ghost" title={tr("Rename")} icon={<Edit3 size={14} />} onClick={() => setEdit({ id: k.id, name: k.name })} />
                    <Button size="sm" variant="ghost" className="text-danger" title={tr("Delete")} icon={<Trash2 size={14} />} onClick={() => remove(k)} />
                  </>}
                </div>
              ))}
            </div>
          )}

          {adding ? (
            <div className="space-y-3 rounded-xl bg-panel-2 p-4">
              <Field label={tr("Name of the passkey")} hint={tr("e.g. Work laptop, iPhone, YubiKey")}>
                <Input autoFocus value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />
              </Field>
              <Field label={tr("Confirm your password")}>
                <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
              <ErrorBox error={err} />
              <div className="flex gap-2">
                <Button variant="primary" icon={<FingerprintPattern size={15} />} loading={busy} disabled={!password || !name.trim()} onClick={add}>{tr("Create the passkey")}</Button>
                <Button onClick={() => { setAdding(false); setErr(null); }}>{tr("Cancel")}</Button>
              </div>
            </div>
          ) : (
            <>
              <ErrorBox error={err} />
              <Button variant="primary" icon={<Plus size={15} />} disabled={!d.available || !browser} onClick={() => setAdding(true)}>{tr("Add a passkey")}</Button>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}
