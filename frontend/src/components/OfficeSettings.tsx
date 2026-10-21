import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FileText } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import { tr } from "../lib/i18n";
import { Badge, Button, Card, ErrorBox, Field, Input, Switch } from "./ui";

interface Office { enabled?: boolean; serverUrl?: string; internalUrl?: string; strataUrl?: string; secret?: string }
interface OfficeInfo { settings: Office; installed: boolean; stack: string; port: string; ready: boolean; defaultStrataUrl: string; internalUrl: string; appSecret: boolean }

/** Settings → Office documents: OnlyOffice in the file manager. */
export function OfficeSettingsCard() {
  const info = useQuery({ queryKey: ["office-settings"], queryFn: () => api.get<OfficeInfo>("/api/office/settings") });
  const [o, setO] = useState<Office>({});
  useEffect(() => { if (info.data) setO(info.data.settings); }, [info.data]);
  const save = useAction(() => api.put("/api/office/settings", o), { success: tr("Settings saved") });
  const check = useAction(() => api.post<{ ok: boolean; error?: string }>("/api/office/check"), { success: (r) => (r.ok ? tr("The document server answers") : tr("The document server does not answer: {0}", r.error ?? "")) });
  const d = info.data;
  const set = (k: keyof Office, v: string | boolean) => setO({ ...o, [k]: v });
  return (
    <Card title={<span className="flex items-center gap-2"><FileText size={16} /> {tr("Office documents (OnlyOffice)")}</span>} className="xl:col-span-2" actions={<>
      {d && (d.ready ? <Badge tone="ok" dot>{tr("Ready")}</Badge> : <Badge>{tr("Off")}</Badge>)}
      <Button size="sm" loading={check.isPending} disabled={!d?.internalUrl} onClick={() => check.mutate()}>{tr("Test")}</Button>
      <Button size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>{tr("Save")}</Button>
    </>}>
      <div className="space-y-4">
        <p className="text-[12.5px] text-muted">
          {d?.installed ? tr("OnlyOffice Docs is installed from the App Center (stack {0}, port {1}): leave the addresses empty to use it.", d.stack, d.port) : tr("Install OnlyOffice Docs from the App Center (about 3 GB), or give the address of another document server below.")}
        </p>
        <Switch checked={!!o.enabled} onChange={(v) => set("enabled", v)} label={tr("Open office documents with OnlyOffice")} hint={tr("Files then opens docx, xlsx, pptx, odt, ods, odp and csv in OnlyOffice; users who can write the shared folder edit them, together.")} />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={tr("Document server address (browsers)")} hint={tr("Empty: the NAS address with the app port")}><Input className="mono" placeholder={d?.port ? `http://<nas>:${d.port}` : "https://office.example.com"} value={o.serverUrl ?? ""} onChange={(e) => set("serverUrl", e.target.value)} /></Field>
          <Field label={tr("Document server address (from StrataOS)")} hint={d?.internalUrl ? tr("Current: {0}", d.internalUrl) : undefined}><Input className="mono" placeholder={d?.port ? `http://127.0.0.1:${d.port}` : ""} value={o.internalUrl ?? ""} onChange={(e) => set("internalUrl", e.target.value)} /></Field>
          <Field label={tr("StrataOS address (from the document server)")} hint={tr("Empty: {0} (the host, seen from the container)", d?.defaultStrataUrl ?? "")}><Input className="mono" placeholder={d?.defaultStrataUrl} value={o.strataUrl ?? ""} onChange={(e) => set("strataUrl", e.target.value)} /></Field>
          <Field label={tr("JWT secret")} hint={d?.appSecret ? tr("Empty: the secret chosen when the app was installed") : tr("The JWT_SECRET of the document server")}><Input className="mono" type="password" autoComplete="new-password" value={o.secret ?? ""} onChange={(e) => set("secret", e.target.value)} /></Field>
        </div>
        <ErrorBox error={info.error ?? save.error} />
      </div>
    </Card>
  );
}
