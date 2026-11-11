// Files on demand: the shared folders of the NAS in the file manager,
// downloaded when opened (desktop/internal/ondemand). Linux: a FUSE folder
// with a cache; Windows: a Cloud Files sync root the shell registers with
// Explorer; macOS: a File Provider domain (Finder's own menu keeps or
// removes downloads).
import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { invoke } from "@tauri-apps/api/core";
import { open } from "@tauri-apps/plugin-dialog";
import { Cloud, FolderOpen, HardDriveDownload } from "lucide-react";
import { Badge, Button, Card, Field, Input, Notice, Switch, useUI } from "@nas/components/ui";
import { bytes } from "@nas/lib/format";
import { agent } from "../lib/agent";
import { t } from "../lib/i18n";

interface Provider {
  kind: string;
  root?: string;
  state: string;
  error?: string;
  syncRootId?: string;
}

interface Status {
  supported: boolean;
  kind?: "fuse" | "cfapi" | "fileprovider";
  enabled: boolean;
  path?: string;
  defaultPath?: string;
  cacheMB?: number;
  provider?: Provider;
  error?: string;
  ready: boolean;
  cacheUsed: number;
  cacheBudget: number;
  pinned: number;
  dirty: number;
  hidden?: string[];
}

export function OnDemandCard({ accountId, nasName }: { accountId: string; nasName: string }) {
  const { toast } = useUI();
  const base = `/v1/accounts/${encodeURIComponent(accountId)}/ondemand`;
  const q = useQuery({ queryKey: ["ondemand", accountId], queryFn: () => agent.get<Status>(base), refetchInterval: 5000 });
  const [busy, setBusy] = useState(false);
  const [cache, setCache] = useState<string | null>(null);
  const st = q.data;
  const registering = useRef("");

  // Windows: the shell registers the sync root with Explorer, then the
  // agent connects to it.
  const p = st?.provider;
  useEffect(() => {
    if (p?.kind !== "cfapi" || p.state !== "register" || !p.syncRootId || !p.root || registering.current === p.syncRootId) return;
    registering.current = p.syncRootId;
    void (async () => {
      try {
        await invoke("ondemand_register", { id: p.syncRootId, path: p.root, name: nasName ? `StrataOS - ${nasName}` : "StrataOS" });
        await agent.post(`${base}/registered`, {});
      } catch (e) {
        toast(String((e as Error).message ?? e), "error");
      }
      void q.refetch();
    })();
  }, [p?.kind, p?.state, p?.syncRootId]);

  if (!st || !st.supported) return null;
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await f();
      await q.refetch();
    } catch (e) {
      toast(String((e as Error).message ?? e), "error");
    } finally {
      setBusy(false);
    }
  };
  const set = (body: Record<string, unknown>) => run(async () => {
    const old = st.provider?.syncRootId;
    await agent.put(base, body);
    if (body.enabled === false && old) await invoke("ondemand_unregister", { id: old }).catch(() => {});
  });
  const root = st.provider?.root || st.path || st.defaultPath || "";
  const state = st.error || st.provider?.state === "error" ? <Badge tone="danger">{t("Error")}</Badge>
    : st.provider?.state === "register" ? <Badge tone="warn">{t("Setting up")}</Badge>
    : st.ready ? <Badge tone="ok">{t("Ready")}</Badge>
    : <Badge tone="accent" dot>{t("Listing the NAS")}</Badge>;
  return (
    <Card className="mb-4">
      <div className="flex flex-wrap items-start gap-3">
        <Cloud size={20} className="mt-0.5 text-accent" />
        <div className="min-w-0 flex-1">
          <Switch checked={st.enabled} disabled={busy} onChange={(v) => set({ enabled: v })} label={t("Files on demand")}
            hint={t("Every shared folder of the NAS appears in the file manager without taking space; files download when you open them.")} />
        </div>
        {st.enabled && state}
      </div>
      {st.enabled && (
        <div className="mt-3 space-y-3 border-t border-line pt-3 text-[13px]">
          {(st.error || st.provider?.error) && <Notice tone="warn">{st.error || st.provider?.error}</Notice>}
          {root && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="mono selectable truncate">{root}</span>
              {st.provider?.root && <Button size="sm" icon={<FolderOpen size={14} />} onClick={() => run(() => agent.post("/v1/open", { account: accountId, pair: "ondemand" }))}>{t("Open")}</Button>}
              {st.kind !== "fileprovider" && <Button size="sm" disabled={busy} onClick={async () => {
                const d = await open({ directory: true, defaultPath: root });
                if (typeof d === "string") await set({ path: d });
              }}>{t("Change the folder")}</Button>}
            </div>
          )}
          {st.kind === "fuse" && (
            <>
              <div className="text-muted">{t("Kept on this computer: {0} of {1} · {2} files always kept · {3} changes to send", bytes(st.cacheUsed), bytes(st.cacheBudget), st.pinned, st.dirty)}</div>
              <div className="flex flex-wrap items-end gap-2">
                <Field label={t("Space for downloaded files (MB, 0: automatic)")}>
                  <Input type="number" min={0} className="w-40" value={cache ?? String(st.cacheMB ?? 0)} onChange={(e) => setCache(e.target.value)} />
                </Field>
                {cache !== null && <Button size="sm" disabled={busy} onClick={() => set({ cacheMB: Math.max(0, Number(cache) || 0) }).then(() => setCache(null))}>{t("Save")}</Button>}
                <Button size="sm" icon={<HardDriveDownload size={14} />} disabled={busy} onClick={() => run(async () => {
                  const r = await agent.post<{ freed: number }>(`${base}/free`, {});
                  toast(t("{0} freed", bytes(r.freed)), "ok");
                })}>{t("Free up space")}</Button>
              </div>
              <p className="text-[12px] text-muted">{t("Files you open stay until the space is needed. \"Keep on this device\" and \"Free up space\" are in the file manager menu (Settings) or: strataos-sync pin|unpin PATH.")}</p>
            </>
          )}
          {st.kind === "cfapi" && <p className="text-[12px] text-muted">{t("In Explorer, \"Always keep on this device\" and \"Free up space\" choose what stays on this computer.")}</p>}
          {st.kind === "fileprovider" && <p className="text-[12px] text-muted">{t("Turn the StrataOS extensions on in System Settings (General, Login Items & Extensions). In Finder, \"Download Now\" and \"Remove Download\" choose what stays on this Mac.")}</p>}
          {!!st.hidden?.length && (
            <Notice tone="info">{t("End-to-end encrypted folders are not shown on demand: {0}. Add them in Folder sync with their passphrase.", st.hidden.join(", "))}</Notice>
          )}
        </div>
      )}
    </Card>
  );
}
