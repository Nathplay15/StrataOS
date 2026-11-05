// State of the app shared by the pages: the agent's state (accounts and
// their sync status), the account shown, what each NAS says about itself.
// The window follows the agent's events (long poll) to refresh at once and
// to show the engine's notifications, and the notification feed of each
// NAS for native notifications.
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { isPermissionGranted, requestPermission, sendNotification } from "@tauri-apps/plugin-notification";
import { agent, nas, onAgent } from "./agent";
import { setAccount } from "./nasapi";
import { lastAccount, pref, setLastAccount, setPref } from "./prefs";
import { srv } from "./i18n";
import type { Account, AgentEvent, AgentState, NasInfo } from "./types";

interface Ctx {
  state: AgentState | undefined;
  error: unknown;
  /** The account shown ("" when none is paired). */
  accountId: string;
  account: Account | undefined;
  setAccountId: (id: string) => void;
  info: NasInfo | undefined;
  isAdmin: boolean;
  refresh: () => void;
}

const AppCtx = createContext<Ctx>({ state: undefined, error: null, accountId: "", account: undefined, setAccountId: () => {}, info: undefined, isAdmin: false, refresh: () => {} });
export const useApp = () => useContext(AppCtx);

let permission: boolean | null = null;

/** Shows a notification of the system (asking for the right once). */
export async function notify(title: string, body: string) {
  if (pref("notify") === "off") return;
  try {
    if (permission === null) {
      permission = await isPermissionGranted();
      if (!permission) permission = (await requestPermission()) === "granted";
    }
    if (permission) sendNotification({ title, body });
  } catch {
    /* no notification service */
  }
}

export function AppProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { data: state, error, refetch } = useQuery({
    queryKey: ["agent-state"],
    queryFn: () => agent.get<AgentState>("/v1/state"),
    refetchInterval: (q) => ((q.state.data?.accounts ?? []).some((a) => (a.status.pairs ?? []).some((p) => p.state === "syncing")) ? 1000 : 4000),
    refetchIntervalInBackground: true,
    retry: true,
  });
  useEffect(() => onAgent(() => void qc.invalidateQueries()), [qc]);

  // The agent's events: refresh, and the engine's notifications.
  useEffect(() => {
    let stop = false;
    let seq = -1;
    const ctl = new AbortController();
    (async () => {
      while (!stop) {
        try {
          if (seq < 0) seq = (await agent.get<AgentState>("/v1/state")).seq;
          const r = await agent.get<{ seq: number; events: AgentEvent[] }>(`/v1/events?after=${seq}&wait=45`, ctl.signal);
          if (r.seq < seq) {
            seq = -1; // the agent restarted
            continue;
          }
          seq = r.seq;
          if (r.events.length) {
            void qc.invalidateQueries({ queryKey: ["agent-state"] });
            if (r.events.some((e) => e.kind === "transfer")) void qc.invalidateQueries({ queryKey: ["transfers"] });
          }
          for (const e of r.events) if (e.kind === "notify" && e.title) void notify(e.title, e.body ?? "");
        } catch {
          if (stop) return;
          seq = -1;
          await new Promise((r) => setTimeout(r, 3000));
        }
      }
    })();
    return () => {
      stop = true;
      ctl.abort();
    };
  }, [qc]);

  const paired = (state?.accounts ?? []).filter((a) => a.paired);
  const [chosen, setChosen] = useState(lastAccount());
  const accountId = paired.some((a) => a.id === chosen) ? chosen : (paired[0]?.id ?? "");
  const account = paired.find((a) => a.id === accountId);
  setAccount(accountId || "main");
  const setAccountId = useCallback((id: string) => {
    setChosen(id);
    setLastAccount(id);
  }, []);

  const { data: info } = useQuery({
    queryKey: ["nasinfo", accountId],
    queryFn: () => nas(accountId).get<NasInfo>("/api/mobile/info"),
    enabled: !!accountId,
    staleTime: 5 * 60_000,
    retry: 1,
  });

  useNasNotifications(paired.map((a) => a.id).join(","), state);

  const value = useMemo<Ctx>(() => ({
    state, error, accountId, account, setAccountId, info, isAdmin: info?.user?.role === "admin", refresh: () => void refetch(),
  }), [state, error, accountId, account, setAccountId, info, refetch]);
  return <AppCtx.Provider value={value}>{children}</AppCtx.Provider>;
}

type FeedEvent = { id: number; time: string; level: string; subject: string; message: string };

/** Native notifications from the notification feed of each NAS (every
 *  minute; the first look only remembers where the feed is). */
function useNasNotifications(ids: string, state: AgentState | undefined) {
  const names = useRef<Record<string, string>>({});
  for (const a of state?.accounts ?? []) names.current[a.id] = a.config.nasName || a.config.server || a.id;
  useEffect(() => {
    if (!ids) return;
    let stop = false;
    const check = async () => {
      for (const id of ids.split(",")) {
        const key = `feed-${id}`;
        const since = Number(pref(key) ?? "-1");
        try {
          const r = await nas(id).get<{ cursor: number; events: FeedEvent[] }>(`/api/mobile/events?since=${Math.max(since, 0)}&limit=20`);
          if (since >= 0) {
            for (const e of r.events.slice(-5)) void notify(`${names.current[id] ?? ""} · ${srv(e.subject)}`, srv(e.message));
          }
          setPref(key, String(r.cursor));
        } catch {
          /* NAS unreachable or not an administrator: try again later */
        }
      }
    };
    void check();
    const t = setInterval(() => { if (!stop) void check(); }, 60_000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [ids]);
}
