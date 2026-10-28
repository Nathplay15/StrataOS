import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { Job } from "../lib/types";
import { srv, tr } from "../lib/i18n";
import { Badge, Button, Checkbox, ErrorBox, Modal, Notice, Spinner } from "./ui";

/** Components that can be uninstalled (components.All in the backend). */
export const uninstallable = ["zfs", "docker", "nut", "vms", "lxc", "rclone", "ftp", "directory", "antivirus", "ha", "vpn", "photos", "tailscale", "tunnel", "iscsi", "dlna", "nvr", "docpreview"];

type Blocker = { message: string; force: boolean };
type ComponentPlan = {
  id: string; name: string; installed: boolean; blockers: Blocker[]; blocked: boolean;
  packages: string[]; orphans: string[]; kept: { package: string; reason: string }[];
  services: string[]; removed: string[]; data: string[]; effects: string[];
};
type AppPlan = {
  stack: string; app?: string; name: string; containers: number;
  volumes: string[]; folders: string[]; kept: { path: string; reason: string }[]; images: string[]; integrations: string[];
};

/** Log of the uninstall job, followed until it ends. */
function JobLog({ id }: { id: string }) {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: ["job", id], queryFn: () => api.get<Job>(`/api/jobs/${id}`), refetchInterval: (q) => (q.state.data?.status === "running" ? 1500 : false) });
  const ended = useRef(false);
  useEffect(() => {
    if (data && data.status !== "running" && !ended.current) {
      ended.current = true;
      qc.invalidateQueries();
    }
  }, [data, qc]);
  if (!data) return <Spinner />;
  return (
    <div className="space-y-2">
      <div className="text-[13px]">
        {data.status === "running" ? <Spinner label={tr("Uninstalling…")} /> : data.status === "success" ? <Badge tone="ok">{tr("Uninstalled")}</Badge> : <Badge tone="danger">{srv(data.error) || tr("failed")}</Badge>}
      </div>
      <pre className="mono max-h-80 overflow-auto whitespace-pre-wrap rounded-lg bg-panel-2 p-3 text-[11.5px]">{data.log ?? ""}</pre>
    </div>
  );
}

function Section({ title, items, mono = true }: { title: string; items: ReactNode[]; mono?: boolean }) {
  if (items.length === 0) return null;
  return (
    <div>
      <div className="mb-1 font-medium text-[13px]">{title}</div>
      <ul className={"max-h-40 list-disc space-y-0.5 overflow-auto pl-5 text-[12.5px] text-muted" + (mono ? " mono" : "")}>{items.map((x, i) => <li key={i}>{x}</li>)}</ul>
    </div>
  );
}

/** Confirms and runs the uninstall of an optional component (Settings → Components). */
export function ComponentUninstallModal({ id, label, onClose }: { id: string; label: string; onClose: () => void }) {
  const plan = useQuery({ queryKey: ["uninstall-plan", id], queryFn: () => api.get<ComponentPlan>(`/api/system/components/${id}/uninstall`), gcTime: 0 });
  const [force, setForce] = useState(false);
  const [deleteData, setDeleteData] = useState(false);
  const [job, setJob] = useState("");
  const run = useAction(() => api.post<Job>(`/api/system/components/${id}/uninstall`, { force, deleteData }), { onDone: (j) => setJob(j.id) });
  const p = plan.data;
  const blocked = !!p && (p.blocked || (p.blockers.length > 0 && !force));
  return (
    <Modal open onClose={onClose} wide title={tr("Uninstall {0}", p ? srv(p.name) : label)} footer={job ? <Button onClick={onClose}>{tr("Close")}</Button> : <>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="danger" disabled={!p || blocked} loading={run.isPending} onClick={() => run.mutate()}>{deleteData ? tr("Uninstall and delete the data") : tr("Uninstall")}</Button>
    </>}>
      {job ? <JobLog id={job} /> : plan.isLoading ? <Spinner label={tr("Checking what would be removed…")} /> : !p ? <ErrorBox error={plan.error} /> : (
        <div className="space-y-4">
          {p.blockers.length > 0 && (
            <Notice tone="warn">
              <div className="font-medium">{p.blocked ? tr("This component cannot be uninstalled yet:") : tr("This component is in use:")}</div>
              <ul className="mt-1 list-disc pl-5">{p.blockers.map((b) => <li key={b.message}>{srv(b.message)}</li>)}</ul>
              {!p.blocked && <div className="mt-2"><Checkbox checked={force} onChange={setForce} label={tr("Turn these off and uninstall anyway")} /></div>}
            </Notice>
          )}
          <Section title={tr("Packages removed")} items={p.packages} />
          <Section title={tr("Packages they pulled in, removed too")} items={p.orphans} />
          <Section title={tr("Packages kept")} items={p.kept.map((k) => <><span className="mono">{k.package}</span> — {srv(k.reason)}</>)} mono={false} />
          {p.packages.length === 0 && <p className="text-[12.5px] text-muted">{tr("No package is removed: StrataOS or another component still uses them.")}</p>}
          <Section title={tr("Services stopped")} items={p.services} />
          <Section title={tr("Configuration and caches removed")} items={p.removed} />
          <Section title={tr("Settings changed")} items={p.effects.map(srv)} mono={false} />
          <div className="rounded-xl border border-line p-3">
            <Checkbox checked={deleteData} disabled={p.data.length === 0} onChange={setDeleteData} label={tr("Also delete its data")} />
            {p.data.length === 0 ? <p className="mt-1 text-[12.5px] text-muted">{tr("This component keeps no data.")}</p> : (
              <>
                <p className="mt-1 text-[12.5px] text-muted">{deleteData ? tr("These will be deleted permanently:") : tr("Kept (a later reinstall finds them again):")}</p>
                <ul className={"mono mt-1 list-disc pl-5 text-[12px]" + (deleteData ? " text-danger" : " text-muted")}>{p.data.map((d) => <li key={d}>{srv(d)}</li>)}</ul>
              </>
            )}
          </div>
          <ErrorBox error={run.error} />
        </div>
      )}
    </Modal>
  );
}

/** Confirms and runs the uninstall of an App Center app (catalog or custom). */
export function AppUninstallModal({ stack, name, onClose }: { stack: string; name: string; onClose: () => void }) {
  const plan = useQuery({ queryKey: ["app-uninstall-plan", stack], queryFn: () => api.get<AppPlan>(`/api/apps/${stack}/uninstall`), gcTime: 0 });
  const [deleteData, setDeleteData] = useState(false);
  const [removeImages, setRemoveImages] = useState(true);
  const [job, setJob] = useState("");
  const run = useAction(() => api.post<Job>(`/api/apps/${stack}/uninstall`, { deleteData, removeImages }), { onDone: (j) => setJob(j.id) });
  const p = plan.data;
  const data = [...(p?.folders ?? []), ...(p?.volumes ?? []).map((v) => tr("Docker volume {0}", v))];
  return (
    <Modal open onClose={onClose} wide title={tr("Uninstall {0}", name)} footer={job ? <Button onClick={onClose}>{tr("Close")}</Button> : <>
      <Button onClick={onClose}>{tr("Cancel")}</Button>
      <Button variant="danger" disabled={!p} loading={run.isPending} onClick={() => run.mutate()}>{deleteData ? tr("Uninstall and delete the data") : tr("Uninstall")}</Button>
    </>}>
      {job ? <JobLog id={job} /> : plan.isLoading ? <Spinner /> : !p ? <ErrorBox error={plan.error} /> : (
        <div className="space-y-4">
          <p className="text-[13px]">{tr("The containers and networks of stack {0} are stopped and removed.", p.stack)}</p>
          <Section title={tr("Settings changed")} items={p.integrations.map(srv)} mono={false} />
          <div className="rounded-xl border border-line p-3">
            <Checkbox checked={removeImages} onChange={setRemoveImages} label={tr("Remove its images (unless another container uses them)")} />
            {p.images.length > 0 && <ul className="mono mt-1 list-disc pl-5 text-[12px] text-muted">{p.images.map((i) => <li key={i}>{i}</li>)}</ul>}
          </div>
          <div className="rounded-xl border border-line p-3">
            <Checkbox checked={deleteData} disabled={data.length === 0} onChange={setDeleteData} label={tr("Also delete its data")} />
            {data.length === 0 ? <p className="mt-1 text-[12.5px] text-muted">{tr("No app folder to delete.")}</p> : (
              <>
                <p className="mt-1 text-[12.5px] text-muted">{deleteData ? tr("These will be deleted permanently:") : tr("Kept (a later reinstall finds them again):")}</p>
                <ul className={"mono mt-1 list-disc pl-5 text-[12px]" + (deleteData ? " text-danger" : " text-muted")}>{data.map((d) => <li key={d}>{d}</li>)}</ul>
              </>
            )}
            {p.kept.length > 0 && (
              <>
                <p className="mt-2 text-[12.5px] text-muted">{tr("Always kept:")}</p>
                <ul className="mt-1 list-disc pl-5 text-[12px] text-muted">{p.kept.map((k) => <li key={k.path}><span className="mono">{k.path}</span> — {srv(k.reason)}</li>)}</ul>
              </>
            )}
          </div>
          <ErrorBox error={run.error} />
        </div>
      )}
    </Modal>
  );
}
