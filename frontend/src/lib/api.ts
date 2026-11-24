// Thin fetch wrapper around the StrataOS REST API.

export class ApiError extends Error {
  status: number;
  data: Record<string, unknown>;
  constructor(status: number, message: string, data: Record<string, unknown> = {}) {
    super(message);
    this.status = status;
    this.data = data;
  }
}

// Bearer token for the kiosk page (/hdmi#token): set once from the URL
// fragment, sent on every API call.
let bearer: string | null = null;
export function setBearerToken(t: string | null) { bearer = t; }

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-Strata": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
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
    const msg = (data && typeof data === "object" && "error" in data ? String((data as { error: string }).error) : res.statusText) || "Erreur";
    if (res.status === 401 && !path.startsWith("/api/login") && !path.startsWith("/api/me") && !path.startsWith("/api/s/")) {
      window.dispatchEvent(new Event("strata:unauthorized"));
    }
    throw new ApiError(res.status, msg, data && typeof data === "object" ? (data as Record<string, unknown>) : {});
  }
  return data as T;
}

export const api = {
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b: unknown = {}) => {
    // Signing out also drops the offline copy of the interface (the server
    // sends Clear-Site-Data too; not every browser applies it).
    if (p === "/api/logout" && typeof caches !== "undefined") void caches.keys().then((ks) => Promise.all(ks.map((k) => caches.delete(k)))).catch(() => {});
    return request<T>("POST", p, b);
  },
  put: <T>(p: string, b: unknown = {}) => request<T>("PUT", p, b),
  del: <T>(p: string) => request<T>("DELETE", p),
};

export function wsURL(path: string) {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}

export const q = (params: Record<string, string | number | boolean | undefined>) => {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") u.set(k, String(v));
  const s = u.toString();
  return s ? `?${s}` : "";
};
