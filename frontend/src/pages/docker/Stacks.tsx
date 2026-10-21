import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AppWindow, ArrowDownToLine, Edit3, FileText, Package, Play, Plus, RotateCw, Square, Trash2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Container, Stack } from "../../lib/types";
import { ago } from "../../lib/format";
import { Badge, Button, Card, Empty, ErrorBox, Field, Health, Input, Modal, PageHeader, Spinner, Tabs, useUI } from "../../components/ui";
import { DockerGuard } from "./Containers";
import { CustomMetaModal } from "./AppStore";
import { tr } from "../../lib/i18n";

const sample = `services:
  web:
    image: nginx:alpine
    restart: unless-stopped
    ports:
      - "8081:80"
    volumes:
      - /srv/strata/web:/usr/share/nginx/html:ro
`;

function Editor({ name, onClose }: { name: string | null; onClose: () => void }) {
  const editing = !!name;
  const detail = useQuery({ queryKey: ["stack", name], queryFn: () => api.get<{ compose: string; env: string; containers: Container[] }>(`/api/docker/stacks/${name}`), enabled: editing });
  const [n, setN] = useState("");
  const [compose, setCompose] = useState<string | null>(null);
  const [env, setEnv] = useState<string | null>(null);
  const [tab, setTab] = useState<"compose" | "env">("compose");
  const yml = compose ?? (editing ? detail.data?.compose ?? "" : sample);
  const envv = env ?? (editing ? detail.data?.env ?? "" : "");
  const save = useAction((deploy: boolean) => (editing ? api.put(`/api/docker/stacks/${name}`, { compose: yml, env: envv, deploy }) : api.post("/api/docker/stacks", { name: n, compose: yml, env: envv, deploy })), { success: tr("Stack saved"), onDone: onClose });
  return (
    <Modal open onClose={onClose} title={editing ? tr("Stack {0}", name) : tr("New Compose stack")} wide="xl" footer={<>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button loading={save.isPending} disabled={!editing && !n} onClick={() => save.mutate(false)}>{tr("Save")}</Button>
      <Button variant="primary" loading={save.isPending} disabled={!editing && !n} onClick={() => save.mutate(true)}>{tr("Save and deploy")}</Button>
    </>}>
      {editing && detail.isLoading ? <Spinner /> : (
        <div className="space-y-4">
          {!editing && <Field label={tr("Stack name")} hint={tr("lowercase letters, digits, - and _")}><Input value={n} onChange={(e) => setN(e.target.value.toLowerCase())} /></Field>}
          <Tabs value={tab} onChange={setTab} tabs={[{ id: "compose", label: "compose.yaml" }, { id: "env", label: ".env" }]} />
          <textarea spellCheck={false} className="input mono h-[50vh] resize-y leading-relaxed" value={tab === "compose" ? yml : envv} onChange={(e) => (tab === "compose" ? setCompose(e.target.value) : setEnv(e.target.value))} />
          <ErrorBox error={save.error} />
        </div>
      )}
    </Modal>
  );
}

function LogsView({ name, onClose }: { name: string; onClose: () => void }) {
  const { data, isLoading, refetch } = useQuery({ queryKey: ["stack-logs", name], queryFn: () => api.get<{ logs: string }>(`/api/docker/stacks/${name}/logs?tail=400`) });
  return (
    <Modal open onClose={onClose} title={tr("Logs — {0}", name)} wide="xl" footer={<Button onClick={() => refetch()}>{tr("Refresh")}</Button>}>
      {isLoading ? <Spinner /> : <pre className="mono h-[60vh] overflow-auto whitespace-pre-wrap rounded-xl bg-[#0b0f1c] p-3 text-[12px] text-[#cbd3e6]">{data?.logs || tr("(no log)")}</pre>}
    </Modal>
  );
}

export default function Stacks() {
  const { confirm } = useUI();
  const [edit, setEdit] = useState<string | null | undefined>(undefined);
  const [logs, setLogs] = useState<string | null>(null);
  const [meta, setMeta] = useState<Stack | null>(null);
  const { data, isLoading, error } = useQuery({ queryKey: ["stacks"], queryFn: () => api.get<Stack[]>("/api/docker/stacks"), refetchInterval: 8000 });
  const act = useAction((p: { name: string; action: string }) => api.post(`/api/docker/stacks/${p.name}/${p.action}`));
  const del = useAction((p: { name: string; volumes: boolean }) => api.del(`/api/docker/stacks/${p.name}${p.volumes ? "?volumes=1" : ""}`), { success: tr("Stack deleted") });

  return (
    <div>
      <PageHeader title={tr("Compose stacks")} subtitle={tr("Multi-container applications described by a docker-compose file")} icon={<Package size={20} />} actions={<Button variant="primary" icon={<Plus size={15} />} onClick={() => setEdit(null)}>{tr("New stack")}</Button>} />
      <DockerGuard>
        <ErrorBox error={error} />
        {isLoading ? <Spinner /> : !data?.length ? (
          <Card><Empty icon={<Package />} title={tr("No stacks")} text={tr("Paste a docker-compose.yaml file to deploy a complete application, or use the App Center.")} action={<Button variant="primary" onClick={() => setEdit(null)}>{tr("Create a stack")}</Button>} /></Card>
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {data.map((s) => (
              <Card key={s.name} pad={false}>
                <div className="p-5">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2.5 font-semibold"><Package size={17} className="text-accent" />{s.name}</div>
                    <Health value={s.status} />
                  </div>
                  <div className="mt-2 flex gap-2 text-[12.5px] text-muted">
                    <span>{s.running}/{s.services} {" "}{tr("active container(s)")}</span>
                    <span>{tr("· modified")}{" "}{ago(s.modifiedAt)}</span>
                  </div>
                  {s.app && <div className="mt-2"><Badge tone="accent">{tr("App Center:")}{" "}{s.app}</Badge></div>}
                  {s.custom && <div className="mt-2"><Badge tone="accent">{tr("My apps:")}{" "}{s.custom.name}</Badge></div>}
                </div>
                <div className="flex flex-wrap justify-end gap-1 border-t border-line bg-panel-2/50 px-3 py-2">
                  {s.status === "stopped" ? (
                    <Button size="sm" variant="ghost" icon={<Play size={14} />} onClick={() => act.mutate({ name: s.name, action: "up" })}>{tr("Start")}</Button>
                  ) : (
                    <>
                      <Button size="sm" variant="ghost" icon={<Square size={14} />} onClick={() => act.mutate({ name: s.name, action: "stop" })}>{tr("Stop")}</Button>
                      <Button size="sm" variant="ghost" icon={<RotateCw size={14} />} onClick={() => act.mutate({ name: s.name, action: "restart" })} />
                    </>
                  )}
                  <Button size="sm" variant="ghost" icon={<ArrowDownToLine size={14} />} title={tr("Update images")} onClick={() => act.mutate({ name: s.name, action: "update" })} />
                  <Button size="sm" variant="ghost" icon={<FileText size={14} />} onClick={() => setLogs(s.name)} />
                  <Button size="sm" variant="ghost" icon={<Edit3 size={14} />} onClick={() => setEdit(s.name)} />
                  {!s.app && <Button size="sm" variant="ghost" icon={<AppWindow size={14} />} title={s.custom ? tr("App Center details") : tr("Add to My apps")} onClick={() => setMeta(s)} />}
                  <Button size="sm" variant="ghost" className="text-danger" icon={<Trash2 size={14} />} onClick={async () => {
                    if (await confirm({ title: tr("Delete stack {0}?", s.name), text: tr("Containers and networks will be deleted. Named Docker volumes too; folders mounted from /srv/strata are kept."), danger: true, typeToConfirm: s.name })) del.mutate({ name: s.name, volumes: true });
                  }} />
                </div>
              </Card>
            ))}
          </div>
        )}
      </DockerGuard>
      {edit !== undefined && <Editor name={edit} onClose={() => setEdit(undefined)} />}
      {logs && <LogsView name={logs} onClose={() => setLogs(null)} />}
      {meta && <CustomMetaModal stack={meta.name} initial={meta.custom} onClose={() => setMeta(null)} />}
    </div>
  );
}
