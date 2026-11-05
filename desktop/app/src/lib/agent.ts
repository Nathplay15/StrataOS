// Connection to the sync engine (the Go agent started by the shell): its
// address and per-launch token come from the shell, every call carries the
// token. The agent also forwards calls to the NAS of each account
// (/v1/accounts/{id}/nas/api/…), adding the device token.
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { ApiError } from "@nas/lib/api";

export { ApiError };

type Info = { url: string; token: string; version: string };

let info: Info | null = null;
let waiting: Promise<Info> | null = null;
const listeners = new Set<() => void>();

/** Calls f whenever the agent (re)starts (new address and token). */
export function onAgent(f: () => void) {
  listeners.add(f);
  return () => {
    listeners.delete(f);
  };
}

void listen<Info>("agent", (e) => {
  info = e.payload;
  listeners.forEach((f) => f());
});

/** Waits for the agent (it starts with the app). */
export function agentInfo(): Promise<Info> {
  if (info) return Promise.resolve(info);
  if (!waiting) {
    waiting = (async () => {
      for (let i = 0; ; i++) {
        try {
          info = await invoke<Info>("agent_info");
          return info;
        } catch (e) {
          if (i > 150) throw new Error(String(e));
          await new Promise((r) => setTimeout(r, 200));
        }
      }
    })().finally(() => { waiting = null; });
  }
  return waiting;
}

/** The agent's address and token when known (for <img src> and the like). */
export const current = () => info;

async function parse(res: Response): Promise<unknown> {
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const msg = (data && typeof data === "object" && "error" in data ? String((data as { error: string }).error) : res.statusText) || `HTTP ${res.status}`;
    throw new ApiError(res.status, msg, data && typeof data === "object" ? (data as Record<string, unknown>) : {});
  }
  return data;
}

/** A call to the agent (path starting with /v1/). */
export async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let i = await agentInfo();
  const send = (x: Info) => fetch(x.url + path, {
    method,
    headers: { Authorization: `Bearer ${x.token}`, ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });
  let res: Response;
  try {
    res = await send(i);
  } catch (e) {
    // The agent restarted: ask the shell again once.
    if (signal?.aborted) throw e;
    info = null;
    i = await agentInfo();
    res = await send(i);
  }
  return parse(res) as Promise<T>;
}

export const agent = {
  get: <T>(p: string, signal?: AbortSignal) => call<T>("GET", p, undefined, signal),
  post: <T>(p: string, b: unknown = {}) => call<T>("POST", p, b),
  put: <T>(p: string, b: unknown = {}) => call<T>("PUT", p, b),
  del: <T>(p: string) => call<T>("DELETE", p),
};

const enc = encodeURIComponent;

/** Calls to the NAS API of an account (path starting with /api/). */
export function nas(account: string) {
  const base = `/v1/accounts/${enc(account)}/nas`;
  return {
    get: <T>(p: string, signal?: AbortSignal) => call<T>("GET", base + p, undefined, signal),
    post: <T>(p: string, b: unknown = {}) => call<T>("POST", base + p, b),
    put: <T>(p: string, b: unknown = {}) => call<T>("PUT", base + p, b),
    patch: <T>(p: string, b: unknown = {}) => call<T>("PATCH", base + p, b),
    del: <T>(p: string) => call<T>("DELETE", base + p),
  };
}

/** Address of a NAS resource the webview loads by itself (pictures,
 *  videos): the token goes in the address, GET only. */
export function nasURL(account: string, p: string, params: Record<string, string | number | boolean | undefined> = {}) {
  const i = info;
  if (!i) return "";
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") u.set(k, String(v));
  u.set("st", i.token);
  return `${i.url}/v1/accounts/${enc(account)}/nas${p}?${u}`;
}

export const qs = (params: Record<string, string | number | boolean | undefined>) => {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") u.set(k, String(v));
  const s = u.toString();
  return s ? `?${s}` : "";
};
