import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bell, Clock, Settings as Cog, Lock, Share2, Shield, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Settings, SystemInfo } from "../../lib/types";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, PageHeader, Select, Spinner, Switch, useUI } from "../../components/ui";
import { tr, locale } from "../../lib/i18n";

type CertInfo = { source: "self-signed" | "custom" | "letsencrypt"; subject: string; issuer: string; dnsNames: string[] | null; notAfter: string; active: boolean };

function CertificatesCard() {
  const { confirm } = useUI();
  const { data } = useQuery({ queryKey: ["certs"], queryFn: () => api.get<{ certificates: CertInfo[]; acmeDomain: string; acmeEmail: string }>("/api/system/certificates") });
  const [imp, setImp] = useState<{ cert: string; key: string } | null>(null);
  const [acme, setAcme] = useState<{ domain: string; email: string } | null>(null);
  const upload = useAction(() => api.put("/api/system/certificates/custom", imp), { success: tr("Certificat importé (actif immédiatement)"), onDone: () => setImp(null) });
  const remove = useAction(() => api.del("/api/system/certificates/custom"), { success: tr("Certificat importé supprimé") });
  const saveAcme = useAction((v: { domain: string; email: string }) => api.put("/api/system/certificates/acme", v), { success: tr("Let's Encrypt désactivé"), onDone: () => setAcme(null) });
  const label = { "self-signed": "Auto-signé", custom: "Importé", letsencrypt: "Let's Encrypt" };
  const readFile = (f: File | undefined, k: "cert" | "key") => f?.text().then((t) => setImp((p) => ({ cert: "", key: "", ...p, [k]: t })));
  return (
    <Card title={<span className="flex items-center gap-2"><Lock size={16} /> {" "}{tr("Certificat HTTPS")}</span>} className="xl:col-span-2" actions={<>
      <Button size="sm" onClick={() => setImp({ cert: "", key: "" })}>{tr("Importer un certificat")}</Button>
      <Button size="sm" onClick={() => setAcme({ domain: data?.acmeDomain ?? "", email: data?.acmeEmail ?? "" })}>{tr("Let's Encrypt")}</Button>
    </>}>
      <div className="space-y-2">
        {data?.certificates.map((c) => (
          <div key={c.source} className="flex flex-wrap items-center gap-3 rounded-xl bg-panel-2 px-4 py-2.5 text-[13px]">
            <Badge tone={c.active ? "ok" : "neutral"}>{label[c.source]}{c.active ? tr(" · utilisé") : ""}</Badge>
            <span className="font-medium">{c.subject}</span>
            <span className="text-muted">{tr("émis par")}{" "}{c.issuer} {" "}{tr("· expire le")}{" "}{new Date(c.notAfter).toLocaleDateString(locale)}</span>
            {c.dnsNames && c.dnsNames.length > 0 && <span className="mono text-muted text-[11.5px]">{c.dnsNames.join(", ")}</span>}
            <div className="flex-1" />
            {c.source === "custom" && <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: tr("Supprimer le certificat importé ?"), text: tr("Le certificat auto-signé sera de nouveau utilisé."), danger: true })) remove.mutate(); }}>{tr("Supprimer")}</Button>}
          </div>
        ))}
        {data?.acmeDomain && <div className="text-[12.5px] text-muted">{tr("Let's Encrypt actif pour")}{" "}<b>{data.acmeDomain}</b> {" "}{tr("(utilisé quand le NAS est ouvert par ce nom ; renouvellement automatique).")}</div>}
      </div>
      <Modal open={!!imp} onClose={() => setImp(null)} title={tr("Importer un certificat")} wide footer={<>
        <Button onClick={() => setImp(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!imp?.cert || !imp?.key} loading={upload.isPending} onClick={() => upload.mutate()}>{tr("Importer")}</Button>
      </>}>
        {imp && (
          <div className="space-y-4">
            <Field label={tr("Certificat (PEM, avec la chaîne intermédiaire)")} hint={<input type="file" accept=".pem,.crt,.cer" onChange={(e) => readFile(e.target.files?.[0], "cert")} />}><textarea className="input mono h-32" value={imp.cert} onChange={(e) => setImp({ ...imp, cert: e.target.value })} placeholder={tr("-----BEGIN CERTIFICATE-----")} /></Field>
            <Field label={tr("Clé privée (PEM)")} hint={<input type="file" accept=".pem,.key" onChange={(e) => readFile(e.target.files?.[0], "key")} />}><textarea className="input mono h-32" value={imp.key} onChange={(e) => setImp({ ...imp, key: e.target.value })} placeholder={tr("-----BEGIN PRIVATE KEY-----")} /></Field>
            <ErrorBox error={upload.error} />
          </div>
        )}
      </Modal>
      <Modal open={!!acme} onClose={() => setAcme(null)} title={tr("Certificat Let's Encrypt")} footer={<>
        {data?.acmeDomain && <Button className="mr-auto" variant="danger" onClick={() => saveAcme.mutate({ domain: "", email: "" })}>{tr("Désactiver")}</Button>}
        <Button onClick={() => setAcme(null)}>{tr("Annuler")}</Button>
        <Button variant="primary" disabled={!acme?.domain} loading={saveAcme.isPending} onClick={() => saveAcme.mutate(acme!)}>{tr("Activer")}</Button>
      </>}>
        {acme && (
          <div className="space-y-4">
            <Notice>{tr("Prérequis : un nom de domaine (ex. nas.mondomaine.fr) qui pointe vers votre adresse IP publique, et le")}{" "}<b>{tr("port 80")}</b> {" "}{tr("de votre box redirigé vers le NAS. Le certificat est obtenu puis renouvelé automatiquement.")}</Notice>
            <Field label={tr("Nom de domaine")}><Input value={acme.domain} onChange={(e) => setAcme({ ...acme, domain: e.target.value })} /></Field>
            <Field label={tr("E-mail (avis d'expiration)")}><Input type="email" value={acme.email} onChange={(e) => setAcme({ ...acme, email: e.target.value })} /></Field>
            <ErrorBox error={saveAcme.error} />
          </div>
        )}
      </Modal>
    </Card>
  );
}

export default function SettingsPage() {
  const { data, isLoading, error } = useQuery({ queryKey: ["settings"], queryFn: () => api.get<Settings>("/api/settings") });
  const info = useQuery({ queryKey: ["sysinfo"], queryFn: () => api.get<SystemInfo>("/api/system/info") });
  const tz = useQuery({ queryKey: ["timezones"], queryFn: () => api.get<string[]>("/api/system/timezones"), staleTime: Infinity });
  const comps = useQuery({ queryKey: ["components"], queryFn: () => api.get<Record<string, boolean>>("/api/system/components") });
  const [s, setS] = useState<Settings | null>(null);
  const [zone, setZone] = useState("");
  useEffect(() => { if (data) setS(data); }, [data]);
  useEffect(() => { if (info.data) setZone(info.data.timezone); }, [info.data]);
  const save = useAction(() => api.put("/api/settings", s), { success: tr("Paramètres enregistrés") });
  const test = useAction(() => api.post("/api/settings/test-notification"), { success: tr("Notification envoyée") });
  const setTime = useAction(() => api.put("/api/system/time", { timezone: zone, ntp: true }), { success: tr("Fuseau horaire mis à jour") });
  const install = useAction((c: string) => api.post(`/api/system/components/${c}/install`));
  const report = useAction(() => api.post<{ report: string; warning?: string }>("/api/settings/report"), { success: (r) => r.warning ?? tr("Rapport envoyé") });
  if (isLoading || !s) return <Spinner />;
  const set = <K extends keyof Settings>(k: K, v: Settings[K]) => setS({ ...s, [k]: v });
  return (
    <div>
      <PageHeader title={tr("Paramètres")} subtitle={tr("Configuration générale du NAS")} icon={<Cog size={20} />} actions={<Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Enregistrer")}</Button>} />
      <ErrorBox error={error} />
      <div className="grid gap-5 xl:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Share2 size={16} /> {" "}{tr("Partage de fichiers (SMB)")}</span>}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Groupe de travail")}><Input value={s.workgroup} onChange={(e) => set("workgroup", e.target.value)} /></Field>
            <Field label={tr("Description du serveur")}><Input value={s.serverString} onChange={(e) => set("serverString", e.target.value)} /></Field>
            <Field label={tr("Protocole minimum")} hint={tr("SMB1 est désactivé pour des raisons de sécurité.")}>
              <Select value={s.smbMinProtocol} onChange={(e) => set("smbMinProtocol", e.target.value)}>
                <option value="SMB2">SMB2</option><option value="SMB2_10">{tr("SMB 2.1")}</option><option value="SMB3">SMB3</option><option value="SMB3_11">{tr("SMB 3.1.1")}</option>
              </Select>
            </Field>
            <Field label={tr("Vider la corbeille réseau après (jours)")} hint={tr("Fichiers supprimés via SMB, purgés chaque nuit à 3 h 30. 0 = jamais.")}><Input type="number" min={0} value={s.trashDays} onChange={(e) => set("trashDays", Math.max(0, +e.target.value))} /></Field>
          </div>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Shield size={16} /> {" "}{tr("Sécurité")}</span>}>
          <Field label={tr("Durée de session (heures)")}><Input type="number" min={1} max={720} value={s.sessionHours} onChange={(e) => set("sessionHours", +e.target.value)} /></Field>
          <p className="mt-3 text-[12.5px] text-muted">{tr("L'interface est disponible en HTTPS sur le port 7443 (certificat auto-signé généré au premier démarrage). Ne l'exposez pas directement sur Internet : utilisez un VPN.")}</p>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Bell size={16} /> {" "}{tr("Notifications")}</span>} actions={<Button size="sm" loading={test.isPending} onClick={() => test.mutate()}>{tr("Tester")}</Button>}>
          <div className="grid gap-4 sm:grid-cols-2">
            <TelegramFields s={s} set={set} />
            <Field label={tr("Notifications push (ntfy)")} className="sm:col-span-2" hint={tr("Installez l'application ntfy (Android, iPhone) et abonnez-vous à ce sujet. Un nom long et impossible à deviner protège vos alertes sur le serveur public ntfy.sh.")}>
              <div className="flex gap-2">
                <Input className="mono" value={s.ntfyUrl ?? ""} onChange={(e) => set("ntfyUrl", e.target.value)} placeholder="https://ntfy.sh/…" />
                <Button onClick={() => set("ntfyUrl", `https://ntfy.sh/strata-${Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(36).padStart(2, "0")).join("").slice(0, 20)}`)}>{tr("Générer")}</Button>
              </div>
            </Field>
            {s.ntfyUrl && <Field label={tr("Jeton d'accès ntfy (optionnel)")} hint={tr("Pour un serveur ntfy protégé")}><Input type="password" value={s.ntfyToken ?? ""} onChange={(e) => set("ntfyToken", e.target.value)} /></Field>}
            <Field label={tr("Webhook (Discord, Slack, ntfy, Gotify…)")} className="sm:col-span-2" hint={tr("Reçoit un POST JSON (title, message, text, content).")}><Input value={s.webhookUrl} onChange={(e) => set("webhookUrl", e.target.value)} /></Field>
            <Field label={tr("Serveur SMTP")}><Input value={s.smtpHost} onChange={(e) => set("smtpHost", e.target.value)} /></Field>
            <Field label={tr("Port")}><Input type="number" value={s.smtpPort} onChange={(e) => set("smtpPort", +e.target.value)} /></Field>
            <Field label={tr("Chiffrement")}>
              <Select value={s.smtpSecurity || "auto"} onChange={(e) => set("smtpSecurity", e.target.value)}>
                <option value="auto">{tr("Automatique (TLS sur 465, sinon STARTTLS)")}</option>
                <option value="tls">{tr("TLS direct (SSL)")}</option>
                <option value="starttls">{tr("STARTTLS obligatoire")}</option>
                <option value="none">{tr("Aucun (réseau local uniquement)")}</option>
              </Select>
            </Field>
            <Field label={tr("Utilisateur SMTP")}><Input value={s.smtpUser} onChange={(e) => set("smtpUser", e.target.value)} /></Field>
            <Field label={tr("Mot de passe SMTP")}><Input type="password" value={s.smtpPassword} onChange={(e) => set("smtpPassword", e.target.value)} /></Field>
            <Field label={tr("Expéditeur")}><Input value={s.smtpFrom} onChange={(e) => set("smtpFrom", e.target.value)} /></Field>
            <Field label={tr("Destinataire des alertes")}><Input type="email" value={s.notifyEmail} onChange={(e) => set("notifyEmail", e.target.value)} /></Field>
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-3 rounded-xl border border-line p-4">
            <div className="flex-1"><Switch checked={!!s.monthlyReport} onChange={(v) => set("monthlyReport", v)} label={tr("Rapport mensuel")} hint={tr("Le 1er de chaque mois à 8 h : espace, santé des disques, sauvegardes, antivirus, mises à jour")} /></div>
            <Button size="sm" loading={report.isPending} onClick={() => report.mutate()}>{tr("Envoyer maintenant")}</Button>
          </div>
          {report.data && <pre className="mono mt-3 max-h-72 overflow-auto whitespace-pre-wrap rounded-lg bg-panel-2 p-3 text-[12px]">{report.data.report}</pre>}
          <p className="mt-3 text-[12.5px] text-muted">{tr("Alertes envoyées : RAID dégradé, pool ZFS en erreur, disque SMART défaillant ou qui faiblit, volume plein à 90 %, virus détecté, échec d'une tâche, coupure de courant.")}</p>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Clock size={16} /> {" "}{tr("Date et heure")}</span>}>
          <div className="flex items-end gap-2">
            <Field label={tr("Fuseau horaire")} className="flex-1">
              <Select value={zone} onChange={(e) => setZone(e.target.value)}>{tz.data?.map((z) => <option key={z}>{z}</option>)}</Select>
            </Field>
            <Button loading={setTime.isPending} onClick={() => setTime.mutate()}>{tr("Appliquer")}</Button>
          </div>
          <p className="mt-3 text-[12.5px] text-muted">{tr("Synchronisation NTP activée automatiquement.")}</p>
        </Card>
        <CertificatesCard />
        <Card title={<span className="flex items-center gap-2"><Wrench size={16} /> {" "}{tr("Composants")}</span>} className="xl:col-span-2">
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
            {Object.entries(comps.data ?? {}).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between rounded-xl bg-panel-2 px-3 py-2.5">
                <span className="font-medium capitalize">{k}</span>
                {v ? <Badge tone="ok" dot>{tr("Installé")}</Badge> : (k === "zfs" || k === "docker") ? <Button size="sm" loading={install.isPending} onClick={() => install.mutate(k)}>{tr("Installer")}</Button> : <Badge tone="warn">{tr("Absent")}</Badge>}
              </div>
            ))}
          </div>
        </Card>
      </div>
    </div>
  );
}

function TelegramFields({ s, set }: { s: Settings; set: <K extends keyof Settings>(k: K, v: Settings[K]) => void }) {
  const chats = useAction(() => api.post<{ id: string; name: string; type: string }[]>("/api/settings/telegram-chats", { token: s.telegramToken }));
  return (
    <div className="space-y-3 rounded-xl border border-line p-4 sm:col-span-2">
      <div className="font-medium text-[13px]">{tr("Bot Telegram")}</div>
      <div className="text-muted text-[12.5px]">{tr("1. Dans Telegram, écrivez à @BotFather, envoyez /newbot et copiez le jeton. 2. Envoyez un message à votre bot (ou ajoutez-le à un groupe). 3. Cliquez sur « Trouver la conversation ».")}</div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={tr("Jeton du bot")}><Input type="password" autoComplete="off" value={s.telegramToken ?? ""} onChange={(e) => set("telegramToken", e.target.value)} placeholder="123456789:AA…" /></Field>
        <Field label={tr("Identifiant de la conversation")}>
          <div className="flex gap-2">
            <Input className="mono" value={s.telegramChatId ?? ""} onChange={(e) => set("telegramChatId", e.target.value)} />
            <Button disabled={!s.telegramToken} loading={chats.isPending} onClick={() => chats.mutate()}>{tr("Trouver la conversation")}</Button>
          </div>
        </Field>
      </div>
      {chats.data && (chats.data.length === 0 ? <Notice tone="warn">{tr("Aucun message reçu par le bot : envoyez-lui d'abord un message, puis réessayez.")}</Notice> : (
        <div className="flex flex-wrap gap-2">
          {chats.data.map((c) => <Button key={c.id} size="sm" variant={s.telegramChatId === c.id ? "primary" : "secondary"} onClick={() => set("telegramChatId", c.id)}>{c.name || c.id} <span className="text-muted">({c.type})</span></Button>)}
        </div>
      ))}
    </div>
  );
}
