import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bot, Download, RefreshCw, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { bytes } from "../lib/format";
import { Badge, Button, Field, IconButton, Input, useUI } from "./ui";
import { tr } from "../lib/i18n";

export type LocalModel = { name: string; size: number; family?: string; parameterSize?: string; quantization?: string; modified?: string };

/** Models served on the NAS by the Ollama app: download, import, remove, use. */
export function LocalModels({ current, onUse }: { current?: string; onUse: (name: string) => void }) {
  const { confirm } = useUI();
  const models = useQuery({ queryKey: ["assistant-models"], queryFn: () => api.get<{ reachable: boolean; models: LocalModel[] }>("/api/assistant/models") });
  const [pullName, setPullName] = useState("");
  const [importPath, setImportPath] = useState("");
  const pull = useAction(() => api.post("/api/assistant/models/pull", { name: pullName }), { onDone: () => setPullName("") });
  const imp = useAction(() => api.post("/api/assistant/models/import", { path: importPath }), { onDone: () => setImportPath("") });
  const del = useAction((name: string) => api.post("/api/assistant/models/delete", { name }));
  return (
    <div className="mt-4 rounded-xl border border-line p-4">
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-2 font-medium text-[13px]"><Bot size={15} />{tr("Local models (Ollama app)")}</div>
        <Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} onClick={() => models.refetch()} title={tr("Refresh")} />
      </div>
      {!models.data?.reachable ? (
        <p className="text-[12.5px] text-muted">{tr("Ollama is not reachable. Install the Ollama app from the App Center (default port 11434) to download and run models on the NAS.")}</p>
      ) : (
        <>
          <div className="space-y-1.5">
            {models.data.models.map((m) => (
              <div key={m.name} className="flex flex-wrap items-center gap-2 rounded-lg bg-panel-2 px-3 py-2 text-[13px]">
                <span className="mono break-all">{m.name}</span>
                <span className="text-[12px] text-muted">{[m.parameterSize, m.quantization, bytes(m.size)].filter(Boolean).join(" · ")}</span>
                <div className="flex-1" />
                {current === m.name ? <Badge tone="ok" dot>{tr("Used by the assistant")}</Badge> : <Button size="sm" variant="ghost" onClick={() => onUse(m.name)}>{tr("Use")}</Button>}
                <IconButton title={tr("Delete")} onClick={async () => (await confirm({ title: tr("Delete {0}?", m.name), text: tr("The model is removed from the NAS. It can be downloaded again."), danger: true })) && del.mutate(m.name)}><Trash2 size={15} /></IconButton>
              </div>
            ))}
            {models.data.models.length === 0 && <p className="text-[12.5px] text-muted">{tr("No model yet: download one from the library or import a .gguf file.")}</p>}
          </div>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <Field label={tr("Download a model")} hint={tr("e.g. mistral:7b, llama3.2:3b, qwen2.5-coder:7b")}>
              <Input value={pullName} onChange={(e) => setPullName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && pullName && pull.mutate()} />
            </Field>
            <div className="flex items-end gap-2">
              <Field label={tr("Import a .gguf file")} hint={tr("Path of a GGUF file in a volume")} className="flex-1">
                <Input className="mono" value={importPath} onChange={(e) => setImportPath(e.target.value)} onKeyDown={(e) => e.key === "Enter" && importPath && imp.mutate()} />
              </Field>
              <Button icon={<Download size={14} />} loading={pull.isPending} disabled={!pullName} onClick={() => pull.mutate()}>{tr("Download")}</Button>
              <Button loading={imp.isPending} disabled={!importPath} onClick={() => imp.mutate()}>{tr("Import")}</Button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
