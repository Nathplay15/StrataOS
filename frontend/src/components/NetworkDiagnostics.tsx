import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Activity, CheckCircle2, XCircle } from "lucide-react";
import { api } from "../lib/api";
import { tr } from "../lib/i18n";
import { Badge, Button, Card, ErrorBox, Field, Input, Spinner } from "./ui";

interface Check {
  name: string;
  ok: boolean;
  detail?: string;
  latency?: number;
  loss?: number;
  ips?: string[];
}

const checkLabel: Record<string, string> = {
  dns: tr("Name resolution"),
  ping: tr("Ping"),
  port: tr("Port"),
  gateway: tr("Default gateway"),
  internet: tr("Internet"),
};

/** Network diagnostics run from the NAS itself. */
export function DiagnosticsCard() {
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const run = useMutation({
    mutationFn: () => api.post<Check[]>("/api/system/network/diagnose", { host: host.trim(), port: +port || 0 }),
  });
  return (
    <Card title={tr("Diagnostics")}>
      <div className="space-y-4">
        <p className="text-muted text-[13px]">{tr("Checks run from the NAS: name resolution, ping, a TCP port, the default gateway and the access to the Internet.")}</p>
        <div className="flex flex-wrap items-end gap-3">
          <Field label={tr("Host to check")} className="min-w-[220px] flex-1"><Input className="mono" placeholder="nas.example.com or 192.168.1.20" value={host} onChange={(e) => setHost(e.target.value)} onKeyDown={(e) => e.key === "Enter" && host.trim() && run.mutate()} /></Field>
          <Field label={tr("Port (optional)")} hint={tr("TCP connection tested when set")} className="w-36"><Input className="mono" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} /></Field>
          <Button variant="primary" icon={<Activity size={15} />} disabled={!host.trim()} loading={run.isPending} onClick={() => run.mutate()}>{tr("Run the checks")}</Button>
        </div>
        <ErrorBox error={run.error} />
        {run.isPending && <Spinner label={tr("Checks running…")} />}
        {run.data && (
          <div className="divide-y divide-border rounded-xl border border-border">
            {run.data.map((c) => (
              <div key={c.name} className="flex items-start gap-3 px-4 py-3">
                {c.ok ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : <XCircle size={17} className="mt-0.5 shrink-0 text-danger" />}
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-[13px] font-medium">
                    {checkLabel[c.name] ?? c.name}
                    {c.latency ? <Badge tone="accent">{c.latency.toFixed(1)} ms</Badge> : ""}
                    {c.loss ? <Badge tone="warn">{tr("{0}% packet loss", Math.round(c.loss))}</Badge> : ""}
                  </div>
                  {c.detail && <div className="text-muted text-[12px]">{c.detail}</div>}
                  {c.ips && <div className="mono mt-0.5 text-muted text-[12px]">{c.ips.join(", ")}</div>}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </Card>
  );
}
