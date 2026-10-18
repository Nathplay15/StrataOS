import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import type { Job } from "./types";
import { useUI } from "../components/ui";
import { tr, srv } from "./i18n";

interface JobsCtx {
  jobs: Job[];
  /** Tracks a job returned by the API: toasts and refreshes data when it ends. */
  track: (job: Job | unknown) => void;
}

const Ctx = createContext<JobsCtx>({ jobs: [], track: () => {} });
export const useJobs = () => useContext(Ctx);

function isJob(v: unknown): v is Job {
  return !!v && typeof v === "object" && "id" in v && "status" in v && "title" in v;
}

export function JobsProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [watching, setWatching] = useState(0);
  const seen = useRef(new Map<string, Job["status"]>());

  const { data: jobs = [] } = useQuery({
    queryKey: ["jobs"],
    queryFn: () => api.get<Job[]>("/api/jobs"),
    refetchInterval: (q) => ((q.state.data ?? []).some((j) => j.status === "running") || watching > 0 ? 1500 : 10000),
  });

  useEffect(() => {
    let finished = false;
    for (const j of jobs) {
      const prev = seen.current.get(j.id);
      if (prev === "running" && j.status !== "running") {
        finished = true;
        if (j.status === "success") toast(tr("{0}: completed", srv(j.title)));
        else toast(tr("{0}: {1}", srv(j.title), j.error ?? tr("failed")), "error");
      }
      seen.current.set(j.id, j.status);
    }
    if (finished) {
      qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== "jobs" });
      setWatching((w) => Math.max(0, w - 1));
    }
  }, [jobs, qc, toast]);

  const track = useCallback(
    (job: Job | unknown) => {
      if (!isJob(job)) {
        qc.invalidateQueries();
        return;
      }
      seen.current.set(job.id, "running");
      setWatching((w) => w + 1);
      toast(tr("{0}: started", srv(job.title)), "info");
      qc.invalidateQueries({ queryKey: ["jobs"] });
    },
    [qc, toast],
  );

  return <Ctx.Provider value={{ jobs, track }}>{children}</Ctx.Provider>;
}
