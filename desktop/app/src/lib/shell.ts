// What the window does for the shell (src-tauri): the tray icon's state and
// menu, the tray's commands, and the actions coming from outside (file
// manager menus, pairing links).
import { useEffect, useRef } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { writeText } from "@tauri-apps/plugin-clipboard-manager";
import { agent, nas } from "./agent";
import { notify, useApp } from "./state";
import { t, tk } from "./i18n";
import type { Account, AgentState } from "./types";
import type { PageId } from "../pages";
import type { ShareLink } from "@nas/lib/types";
import { inDays, expiresISO } from "@nas/components/ShareLinkModal";

type Summary = { state: "idle" | "syncing" | "paused" | "error"; text: string };

/** The state of the tray icon and its status line, over every account. */
export function summary(st: AgentState | undefined): Summary {
  const accs = (st?.accounts ?? []).filter((a) => a.paired);
  if (!accs.length) return { state: "paused", text: tk("summary.notpaired") };
  if (accs.some((a) => a.status.signedOut)) return { state: "error", text: tk("summary.signedout") };
  const pairs = accs.flatMap((a) => a.status.pairs ?? []);
  if (accs.every((a) => a.status.paused || !(a.status.pairs ?? []).length) && pairs.length && accs.every((a) => a.status.paused)) {
    return { state: "paused", text: tk("state.paused") };
  }
  if (pairs.some((p) => p.state === "brake")) return { state: "error", text: tk("state.brake") };
  if (pairs.some((p) => p.state === "error")) return { state: "error", text: tk("summary.error") };
  const busy = pairs.filter((p) => p.state === "syncing").reduce((n, p) => n + Math.max(1, p.pending - p.done), 0);
  if (busy > 0) return { state: "syncing", text: tk("summary.syncing", busy) };
  return { state: "idle", text: tk("state.uptodate") };
}

export const allPaused = (accs: Account[]) => accs.length > 0 && accs.every((a) => a.status.paused);

/** The NAS public address of a share link. */
export const linkURL = (server: string | undefined, id: string) => `${server ?? ""}/s/${id}`;

/** Opens a page of the NAS web interface in a window of the app. */
export async function openWeb(account: string, path = "/", title = "StrataOS") {
  const r = await agent.post<{ url: string }>(`/v1/accounts/${encodeURIComponent(account)}/web`, { path });
  await invoke("open_web_window", { url: r.url, title, key: account });
}

type Bridge = { go: (page: PageId, params?: Record<string, string>) => void; pair: (link: string) => void };

type Action = { kind: "shareLink"; path: string } | { kind: "openOnNas"; path: string } | { kind: "deepLink"; url: string };

export function useShellBridge(b: Bridge) {
  const { state, setAccountId } = useApp();
  const ref = useRef({ b, state, setAccountId });
  ref.current = { b, state, setAccountId };

  // Tray: icon, status and texts of the menu.
  const sum = summary(state);
  const accs = (state?.accounts ?? []).filter((a) => a.paired);
  const paused = allPaused(accs);
  useEffect(() => {
    void invoke("tray_update", {
      t: {
        state: sum.state, status: sum.text, paused, paired: accs.length > 0,
        pause: tk("action.pauseall"), resume: tk("action.resumeall"), sync: tk("action.syncnow"),
        web: tk("action.openweb"), open: t("Open StrataOS"), quit: t("Quit"),
      },
    }).catch(() => {});
  }, [sum.state, sum.text, paused, accs.length]);

  useEffect(() => {
    const un = [
      listen<string>("tray", async (e) => {
        const { state: st } = ref.current;
        const paired = (st?.accounts ?? []).filter((a) => a.paired);
        try {
          if (e.payload === "pause") await agent.post("/v1/pause", { on: !allPaused(paired) });
          if (e.payload === "sync") await agent.post("/v1/sync-now", {});
          if (e.payload === "web" && paired[0]) await openWeb(paired[0].id);
        } catch (err) {
          void notify("StrataOS", String((err as Error).message ?? err));
        }
      }),
      listen("actions", () => void takeActions()),
    ];
    void takeActions();
    return () => un.forEach((p) => void p.then((f) => f()));
  }, []);

  async function takeActions() {
    const list = await invoke<Action[]>("take_actions").catch(() => [] as Action[]);
    for (const a of list) await run(a);
  }

  async function run(a: Action) {
    const { b, state: st, setAccountId } = ref.current;
    try {
      if (a.kind === "deepLink") {
        b.pair(a.url);
        return;
      }
      const r = await agent.post<{ account: string; remote: string; dir: boolean }>("/v1/resolve", { path: a.path });
      if (a.kind === "openOnNas") {
        setAccountId(r.account);
        const folder = r.dir ? r.remote : r.remote.slice(0, r.remote.lastIndexOf("/"));
        b.go("files", { path: folder, select: r.dir ? "" : r.remote });
        return;
      }
      const link = await nas(r.account).post<ShareLink>("/api/links", { path: r.remote, download: true, upload: false, expires: expiresISO(inDays(30)), maxDownloads: 0 });
      const server = st?.accounts.find((x) => x.id === r.account)?.config.server;
      const u = linkURL(server, link.id);
      await writeText(u);
      void notify(t("Link copied"), t("{0}: valid 30 days. Manage it in Share links.", r.remote.split("/").pop() ?? ""));
    } catch (err) {
      void notify("StrataOS", String((err as Error).message ?? err));
    }
  }
}
