import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bell, Clock, Settings as Cog, Lock, Share2, Shield, Wrench } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import type { Settings, SystemInfo } from "../../lib/types";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, PageHeader, Select, Spinner, useUI } from "../../components/ui";

type CertInfo = { source: "self-signed" | "custom" | "letsencrypt"; subject: string; issuer: string; dnsNames: string[] | null; notAfter: string; active: boolean };

function CertificatesCard() {
  const { confirm } = useUI();
  const { data } = useQuery({ queryKey: ["certs"], queryFn: () => api.get<{ certificates: CertInfo[]; acmeDomain: string; acmeEmail: string }>("/api/system/certificates") });
  const [imp, setImp] = useState<{ cert: string; key: string } | null>(null);
  const [acme, setAcme] = useState<{ domain: string; email: string } | null>(null);
  const upload = useAction(() => api.put("/api/system/certificates/custom", imp), { success: "Certificat importé (actif immédiatement)", onDone: () => setImp(null) });
  const remove = useAction(() => api.del("/api/system/certificates/custom"), { success: "Certificat importé supprimé" });
  const saveAcme = useAction((v: { domain: string; email: string }) => api.put("/api/system/certificates/acme", v), { success: "Let's Encrypt désactivé", onDone: () => setAcme(null) });
  const label = { "self-signed": "Auto-signé", custom: "Importé", letsencrypt: "Let's Encrypt" };
  const readFile = (f: File | undefined, k: "cert" | "key") => f?.text().then((t) => setImp((p) => ({ cert: "", key: "", ...p, [k]: t })));
  return (
    <Card title={<span className="flex items-center gap-2"><Lock size={16} /> Certificat HTTPS</span>} className="xl:col-span-2" actions={<>
      <Button size="sm" onClick={() => setImp({ cert: "", key: "" })}>Importer un certificat</Button>
      <Button size="sm" onClick={() => setAcme({ domain: data?.acmeDomain ?? "", email: data?.acmeEmail ?? "" })}>Let's Encrypt</Button>
    </>}>
      <div className="space-y-2">
        {data?.certificates.map((c) => (
          <div key={c.source} className="flex flex-wrap items-center gap-3 rounded-xl bg-panel-2 px-4 py-2.5 text-[13px]">
            <Badge tone={c.active ? "ok" : "neutral"}>{label[c.source]}{c.active ? " · utilisé" : ""}</Badge>
            <span className="font-medium">{c.subject}</span>
            <span className="text-muted">émis par {c.issuer} · expire le {new Date(c.notAfter).toLocaleDateString("fr-FR")}</span>
            {c.dnsNames && c.dnsNames.length > 0 && <span className="mono text-muted text-[11.5px]">{c.dnsNames.join(", ")}</span>}
            <div className="flex-1" />
            {c.source === "custom" && <Button size="sm" variant="ghost" className="text-danger" onClick={async () => { if (await confirm({ title: "Supprimer le certificat importé ?", text: "Le certificat auto-signé sera de nouveau utilisé.", danger: true })) remove.mutate(); }}>Supprimer</Button>}
          </div>
        ))}
        {data?.acmeDomain && <div className="text-[12.5px] text-muted">Let's Encrypt actif pour <b>{data.acmeDomain}</b> (utilisé quand le NAS est ouvert par ce nom ; renouvellement automatique).</div>}
      </div>
      <Modal open={!!imp} onClose={() => setImp(null)} title="Importer un certificat" wide footer={<>
        <Button onClick={() => setImp(null)}>Annuler</Button>
        <Button variant="primary" disabled={!imp?.cert || !imp?.key} loading={upload.isPending} onClick={() => upload.mutate()}>Importer</Button>
      </>}>
        {imp && (
          <div className="space-y-4">
            <Field label="Certificat (PEM, avec la chaîne intermédiaire)" hint={<input type="file" accept=".pem,.crt,.cer" onChange={(e) => readFile(e.target.files?.[0], "cert")} />}><textarea className="input mono h-32" value={imp.cert} onChange={(e) => setImp({ ...imp, cert: e.target.value })} placeholder="-----BEGIN CERTIFICATE-----" /></Field>
            <Field label="Clé privée (PEM)" hint={<input type="file" accept=".pem,.key" onChange={(e) => readFile(e.target.files?.[0], "key")} />}><textarea className="input mono h-32" value={imp.key} onChange={(e) => setImp({ ...imp, key: e.target.value })} placeholder="-----BEGIN PRIVATE KEY-----" /></Field>
            <ErrorBox error={upload.error} />
          </div>
        )}
      </Modal>
      <Modal open={!!acme} onClose={() => setAcme(null)} title="Certificat Let's Encrypt" footer={<>
        {data?.acmeDomain && <Button className="mr-auto" variant="danger" onClick={() => saveAcme.mutate({ domain: "", email: "" })}>Désactiver</Button>}
        <Button onClick={() => setAcme(null)}>Annuler</Button>
        <Button variant="primary" disabled={!acme?.domain} loading={saveAcme.isPending} onClick={() => saveAcme.mutate(acme!)}>Activer</Button>
      </>}>
        {acme && (
          <div className="space-y-4">
            <Notice>Prérequis : un nom de domaine (ex. nas.mondomaine.fr) qui pointe vers votre adresse IP publique, et le <b>port 80</b> de votre box redirigé vers le NAS. Le certificat est obtenu puis renouvelé automatiquement.</Notice>
            <Field label="Nom de domaine"><Input value={acme.domain} onChange={(e) => setAcme({ ...acme, domain: e.target.value })} /></Field>
            <Field label="E-mail (avis d'expiration)"><Input type="email" value={acme.email} onChange={(e) => setAcme({ ...acme, email: e.target.value })} /></Field>
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
  const save = useAction(() => api.put("/api/settings", s), { success: "Paramètres enregistrés" });
  const test = useAction(() => api.post("/api/settings/test-notification"), { success: "Notification envoyée" });
  const setTime = useAction(() => api.put("/api/system/time", { timezone: zone, ntp: true }), { success: "Fuseau horaire mis à jour" });
  const install = useAction((c: string) => api.post(`/api/system/components/${c}/install`));
  if (isLoading || !s) return <Spinner />;
  const set = <K extends keyof Settings>(k: K, v: Settings[K]) => setS({ ...s, [k]: v });
  return (
    <div>
      <PageHeader title="Paramètres" subtitle="Configuration générale du NAS" icon={<Cog size={20} />} actions={<Button variant="primary" loading={save.isPending} onClick={() => save.mutate()}>Enregistrer</Button>} />
      <ErrorBox error={error} />
      <div className="grid gap-5 xl:grid-cols-2">
        <Card title={<span className="flex items-center gap-2"><Share2 size={16} /> Partage de fichiers (SMB)</span>}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Groupe de travail"><Input value={s.workgroup} onChange={(e) => set("workgroup", e.target.value)} /></Field>
            <Field label="Description du serveur"><Input value={s.serverString} onChange={(e) => set("serverString", e.target.value)} /></Field>
            <Field label="Protocole minimum" hint="SMB1 est désactivé pour des raisons de sécurité.">
              <Select value={s.smbMinProtocol} onChange={(e) => set("smbMinProtocol", e.target.value)}>
                <option value="SMB2">SMB2</option><option value="SMB2_10">SMB 2.1</option><option value="SMB3">SMB3</option><option value="SMB3_11">SMB 3.1.1</option>
              </Select>
            </Field>
            <Field label="Vider la corbeille réseau après (jours)" hint="Fichiers supprimés via SMB, purgés chaque nuit à 3 h 30. 0 = jamais."><Input type="number" min={0} value={s.trashDays} onChange={(e) => set("trashDays", Math.max(0, +e.target.value))} /></Field>
          </div>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Shield size={16} /> Sécurité</span>}>
          <Field label="Durée de session (heures)"><Input type="number" min={1} max={720} value={s.sessionHours} onChange={(e) => set("sessionHours", +e.target.value)} /></Field>
          <p className="mt-3 text-[12.5px] text-muted">L'interface est disponible en HTTPS sur le port 7443 (certificat auto-signé généré au premier démarrage). Ne l'exposez pas directement sur Internet : utilisez un VPN.</p>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Bell size={16} /> Notifications</span>} actions={<Button size="sm" loading={test.isPending} onClick={() => test.mutate()}>Tester</Button>}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Webhook (Discord, Slack, ntfy, Gotify…)" className="sm:col-span-2" hint="Reçoit un POST JSON (title, message, text, content)."><Input value={s.webhookUrl} onChange={(e) => set("webhookUrl", e.target.value)} /></Field>
            <Field label="Serveur SMTP"><Input value={s.smtpHost} onChange={(e) => set("smtpHost", e.target.value)} /></Field>
            <Field label="Port"><Input type="number" value={s.smtpPort} onChange={(e) => set("smtpPort", +e.target.value)} /></Field>
            <Field label="Chiffrement">
              <Select value={s.smtpSecurity || "auto"} onChange={(e) => set("smtpSecurity", e.target.value)}>
                <option value="auto">Automatique (TLS sur 465, sinon STARTTLS)</option>
                <option value="tls">TLS direct (SSL)</option>
                <option value="starttls">STARTTLS obligatoire</option>
                <option value="none">Aucun (réseau local uniquement)</option>
              </Select>
            </Field>
            <Field label="Utilisateur SMTP"><Input value={s.smtpUser} onChange={(e) => set("smtpUser", e.target.value)} /></Field>
            <Field label="Mot de passe SMTP"><Input type="password" value={s.smtpPassword} onChange={(e) => set("smtpPassword", e.target.value)} /></Field>
            <Field label="Expéditeur"><Input value={s.smtpFrom} onChange={(e) => set("smtpFrom", e.target.value)} /></Field>
            <Field label="Destinataire des alertes"><Input type="email" value={s.notifyEmail} onChange={(e) => set("notifyEmail", e.target.value)} /></Field>
          </div>
          <p className="mt-3 text-[12.5px] text-muted">Alertes envoyées : RAID dégradé, pool ZFS en erreur, disque SMART défaillant, volume plein à 90 %, échec d'une tâche.</p>
        </Card>
        <Card title={<span className="flex items-center gap-2"><Clock size={16} /> Date et heure</span>}>
          <div className="flex items-end gap-2">
            <Field label="Fuseau horaire" className="flex-1">
              <Select value={zone} onChange={(e) => setZone(e.target.value)}>{tz.data?.map((z) => <option key={z}>{z}</option>)}</Select>
            </Field>
            <Button loading={setTime.isPending} onClick={() => setTime.mutate()}>Appliquer</Button>
          </div>
          <p className="mt-3 text-[12.5px] text-muted">Synchronisation NTP activée automatiquement.</p>
        </Card>
        <CertificatesCard />
        <Card title={<span className="flex items-center gap-2"><Wrench size={16} /> Composants</span>} className="xl:col-span-2">
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
            {Object.entries(comps.data ?? {}).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between rounded-xl bg-panel-2 px-3 py-2.5">
                <span className="font-medium capitalize">{k}</span>
                {v ? <Badge tone="ok" dot>Installé</Badge> : (k === "zfs" || k === "docker") ? <Button size="sm" loading={install.isPending} onClick={() => install.mutate(k)}>Installer</Button> : <Badge tone="warn">Absent</Badge>}
              </div>
            ))}
          </div>
        </Card>
      </div>
    </div>
  );
}
