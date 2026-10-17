import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useUI } from "../components/ui";
import { useJobs } from "./jobs";
import { srv } from "./i18n";

/**
 * Wraps an API call: shows a toast on success/error, tracks background jobs
 * and refreshes cached data.
 */
export function useAction<A = void, R = unknown>(fn: (arg: A) => Promise<R>, opts: { success?: string | ((r: R) => string); onDone?: (r: R) => void } = {}) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const { track } = useJobs();
  return useMutation({
    mutationFn: fn,
    onSuccess: (r) => {
      if (r && typeof r === "object" && "status" in r && "title" in r && (r as { status: string }).status === "running") {
        track(r);
      } else {
        if (opts.success) toast(typeof opts.success === "function" ? opts.success(r) : opts.success);
        qc.invalidateQueries();
      }
      opts.onDone?.(r);
    },
    onError: (e: Error) => toast(srv(e.message), "error"),
  });
}
