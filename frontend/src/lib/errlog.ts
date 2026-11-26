// Errors of the interface go to the NAS log (POST /api/client-errors): the
// page (its path only), the message and the stack, nothing else. A few per
// minute at most, each message once.

const sent = new Set<string>();
let recent: number[] = [];

export function reportError(kind: "render" | "error" | "rejection", err: unknown) {
  try {
    const e = err instanceof Error ? err : new Error(typeof err === "string" ? err : JSON.stringify(err ?? null));
    const message = String(e.message || e.name || "error").slice(0, 500);
    if (sent.has(message)) return;
    const now = Date.now();
    recent = recent.filter((t) => now - t < 60_000);
    if (recent.length >= 5) return;
    recent.push(now);
    sent.add(message);
    void fetch("/api/client-errors", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-Strata": "1" },
      body: JSON.stringify({ kind, page: location.pathname, message, stack: String(e.stack ?? "").slice(0, 4000) }),
    }).catch(() => {});
  } catch {
    /* never fail while reporting */
  }
}

/** Reports the errors no component caught. */
export function watchErrors() {
  addEventListener("error", (ev) => {
    // Resource load errors (an image) have no error object: not ours.
    if (ev.error) reportError("error", ev.error);
  });
  addEventListener("unhandledrejection", (ev) => reportError("rejection", ev.reason));
}
