import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bot, CircleCheck, CircleX, SendHorizonal, User } from "lucide-react";
import { api } from "../lib/api";
import { Badge, Button, ErrorBox, Input, Notice, PageHeader, Spinner } from "../components/ui";
import { tr } from "../lib/i18n";

type ChatMessage = { role: "user" | "assistant"; content: string };
type ArmedAction = { token: string; tool: string; summary: string };
type ActionState = ArmedAction & { status: "pending" | "done" | "cancelled"; result?: string };

/** Conversational assistant: it answers with the signed-in user's permissions
 * and every modifying action asks for an explicit confirmation. */
export default function Assistant() {
  const config = useQuery({ queryKey: ["assistant-config"], queryFn: () => api.get<{ enabled: boolean; model: string }>("/api/assistant/config") });
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [actions, setActions] = useState<ActionState[]>([]);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => { end.current?.scrollIntoView({ block: "end" }); }, [messages, actions, busy]);

  const send = async () => {
    const q = text.trim();
    if (!q || busy || !config.data?.enabled) return;
    setText("");
    setError(null);
    const next = [...messages, { role: "user" as const, content: q }];
    setMessages(next);
    setBusy(true);
    try {
      const r = await api.post<{ reply: string; actions?: ArmedAction[] }>("/api/assistant/chat", { messages: next });
      setMessages([...next, { role: "assistant", content: r.reply }]);
      if (r.actions?.length) setActions((a) => [...a, ...r.actions!.map((x) => ({ ...x, status: "pending" as const }))]);
    } catch (e) { setError(e); }
    setBusy(false);
  };

  const confirm = async (i: number) => {
    try {
      const r = await api.post<{ result: string }>("/api/assistant/confirm", { token: actions[i].token });
      setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "done" as const, result: r.result } : x)));
    } catch (e) { setError(e); }
  };
  const cancel = (i: number) => setActions((xs) => xs.map((x, j) => (j === i ? { ...x, status: "cancelled" as const } : x)));

  if (config.isLoading) return <Spinner />;
  return (
    <div className="mx-auto flex h-full max-w-3xl flex-col">
      <PageHeader title={tr("Assistant")} subtitle={tr("Ask anything about your NAS")} icon={<Bot size={20} />} />
      <ErrorBox error={error} />
      {!config.data?.enabled ? (
        <Notice tone="warn">{tr("The assistant is not configured: an administrator must set an API endpoint in Settings.")}</Notice>
      ) : (
        <div className="flex min-h-64 flex-1 flex-col rounded-2xl border border-line bg-panel">
          <div className="flex-1 space-y-3 overflow-y-auto p-4">
            {messages.length === 0 && !actions.length && (
              <div className="flex h-full items-center justify-center p-6 text-center text-[13px] text-muted">
                {tr("The assistant answers with your permissions; actions that modify the NAS always ask for your confirmation first.")}
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
                    <Button size="sm" variant="primary" onClick={() => confirm(i)}>{tr("Confirm")}</Button>
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
            <Button variant="primary" disabled={!text.trim()} loading={busy} onClick={send} icon={<SendHorizonal size={15} />} />
          </div>
        </div>
      )}
    </div>
  );
}
