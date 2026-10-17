import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Link2, Network as NetIcon, Plus, RotateCcw, Trash2, Undo2 } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { Badge, Button, Card, Checkbox, ErrorBox, Field, IconButton, Input, Modal, Notice, PageHeader, Select, Spinner, useUI } from "../../components/ui";

type Link = { name: string; mac: string; state: string; mtu: number; addresses: string[]; speed: number; kind: string };
type Status = { interfaces: Link[]; gateway: string; dns: string[]; hostname: string };
type IP = { mode: "dhcp" | "static" | "none"; addresses?: string[]; gateway?: string; dns?: string[]; mtu?: number };
type Iface = IP & { name: string };
type Bond = IP & { name: string; bondMode: string; members: string[] };
type VLAN = IP & { parent: string; id: number };
type Config = { interfaces: Iface[]; bonds: Bond[]; vlans: VLAN[] };
type Pending = { deadline: string; links: string[]; token?: string };
type Net = { status: Status; manager: string; config: Config; pending: Pending | null };

const bondModes: [string, string][] = [
  ["active-backup", "Actif/secours (tolérance de panne, aucun réglage du switch)"],
  ["802.3ad", "LACP 802.3ad (agrégation, switch compatible requis)"],
  ["balance-alb", "Équilibrage adaptatif (sans switch spécial)"],
  ["balance-tlb", "Équilibrage en émission"],
  ["balance-rr", "Round-robin"],
  ["balance-xor", "XOR"],
  ["broadcast", "Diffusion"],
];

const list = (s: string) => s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

function IPEditor({ ip, onChange }: { ip: IP; onChange: (ip: IP) => void }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <Field label="Adressage">
        <Select value={ip.mode} onChange={(e) => onChange({ ...ip, mode: e.target.value as IP["mode"] })}>
          <option value="dhcp">DHCP (automatique)</option>
          <option value="static">Adresse fixe</option>
          <option value="none">Aucune adresse IP</option>
        </Select>
      </Field>
      {ip.mode === "static" && (
        <>
          <Field label="Adresse(s) / masque" hint="ex. 192.168.1.20/24"><Input className="mono" value={(ip.addresses ?? []).join(" ")} onChange={(e) => onChange({ ...ip, addresses: list(e.target.value) })} /></Field>
          <Field label="Passerelle"><Input className="mono" value={ip.gateway ?? ""} onChange={(e) => onChange({ ...ip, gateway: e.target.value.trim() })} /></Field>
        </>
      )}
      {ip.mode !== "none" && <Field label="DNS" hint="optionnel, séparés par des espaces"><Input className="mono" value={(ip.dns ?? []).join(" ")} onChange={(e) => onChange({ ...ip, dns: list(e.target.value) })} /></Field>}
      <Field label="MTU" hint="vide = défaut, 9000 = jumbo frames"><Input type="number" value={ip.mtu || ""} onChange={(e) => onChange({ ...ip, mtu: +e.target.value || 0 })} /></Field>
    </div>
  );
}

/** Watches a trial configuration: confirms it when the NAS answers, offers the new addresses otherwise. */
function TrialModal({ pending, newURLs, onClose }: { pending: Pending; newURLs: string[]; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const [left, setLeft] = useState(Math.round((new Date(pending.deadline).getTime() - Date.now()) / 1000));
  const [state, setState] = useState<"waiting" | "confirmed" | "unreachable">("waiting");
  useEffect(() => {
    const t = setInterval(() => setLeft(Math.round((new Date(pending.deadline).getTime() - Date.now()) / 1000)), 1000);
    return () => clearInterval(t);
  }, [pending.deadline]);
  useEffect(() => {
    if (!pending.token) return;
    let stop = false;
    (async () => {
      let fails = 0;
      // Give the links a few seconds to come up before judging connectivity.
      await new Promise((r) => setTimeout(r, 4000));
      while (!stop) {
        try {
          const r = await fetch("/api/system/network/confirm", { method: "POST", headers: { "Content-Type": "application/json", "X-Strata": "1" }, body: JSON.stringify({ token: pending.token }), signal: AbortSignal.timeout(4000) });
          if (r.ok) {
            setState("confirmed");
            toast("Nouvelle configuration réseau confirmée");
            qc.invalidateQueries();
            return;
          }
          // 409: the NAS answers but the change is still settling — keep waiting.
          if (r.status !== 409) fails++;
        } catch {
          fails++;
        }
        if (fails >= 3) setState("unreachable");
        await new Promise((r) => setTimeout(r, 2500));
      }
    })();
    return () => { stop = true; };
  }, [pending.token, qc, toast]);
  const expired = left <= 0 && state !== "confirmed";
  return (
    <Modal open onClose={onClose} title="Test de la nouvelle configuration réseau" footer={state === "confirmed" || expired ? <Button variant="primary" onClick={onClose}>Fermer</Button> : undefined}>
      {state === "confirmed" ? (
        <div className="flex items-center gap-3 text-ok"><CheckCircle2 size={22} /> Configuration conservée : le NAS est joignable.</div>
      ) : expired ? (
        <Notice tone="warn">Délai écoulé : l'ancienne configuration a été restaurée.</Notice>
      ) : (
        <div className="space-y-4">
          <div className="text-center">
            <div className="text-[34px] font-semibold tabular-nums">{left} s</div>
            <div className="text-muted text-[13px]">avant le retour automatique à l'ancienne configuration</div>
          </div>
          {state === "waiting" ? <Spinner label="Vérification que le NAS reste joignable…" /> : (
            <Notice tone="warn">
              Le NAS ne répond plus à cette adresse. S'il a changé d'adresse, ouvrez l'un de ces liens <b>avant la fin du délai</b> pour confirmer :
              <div className="mt-2 space-y-1">{newURLs.map((u) => <a key={u} href={u} className="mono block text-accent underline">{u.split("?")[0]}</a>)}</div>
              {newURLs.length === 0 && <div className="mt-2">Sinon, attendez : l'ancienne configuration reviendra seule.</div>}
            </Notice>
          )}
        </div>
      )}
    </Modal>
  );
}

export default function NetworkPage() {
  const { confirm } = useUI();
  const { data, isLoading, error } = useQuery({ queryKey: ["network"], queryFn: () => api.get<Net>("/api/system/network"), refetchInterval: 10000 });
  const [cfg, setCfg] = useState<Config | null>(null);
  const [host, setHost] = useState<string | null>(null);
  const [trial, setTrial] = useState<{ p: Pending; urls: string[] } | null>(null);
  const saveHost = useAction((h: string) => api.put("/api/system/hostname", { hostname: h }), { success: "Nom d'hôte modifié", onDone: () => setHost(null) });
  const rollback = useAction(() => api.post("/api/system/network/rollback"), { success: "Ancienne configuration restaurée" });
  const apply = useAction(() => api.put<Pending>("/api/system/network/config", { config: cfg, timeout: 120 }), {
    onDone: (p) => {
      const port = location.port ? `:${location.port}` : "";
      const statics = [...(cfg?.interfaces ?? []), ...(cfg?.bonds ?? []), ...(cfg?.vlans ?? [])].filter((x) => x.mode === "static").flatMap((x) => x.addresses ?? []);
      const addrs = statics.map((a) => a.split("/")[0]).filter((a) => a !== location.hostname);
      setTrial({ p, urls: addrs.map((a) => `${location.protocol}//${a}${port}/?netconfirm=${p.token}`) });
    },
  });
  useEffect(() => {
    if (data && !cfg) setCfg(structuredClone(data.config));
  }, [data, cfg]);

  const physical = useMemo(() => (data?.status.interfaces ?? []).filter((i) => i.kind === "physical" && !i.name.startsWith("bond")), [data]);
  if (isLoading || !data || !cfg) return <div><PageHeader title="Réseau" icon={<NetIcon size={20} />} /><ErrorBox error={error} /><Spinner /></div>;

  const dirty = JSON.stringify(cfg) !== JSON.stringify(data.config);
  const members = new Set(cfg.bonds.flatMap((b) => b.members));
  const managed = (name: string) => cfg.interfaces.find((i) => i.name === name);
  const setIface = (name: string, ip: IP | null) =>
    setCfg({
      ...cfg,
      interfaces: ip ? [...cfg.interfaces.filter((i) => i.name !== name), { ...ip, name }].sort((a, b) => a.name.localeCompare(b.name)) : cfg.interfaces.filter((i) => i.name !== name),
      vlans: ip ? cfg.vlans : cfg.vlans.filter((v) => v.parent !== name),
    });
  const parents = [...cfg.interfaces.map((i) => i.name), ...cfg.bonds.map((b) => b.name)];
  const nm = data.manager === "NetworkManager";

  const doApply = async () => {
    if (await confirm({ title: "Appliquer la configuration réseau ?", text: <>Elle sera appliquée <b>à l'essai</b> pendant 2 minutes. Si le NAS ne peut pas être joint pour la confirmer (adresse erronée, câble, switch…), l'ancienne configuration revient automatiquement.</>, confirm: "Appliquer à l'essai" })) apply.mutate();
  };

  return (
    <div>
      <PageHeader title="Réseau" subtitle="Interfaces, agrégation de liens, VLAN et nom du NAS" icon={<NetIcon size={20} />} actions={<>
        {dirty && <Button icon={<Undo2 size={15} />} onClick={() => setCfg(structuredClone(data.config))}>Annuler les modifications</Button>}
        <Button variant="primary" disabled={!dirty || nm || !!data.pending} loading={apply.isPending} onClick={doApply}>Appliquer</Button>
      </>} />
      <ErrorBox error={error || apply.error} />
      {nm && <div className="mb-4"><Notice tone="warn">NetworkManager gère le réseau de cette machine : StrataOS ne modifie pas la configuration. Désactivez NetworkManager pour gérer le réseau depuis cette page.</Notice></div>}
      {data.pending && !trial && (
        <div className="mb-4"><Notice tone="warn">
          <div className="flex flex-wrap items-center gap-3">Une configuration réseau est à l'essai jusqu'à {new Date(data.pending.deadline).toLocaleTimeString("fr-FR")}.
            <Button size="sm" icon={<RotateCcw size={14} />} loading={rollback.isPending} onClick={() => rollback.mutate()}>Restaurer maintenant</Button>
          </div>
        </Notice></div>
      )}

      <div className="space-y-5">
        <Card title="Général">
          <div className="grid gap-5 sm:grid-cols-4">
            <div className="flex items-end gap-2 sm:col-span-2">
              <Field label="Nom d'hôte" className="flex-1"><Input value={host ?? data.status.hostname} onChange={(e) => setHost(e.target.value)} /></Field>
              <Button disabled={!host || host === data.status.hostname} loading={saveHost.isPending} onClick={() => saveHost.mutate(host!)}>Enregistrer</Button>
            </div>
            <div><div className="text-muted text-[12px]">Passerelle actuelle</div><div className="mono mt-2">{data.status.gateway || "—"}</div></div>
            <div><div className="text-muted text-[12px]">DNS actuels</div><div className="mono mt-2 truncate">{data.status.dns.join(", ") || "—"}</div></div>
          </div>
          <div className="mt-3 text-[12px] text-muted">Gestionnaire réseau détecté : <b>{data.manager}</b></div>
        </Card>

        <Card title="Interfaces">
          <div className="space-y-4">
            {physical.map((l) => {
              const m = managed(l.name);
              const inBond = cfg.bonds.find((b) => b.members.includes(l.name));
              return (
                <div key={l.name} className="rounded-xl border border-line p-4">
                  <div className="mb-3 flex flex-wrap items-center gap-2">
                    <span className="font-semibold">{l.name}</span>
                    <Badge tone={l.state === "UP" ? "ok" : "neutral"} dot>{l.state}</Badge>
                    {l.speed > 0 && <Badge tone="accent">{l.speed >= 1000 ? `${l.speed / 1000} Gb/s` : `${l.speed} Mb/s`}</Badge>}
                    <span className="mono text-muted text-[12px]">{l.mac} · {l.addresses.join(", ") || "sans adresse"}</span>
                    <div className="flex-1" />
                    {inBond ? <Badge>membre de {inBond.name}</Badge> : (
                      <Select className="!w-auto !py-1 !text-[12.5px]" value={m ? "managed" : "system"} onChange={(e) => setIface(l.name, e.target.value === "managed" ? { mode: "dhcp" } : null)}>
                        <option value="system">Configuration du système</option>
                        <option value="managed">Gérée par StrataOS</option>
                      </Select>
                    )}
                  </div>
                  {m && !inBond && <IPEditor ip={m} onChange={(ip) => setIface(l.name, ip)} />}
                </div>
              );
            })}
          </div>
        </Card>

        <Card title="Agrégation de liens (bonding)" actions={<Button size="sm" icon={<Plus size={14} />} onClick={() => {
          let n = 0;
          while (cfg.bonds.some((b) => b.name === `bond${n}`)) n++;
          setCfg({ ...cfg, bonds: [...cfg.bonds, { name: `bond${n}`, bondMode: "active-backup", members: [], mode: "dhcp" }] });
        }}>Ajouter</Button>}>
          {cfg.bonds.length === 0 ? <div className="text-muted text-[13px]">Combinez plusieurs ports réseau pour la tolérance de panne ou plus de débit.</div> : (
            <div className="space-y-4">
              {cfg.bonds.map((b, bi) => {
                const upd = (x: Partial<Bond>) => setCfg({ ...cfg, bonds: cfg.bonds.map((y, i) => (i === bi ? { ...y, ...x } : y)), interfaces: x.members ? cfg.interfaces.filter((i) => !x.members!.includes(i.name)) : cfg.interfaces });
                return (
                  <div key={bi} className="space-y-3 rounded-xl border border-line p-4">
                    <div className="flex flex-wrap items-center gap-3">
                      <span className="flex items-center gap-2 font-semibold"><Link2 size={16} className="text-accent" />{b.name}</span>
                      <Select className="!w-auto !py-1 !text-[12.5px]" value={b.bondMode} onChange={(e) => upd({ bondMode: e.target.value })}>
                        {bondModes.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                      </Select>
                      <div className="flex-1" />
                      <IconButton title="Supprimer" onClick={() => setCfg({ ...cfg, bonds: cfg.bonds.filter((_, i) => i !== bi), vlans: cfg.vlans.filter((v) => v.parent !== b.name) })}><Trash2 size={15} /></IconButton>
                    </div>
                    <div className="flex flex-wrap gap-4">
                      {physical.filter((l) => !members.has(l.name) || b.members.includes(l.name)).map((l) => (
                        <Checkbox key={l.name} checked={b.members.includes(l.name)} onChange={(v) => upd({ members: v ? [...b.members, l.name] : b.members.filter((x) => x !== l.name) })} label={<span className="mono">{l.name}</span>} />
                      ))}
                    </div>
                    {b.bondMode === "active-backup" && b.members.length > 0 && <div className="text-muted text-[12px]">Port principal : <span className="mono">{b.members[0]}</span></div>}
                    <IPEditor ip={b} onChange={(ip) => upd(ip)} />
                  </div>
                );
              })}
            </div>
          )}
        </Card>

        <Card title="VLAN" actions={<Button size="sm" icon={<Plus size={14} />} disabled={parents.length === 0} onClick={() => setCfg({ ...cfg, vlans: [...cfg.vlans, { parent: parents[0], id: 10, mode: "dhcp" }] })}>Ajouter</Button>}>
          {parents.length === 0 && <div className="text-muted text-[13px]">Passez d'abord une interface en « Gérée par StrataOS » pour y créer des VLAN.</div>}
          <div className="space-y-4">
            {cfg.vlans.map((v, vi) => {
              const upd = (x: Partial<VLAN>) => setCfg({ ...cfg, vlans: cfg.vlans.map((y, i) => (i === vi ? { ...y, ...x } : y)) });
              return (
                <div key={vi} className="space-y-3 rounded-xl border border-line p-4">
                  <div className="flex flex-wrap items-end gap-3">
                    <Field label="Interface parente"><Select value={v.parent} onChange={(e) => upd({ parent: e.target.value })}>{parents.map((p) => <option key={p}>{p}</option>)}</Select></Field>
                    <Field label="Identifiant VLAN"><Input type="number" min={1} max={4094} value={v.id} onChange={(e) => upd({ id: +e.target.value })} /></Field>
                    <span className="mono pb-2 text-muted">{v.parent}.{v.id}</span>
                    <div className="flex-1" />
                    <IconButton title="Supprimer" onClick={() => setCfg({ ...cfg, vlans: cfg.vlans.filter((_, i) => i !== vi) })}><Trash2 size={15} /></IconButton>
                  </div>
                  <IPEditor ip={v} onChange={(ip) => upd(ip)} />
                </div>
              );
            })}
          </div>
        </Card>
      </div>
      {trial && <TrialModal pending={trial.p} newURLs={trial.urls} onClose={() => { setTrial(null); setCfg(null); }} />}
    </div>
  );
}
