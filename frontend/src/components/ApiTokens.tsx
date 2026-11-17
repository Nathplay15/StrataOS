import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, KeyRound, Plus, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { dateTime } from "../lib/format";
import { tr } from "../lib/i18n";
import { Badge, Button, ErrorBox, Field, Input, Modal, Notice, Spinner, useUI } from "./ui";

interface TokenView {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  createdAt: string;
  expiresAt?: string;
  lastUsed?: string;
  lastIp?: string;
}

const mcpPreset = (url: string, tok: string) => JSON.stringify({ mcpServers: { strataos: { url, headers: { Authorization: `Bearer ${tok}` } } } }, null, 2);

const SCOPES: { id: string; label: string }[] = [
  { id: "read", label: tr("Read (GET requests)") },
  { id: "write", label: tr("Write (changes on the NAS)") },
  { id: "admin", label: tr("Administrator (everything but the account's sign-in settings)") },
  { id: "mcp", label: tr("MCP (AI assistants)") },
];

function Copyable({ value }: { value: string }) {
  const { toast } = useUI();
  return (
    <div className="flex items-center gap-2">
      <code className="mono min-w-0 flex-1 truncate rounded-lg bg-panel-2 px-2.5 py-1.5 text-[12.5px]">{value}</code>
      <Button size="sm" variant="ghost" title={tr("Copy")} icon={<Copy size={14} />} onClick={() => { navigator.clipboard?.writeText(value); toast(tr("Copied")); }} />
    </div>
  );
}

/** The user's scoped API tokens for scripts, automations and AI assistants. */
export function ApiTokensModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const { confirm, toast } = useUI();
  const { data, error } = useQuery({ queryKey: ["my-tokens"], queryFn: () => api.get<TokenView[]>("/api/me/tokens"), enabled: open });
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["read"]);
  const [days, setDays] = useState(0);
  const [created, setCreated] = useState<string | null>(null);
  const [createdMcp, setCreatedMcp] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (!open) { setCreated(null); setCreatedMcp(false); setName(""); setScopes(["read"]); setDays(0); setErr(null); } }, [open]);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try { await fn(); } catch (e) { setErr(e); } finally { setBusy(false); qc.invalidateQueries({ queryKey: ["my-tokens"] }); }
  };
  const create = () => run(async () => {
    const out = await api.post<{ token: string }>("/api/me/tokens", { name, scopes, days });
    setCreated(out.token);
    setCreatedMcp(scopes.includes("mcp"));
    setName("");
  });
  const toggle = (id: string) => setScopes((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]));
  const revoke = async (t: TokenView) => {
    if (!(await confirm({ title: tr("Revoke the token {0}?", t.name), text: tr("Applications using it lose access at once."), danger: true }))) return;
    run(async () => { await api.del(`/api/me/tokens/${t.id}`); toast(tr("Token revoked")); });
  };
  return (
    <Modal open={open} onClose={onClose} title={tr("API tokens")} wide>
      {!data ? <><ErrorBox error={error} /><Spinner /></> : (
        <div className="space-y-4">
          <p className="text-[13.5px]">{tr("API tokens let a script, an automation or an AI assistant call this NAS without your password. Give each client its own token with the smallest scopes it needs.")}</p>
          {created && (
            <Notice>
              <div className="space-y-2">
                <p>{tr("Copy the token now: it is not shown again.")}</p>
                <Copyable value={created} />
                {createdMcp && (
                  <div className="space-y-2">
                    <p>{tr("MCP configuration with this token:")}</p>
                    <Copyable value={mcpPreset(`${location.origin}/api/mcp`, created)} />
                  </div>
                )}
              </div>
            </Notice>
          )}
          <div className="space-y-2">
            {data.length === 0 && <p className="text-[13px] opacity-70">{tr("No token yet.")}</p>}
            {data.map((t) => (
              <div key={t.id} className="flex items-center gap-3 rounded-xl border border-line bg-panel-2/40 px-3 py-2.5">
                <KeyRound size={16} className="shrink-0 opacity-60" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-[13.5px] font-medium">{t.name}</span>
                    <code className="mono rounded bg-panel-2 px-1.5 py-0.5 text-[11.5px] opacity-70">{t.prefix}…</code>
                    {t.scopes.map((s) => <Badge key={s}>{s}</Badge>)}
                  </div>
                  <div className="mt-0.5 text-[12px] opacity-60">
                    {tr("Created {0}", dateTime(t.createdAt))}
                    {t.lastUsed ? ` · ${tr("Used {0}", dateTime(t.lastUsed))}` : ` · ${tr("Never used")}`}
                    {t.expiresAt ? ` · ${tr("Expires {0}", dateTime(t.expiresAt))}` : ""}
                  </div>
                </div>
                <Button size="sm" variant="ghost" title={tr("Revoke")} icon={<Trash2 size={14} />} onClick={() => revoke(t)} />
              </div>
            ))}
          </div>
          <div className="space-y-3 rounded-xl border border-line p-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={tr("Name")}>
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={tr("Backup script of the living room")} maxLength={100} />
              </Field>
              <Field label={tr("Expiry")}>
                <select className="w-full rounded-lg border border-line bg-panel px-3 py-2 text-[13.5px]" value={days} onChange={(e) => setDays(Number(e.target.value))}>
                  <option value={0}>{tr("Never expires")}</option>
                  <option value={30}>{tr("In 30 days")}</option>
                  <option value={90}>{tr("In 90 days")}</option>
                  <option value={365}>{tr("In one year")}</option>
                </select>
              </Field>
            </div>
            <div className="flex flex-wrap gap-2">
              {SCOPES.map((s) => (
                <button key={s.id} type="button" onClick={() => toggle(s.id)}
                  className={`rounded-full border px-3 py-1.5 text-[12.5px] ${scopes.includes(s.id) ? "border-accent bg-accent/10 text-accent" : "border-line opacity-70 hover:opacity-100"}`}>
                  {s.label}
                </button>
              ))}
            </div>
            {scopes.includes("mcp") && (
              <div className="space-y-2 rounded-xl border border-line p-3 text-[13px]">
                <p className="font-medium">{tr("Connect an AI assistant (MCP)")}</p>
                <p className="opacity-70">{tr("Point your assistant to this endpoint:")}</p>
                <Copyable value={`${location.origin}/api/mcp`} />
                <p className="opacity-70">{tr("In your MCP client (Claude Desktop, Cursor…), add this server:")}</p>
                <pre className="mono overflow-x-auto rounded-lg bg-panel-2 p-2.5 text-[12px]">{mcpPreset(`${location.origin}/api/mcp`, "YOUR_TOKEN")}</pre>
                <p className="opacity-70">{tr("Write tools need your approval before each change: approve the requests on the Assistant page. Give the write scope only to assistants you trust.")}</p>
              </div>
            )}
            <ErrorBox error={err} />
            <Button disabled={busy || !name.trim() || scopes.length === 0} icon={<Plus size={15} />} onClick={create}>{tr("Create the token")}</Button>
          </div>
        </div>
      )}
    </Modal>
  );
}
