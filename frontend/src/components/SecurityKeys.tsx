import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Edit3, KeyRound, Link2, Plus, RefreshCw, Trash2, Unlink, X } from "lucide-react";
import { api } from "../lib/api";
import type { Me } from "../lib/types";
import { dateTime } from "../lib/format";
import { srv, tr } from "../lib/i18n";
import { cancelled, createPasskey, passkeysSupported } from "../lib/webauthn";
import { Badge, Button, ErrorBox, Field, Input, Modal, Notice, Spinner, useUI } from "./ui";

interface Key {
  id: string;
  name: string;
  rpId: string;
  here: boolean;
  transports: string[];
  createdAt: string;
  lastUsed?: string;
}

interface KeyList {
  available: boolean;
  rpId: string;
  reason?: string;
  keys: Key[];
  recoveryCodes: number;
  totp: boolean;
  password: boolean;
  keyRequired: boolean;
}

interface Links {
  providers: { id: string; name: string }[];
  links: { provider: string; name: string; created: boolean; linked: string; lastUsed?: string }[];
}

/** Recovery codes, shown once. */
export function RecoveryCodes({ codes }: { codes: string[] }) {
  const { toast } = useUI();
  return (
    <div className="space-y-2 rounded-xl border border-border p-4">
      <p className="font-medium">{tr("Your recovery codes")}</p>
      <p className="text-muted text-[12.5px]">{tr("Each code signs you in once if you lose your security key or phone. Keep them somewhere safe: they will not be shown again.")}</p>
      <div className="mono grid grid-cols-2 gap-1 text-[13px]">{codes.map((c) => <span key={c}>{c}</span>)}</div>
      <Button size="sm" icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(codes.join("\n")); toast(tr("Copied")); }}>{tr("Copy")}</Button>
    </div>
  );
}

/** Security keys (second factor after the password), recovery codes and
 *  the identity providers linked to the account. */
export function SecurityKeysModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast, confirm } = useUI();
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me"), enabled: open }).data;
  const list = useQuery({ queryKey: ["security-keys"], queryFn: () => api.get<KeyList>("/api/me/security-keys"), enabled: open });
  const links = useQuery({ queryKey: ["oidc-links"], queryFn: () => api.get<Links>("/api/me/oidc"), enabled: open });
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [edit, setEdit] = useState<{ id: string; name: string } | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [regen, setRegen] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setAdding(false); setName(""); setPassword(""); setEdit(null); setErr(null); setCodes(null); setRegen(false); }
  }, [open]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try { await fn(); } catch (e) { if (!cancelled(e)) setErr(e); } finally {
      setBusy(false);
      qc.invalidateQueries({ queryKey: ["security-keys"] });
      qc.invalidateQueries({ queryKey: ["oidc-links"] });
      qc.invalidateQueries({ queryKey: ["me"] });
    }
  };
  const add = () => run(async () => {
    const b = await api.post<{ flow: string; publicKey: unknown }>("/api/me/security-keys/begin", { password, name });
    setPassword("");
    const credential = await createPasskey(b.publicKey);
    const r = await api.post<{ recoveryCodes?: string[] }>("/api/me/security-keys/finish", { flow: b.flow, credential });
    toast(tr("Security key added"));
    if (r.recoveryCodes) setCodes(r.recoveryCodes);
    setAdding(false);
    setName("");
  });
  const newCodes = () => run(async () => {
    const r = await api.post<{ recoveryCodes: string[] }>("/api/me/recovery-codes", { password });
    setPassword("");
    setRegen(false);
    setCodes(r.recoveryCodes);
  });
  const rename = () => run(async () => {
    await api.put(`/api/me/security-keys/${edit!.id}`, { name: edit!.name });
    setEdit(null);
  });
  const remove = async (k: Key) => {
    if (!(await confirm({ title: tr("Remove the security key {0}?", k.name), text: tr("It will no longer complete your sign-in."), danger: true }))) return;
    run(async () => {
      await api.del(`/api/me/security-keys/${k.id}`);
      toast(tr("Security key removed"));
    });
  };
  const unlink = async (p: { provider: string; name: string }) => {
    if (!(await confirm({ title: tr("Unlink {0}?", p.name || p.provider), text: tr("You will no longer be able to sign in with it."), danger: true }))) return;
    run(async () => { await api.del(`/api/me/oidc/${encodeURIComponent(p.provider)}`); });
  };
  const link = (id: string) => {
    window.location.href = `/api/login/oidc/${encodeURIComponent(id)}?link=1&next=${encodeURIComponent(location.pathname)}`;
  };

  const d = list.data;
  const browser = passkeysSupported();
  const needPassword = !me?.noPassword;
  return (
    <Modal open={open} onClose={onClose} title={tr("Security keys and sign-in")} wide>
      {list.isLoading || !d ? <Spinner /> : (
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("A security key (YubiKey, SoloKey, Titan…) plugged in or tapped after your password completes your sign-in. Unlike a passkey, it does not replace the password. Register two keys and keep one in a safe place.")}</p>
          {!window.isSecureContext ? <Notice tone="warn">{tr("Security keys need a secure connection: open the NAS over HTTPS (port 7443) by its name, e.g. https://{0}:7443.", location.hostname)}</Notice>
            : !d.available ? <Notice tone="warn">{srv(d.reason)}</Notice>
            : !browser && <Notice tone="warn">{tr("This browser does not support security keys.")}</Notice>}
          {d.keyRequired && <Notice>{tr("Administrators must use a security key or a passkey: the authenticator app alone is not accepted.")}</Notice>}
          {d.available && <p className="text-muted text-[12.5px]">{tr("A security key only works on the name it was registered on. This page is open on {0}.", d.rpId)}</p>}

          {d.keys.length === 0 ? <p className="text-muted text-[13px]">{tr("No security key yet.")}</p> : (
            <div className="divide-y divide-border rounded-xl border border-border">
              {d.keys.map((k) => (
                <div key={k.id} className="flex items-center gap-3 px-4 py-3">
                  <KeyRound size={18} className={k.here ? "text-accent" : "text-muted"} />
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
                      </div>
                    )}
                    <div className="text-muted text-[12px]">
                      {tr("Added {0}", dateTime(k.createdAt))} · {k.lastUsed ? tr("last used {0}", dateTime(k.lastUsed)) : tr("never used")}
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

          {codes && <RecoveryCodes codes={codes} />}

          {adding || regen ? (
            <div className="space-y-3 rounded-xl bg-panel-2 p-4">
              {adding && (
                <Field label={tr("Name of the key")} hint={tr("e.g. YubiKey blue, Spare key")}>
                  <Input autoFocus value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />
                </Field>
              )}
              {needPassword && (
                <Field label={tr("Confirm your password")}>
                  <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </Field>
              )}
              <ErrorBox error={err} />
              <div className="flex gap-2">
                {adding
                  ? <Button variant="primary" icon={<KeyRound size={15} />} loading={busy} disabled={(needPassword && !password) || !name.trim()} onClick={add}>{tr("Register the key")}</Button>
                  : <Button variant="primary" icon={<RefreshCw size={15} />} loading={busy} disabled={needPassword && !password} onClick={newCodes}>{tr("Generate new codes")}</Button>}
                <Button onClick={() => { setAdding(false); setRegen(false); setErr(null); }}>{tr("Cancel")}</Button>
              </div>
            </div>
          ) : (
            <>
              <ErrorBox error={err} />
              <div className="flex flex-wrap gap-2">
                <Button variant="primary" icon={<Plus size={15} />} disabled={!d.available || !browser} onClick={() => setAdding(true)}>{tr("Add a security key")}</Button>
                {(d.keys.length > 0 || d.totp) && <Button icon={<RefreshCw size={15} />} onClick={() => setRegen(true)}>{tr("New recovery codes")}</Button>}
              </div>
              {(d.keys.length > 0 || d.totp) && <p className="text-muted text-[12px]">{tr("{0} recovery code(s) left.", d.recoveryCodes)}</p>}
            </>
          )}

          {links.data && (links.data.providers.length > 0 || links.data.links.length > 0) && (
            <div className="space-y-2 border-t border-border pt-4">
              <p className="font-medium">{tr("Sign-in with another account")}</p>
              <p className="text-muted text-[12.5px]">{tr("Link your account of the organisation's identity provider to sign in with it.")}</p>
              {links.data.links.map((l) => (
                <div key={l.provider} className="flex items-center gap-3 rounded-xl border border-border px-4 py-2.5">
                  <Link2 size={16} className="text-accent" />
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">{l.name || l.provider}</div>
                    <div className="text-muted text-[12px]">{tr("Linked {0}", dateTime(l.linked))}{l.lastUsed && <> · {tr("last used {0}", dateTime(l.lastUsed))}</>}</div>
                  </div>
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Unlink size={14} />} onClick={() => unlink(l)}>{tr("Unlink")}</Button>
                </div>
              ))}
              <div className="flex flex-wrap gap-2">
                {links.data.providers.filter((p) => !links.data!.links.some((l) => l.provider === p.id)).map((p) => (
                  <Button key={p.id} size="sm" icon={<Link2 size={14} />} onClick={() => link(p.id)}>{tr("Link {0}", p.name)}</Button>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </Modal>
  );
}
