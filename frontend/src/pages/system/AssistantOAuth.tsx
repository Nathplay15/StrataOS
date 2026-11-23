import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ExternalLink, KeyRound } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Settings } from "../../lib/types";
import { Button, ErrorBox, Field, Input, Notice } from "../../components/ui";
import { tr, locale } from "../../lib/i18n";

type Device = { provider: string; clientId: string; deviceCode: string; userCode: string; verification: string; interval: number };

// Account sign-in by OAuth device flow: the NAS gets a device code from the
// provider, the admin approves it in a browser, the NAS polls until the
// tokens arrive. No long-lived key is stored; the NAS renews the token.
export function OAuthSignIn({ data, oauthDisconnect }: { data?: Settings; oauthDisconnect: { mutate: () => void; isPending: boolean } }) {
  const qc = useQueryClient();
  const connected = !!data?.oauthProvider;
  const [clientId, setClientId] = useState(data?.oauthClientId ?? "");
  const [device, setDevice] = useState<Device | null>(null);
  const [err, setErr] = useState("");
  const start = useAction(() => api.post<Device>("/api/assistant/oauth/start", { provider: "mistral", clientId }), {
    onDone: (r) => { setDevice(r); setErr(""); },
  });

  // Poll the provider until the admin approves the code (RFC 8628).
  useEffect(() => {
    if (!device) return;
    let interval = Math.max(1, device.interval);
    let alive = true;
    let timer = 0;
    const tick = async () => {
      if (!alive) return;
      try {
        const r = await api.post<{ done: boolean; status?: string }>("/api/assistant/oauth/poll", { provider: device.provider, clientId: device.clientId, deviceCode: device.deviceCode });
        if (r.done) {
          setDevice(null);
          qc.invalidateQueries({ queryKey: ["settings"] });
          return;
        }
        if (r.status === "expired") {
          setDevice(null);
          setErr(tr("The sign-in code expired: get a new one."));
          return;
        }
        if (r.status === "slow_down") interval += 5;
      } catch (e) {
        setDevice(null);
        setErr(e instanceof Error ? e.message : String(e));
        return;
      }
      timer = window.setTimeout(tick, interval * 1000);
    };
    timer = window.setTimeout(tick, interval * 1000);
    return () => { alive = false; clearTimeout(timer); };
  }, [device, qc]);

  if (connected) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-0 flex-1">
          <Notice tone="info">
            {data?.oauthExpires
              ? tr("Connected to Mistral as {0} until {1}", data.oauthEmail || data.oauthProvider || "", new Date(data.oauthExpires).toLocaleDateString(locale))
              : tr("Connected to Mistral as {0}", data.oauthEmail || data.oauthProvider || "")}
          </Notice>
        </div>
        <Button loading={oauthDisconnect.isPending} onClick={() => oauthDisconnect.mutate()}>{tr("Disconnect")}</Button>
      </div>
    );
  }
  return (
    <div className="space-y-3">
      <Field label={tr("Client ID of your app at the provider")} hint={tr("Register an app at console.mistral.ai, then paste its client ID here.")}>
        <Input className="mono" value={clientId} onChange={(e) => setClientId(e.target.value)} />
      </Field>
      <div className="flex flex-wrap items-center gap-2">
        <Button icon={<KeyRound size={14} />} loading={start.isPending} disabled={!clientId.trim()} onClick={() => start.mutate()}>{tr("Get a sign-in code")}</Button>
      </div>
      {device && (
        <div className="space-y-2 rounded-xl border border-line bg-panel-2 p-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="mono rounded-lg bg-panel px-3 py-2 text-[18px] tracking-widest">{device.userCode}</code>
            <a href={device.verification} target="_blank" rel="noreferrer"><Button size="sm" icon={<ExternalLink size={14} />}>{tr("Open the provider page")}</Button></a>
          </div>
          <div className="text-[12.5px] text-muted">{tr("Enter this code at {0} to connect your account, then come back here: the NAS finishes the sign-in by itself.", device.verification)}</div>
          <div className="text-[12px] text-accent">{tr("Waiting for approval…")}</div>
        </div>
      )}
      <ErrorBox error={start.error ?? (err || null)} />
    </div>
  );
}
