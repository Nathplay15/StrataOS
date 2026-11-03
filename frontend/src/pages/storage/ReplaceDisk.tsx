import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowRight, CheckCircle2, HardDrive, Lightbulb, LightbulbOff, Replace, XCircle } from "lucide-react";
import { api, q } from "../../lib/api";
import { useJobs } from "../../lib/jobs";
import type { Disk, Job } from "../../lib/types";
import { bytes, cx, dateTime, duration } from "../../lib/format";
import { Badge, Button, Checkbox, ErrorBox, Field, Input, Modal, Notice, Progress, Spinner, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";
import { AppLink } from "../../components/AppLink";
import { postIntent } from "../../lib/intent";

// Guided disk replacement: the plan comes from GET /api/storage/replace/plan
// (the server refuses the dangerous cases), the job from POST /api/storage/replace.
// A running replacement is read back from GET /api/storage/replace, so the
// wizard finds it again after a page reload.

export type ReplaceSel = { disk?: string; group?: string; member?: string };

type Member = { name: string; disk?: string; serial?: string; model?: string; size?: number; used?: number; state: string; detail?: string; was?: string; failing?: boolean; weak?: string[] };
type Group = { id: string; kind: "raid" | "zfs" | "lvm"; name: string; device?: string; level: string; vdev?: string; tolerates: number; members: Member[]; usedBy?: string[]; rebuilding?: boolean; progress?: number };
type Candidate = { path: string; name: string; model?: string; serial?: string; size: number; ok: boolean; reason?: string; erase?: string; weak?: string[] };
type Plan = {
  group?: Group; old?: Member; mode?: string; modes: { id: string; ok: boolean; reason?: string }[]; need?: number; left: number;
  candidates: Candidate[]; refusal?: string; warnings?: string[]; backup: "none" | "old" | "recent"; lastBackup?: string; steps?: string[];
};
export type ReplaceItem = {
  id: string; jobId?: string; kind: string; group: string; name: string; level?: string; mode: string; old?: string; oldDisk?: string; oldSerial?: string; oldModel?: string; oldById?: string;
  new: string; newSerial?: string; newModel?: string; phase: "check" | "add" | "rebuild" | "remove" | "done" | "failed"; error?: string; warning?: string;
  started: string; finished?: string; running: boolean; progress: number; eta?: number; waiting?: boolean;
};

export const useReplacements = () => useQuery({
  queryKey: ["replace"], queryFn: () => api.get<{ ledctl: boolean; items: ReplaceItem[] }>("/api/storage/replace"),
  refetchInterval: (qq) => (qq.state.data?.items.some((i) => i.running) ? 3000 : 30000),
});

/** Opens the wizard from another page (dashboard alert, security advisor). */
export function ReplaceLink({ sel, children }: { sel: ReplaceSel; children: React.ReactNode }) {
  return <AppLink to="/storage/disks"><span onClick={() => postIntent({ page: "disks", replace: sel })}>{children}</span></AppLink>;
}

const levelName = (g: Group) => (g.kind === "raid" ? g.level.toUpperCase().replace("RAID", "RAID ") : g.kind === "zfs" ? `ZFS ${g.level === "stripe" ? tr("single disk") : g.level}` : "LVM");

const stateBadge = (m: Member) => {
  const t = { active: ["ok", tr("Active")], spare: ["accent", tr("Spare")], faulty: ["danger", tr("Failed")], missing: ["danger", tr("Missing")], rebuilding: ["warn", tr("Rebuilding")] }[m.state] as ["ok" | "accent" | "danger" | "warn", string] | undefined;
  return <Badge tone={t?.[0] ?? "neutral"}>{t?.[1] ?? m.state}</Badge>;
};

const phaseLabel = (p: ReplaceItem["phase"]) => ({ check: tr("Checking the new disk"), add: tr("Erasing and adding the new disk"), rebuild: tr("Rebuilding"), remove: tr("Taking the old disk out"), done: tr("Finished"), failed: tr("Failed") }[p]);

/** One line per running (or recently finished) replacement, shown on the storage pages. */
export function ReplaceBanner({ onOpen }: { onOpen: (id: string) => void }) {
  const { data } = useReplacements();
  const qc = useQueryClient();
  const recent = (data?.items ?? []).filter((i) => i.running || (i.finished && Date.now() - new Date(i.finished).getTime() < 24 * 3600e3));
  if (!recent.length) return null;
  return (
    <div className="mb-4 space-y-2">
      {recent.map((i) => (
        <div key={i.id} className={cx("flex flex-wrap items-center gap-3 rounded-xl border px-4 py-3 text-[13px]", i.phase === "failed" ? "border-danger/30 bg-danger/10" : i.phase === "done" ? "border-ok/30 bg-ok/10" : "border-accent/30 bg-accent-soft")}>
          <Replace size={16} className="shrink-0" />
          <span className="min-w-0 flex-1">
            <b>{tr("Disk replacement in {0}", i.name)}</b>{" · "}
            {i.running ? (i.phase === "rebuild" ? (i.waiting ? tr("Rebuilding (waiting to start)") : tr("Rebuilding {0} %", i.progress.toFixed(1))) : phaseLabel(i.phase))
              : i.phase === "done" ? (i.oldSerial || i.oldDisk ? tr("finished: pull the disk with serial number {0}", i.oldSerial || i.oldDisk) : tr("finished")) : srv(i.error) || tr("failed")}
            {i.running && i.eta ? ` · ${tr("about {0} left", duration(i.eta))}` : ""}
          </span>
          {i.running && i.phase === "rebuild" && <Progress className="!w-32" value={i.progress} tone="accent" />}
          <Button size="sm" onClick={() => onOpen(i.id)}>{tr("Open")}</Button>
          {!i.running && <Button size="sm" variant="ghost" onClick={async () => { await api.del(`/api/storage/replace/${i.id}`); qc.invalidateQueries({ queryKey: ["replace"] }); }}>{tr("Dismiss")}</Button>}
        </div>
      ))}
    </div>
  );
}

function Situation({ plan }: { plan: Plan }) {
  const g = plan.group!;
  const old = plan.old;
  return (
    <div className="space-y-4 text-[13px]">
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="rounded-xl bg-panel-2 p-3">
          <div className="text-muted text-[12px]">{g.kind === "lvm" ? tr("Volume group") : g.kind === "zfs" ? tr("ZFS pool") : tr("RAID array")}</div>
          <div className="font-semibold">{g.name} <span className="font-normal text-muted">· {levelName(g)}{g.vdev && g.vdev !== g.members[0]?.name ? ` (${g.vdev})` : ""}</span></div>
          {!!g.usedBy?.length && <div className="text-muted text-[12px]">{tr("Holds: {0}", g.usedBy.map((u) => srv(u)).join(", "))}</div>}
        </div>
        <div className="rounded-xl bg-panel-2 p-3">
          <div className="text-muted text-[12px]">{tr("Redundancy left")}</div>
          <div className={cx("font-semibold", plan.left <= 0 ? "text-warn" : "")}>
            {g.tolerates === 0 ? tr("None (no redundant copy)") : plan.left <= 0 ? tr("None until the rebuild ends") : tr("{0} more disk(s) can fail", plan.left)}
          </div>
        </div>
        <div className="rounded-xl bg-panel-2 p-3">
          <div className="text-muted text-[12px]">{tr("Last successful backup")}</div>
          <div className={cx("font-semibold", plan.backup !== "recent" ? "text-warn" : "")}>{plan.lastBackup ? dateTime(plan.lastBackup) : tr("None")}</div>
          {plan.backup !== "recent" && <AppLink to="/backup" className="text-accent text-[12px]">{tr("Back up first")}</AppLink>}
        </div>
      </div>
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="table">
          <thead><tr><th>{tr("Disk")}</th><th>{tr("Serial number")}</th><th>{tr("Status")}</th><th>SMART</th></tr></thead>
          <tbody>
            {g.members.map((m, i) => {
              const isOld = old && m.name === old.name && m.disk === old.disk && m.state === old.state;
              return (
                <tr key={i} className={isOld ? "bg-warn/10" : ""}>
                  <td className="mono">{m.disk || m.was || m.name || tr("(empty slot)")}{isOld && <span className="ml-2"><Badge tone="warn">{tr("to replace")}</Badge></span>}</td>
                  <td className="mono">{m.serial || "—"}</td>
                  <td>{stateBadge(m)}</td>
                  <td>{m.failing ? <Badge tone="danger" dot>{tr("Failing")}</Badge> : m.weak?.length ? <Badge tone="warn" dot title={m.weak.map((w) => srv(w)).join(", ")}>{tr("Signs of wear")}</Badge> : m.disk ? <Badge tone="ok" dot>{tr("OK")}</Badge> : "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {old?.weak?.length ? <div className="text-warn text-[12.5px]">{tr("Signs of wear on this disk: {0}", old.weak.map((w) => srv(w)).join(", "))}</div> : null}
    </div>
  );
}

function RunView({ item, ledctl }: { item: ReplaceItem; ledctl: boolean }) {
  const { toast } = useUI();
  const locate = useMutation({
    mutationFn: (on: boolean) => api.post(`/api/storage/replace/${item.id}/locate`, { on }),
    onSuccess: (_r, on) => toast(on ? tr("The LED of the bay is blinking") : tr("LED off")),
    onError: (e: Error) => toast(srv(e.message), "error"),
  });
  const order: ReplaceItem["phase"][] = ["check", "add", "rebuild", "remove", "done"];
  const cur = order.indexOf(item.phase);
  return (
    <div className="space-y-4 text-[13px]">
      <div className="text-muted">{tr("{0}: {1} → new disk {2}", item.name, item.oldDisk ? `${item.oldDisk}${item.oldSerial ? ` (S/N ${item.oldSerial})` : ""}` : tr("missing disk"), `${item.new}${item.newSerial ? ` (S/N ${item.newSerial})` : ""}`)}</div>
      <ol className="space-y-1.5">
        {order.slice(0, 4).map((p, i) => (
          <li key={p} className={cx("flex items-center gap-2", item.phase === "failed" ? "text-muted" : i < cur || item.phase === "done" ? "text-ok" : i === cur ? "font-semibold" : "text-muted")}>
            {i < cur || item.phase === "done" ? <CheckCircle2 size={15} /> : i === cur && item.running ? <Spinner /> : <span className="inline-block h-[15px] w-[15px] rounded-full border border-line" />}
            {phaseLabel(p)}
          </li>
        ))}
      </ol>
      {item.running && item.phase === "rebuild" && (
        <div className="rounded-xl bg-panel-2 p-3">
          <div className="mb-2 flex justify-between"><span>{item.waiting ? tr("Rebuilding (waiting to start)") : tr("Rebuilding {0} %", item.progress.toFixed(1))}</span><span className="text-muted">{item.eta ? tr("about {0} left", duration(item.eta)) : ""}</span></div>
          <Progress value={item.progress} tone="accent" indeterminate={item.waiting} />
          <div className="mt-2 text-muted text-[12px]">{tr("You can close this window and even reload the page: the rebuild goes on, and you are notified at the end.")}</div>
        </div>
      )}
      {item.phase === "failed" && <Notice tone="warn"><b>{tr("The replacement failed:")}</b>{" "}{srv(item.error)}</Notice>}
      {item.warning && <Notice tone="warn">{srv(item.warning)}</Notice>}
      {item.phase === "done" && (
        <div className="rounded-xl border border-ok/30 bg-ok/10 p-4">
          <div className="mb-2 flex items-center gap-2 font-semibold text-ok"><CheckCircle2 size={17} />{tr("{0} is protected again", item.name)}</div>
          {item.oldDisk || item.oldSerial ? (
            <>
              <div>{tr("You can now shut down the NAS if your disks are not hot-swappable, and pull this disk:")}</div>
              <div className="mt-2 grid gap-1 rounded-lg bg-panel p-3 sm:grid-cols-2">
                <div><span className="text-muted">{tr("Serial number")}</span> <b className="mono">{item.oldSerial || "—"}</b></div>
                <div><span className="text-muted">{tr("Model")}</span> <b>{item.oldModel || "—"}</b></div>
                <div><span className="text-muted">{tr("Device")}</span> <span className="mono">{item.oldDisk}</span></div>
                {item.oldById && <div className="truncate"><span className="text-muted">by-id</span> <span className="mono text-[12px]">{item.oldById}</span></div>}
              </div>
              <div className="mt-2 text-muted text-[12px]">{tr("The serial number is printed on the disk label: check it before pulling the disk.")}</div>
              {ledctl ? (
                <div className="mt-3 flex gap-2">
                  <Button size="sm" icon={<Lightbulb size={14} />} loading={locate.isPending && locate.variables} onClick={() => locate.mutate(true)}>{tr("Blink its LED")}</Button>
                  <Button size="sm" variant="ghost" icon={<LightbulbOff size={14} />} onClick={() => locate.mutate(false)}>{tr("Stop blinking")}</Button>
                </div>
              ) : <div className="mt-2 text-muted text-[12px]">{tr("Bay LEDs: install ledmon (ledctl) if your enclosure supports it.")}</div>}
            </>
          ) : <div>{tr("The failed disk was already removed: there is nothing to pull.")}</div>}
        </div>
      )}
    </div>
  );
}

/** Asks which disk to replace when the wizard opens without one (security advisor with several disks). */
function ChooseDisk({ onPick }: { onPick: (path: string) => void }) {
  const { data, isLoading } = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks") });
  if (isLoading) return <Spinner />;
  const list = (data ?? []).filter((d) => ["raid", "zfs", "lvm"].includes(d.usage ?? "")).sort((a, b) => Number(b.healthy === false || !!b.warnings?.length) - Number(a.healthy === false || !!a.warnings?.length));
  if (!list.length) return <Notice>{tr("No disk belongs to a RAID array, a ZFS pool or an LVM volume group.")}</Notice>;
  return (
    <div className="grid gap-2">
      {list.map((d) => (
        <button key={d.path} onClick={() => onPick(d.path)} className="flex items-center gap-3 rounded-xl border border-line bg-panel-2 px-3 py-2.5 text-left hover:border-accent/40">
          <HardDrive size={18} className={d.healthy === false ? "text-danger" : d.warnings?.length ? "text-warn" : "text-muted"} />
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium text-[13px]">{d.model || d.name} · {d.path}</div>
            <div className="truncate text-muted text-[11.5px]">{d.serial} · {d.usage} {d.usedBy}{d.warnings?.length ? ` · ${d.warnings.map((w) => srv(w)).join(", ")}` : ""}</div>
          </div>
          <ArrowRight size={15} className="text-muted" />
        </button>
      ))}
    </div>
  );
}

export function ReplaceWizard({ sel, replacement, onClose }: { sel: ReplaceSel | null; replacement?: string | null; onClose: () => void }) {
  const open = !!sel || !!replacement;
  const [picked, setPicked] = useState<string>("");
  const s: ReplaceSel = sel && !sel.disk && !sel.group && picked ? { disk: picked } : sel ?? {};
  const choosing = !!sel && !s.disk && !s.group;
  const [step, setStep] = useState<"situation" | "method" | "disk">("situation");
  const [mode, setMode] = useState("");
  const [dev, setDev] = useState("");
  const [typed, setTyped] = useState("");
  const [smartTest, setSmartTest] = useState(true);
  const [started, setStarted] = useState<string | null>(null);
  const reps = useReplacements();
  const { track } = useJobs();
  const qc = useQueryClient();
  const plan = useQuery({
    queryKey: ["replace-plan", s.disk, s.group, s.member, mode],
    queryFn: () => api.get<{ plan: Plan; ledctl: boolean; running?: ReplaceItem }>(`/api/storage/replace/plan${q({ disk: s.disk ?? "", group: s.group ?? "", member: s.member ?? "", mode })}`),
    enabled: open && !replacement && !started && !choosing,
  });
  const start = useMutation({
    mutationFn: () => api.post<{ job: Job; id: string }>("/api/storage/replace", { disk: s.disk ?? "", group: plan.data?.plan.group?.id ?? s.group ?? "", member: plan.data?.plan.old?.name || s.member || "", mode: p?.mode ?? mode, new: dev, confirm: typed, smartTest }),
    onSuccess: (r) => { track(r.job); setStarted(r.id); qc.invalidateQueries({ queryKey: ["replace"] }); },
  });
  const close = () => { setStep("situation"); setMode(""); setDev(""); setTyped(""); setStarted(null); setPicked(""); start.reset(); onClose(); };

  const p = plan.data?.plan;
  const runId = replacement || started || plan.data?.running?.id;
  const item = runId ? reps.data?.items.find((i) => i.id === runId) ?? (plan.data?.running?.id === runId ? plan.data.running : undefined) : undefined;
  const cand = p?.candidates.find((c) => c.path === dev);
  const devName = cand?.name ?? "";

  let body: React.ReactNode;
  let footer: React.ReactNode = <Button onClick={close}>{tr("Close")}</Button>;
  if (runId) {
    body = item ? <RunView item={item} ledctl={!!reps.data?.ledctl} /> : <Spinner />;
  } else if (choosing) {
    body = <div className="space-y-3"><p className="text-muted text-[13px]">{tr("Which disk do you want to replace?")}</p><ChooseDisk onPick={setPicked} /></div>;
  } else if (plan.isLoading || !p) {
    body = plan.error ? <ErrorBox error={plan.error} /> : <Spinner />;
  } else if (p.refusal) {
    body = (
      <div className="space-y-4">
        {p.group && <Situation plan={p} />}
        <div className="flex gap-3 rounded-xl border border-danger/30 bg-danger/10 p-4 text-[13px]">
          <XCircle size={18} className="shrink-0 text-danger" />
          <div><div className="font-semibold">{tr("StrataOS will not do this replacement")}</div><div className="mt-1">{srv(p.refusal)}</div></div>
        </div>
      </div>
    );
  } else if (step === "situation") {
    body = (
      <div className="space-y-4">
        <Situation plan={p} />
        {!!p.warnings?.length && <div className="space-y-2">{p.warnings.map((w) => <div key={w} className="flex gap-2 text-warn text-[12.5px]"><AlertTriangle size={15} className="shrink-0" />{srv(w)}</div>)}</div>}
      </div>
    );
    footer = <><Button onClick={close}>{tr("Cancel")}</Button><Button variant="primary" onClick={() => { setMode(p.mode ?? ""); setStep("method"); }}>{tr("Next")}</Button></>;
  } else if (step === "method") {
    const labels: Record<string, [string, string]> = {
      "add-first": [tr("Add the new disk first (recommended)"), tr("The old disk stays in service while its data is copied onto the new one, so the data keeps its protection. Possible when the old disk still works and a free bay or port is available.")],
      degraded: [tr("The failed disk is already removed"), tr("The data is rebuilt onto the new disk from the other disks. Until the end, the array has less (or no) redundancy.")],
    };
    body = (
      <div className="space-y-4 text-[13px]">
        <div className="grid gap-2">
          {p.modes.map((m) => (
            <button key={m.id} disabled={!m.ok} onClick={() => setMode(m.id)} className={cx("rounded-xl border p-3 text-left", !m.ok ? "cursor-not-allowed border-line opacity-60" : (mode || p.mode) === m.id ? "border-accent bg-accent-soft" : "border-line hover:border-accent/40")}>
              <div className="font-semibold">{labels[m.id]?.[0] ?? m.id}</div>
              <div className="text-muted text-[12px]">{m.ok ? labels[m.id]?.[1] : srv(m.reason)}</div>
            </button>
          ))}
        </div>
        {!!p.steps?.length && (
          <div>
            <div className="mb-1.5 font-medium">{tr("What StrataOS will do")}</div>
            <ol className="list-decimal space-y-1 pl-5 text-muted">{p.steps.map((x) => <li key={x}>{srv(x)}</li>)}</ol>
          </div>
        )}
      </div>
    );
    footer = <><Button onClick={() => setStep("situation")}>{tr("Back")}</Button><Button variant="primary" onClick={() => setStep("disk")}>{tr("Next")}</Button></>;
  } else {
    body = (
      <div className="space-y-4 text-[13px]">
        <p className="text-muted">{tr("Choose the new disk. It must hold at least {0} and must not be in use.", bytes(p.need ?? 0))}</p>
        {!p.candidates.length ? <Notice tone="warn">{tr("No other disk is connected. Plug in the new disk (with the NAS off if your disks are not hot-swappable), then open this wizard again.")}</Notice> : (
          <div className="grid gap-2">
            {p.candidates.map((c) => (
              <button key={c.path} disabled={!c.ok} onClick={() => { setDev(c.path); setTyped(""); }} className={cx("flex items-center gap-3 rounded-xl border px-3 py-2.5 text-left", !c.ok ? "cursor-not-allowed border-line opacity-60" : dev === c.path ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                <HardDrive size={18} className={dev === c.path ? "text-accent" : "text-muted"} />
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">{c.model || c.name} · {c.path}</div>
                  <div className="truncate text-muted text-[11.5px]">{c.serial || tr("no serial number")}{c.ok ? (c.erase ? ` · ${tr("holds {0}: it will be erased", c.erase)}` : "") : ` · ${srv(c.reason)}`}{c.weak?.length ? ` · ${c.weak.map((w) => srv(w)).join(", ")}` : ""}</div>
                </div>
                <div className="text-[12.5px] font-medium">{bytes(c.size)}</div>
              </button>
            ))}
          </div>
        )}
        <Checkbox checked={smartTest} onChange={setSmartTest} label={tr("SMART short self-test of the new disk first (about 2 minutes)")} />
        <p className="text-muted text-[12px]">{tr("For a thorough check, run a burn-in test of the new disk from the Disks page before replacing (several hours).")}</p>
        {cand && (
          <Notice tone="warn">
            <b>{tr("Everything on {0} will be erased.", `${cand.model || cand.name} (${cand.path}, ${bytes(cand.size)})`)}</b>
            <Field label={tr("Type \"{0}\" to confirm", devName)} className="mt-3"><Input value={typed} onChange={(e) => setTyped(e.target.value)} /></Field>
          </Notice>
        )}
        <ErrorBox error={start.error} />
      </div>
    );
    footer = <><Button onClick={() => setStep("method")}>{tr("Back")}</Button><Button variant="danger" icon={<Replace size={15} />} disabled={!cand || typed !== devName} loading={start.isPending} onClick={() => start.mutate()}>{tr("Erase and start the replacement")}</Button></>;
  }
  const title = item ? tr("Disk replacement in {0}", item.name) : p?.old?.disk ? tr("Replace {0}", p.old.disk) : tr("Replace a disk");
  return <Modal open={open} onClose={close} wide title={title} footer={footer}>{body}</Modal>;
}
