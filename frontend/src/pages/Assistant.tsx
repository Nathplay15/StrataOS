import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Bot, CircleCheck, CircleX, History, MessageSquarePlus, SendHorizonal, Square, Trash2, User } from "lucide-react";
import { api, ApiError } from "../lib/api";
import type { LocalModel } from "../components/LocalModels";
import type { Me } from "../lib/types";
import { Badge, Button, ErrorBox, Input, Notice, PageHeader, Select, Spinner, useUI } from "../components/ui";
import { useAction } from "../lib/hooks";
import { tr } from "../lib/i18n";
import { postIntent } from "../lib/intent";

type ChatMessage = { role: "user" | "assistant"; content: string };

// A NAS path in an assistant answer (its "Sources:" list, or a file named in
// the text): the leading "/" must start the token, so addresses like
// https://… are not matched.
const nasPathRe = /(^|[\s(>])(\/[^\s"'`<>]+)/g;

/** Renders an assistant message, with the NAS paths it cites as clickable
 * references that open the file in Files. */
function AssistantContent({ text }: { text: string }) {
  const nav = useNavigate();
  const parts: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of text.matchAll(nasPathRe)) {
    const raw = m[2].replace(/[.,;:]+$/, "");
    if (raw.length < 2) continue;
    const start = (m.index ?? 0) + m[1].length;
    if (start > last) parts.push(text.slice(last, start));
    last = start + m[2].length;
    const segs = raw.slice(1).split("/");
    const name = segs[segs.length - 1];
    const isFile = /\.[A-Za-z0-9]{1,10}$/.test(name);
    const dir = isFile ? (segs.length > 1 ? "/" + segs.slice(0, -1).join("/") : "/") : raw;
    parts.push(
      <button
        key={n++}
        className="break-all text-left font-medium text-accent underline decoration-dotted underline-offset-2 hover:decoration-solid"
        title={tr("Open in Files")}
        onClick={() => {
          nav("/files");
          postIntent({ page: "files", dir, file: isFile ? { name, path: raw, dir: false, size: 0, mode: "", modTime: "", link: false } : undefined });
        }}
      >
        {raw}
      </button>
    );
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

const suggestions = ["How much free space is left?", "Are my disks healthy?", "Which containers are running?", "Any failed backups?"];
type ArmedAction = { token: string; tool: string; summary: string; resume?: boolean };
type ApprovalRequest = { id: string; tool: string; summary: string; approved: boolean; expires: string };
type ActionState = ArmedAction & { status: "pending" | "done" | "cancelled"; result?: string };
type ConvMeta = { id: string; title: string; updated: string };

/** Conversational assistant: it answers with the signed-in user's permissions
 * and every modifying action asks for an explicit confirmation. Conversations
 * are saved server-side and can be reopened or deleted. */
export default function Assistant() {
  const config = useQuery({ queryKey: ["assistant-config"], queryFn: () => api.get<{ enabled: boolean; model: string }>("/api/assistant/config") });
  const qc = useQueryClient();
  const me = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me"), staleTime: 60000 });
  const localModels = useQuery({ queryKey: ["assistant-models"], queryFn: () => api.get<{ reachable: boolean; models: LocalModel[] }>("/api/assistant/models"), enabled: me.data?.role === "admin", staleTime: 60000 });
  const setModel = useAction((model: string) => api.put("/api/assistant/model", { model }), {
    success: tr("Assistant model updated"),
    onDone: () => qc.invalidateQueries({ queryKey: ["assistant-config"] }),
  });
  const { confirm } = useUI();
  const convs = useQuery({ queryKey: ["assistant-conversations"], queryFn: () => api.get<ConvMeta[]>("/api/assistant/conversations") });
  // Write requests of AI clients (MCP) waiting for this user's approval.
  const approvals = useQuery({
    queryKey: ["assistant-approvals"],
    queryFn: () => api.get<ApprovalRequest[]>("/api/assistant/approvals"),
    refetchInterval: 5000,
  });
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [actions, setActions] = useState<ActionState[]>([]);
  const [convId, setConvId] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [tool, setTool] = useState<string | null>(null);
  const [think, setThink] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [secs, setSecs] = useState(0);
  const abortRef = useRef<AbortController | null>(null);
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => { end.current?.scrollIntoView({ block: "end" }); }, [messages, actions, busy]);
  useEffect(() => {
    if (!busy) { setSecs(0); return; }
    const t0 = Date.now();
    const iv = setInterval(() => setSecs(Math.floor((Date.now() - t0) / 1000)), 1000);
    return () => clearInterval(iv);
  }, [busy]);

  /** Streams one answer of the assistant (chat or continuation after a
   * confirmed web tool) after the messages in base. */
  const runStream = async (url: string, body: unknown, base: ChatMessage[]) => {
    setMessages([...base, { role: "assistant" as const, content: "" }]);
    setBusy(true);
    setTool(null);
    setThink(null);
    const ac = new AbortController();
    abortRef.current = ac;
    const show = (reply: string) => setMessages([...base, { role: "assistant", content: reply }]);
    try {
      const res = await fetch(url, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-Strata": "1" },
        body: JSON.stringify(body),
        signal: ac.signal,
      });
      if (!res.ok || !res.body) {
        const data = await res.json().catch(() => null);
        throw new ApiError(res.status, data && typeof data === "object" && "error" in data ? String(data.error) : res.statusText);
      }
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      let reply = "";
      const armed: ArmedAction[] = [];
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n\n")) >= 0) {
          const raw = buf.slice(0, i);
          buf = buf.slice(i + 2);
          const line = raw.split("\n").find((l) => l.startsWith("data:"));
          if (!line) continue;
          const ev = JSON.parse(line.slice(5).trim()) as { type: string; text?: string; tool?: string; token?: string; summary?: string; resume?: boolean; conversationId?: string };
          if (ev.type === "delta") { reply += ev.text ?? ""; setThink(null); show(reply); }
          else if (ev.type === "status") setTool(ev.tool ?? null);
          else if (ev.type === "reasoning") setThink((t) => (t ?? "") + (ev.text ?? ""));
          else if (ev.type === "action" && ev.token) armed.push({ token: ev.token, tool: ev.tool ?? "", summary: ev.summary ?? "", resume: ev.resume });
          else if (ev.type === "error") throw new Error(ev.text || "assistant error");
          else if (ev.type === "done") { if (ev.conversationId) setConvId(ev.conversationId); qc.invalidateQueries({ queryKey: ["assistant-conversations"] }); }
        }
      }
      if (!reply) setMessages(base);
      if (armed.length) setActions((a) => [...a, ...armed.map((x) => ({ ...x, status: "pending" as const }))]);
    } catch (e) {
      if ((e as Error)?.name !== "AbortError") setError(e);
      setMessages((ms) => (ms.length && ms[ms.length - 1].role === "assistant" && !ms[ms.length - 1].content ? base : ms));
    }
    abortRef.current = null;
    setTool(null);
    setThink(null);
    setBusy(false);
  };

  const stop = () => abortRef.current?.abort();

  const send = async (preset?: string) => {
    const q = (preset ?? text).trim();
    if (!q || busy || !config.data?.enabled) return;
    setText("");
    setError(null);
    const next = [...messages, { role: "user" as const, content: q }];
    await runStream("/api/assistant/chat/stream", { messages: next, conversationId: convId ?? undefined }, next);
  };

  const newConversation = () => { setMessages([]); setActions([]); setConvId(null); setError(null); };

  const open = async (id: string) => {
    if (busy) return;
    setError(null);
    try {
      const c = await api.get<{ id: string; messages: ChatMessage[] }>(`/api/assistant/conversations/${id}`);
      setConvId(c.id);
      setMessages(c.messages);
      setActions([]);
    } catch (e) { setError(e); }
  };

  const remove = async (id: string) => {
    if (!(await confirm({ title: tr("Delete this conversation?"), text: tr("The messages of this conversation will be removed."), danger: true }))) return;
    try {
      await api.del(`/api/assistant/conversations/${id}`);
      if (convId === id) newConversation();
      qc.invalidateQueries({ queryKey: ["assistant-conversations"] });
    } catch (e) { setError(e); }
  };

  const confirmAction = async (i: number) => {
    const a = actions[i];
    if (a.resume) {
      // A web page or search the user allowed: the assistant goes on with it.
      if (busy) return;
      setError(null);
      setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "done" as const } : x)));
      await runStream("/api/assistant/resume", { token: a.token }, messages);
      return;
    }
    try {
      const r = await api.post<{ result: string }>("/api/assistant/confirm", { token: actions[i].token });
      setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "done" as const, result: r.result } : x)));
    } catch (e) { setError(e); }
  };
  const cancel = (i: number) => setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "cancelled" as const } : x)));
  const decide = async (id: string, approve: boolean) => {
    try {
      await api.post(`/api/assistant/approvals/${encodeURIComponent(id)}`, { approve });
      qc.invalidateQueries({ queryKey: ["assistant-approvals"] });
    } catch (e) { setError(e); }
  };

  if (config.isLoading) return <Spinner />;
  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col">
      <PageHeader
        title={tr("Assistant")}
        subtitle={tr("Ask anything about your NAS")}
        icon={<Bot size={20} />}
        actions={
          <div className="flex items-center gap-2">
            {me.data?.role === "admin" && config.data?.enabled && (
              <Select
                aria-label={tr("Model")}
                title={tr("Model")}
                className="!w-auto text-[12.5px]"
                value={config.data.model}
                disabled={setModel.isPending}
                onChange={(e) => setModel.mutate(e.target.value)}
              >
                {(() => {
                  const names = [...new Set([...(localModels.data?.reachable ? localModels.data.models.map((m) => m.name) : []), config.data.model])];
                  return names.map((n) => <option key={n} value={n}>{n}</option>);
                })()}
              </Select>
            )}
            <Button size="sm" icon={<MessageSquarePlus size={14} />} disabled={!messages.length && !actions.length} onClick={newConversation}>
              {tr("New conversation")}
            </Button>
          </div>
        }
      />
      <ErrorBox error={error} />
      {approvals.data && approvals.data.length > 0 && (
        <div className="mb-3 space-y-2 rounded-2xl border border-warn/40 bg-warn/5 p-3">
          <div className="text-[13px] font-medium">{tr("Requests from AI clients (MCP)")}</div>
          <div className="text-[12px] text-muted">{tr("An application connected with one of your API tokens wants to change the NAS. Approve only what you asked it to do; it then has to call the tool again.")}</div>
          {approvals.data.map((a) => (
            <div key={a.id} className="flex flex-wrap items-center gap-2 text-[13px]">
              <Bot size={14} className="text-muted" />
              <span className="flex-1">{a.summary}</span>
              {a.approved ? (
                <Badge tone="ok"><CircleCheck size={12} /> {tr("Approved")}</Badge>
              ) : (
                <>
                  <Button size="sm" variant="primary" onClick={() => decide(a.id, true)}>{tr("Approve")}</Button>
                  <Button size="sm" variant="ghost" onClick={() => decide(a.id, false)}>{tr("Decline")}</Button>
                </>
              )}
            </div>
          ))}
        </div>
      )}
      {!config.data?.enabled ? (
        <Notice tone="warn">{tr("The assistant is not configured: an administrator must set an API endpoint in Settings.")}</Notice>
      ) : (
        <div className="flex min-h-64 flex-1 flex-col rounded-2xl border border-line bg-panel">
          {convs.data && convs.data.length > 0 && (
            <div className="flex items-center gap-2 border-b border-line px-3 py-2">
              <History size={14} className="shrink-0 text-muted" />
              <div className="flex flex-1 gap-1.5 overflow-x-auto">
                {convs.data.map((c) => (
                  <div key={c.id} className={`group flex shrink-0 items-center gap-1 rounded-full border py-1 pl-3 pr-1.5 text-[12.5px] ${c.id === convId ? "border-accent/40 bg-accent/10 text-accent" : "border-line bg-panel-2 text-muted"}`}>
                    <button className="max-w-52 truncate hover:underline" title={c.title} onClick={() => open(c.id)}>{c.title || tr("New conversation")}</button>
                    <button className="rounded-full p-0.5 opacity-0 transition-opacity hover:text-danger group-hover:opacity-100" title={tr("Delete this conversation?")} onClick={() => remove(c.id)}>
                      <Trash2 size={12} />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}
          <div className="flex-1 space-y-3 overflow-y-auto p-4">
            {messages.length === 0 && !actions.length && (
              <div className="flex h-full flex-col items-center justify-center gap-4 p-6 text-center">
                <div className="text-[13px] text-muted">{tr("The assistant answers with your permissions; actions that modify the NAS, and opening web pages, always ask for your confirmation first.")}</div>
                <div className="flex flex-wrap justify-center gap-2">
                  {suggestions.map((s) => (
                    <Button key={s} size="sm" variant="secondary" disabled={busy} onClick={() => send(s)}>{tr(s)}</Button>
                  ))}
                </div>
              </div>
            )}
            {messages.map((m, i) => (
              <div key={i} className={`flex items-start gap-2.5 ${m.role === "user" ? "flex-row-reverse" : ""}`}>
                <div className={`mt-0.5 rounded-full p-1.5 ${m.role === "user" ? "bg-accent/15 text-accent" : "bg-panel-2 text-muted"}`}>
                  {m.role === "user" ? <User size={14} /> : <Bot size={14} />}
                </div>
                <div className={`max-w-[85%] whitespace-pre-wrap rounded-2xl px-3.5 py-2 text-[13.5px] ${m.role === "user" ? "rounded-tr-sm bg-accent/15" : "rounded-tl-sm bg-panel-2"}`}>
                  {m.role === "assistant" ? <AssistantContent text={m.content} /> : m.content}
                </div>
              </div>
            ))}
            {busy && think && (
              <div className="ml-9 max-h-32 max-w-[85%] overflow-y-auto whitespace-pre-wrap rounded-2xl rounded-tl-sm bg-panel-2 px-3.5 py-2 text-[12px] text-muted">{think}</div>
            )}
            {busy && !think && <div className="text-[12.5px] text-muted">{tool ? tr("Running {0}…", tool) : tr("Thinking…")}{secs >= 5 ? ` ${secs}s` : ""}</div>}
            {actions.map((a, i) => (
              <div key={a.token} className="ml-7 rounded-xl border border-line bg-panel-2 p-3">
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <Bot size={14} className="text-muted" />
                  <span className="flex-1">{a.summary}</span>
                  {a.status === "pending" && <>
                    <Button size="sm" variant="primary" disabled={busy && a.resume} onClick={() => confirmAction(i)}>{tr("Confirm")}</Button>
                    <Button size="sm" variant="ghost" onClick={() => cancel(i)}>{tr("Cancel")}</Button>
                  </>}
                  {a.status === "done" && <Badge tone="ok"><CircleCheck size={12} /> {tr("Done")}</Badge>}
                  {a.status === "cancelled" && <Badge tone="neutral"><CircleX size={12} /> {tr("Declined")}</Badge>}
                </div>
                {a.result && <pre className="mono mt-2 max-h-40 overflow-auto whitespace-pre-wrap rounded-lg bg-panel p-2 text-[11.5px]">{a.result}</pre>}
              </div>
            ))}
            <div ref={end} />
          </div>
          <div className="flex items-center gap-2 border-t border-line p-3">
            <Input
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); } }}
              placeholder={tr("Send a message")}
              disabled={busy}
            />
            {busy && <Button variant="ghost" title={tr("Stop")} onClick={stop} icon={<Square size={14} />} />}
            <Button variant="primary" disabled={!text.trim()} loading={busy} onClick={() => send()} icon={<SendHorizonal size={15} />} />
          </div>
        </div>
      )}
    </div>
  );
}
