import { useState } from "react";
import { Copy, Link2 } from "lucide-react";
import { api } from "../lib/api";
import { useAction } from "../lib/hooks";
import type { ShareLink } from "../lib/types";
import { Button, ErrorBox, Field, Input, Modal, Notice, Select, Switch, useUI } from "./ui";
import { tr } from "../lib/i18n";

export const linkURL = (id: string) => `${location.origin}/s/${id}`;

/** Creates a public link to a file, a folder or a photo album. */
export function ShareLinkModal({ path, album, dir, name, onClose }: { path?: string; album?: string; dir?: boolean; name: string; onClose: () => void }) {
  const { toast } = useUI();
  const [f, setF] = useState({ download: true, upload: false, password: "", expiresDays: 7, maxDownloads: 0 });
  const [made, setMade] = useState<ShareLink | null>(null);
  const create = useAction(() => api.post<ShareLink>("/api/links", { path, album, ...f }), { onDone: (l) => setMade(l) });
  const copy = (u: string) => { navigator.clipboard?.writeText(u); toast(tr("Lien copié")); };

  return (
    <Modal open onClose={onClose} title={<span className="flex items-center gap-2"><Link2 size={17} />{tr("Partager « {0} »", name)}</span>} footer={made ? <Button variant="primary" onClick={onClose}>{tr("Terminé")}</Button> : <>
      <Button onClick={onClose}>{tr("Annuler")}</Button>
      <Button variant="primary" loading={create.isPending} disabled={!f.download && !f.upload} onClick={() => create.mutate()}>{tr("Créer le lien")}</Button>
    </>}>
      {made ? (
        <div className="space-y-3">
          <p className="text-muted text-[13px]">{tr("Toute personne qui a ce lien peut l'ouvrir, sans compte sur le NAS.")}</p>
          <div className="flex gap-2"><Input readOnly className="mono" value={linkURL(made.id)} onFocus={(e) => e.target.select()} /><Button icon={<Copy size={15} />} onClick={() => copy(linkURL(made.id))}>{tr("Copier")}</Button></div>
          <Notice>{tr("Pour l'ouvrir depuis Internet, le NAS doit être joignable de l'extérieur (redirection de port ou DNS dynamique dans Accès à distance). Retrouvez vos liens dans Liens partagés.")}</Notice>
        </div>
      ) : (
        <div className="space-y-4">
          {!album && <Switch checked={f.download} onChange={(v) => setF({ ...f, download: v })} label={dir ? tr("Voir et télécharger le contenu") : tr("Télécharger le fichier")} />}
          {dir && <Switch checked={f.upload} onChange={(v) => setF({ ...f, upload: v })} label={tr("Déposer des fichiers")} hint={tr("Les visiteurs peuvent envoyer des fichiers dans ce dossier, sans jamais remplacer les existants. Sans téléchargement, le contenu reste caché (boîte de dépôt).")} />}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={tr("Expiration")}>
              <Select value={f.expiresDays} onChange={(e) => setF({ ...f, expiresDays: +e.target.value })}>
                <option value={1}>{tr("1 jour")}</option>
                <option value={7}>{tr("7 jours")}</option>
                <option value={30}>{tr("30 jours")}</option>
                <option value={90}>{tr("90 jours")}</option>
                <option value={0}>{tr("Jamais")}</option>
              </Select>
            </Field>
            <Field label={tr("Téléchargements maximum")} hint={tr("0 = illimité")}><Input type="number" min={0} value={f.maxDownloads} onChange={(e) => setF({ ...f, maxDownloads: Math.max(0, +e.target.value) })} /></Field>
          </div>
          <Field label={tr("Mot de passe (facultatif)")}><Input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
          <ErrorBox error={create.error} />
        </div>
      )}
    </Modal>
  );
}
