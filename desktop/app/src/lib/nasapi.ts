// Stands in for frontend/src/lib/api.ts in the NAS web interface files the
// app shares (see vite.config.ts): the same calls, sent to the NAS of the
// current account through the agent.
import { ApiError } from "@nas/lib/api";
import { call } from "./agent";

export { ApiError };

let account = "main";

/** Selects the NAS the shared components talk to. */
export function setAccount(id: string) {
  account = id;
}
export const currentAccount = () => account;

function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  return call<T>(method, `/v1/accounts/${encodeURIComponent(account)}/nas${path}`, body).catch((e) => {
    if (e instanceof ApiError && e.status === 401) window.dispatchEvent(new Event("strata:unauthorized"));
    throw e;
  });
}

export const api = {
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b: unknown = {}) => request<T>("POST", p, b),
  put: <T>(p: string, b: unknown = {}) => request<T>("PUT", p, b),
  del: <T>(p: string) => request<T>("DELETE", p),
};

/** No websockets through the agent: the pages that need them open in the web interface window. */
export function wsURL(path: string) {
  return path;
}

export const q = (params: Record<string, string | number | boolean | undefined>) => {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") u.set(k, String(v));
  const s = u.toString();
  return s ? `?${s}` : "";
};
