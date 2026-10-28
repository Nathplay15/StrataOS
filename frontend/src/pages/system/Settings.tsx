import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Activity, Bell, Clock, Download, Settings as Cog, Lock, Share2, Shield, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { NotifyChannel, NotifyChannelId, NotifyLevel, Settings, SystemInfo } from "../../lib/types";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr, locale, languages } from "../../lib/i18n";
import { ConfigBackupCard } from "./ConfigBackup";
import { OfficeSettingsCard } from "../../components/OfficeSettings";
import { ComponentUninstallModal, uninstallable } from "../../components/Uninstall";
import { ApplianceConsoleCard } from "../../components/ApplianceConsole";

type CertInfo = { source: "self-signed" | "custom" | "letsencrypt"; subject: string; issuer: string; dnsNames: string[] | null; notAfter: string; active: boolean };

function CertificatesCard() {
  const { confirm } = useUI();
  const { data } = useQuery({ queryKey: ["certs"], queryFn: () => api.get<{ certificates: CertInfo[]; acmeDomain: string; acmeEmail: string }>("/api/system/certificates") });
  const [imp, setImp] = useState<{ cert: string; key: string } | null>(null);
  const [acme, setAcme] = useState<{ domain: string; email: string } | null>(null);
  const upload = useAction(() => api.put("/api/system/certificates/custom", imp), { success: tr("Certificate imported (active immediately)"), onDone: () => setImp(null) });
  const remove = useAction(() => api.del("/api/system/certificates/custom"), { success: tr("Imported certificate deleted") });
  const saveAcme = useAction((v: { domain: string; email: string }) => api.put("/api/system/certificates/acme", v), { success: tr("Let's Encrypt disabled"), onDone: () => setAcme(null) });
  const label = { "self-signed": tr("Self-signed"), custom: tr("Imported"), letsencrypt: "Let's Encrypt" };
  const readFile = (f: File | undefined, k: "cert" | "key") => f?.text().then((t) => setImp((p) => ({ cert: "", key: "", ...p, [k]: t })));
  return (
    <Card title={<span className="flex items-center gap-2"><Lock size={16} /> {" "}{tr("HTTPS certificate")}</span>} className="xl:col-span-2" actions={<>
      <Button size="sm" onClick={() => setImp({ cert: "", key: "" })}>{tr("Import a certificate")}</Button>
      <Button size="sm" onClick={() => setAcme({ domain: data?.acmeDomain ?? "", email: data?.acmeEmail ?? "" })}>{tr("Let's Encrypt")}</Button>
    </>}>
      <div className="space-y-2">
        {data?.certificates.map((c) => (
          <div key={c.source} className="flex flex-wrap items-center gap-3 rounded-xl bg-panel-2 px-4 py-2.5 text-[13px]">
            <Badge tone={c.active ? "ok" : "neutral"}>{label[c.source]}{c.active ? tr(" · used") : ""}</Badge>
            <span className="font-medium">{c.subject}</span>
            <span className="text-muted">{tr("issued by")}{" "}{c.issuer} {" "}{tr("· expires on")}{" "}{new Date(c.notAfter).toLocaleDateString(locale)}</span>
            {c.dnsNames && c.dnsNames.length > 0 && <span className="mono text-muted text-[11.5px]">{c.dnsNames.join(", ")}</span>}
            <div className="flex-1" />
            {c.source === "custom" && <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Delete the imported certificate?"), text: tr("The self-signed certificate will be used again."), danger: true })) remove.mutate(); }}>{tr("Delete")}</Button>}
          </div>
        ))}
        {data?.acmeDomain && <div className="text-[12.5px] text-muted">{tr("Let's Encrypt active for")}{" "}<b>{data.acmeDomain}</b> {" "}{tr("(used when the NAS is accessed by this name; automatic renewal).")}</div>}
      </div>
      <Modal open={!!imp} onClose={() => setImp(null)} title={tr("Import a certificate")} wide footer={<>
        <Button onClick={() => setImp(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!imp?.cert || !imp?.key} loading={upload.isPending} onClick={() => upload.mutate()}>{tr("Import")}</Button>
      </>}>
        {imp && (
          <div className="space-y-4">
            <Field label={tr("Certificate (PEM, with intermediate chain)")} hint={<input type="file" accept=".pem,.crt,.cer" onChange={(e) => readFile(e.target.files?.[0], "cert")} />}><textarea className="input mono h-32" value={imp.cert} onChange={(e) => setImp({ ...imp, cert: e.target.value })} placeholder={tr("-----BEGIN CERTIFICATE-----")} /></Field>
            <Field label={tr("Private key (PEM)")} hint={<input type="file" accept=".pem,.key" onChange={(e) => readFile(e.target.files?.[0], "key")} />}><textarea className="input mono h-32" value={imp.key} onChange={(e) => setImp({ ...imp, key: e.target.value })} placeholder={tr("-----BEGIN PRIVATE KEY-----")} /></Field>
            <ErrorBox error={upload.error} />
          </div>
        )}
      </Modal>
      <Modal open={!!acme} onClose={() => setAcme(null)} title={tr("Let's Encrypt certificate")} footer={<>
        {data?.acmeDomain && <Button className="mr-auto" variant="danger" onClick={() => saveAcme.mutate({ domain: "", email: "" })}>{tr("Disable")}</Button>}
        <Button onClick={() => setAcme(null)}>{tr("Cancel")}</Button>
        <Button variant="primary" disabled={!acme?.domain} loading={saveAcme.isPending} onClick={() => saveAcme.mutate(acme!)}>{tr("Enable")}</Button>
      </>}>
        {acme && (
          <div className="space-y-4">
            <Notice>{tr("Prerequisites: a domain name (e.g. nas.mydomain.com) pointing to your public IP address, and the")}{" "}<b>{tr("port 80")}</b> {" "}{tr("on your router forwarded to the NAS. The certificate is obtained and then renewed automatically.")}</Notice>
            <Field label={tr("Domain name")}><Input value={acme.domain} onChange={(e) => setAcme({ ...acme, domain: e.target.value })} /></Field>
            <Field label={tr("Email (expiration notice)")}><Input type="email" value={acme.email} onChange={(e) => setAcme({ ...acme, email: e.target.value })} /></Field>
            <ErrorBox error={saveAcme.error} />
          </div>
        )}
      </Modal>
    </Card>
  );
}

function MetricsCard({ enabled }: { enabled: boolean }) {
  const { toast } = useUI();
  const [token, setToken] = useState("");
  const toggle = useAction((on: boolean) => api.put<{ token: string }>("/api/metrics", { enabled: on }), { onDone: (r) => setToken(r.token) });
  const url = `${location.protocol}//${location.host}/metrics`;
  const yaml = `scrape_configs:\n  - job_name: strataos\n    scheme: ${location.protocol.replace(":", "")}\n    tls_config: { insecure_skip_verify: true }\n    authorization: { credentials: ${token || "<jeton>"} }\n    static_configs:\n      - targets: ["${location.host}"]`;
  const grafana = async () => {
    const d = await api.get<unknown>("/api/metrics/grafana");
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([JSON.stringify(d, null, 2)], { type: "application/json" }));
    a.download = "strataos-grafana.json";
    a.click();
  };
  return (
    <Card title={<span className="flex items-center gap-2"><Activity size={16} />{tr("Monitoring (Prometheus, Grafana)")}</span>}>
      <div className="space-y-4">
        <Switch checked={enabled || !!token} onChange={(v) => toggle.mutate(v)} label={tr("Publish metrics")} hint={tr("CPU, memory, network, disks, volumes, RAID/ZFS/SMART health, backups, UPS, security, in Prometheus format at {0}", url)} />
        {token && (
          <div className="space-y-2">
            <Notice tone="warn">{tr("Access token (shown only once): copy it into the Prometheus configuration.")}</Notice>
            <div className="flex gap-2"><Input readOnly className="mono" value={token} onFocus={(e) => e.target.select()} /><Button onClick={() => { navigator.clipboard?.writeText(token); toast(tr("Token copied")); }}>{tr("Copy")}</Button></div>
          </div>
        )}
        {(enabled || token) && <>
          <pre className="mono overflow-auto rounded-lg bg-panel-2 p-3 text-[11.5px]">{yaml}</pre>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" icon={<Download size={14} />} onClick={grafana}>{tr("Grafana dashboard")}</Button>
            {enabled && !token && <Button size="sm" onClick={() => toggle.mutate(true)}>{tr("New token")}</Button>}
          </div>
          <p className="text-muted text-[12px]">{tr("Prometheus and Grafana are installed from the App Center; import the dashboard into Grafana, then choose the Prometheus data source.")}</p>
        </>}
        <ErrorBox error={toggle.error} />
      </div>
    </Card>
  );
}

// Components whose key is not self-explanatory.
const componentLabel: Record<string, string> = { lxc: tr("Containers (LXC)"), docpreview: tr("Document previews (≈ 400 MB)"), photos: tr("Media tools (ffmpeg, libvips)") };

export default function SettingsPage() {
  const { data, isLoading, error } = useQuery({ queryKey: ["settings"], queryFn: () => api.get<Settings>("/api/settings") });
  const info = useQuery({ queryKey: ["sysinfo"], queryFn: () => api.get<SystemInfo>("/api/system/info") });
  const tz = useQuery({ queryKey: ["timezones"], queryFn: () => api.get<string[]>("/api/system/timezones"), staleTime: Infinity });
  const comps = useQuery({ queryKey: ["components"], queryFn: () => api.get<Record<string, boolean>>("/api/system/components") });
  const [s, setS] = useState<Settings | null>(null);
  const [zone, setZone] = useState("");
  useEffect(() => { if (data) setS(data); }, [data]);
  useEffect(() => { if (info.data) setZone(info.data.timezone); }, [info.data]);
  const save = useAction(() => api.put("/api/settings", s), { success: tr("Settings saved") });
  const test = useAction(() => api.post<{ sent: string[] }>("/api/settings/test-notification"), { success: (r) => tr("Notification sent ({0})", r.sent.join(", ")) });
  const setTime = useAction(() => api.put("/api/system/time", { timezone: zone, ntp: true }), { success: tr("Time zone updated") });
  const install = useAction((c: string) => api.post(`/api/system/components/${c}/install`));
  const [uninstall, setUninstall] = useState("");
  const report = useAction(() => api.post<{ report: string; warning?: string }>("/api/settings/report"), { success: (r) => r.warning ?? tr("Report sent") });
  if (isLoading || !s) return <Spinner />;
  const set = <K extends keyof Settings>(k: K, v: Settings[K]) => setS({ ...s, [k]: v });
  return (
    <div>
      <PageHeader title={tr("Settings")} subtitle={tr("General NAS configuration")} icon={<Cog size={20} />} actions={<Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>} />
      <ErrorBox error={error} />
      <div className="grid gap-5 xl:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Share2 size={16} /> {" "}{tr("File sharing (SMB)")}</span>}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Workgroup")}><Input value={s.workgroup} onChange={(e) => set("workgroup", e.target.value)} /></Field>
            <Field label={tr("Server description")}><Input value={s.serverString} onChange={(e) => set("serverString", e.target.value)} /></Field>
            <Field label={tr("Minimum protocol")} hint={tr("SMB1 is disabled for security reasons.")}>
              <Select value={s.smbMinProtocol} onChange={(e) => set("smbMinProtocol", e.target.value)}>
                <option value="SMB2">SMB2</option><option value="SMB2_10">{tr("SMB 2.1")}</option><option value="SMB3">SMB3</option><option value="SMB3_11">{tr("SMB 3.1.1")}</option>
              </Select>
            </Field>
            <Field label={tr("Empty the recycle bin after (days)")} hint={tr("Files deleted via SMB, purged every night at 3:30 AM. 0 = never.")}><Input type="number" min={0} value={s.trashDays} onChange={(e) => set("trashDays", Math.max(0, +e.target.value))} /></Field>
          </div>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Shield size={16} /> {" "}{tr("Security")}</span>}>
          <Field label={tr("Session duration (hours)")}><Input type="number" min={1} max={720} value={s.sessionHours} onChange={(e) => set("sessionHours", +e.target.value)} /></Field>
          <p className="mt-3 text-[12.5px] text-muted">{tr("The interface is available over HTTPS on port 7443 (self-signed certificate generated on first boot). Do not expose it directly to the Internet: use a VPN.")}</p>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Bell size={16} /> {" "}{tr("Notifications")}</span>} actions={<Button size="sm" loading={test.isPending} onClick={() => test.mutate()}>{tr("Test")}</Button>}>
          <div className="grid gap-4 sm:grid-cols-2">
            <TelegramFields s={s} set={set} />
            <Field label={tr("Push notifications (ntfy)")} className="sm:col-span-2" hint={tr("Install the ntfy app (Android, iPhone) and subscribe to this topic. A long, unguessable name protects your alerts on the public ntfy.sh server.")}>
              <div className="flex gap-2">
                <Input className="mono" value={s.ntfyUrl ?? ""} onChange={(e) => set("ntfyUrl", e.target.value)} placeholder="https://ntfy.sh/…" />
                <Button onClick={() => set("ntfyUrl", `https://ntfy.sh/strata-${Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(36).padStart(2, "0")).join("").slice(0, 20)}`)}>{tr("Generate")}</Button>
              </div>
            </Field>
            {s.ntfyUrl && <Field label={tr("ntfy access token (optional)")} hint={tr("For a protected ntfy server")}><Input type="password" value={s.ntfyToken ?? ""} onChange={(e) => set("ntfyToken", e.target.value)} /></Field>}
            <Field label={tr("Webhook (Discord, Slack, ntfy, Gotify…)")} className="sm:col-span-2" hint={tr("Receives a JSON POST (title, message, text, content).")}><Input value={s.webhookUrl} onChange={(e) => set("webhookUrl", e.target.value)} /></Field>
            <Field label={tr("SMTP server")}><Input value={s.smtpHost} onChange={(e) => set("smtpHost", e.target.value)} /></Field>
            <Field label={tr("Port")}><Input type="number" value={s.smtpPort} onChange={(e) => set("smtpPort", +e.target.value)} /></Field>
            <Field label={tr("Encryption")}>
              <Select value={s.smtpSecurity || "auto"} onChange={(e) => set("smtpSecurity", e.target.value)}>
                <option value="auto">{tr("Automatic (TLS on 465, otherwise STARTTLS)")}</option>
                <option value="tls">{tr("Direct TLS (SSL)")}</option>
                <option value="starttls">{tr("STARTTLS required")}</option>
                <option value="none">{tr("None (local network only)")}</option>
              </Select>
            </Field>
            <Field label={tr("SMTP user")}><Input value={s.smtpUser} onChange={(e) => set("smtpUser", e.target.value)} /></Field>
            <Field label={tr("SMTP password")}><Input type="password" value={s.smtpPassword} onChange={(e) => set("smtpPassword", e.target.value)} /></Field>
            <Field label={tr("Sender")}><Input value={s.smtpFrom} onChange={(e) => set("smtpFrom", e.target.value)} /></Field>
            <Field label={tr("Alert recipient")}><Input type="email" value={s.notifyEmail} onChange={(e) => set("notifyEmail", e.target.value)} /></Field>
            <Field label={tr("Notification language")} hint={tr("Alerts and monthly report, on all channels")}>
              <Select value={s.notifyLang || "en"} onChange={(e) => set("notifyLang", e.target.value)}>
                {languages.map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
              </Select>
            </Field>
          </div>
          <NotifyDelivery s={s} set={set} />
          <div className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-line p-4">
            <div className="flex-1"><Switch checked={!!s.monthlyReport} onChange={(v) => set("monthlyReport", v)} label={tr("Monthly report")} hint={tr("On the 1st of each month at 8 AM: space, disk health, backups, antivirus, updates")} /></div>
            <Button size="sm" loading={report.isPending} onClick={() => report.mutate()}>{tr("Send now")}</Button>
          </div>
          {report.data && <pre className="mono mt-3 max-h-72 overflow-auto whitespace-pre-wrap rounded-lg bg-panel-2 p-3 text-[12px]">{report.data.report}</pre>}
          <p className="mt-3 text-[12.5px] text-muted">{tr("Alerts sent: degraded RAID, ZFS pool error, failing or weakening disk (SMART), volume 90% full, virus found, failed task, power outage.")}</p>
        </Card>
        <MetricsCard enabled={!!data?.metricsToken} />
        <Card title={<span className="flex items-center gap-2"><Clock size={16} /> {" "}{tr("Date and time")}</span>}>
          <div className="flex items-end gap-2">
            <Field label={tr("Time zone")} className="flex-1">
              <Select value={zone} onChange={(e) => setZone(e.target.value)}>{tz.data?.map((z) => <option key={z}>{z}</option>)}</Select>
            </Field>
            <Button loading={setTime.isPending} onClick={() => setTime.mutate()}>{tr("Apply")}</Button>
          </div>
          <p className="mt-3 text-[12.5px] text-muted">{tr("NTP synchronization enabled automatically.")}</p>
        </Card>
        <ApplianceConsoleCard />
        <CertificatesCard />
        <ConfigBackupCard />
        <OfficeSettingsCard />
        <Card title={<span className="flex items-center gap-2"><Wrench size={16} /> {" "}{tr("Components")}</span>} className="xl:col-span-2">
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
            {Object.entries(comps.data ?? {}).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between rounded-xl bg-panel-2 px-3 py-2.5">
                <span className="font-medium capitalize">{componentLabel[k] ?? k}</span>
                {v ? <span className="flex items-center gap-1.5"><Badge tone="ok" dot>{tr("Installed")}</Badge>{uninstallable.includes(k) && <Button size="sm" variant="ghost" className="text-danger" onClick={() => setUninstall(k)}>{tr("Uninstall")}</Button>}</span> : (k === "zfs" || k === "docker" || k === "docpreview" || k === "photos") ? <Button size="sm" loading={install.isPending} onClick={() => install.mutate(k)}>{tr("Install")}</Button> : <Badge tone="warn">{tr("Absent")}</Badge>}
              </div>
            ))}
          </div>
          {uninstall && <ComponentUninstallModal id={uninstall} label={componentLabel[uninstall] ?? uninstall} onClose={() => setUninstall("")} />}
        </Card>
      </div>
    </div>
  );
}

function TelegramFields({ s, set }: { s: Settings; set: <K extends keyof Settings>(k: K, v: Settings[K]) => void }) {
  const chats = useAction(() => api.post<{ id: string; name: string; type: string }[]>("/api/settings/telegram-chats", { token: s.telegramToken }));
  return (
    <div className="space-y-3 rounded-xl border border-line p-4 sm:col-span-2">
      <div className="font-medium text-[13px]">{tr("Telegram bot")}</div>
      <div className="text-muted text-[12.5px]">{tr("1. In Telegram, message @BotFather, send /newbot and copy the token. 2. Send a message to your bot (or add it to a group). 3. Click \"Find the chat\".")}</div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={tr("Bot token")}><Input type="password" autoComplete="off" value={s.telegramToken ?? ""} onChange={(e) => set("telegramToken", e.target.value)} placeholder="123456789:AA…" /></Field>
        <Field label={tr("Chat ID")}>
          <div className="flex gap-2">
            <Input className="mono" value={s.telegramChatId ?? ""} onChange={(e) => set("telegramChatId", e.target.value)} />
            <Button disabled={!s.telegramToken} loading={chats.isPending} onClick={() => chats.mutate()}>{tr("Find the chat")}</Button>
          </div>
        </Field>
      </div>
      {chats.data && (chats.data.length === 0 ? <Notice tone="warn">{tr("The bot has not received any message: send it a message first, then try again.")}</Notice> : (
        <div className="flex flex-wrap gap-2">
          {chats.data.map((c) => <Button key={c.id} size="sm" variant={s.telegramChatId === c.id ? "primary" : "secondary"} onClick={() => set("telegramChatId", c.id)}>{c.name || c.id} <span className="text-muted">({c.type})</span></Button>)}
        </div>
      ))}
    </div>
  );
}

// Per-channel switch and minimum level, and quiet hours.
function NotifyDelivery({ s, set }: { s: Settings; set: <K extends keyof Settings>(k: K, v: Settings[K]) => void }) {
  const channels: { id: NotifyChannelId; name: string; configured: boolean }[] = [
    { id: "email", name: tr("Email"), configured: !!s.smtpHost && !!s.notifyEmail },
    { id: "telegram", name: "Telegram", configured: !!s.telegramToken && !!s.telegramChatId },
    { id: "ntfy", name: "ntfy", configured: !!s.ntfyUrl },
    { id: "webhook", name: tr("Webhook"), configured: !!s.webhookUrl },
    // Paired phones of administrators (account menu → Mobile devices); always listed.
    { id: "mobile", name: tr("StrataOS app (phones)"), configured: true },
  ];
  const prefs = s.notifyChannels ?? {};
  const setChannel = (id: NotifyChannelId, v: NotifyChannel) => set("notifyChannels", { ...prefs, [id]: { ...prefs[id], ...v } });
  const q = s.quietHours ?? {};
  const setQuiet = (v: Partial<typeof q>) => set("quietHours", { start: "22:00", end: "07:00", ...q, ...v });
  return (
    <div className="mt-4 space-y-4 rounded-xl border border-line p-4">
      <div className="font-medium text-[13px]">{tr("Delivery")}</div>
      <div className="space-y-2">
        {channels.map((c) => {
          const p = prefs[c.id] ?? {};
          return (
            <div key={c.id} className="flex flex-wrap items-center gap-3 rounded-lg bg-panel-2 px-3 py-2">
              <div className="min-w-32 flex-1"><Switch checked={!p.disabled} onChange={(v) => setChannel(c.id, { disabled: !v })} label={c.name} hint={c.configured ? undefined : tr("Not configured")} /></div>
              <Select className="w-56" value={p.minLevel ?? ""} disabled={p.disabled} onChange={(e) => setChannel(c.id, { minLevel: e.target.value as NotifyLevel })}>
                <option value="">{tr("Everything (information and above)")}</option>
                <option value="warning">{tr("Warnings and errors")}</option>
                <option value="error">{tr("Errors only")}</option>
              </Select>
            </div>
          );
        })}
      </div>
      <div className="space-y-3 border-t border-line pt-4">
        <Switch checked={!!q.enabled} onChange={(v) => setQuiet({ enabled: v })} label={tr("Quiet hours")} hint={tr("During this time only errors are sent. Other notifications are dropped, not sent later (they stay in the event log).")} />
        {q.enabled && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={tr("From")}><Input type="time" value={q.start ?? ""} onChange={(e) => setQuiet({ start: e.target.value })} /></Field>
            <Field label={tr("To")} hint={tr("NAS local time; may end the next day")}><Input type="time" value={q.end ?? ""} onChange={(e) => setQuiet({ end: e.target.value })} /></Field>
            <div className="sm:col-span-2"><Switch checked={!!q.silenceAll} onChange={(v) => setQuiet({ silenceAll: v })} label={tr("Silence everything, errors included")} /></div>
          </div>
        )}
      </div>
      <p className="text-[12px] text-muted">{tr("The Test button and the monthly report ignore levels and quiet hours, but not a disabled channel.")}</p>
    </div>
  );
}
