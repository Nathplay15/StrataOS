import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, CircleStop, Square, XCircle } from "lucide-react";
import { cx } from "../lib/format";
import { api } from "../lib/api";
import { useJobs } from "../lib/jobs";
import type { Job } from "../lib/types";
import { tr, srv } from "../lib/i18n";
import { Button, useUI } from "./ui";

/**
 * Stops a running background job (POST /api/jobs/{id}/cancel) after a
 * confirmation. Jobs that must not be stopped (updates, RAID rebuilds…)
 * show the button disabled, with the reason as a tooltip.
 */
export function StopJobButton({ id, job, size = "sm", variant = "ghost", className }: { id?: string; job?: Job; size?: "sm" | "md"; variant?: "ghost" | "secondary" | "danger"; className?: string }) {
  const { jobs } = useJobs();
  const { confirm, toast } = useUI();
  const qc = useQueryClient();
  const jid = job?.id ?? id;
  const j = job ?? jobs.find((x) => x.id === jid);
  const [state, setState] = useState<"" | "pending" | "sent">("");
  // A plain call rather than a mutation: the button may unmount while the
  // confirmation is open (the tasks menu closes on an outside click).
  const stop = async () => {
    setState("pending");
    try {
      await api.post(`/api/jobs/${jid}/cancel`);
      toast(tr("Stopping…"), "info");
      setState("sent");
      qc.invalidateQueries();
    } catch (e) {
      toast(srv((e as Error).message), "error");
      setState("");
    }
  };
  if (!jid || (j && j.status !== "running")) return null;
  // Unknown to the list yet (just started): let the server decide.
  const blocked = j ? !j.cancellable : false;
  const title = blocked ? srv(j?.cancelReason) || tr("This task cannot be stopped") : tr("Stop this task");
  return (
    <span title={title} className={className}>
      <Button
        size={size}
        variant={variant}
        icon={<Square size={13} />}
        disabled={blocked || state === "sent"}
        loading={state === "pending"}
        aria-label={title}
        onClick={async (e) => {
          e.stopPropagation();
          const ok = await confirm({
            title: tr("Stop \"{0}\"?", srv(j?.title ?? tr("this task"))),
            text: tr("The task stops as soon as possible. What it has already done is kept: a stopped backup keeps its earlier versions and the next run starts again."),
            confirm: tr("Stop"),
            danger: true,
          });
          if (ok) void stop();
        }}
      >
        {tr("Stop")}
      </Button>
    </span>
  );
}

/** Icon of a history entry: success, stopped by an administrator, or failed. */
export function RunIcon({ status, size = 15, className = "" }: { status: string; size?: number; className?: string }) {
  if (status === "success") return <CheckCircle2 size={size} className={cx("mt-0.5 shrink-0 text-ok", className)} />;
  if (status === "stopped") return <CircleStop size={size} className={cx("mt-0.5 shrink-0 text-warn", className)} />;
  return <XCircle size={size} className={cx("mt-0.5 shrink-0 text-danger", className)} />;
}

/** Background of the last-run line of a task card. */
export const runTone = (status: string) => (status === "success" ? "bg-ok/10" : status === "stopped" ? "bg-warn/10 text-warn" : "bg-danger/10 text-danger");
