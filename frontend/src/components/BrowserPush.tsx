import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe } from "lucide-react";
import { api, q } from "../lib/api";
import { tr } from "../lib/i18n";
import { Badge, Button, ErrorBox, Spinner, useUI } from "./ui";

const b64url = (s: string) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));

/** Web push of the installable web app (PWA): subscribe this browser to the NAS alerts. */
export function BrowserPush() {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const push = useQuery({ queryKey: ["me-push"], queryFn: () => api.get<{ enabled: boolean; name: string; vapid: string }>("/api/me/push") });
  const supported = !!push.data?.vapid && "serviceWorker" in navigator && "PushManager" in window;

  const enable = async () => {
    setBusy(true);
    setErr(null);
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64url(push.data!.vapid) });
      const j = sub.toJSON();
      await api.post("/api/me/push/subscribe", { endpoint: j.endpoint, keys: { p256dh: j.keys?.p256dh, auth: j.keys?.auth }, name: navigator.userAgent });
      toast(tr("Enabled"));
      qc.invalidateQueries({ queryKey: ["me-push"] });
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  const disable = async () => {
    setBusy(true);
    setErr(null);
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      await api.del(`/api/me/push${q({ endpoint: sub?.endpoint ?? "" })}`);
      if (sub) await sub.unsubscribe();
      toast(tr("Disabled"));
      qc.invalidateQueries({ queryKey: ["me-push"] });
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };

  if (push.isLoading) return <Spinner />;
  return (
    <div className="flex items-start gap-3 rounded-xl border border-border px-4 py-3">
      <Globe size={18} className="mt-0.5 shrink-0 text-accent" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 text-[13px] font-medium">
          {tr("Notifications on this browser")}
          {push.data?.enabled && <Badge tone="ok">{tr("Enabled")}</Badge>}
        </div>
        <p className="text-muted text-[12px]">{tr("The web app installed on the home screen shows the NAS alerts here, even when it is closed.")}</p>
        <ErrorBox error={err} />
      </div>
      {!supported ? (
        <span className="shrink-0 text-muted text-[12px]">{tr("Unavailable in this browser")}</span>
      ) : push.data?.enabled ? (
        <Button size="sm" variant="ghost" loading={busy} onClick={disable}>{tr("Disable")}</Button>
      ) : (
        <Button size="sm" loading={busy} onClick={enable}>{tr("Enable")}</Button>
      )}
    </div>
  );
}
