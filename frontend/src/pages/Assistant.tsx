import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, CircleCheck, CircleX, History, MessageSquarePlus, SendHorizonal, Trash2, User } from "lucide-react";
import { api } from "../lib/api";
import { Badge, Button, ErrorBox, Input, Notice, PageHeader, Spinner, useUI } from "../components/ui";
import { tr } from "../lib/i18n";

type ChatMessage = { role: "user" | "assistant"; content: string };

const suggestions = ["How much free space is left?", "Are my disks healthy?", "Which containers are running?", "Any failed backups?"];
type ArmedAction = { token: string; tool: string; summary: string };
type ActionState = ArmedAction & { status: "pending" | "done" | "cancelled"; result?: string };
type ConvMeta = { id: string; title: string; updated: string };

/** Conversational assistant: it answers with the signed-in user's permissions
 * and every modifying action asks for an explicit confirmation. Conversations
 * are saved server-side and can be reopened or deleted. */
export default function Assistant() {
  const config = useQuery({ queryKey: ["assistant-config"], queryFn: () => api.get<{ enabled: boolean; model: string }>("/api/assistant/config") });
  const qc = useQueryClient();
  const { confirm } = useUI();
  const convs = useQuery({ queryKey: ["assistant-conversations"], queryFn: () => api.get<ConvMeta[]>("/api/assistant/conversations") });
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [actions, setActions] = useState<ActionState[]>([]);
  const [convId, setConvId] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => { end.current?.scrollIntoView({ block: "end" }); }, [messages, actions, busy]);

  const send = async (preset?: string) => {
    const q = (preset ?? text).trim();
    if (!q || busy || !config.data?.enabled) return;
    setText("");
    setError(null);
    const next = [...messages, { role: "user" as const, content: q }];
    setMessages(next);
    setBusy(true);
    try {
      const r = await api.post<{ reply: string; conversationId?: string; actions?: ArmedAction[] }>("/api/assistant/chat", { messages: next, conversationId: convId ?? undefined });
      setConvId(r.conversationId || null);
      setMessages([...next, { role: "assistant", content: r.reply }]);
      if (r.actions?.length) setActions((a) => [...a, ...r.actions!.map((x) => ({ ...x, status: "pending" as const }))]);
      qc.invalidateQueries({ queryKey: ["assistant-conversations"] });
    } catch (e) { setError(e); }
    setBusy(false);
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
    try {
      const r = await api.post<{ result: string }>("/api/assistant/confirm", { token: actions[i].token });
      setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "done" as const, result: r.result } : x)));
    } catch (e) { setError(e); }
  };
  const cancel = (i: number) => setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "cancelled" as const } : x)));

  if (config.isLoading) return <Spinner />;
  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col">
      <PageHeader
        title={tr("Assistant")}
        subtitle={tr("Ask anything about your NAS")}
        icon={<Bot size={20} />}
        actions={
          <Button size="sm" icon={<MessageSquarePlus size={14} />} disabled={!messages.length && !actions.length} onClick={newConversation}>
            {tr("New conversation")}
          </Button>
        }
      />
      <ErrorBox error={error} />
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
                <div className="text-[13px] text-muted">{tr("The assistant answers with your permissions; actions that modify the NAS always ask for your confirmation first.")}</div>
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
                  {m.content}
                </div>
              </div>
            ))}
            {busy && <div className="text-[12.5px] text-muted">{tr("Thinking…")}</div>}
            {actions.map((a, i) => (
              <div key={a.token} className="ml-7 rounded-xl border border-line bg-panel-2 p-3">
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <Bot size={14} className="text-muted" />
                  <span className="flex-1">{a.summary}</span>
                  {a.status === "pending" && <>
                    <Button size="sm" variant="primary" onClick={() => confirmAction(i)}>{tr("Confirm")}</Button>
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
            <Button variant="primary" disabled={!text.trim()} loading={busy} onClick={() => send()} icon={<SendHorizonal size={15} />} />
          </div>
        </div>
      )}
    </div>
  );
}
