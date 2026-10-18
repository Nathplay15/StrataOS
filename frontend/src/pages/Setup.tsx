import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowLeft, ArrowRight, Bell, Check, CheckCircle2, FolderPlus, Globe, HardDrive, Loader2, ShieldCheck, Sparkles } from "lucide-react";
import logo from "../logo.svg";
import { api } from "../lib/api";
import { bytes, cx } from "../lib/format";
import type { Disk, Job, Me, Settings } from "../lib/types";
import { Button, Checkbox, ErrorBox, Field, Input, Notice, Select, Switch } from "../components/ui";
import { useTheme } from "../components/Layout";
import { lang, languages, setLang, tr } from "../lib/i18n";

/** Waits for a background job started by the API and fails on its error. */
async function waitJob(j: Job, onLog?: (l: string) => void): Promise<void> {
  for (;;) {
    const s = await api.get<Job & { log?: string }>(`/api/jobs/${j.id}`);
    if (s.log && onLog) onLog(s.log.trim().split("\n").pop() ?? "");
    if (s.status === "success") return;
    if (s.status !== "running") throw new Error(s.error || tr("failed"));
    await new Promise((r) => setTimeout(r, 1500));
  }
}

type Layout = "protect" | "capacity" | "later";

/** What StrataOS recommends for the free disks (like Synology SHR: redundancy first). */
function plan(disks: Disk[], layout: Layout) {
  const n = disks.length;
  const smallest = Math.min(...disks.map((d) => d.size));
  if (layout === "later" || n === 0) return null;
  if (layout === "capacity" || n === 1) {
    return { kind: "btrfs-single" as const, label: n === 1 ? tr("Btrfs volume on one disk (no redundancy)") : tr("Btrfs volume on {0} disks, no redundancy", n), size: disks.reduce((s, d) => s + d.size, 0), safe: false };
  }
  if (n === 2) return { kind: "btrfs-raid1" as const, label: tr("Btrfs RAID 1 mirror: every file is on both disks"), size: smallest, safe: true };
  return { kind: "raid5" as const, label: tr("RAID 5 on {0} disks + Btrfs: one disk can fail", n), size: (n - 1) * smallest, safe: true };
}

const folders = [
  { name: "Documents", label: () => tr("Documents") },
  { name: "Photos", label: () => tr("Photos") },
  { name: "Musique", label: () => tr("Music") },
  { name: "Videos", label: () => tr("Videos") },
  { name: "Sauvegardes", label: () => tr("Backups (Time Machine)") },
];

function Steps({ step, labels }: { step: number; labels: string[] }) {
  return (
    <div className="mb-6 flex items-center gap-1.5">
      {labels.map((l, i) => (
        <div key={l} className="flex flex-1 flex-col gap-1.5">
          <div className={cx("h-1 rounded-full", i <= step ? "bg-accent" : "bg-line")} />
          <span className={cx("hidden text-[11px] sm:block", i === step ? "text-fg" : "text-muted")}>{l}</span>
        </div>
      ))}
    </div>
  );
}

function Title({ icon, children, sub }: { icon: ReactNode; children: ReactNode; sub?: ReactNode }) {
  return (
    <div className="mb-4">
      <h2 className="flex items-center gap-2 text-[17px] font-semibold"><span className="text-accent">{icon}</span>{children}</h2>
      {sub && <p className="text-muted mt-1 text-[13px]">{sub}</p>}
    </div>
  );
}

export function Setup({ hostname, onDone }: { hostname: string; onDone: (m: Me) => void }) {
  useTheme();
  const [step, setStep] = useState(0);
  const [me, setMe] = useState<Me | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState("");
  const zones = useMemo(() => { try { return Intl.supportedValuesOf("timeZone"); } catch { return ["Europe/Paris"]; } }, []);
  const [f, setF] = useState({ username: "", fullName: "", email: "", password: "", confirm: "", hostname, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "Europe/Paris" });
  const [disks, setDisks] = useState<Disk[] | null>(null);
  const [chosen, setChosen] = useState<string[]>([]);
  const [layout, setLayout] = useState<Layout>("protect");
  const [volumePath, setVolumePath] = useState<string | null>(null);
  const [shares, setShares] = useState<string[]>(["Documents", "Photos", "Musique", "Videos"]);
  const [svc, setSvc] = useState({ maintenance: true, webdav: false, sftp: false, ntfyUrl: "", telegramToken: "", telegramChatId: "" });
  const [done, setDone] = useState<string[]>([]);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const labels = [tr("Welcome"), tr("Administrator"), tr("Storage"), tr("Folders"), tr("Services"), tr("Completed")];

  useEffect(() => {
    if (step === 2 && me && !disks) {
      api.get<Disk[]>("/api/storage/disks").then((d) => {
        const free = d.filter((x) => x.usage === "free" && !x.system && !x.removable);
        setDisks(free);
        setChosen(free.map((x) => x.path));
        if (free.length === 0) setLayout("later");
      }).catch(setErr);
    }
  }, [step, me, disks]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr(null);
    try {
      await fn();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
      setProgress("");
    }
  };

  const createAdmin = () => run(async () => {
    if (f.password !== f.confirm) throw new Error(tr("Passwords do not match"));
    const m = await api.post<Me>("/api/setup", { username: f.username.toLowerCase(), fullName: f.fullName, password: f.password, hostname: f.hostname, timezone: f.timezone });
    if (f.email) {
      const s = await api.get<Settings>("/api/settings");
      await api.put("/api/settings", { ...s, notifyEmail: f.email });
    }
    setMe(m);
    setStep(2);
  });

  const selected = (disks ?? []).filter((d) => chosen.includes(d.path));
  const p = plan(selected, layout);

  const createStorage = () => run(async () => {
    if (!p) {
      setStep(3);
      return;
    }
    const devs = selected.map((d) => d.path);
    let target = devs;
    if (p.kind === "raid5") {
      setProgress(tr("Creating RAID 5 array…"));
      await waitJob(await api.post<Job>("/api/storage/raid", { name: "volume1", level: "5", devices: devs }), setProgress);
      target = ["/dev/md/volume1"];
    }
    setProgress(tr("Creating volume…"));
    const profile = p.kind === "btrfs-raid1" ? "raid1" : "single";
    await waitJob(await api.post<Job>("/api/storage/volumes", { name: "volume1", fsType: "btrfs", devices: target, dataProfile: profile, compression: true }), setProgress);
    setVolumePath("/srv/strata/volume1");
    setDone((d) => [...d, p.label]);
    setStep(3);
  });

  const createShares = () => run(async () => {
    if (volumePath && me) {
      for (const name of shares) {
        setProgress(tr("Folder {0}…", name));
        await api.post("/api/shares", { name, path: `${volumePath}/${name}`, description: "", smb: true, nfs: false, guest: false, browseable: true, recycleBin: true, timeMachine: name === "Sauvegardes", permissions: { [me.username]: "rw" }, nfsClients: "", nfsOptions: "" });
      }
      if (shares.length) setDone((d) => [...d, tr("Shared folders: {0}", shares.join(", "))]);
    }
    setStep(4);
  });

  const applyServices = () => run(async () => {
    if (svc.maintenance) {
      setProgress(tr("Maintenance plan…"));
      await api.post("/api/schedules/defaults").catch(() => undefined);
      setDone((d) => [...d, tr("Maintenance plan: RAID checks, SMART tests, snapshots")]);
    }
    if (svc.webdav || svc.sftp) {
      await api.put("/api/fileservices", { webdav: svc.webdav, sftp: svc.sftp, ftp: false, ftpRequireTls: true });
      setDone((d) => [...d, [svc.webdav && "WebDAV", svc.sftp && "SFTP"].filter(Boolean).join(" + ")]);
    }
    if (svc.ntfyUrl || (svc.telegramToken && svc.telegramChatId)) {
      const s = await api.get<Settings>("/api/settings");
      await api.put("/api/settings", { ...s, ntfyUrl: svc.ntfyUrl, telegramToken: svc.telegramToken, telegramChatId: svc.telegramChatId });
      setDone((d) => [...d, tr("Notifications: {0}", [svc.ntfyUrl && tr("push"), svc.telegramToken && "Telegram"].filter(Boolean).join(", "))]);
    }
    setStep(5);
  });

  const nav = (back: boolean, next: ReactNode) => (
    <div className="mt-6 flex gap-2">
      {back && <Button type="button" icon={<ArrowLeft size={15} />} disabled={busy} onClick={() => setStep(step - 1)}>{tr("Back")}</Button>}
      <div className="flex-1" />
      {next}
    </div>
  );

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden px-4 py-10">
      <div className="pointer-events-none absolute -top-40 left-1/2 h-[520px] w-[820px] -translate-x-1/2 rounded-full bg-gradient-to-r from-accent/25 to-accent-2/20 blur-3xl" />
      <div className="relative w-full max-w-[640px]">
        <div className="mb-6 flex items-center justify-center gap-3">
          <img src={logo} alt="" className="h-10 w-10" />
          <h1 className="text-[22px] font-semibold tracking-tight">Strata<span className="text-accent">OS</span></h1>
        </div>
        <div className="card p-7">
          <Steps step={step} labels={labels} />

          {step === 0 && (
            <>
              <Title icon={<Globe size={18} />} sub={tr("It only takes a few minutes: we will create your account and set up the disks and shared folders.")}>{tr("Welcome to StrataOS")}</Title>
              <div className="space-y-4">
                <Field label={tr("Language")}>
                  <div className="flex flex-wrap gap-2">
                    {languages.map((l) => <Button key={l.id} type="button" size="sm" variant={l.id === lang ? "primary" : "secondary"} onClick={() => l.id !== lang && setLang(l.id)}>{l.name}</Button>)}
                  </div>
                </Field>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label={tr("NAS name")} hint={tr("Visible on the network (SMB, mDNS)")}><Input value={f.hostname} onChange={set("hostname")} /></Field>
                  <Field label={tr("Time zone")}>
                    <Select value={f.timezone} onChange={set("timezone")}>{zones.map((z) => <option key={z}>{z}</option>)}</Select>
                  </Field>
                </div>
              </div>
              {nav(false, <Button variant="primary" icon={<ArrowRight size={15} />} disabled={!f.hostname} onClick={() => setStep(1)}>{tr("Continue")}</Button>)}
            </>
          )}

          {step === 1 && (
            <>
              <Title icon={<ShieldCheck size={18} />} sub={tr("This account administers the NAS. Its password is also used for SMB and SSH.")}>{tr("Administrator account")}</Title>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={tr("Username")} hint={tr("Lowercase letters, digits, - and _")}><Input autoFocus value={f.username} onChange={set("username")} /></Field>
                <Field label={tr("Full name")}><Input value={f.fullName} onChange={set("fullName")} /></Field>
                <Field label={tr("Password")} hint={tr("8 characters minimum")}><Input type="password" autoComplete="new-password" value={f.password} onChange={set("password")} /></Field>
                <Field label={tr("Confirmation")}><Input type="password" autoComplete="new-password" value={f.confirm} onChange={set("confirm")} /></Field>
                <Field label={tr("Email for alerts (optional)")} className="sm:col-span-2"><Input type="email" value={f.email} onChange={set("email")} /></Field>
              </div>
              <ErrorBox error={err} />
              {nav(true, <Button variant="primary" icon={<ArrowRight size={15} />} loading={busy} disabled={!f.username || f.password.length < 8} onClick={createAdmin}>{tr("Create account")}</Button>)}
            </>
          )}

          {step === 2 && (
            <>
              <Title icon={<HardDrive size={18} />} sub={tr("The selected disks will be completely erased. The system disk is never offered.")}>{tr("Storage")}</Title>
              {!disks ? <div className="text-muted flex items-center gap-2 text-[13px]"><Loader2 size={15} className="animate-spin" />{tr("Detecting disks…")}</div> : disks.length === 0 ? (
                <Notice>{tr("No free disk detected. You can create your volumes later in Storage Manager.")}</Notice>
              ) : (
                <div className="space-y-4">
                  <div className="space-y-1.5">
                    {disks.map((d) => (
                      <label key={d.path} className="flex cursor-pointer items-center gap-3 rounded-xl border border-line px-3 py-2.5">
                        <Checkbox checked={chosen.includes(d.path)} onChange={(v) => setChosen(v ? [...chosen, d.path] : chosen.filter((x) => x !== d.path))} />
                        <HardDrive size={16} className="text-muted" />
                        <span className="flex-1 text-[13px]"><b>{d.model || d.name}</b> <span className="text-muted">{d.rotational ? tr("hard disk") : "SSD"} · {d.path}</span></span>
                        <span className="text-[13px]">{bytes(d.size)}</span>
                      </label>
                    ))}
                  </div>
                  <div className="grid gap-2 sm:grid-cols-3">
                    {([["protect", tr("Protection"), tr("Survives a single disk failure")], ["capacity", tr("Capacity"), tr("All the space, no redundancy")], ["later", tr("Later"), tr("I'll configure it myself")]] as [Layout, string, string][]).map(([id, l, s]) => (
                      <button key={id} type="button" disabled={id === "protect" && selected.length < 2} onClick={() => setLayout(id)} className={cx("rounded-xl border px-3 py-2.5 text-left transition disabled:opacity-40", layout === id ? "border-accent bg-accent-soft" : "border-line bg-panel-2 hover:border-accent/40")}>
                        <div className="text-[13px] font-medium">{l}</div><div className="text-muted text-[11.5px]">{s}</div>
                      </button>
                    ))}
                  </div>
                  {p && (
                    <Notice tone={p.safe ? "info" : "warn"}>
                      <div className="flex items-center justify-between gap-3"><span>{p.label}</span><b className="whitespace-nowrap">{tr("≈ {0} usable", bytes(p.size))}</b></div>
                    </Notice>
                  )}
                  {layout === "protect" && selected.length === 1 && <Notice tone="warn">{tr("With a single disk, there is no protection against failure: remember to back up.")}</Notice>}
                </div>
              )}
              {progress && <div className="text-muted mt-4 flex items-center gap-2 text-[13px]"><Loader2 size={15} className="animate-spin" />{progress}</div>}
              <ErrorBox error={err} />
              {nav(false, <Button variant="primary" icon={p ? <Sparkles size={15} /> : <ArrowRight size={15} />} loading={busy} disabled={!disks || (layout !== "later" && selected.length === 0)} onClick={createStorage}>{p ? tr("Erase and create volume") : tr("Continue")}</Button>)}
            </>
          )}

          {step === 3 && (
            <>
              <Title icon={<FolderPlus size={18} />} sub={volumePath ? tr("Created on the new volume, accessible over SMB (Windows, macOS) with the network recycle bin.") : tr("No volume was created: shared folders can be added later.")}>{tr("Shared folders")}</Title>
              {volumePath && (
                <div className="grid gap-2 sm:grid-cols-2">
                  {folders.map((fo) => <Checkbox key={fo.name} checked={shares.includes(fo.name)} onChange={(v) => setShares(v ? [...shares, fo.name] : shares.filter((x) => x !== fo.name))} label={fo.label()} />)}
                </div>
              )}
              {progress && <div className="text-muted mt-4 flex items-center gap-2 text-[13px]"><Loader2 size={15} className="animate-spin" />{progress}</div>}
              <ErrorBox error={err} />
              {nav(false, <Button variant="primary" icon={<ArrowRight size={15} />} loading={busy} onClick={createShares}>{tr("Continue")}</Button>)}
            </>
          )}

          {step === 4 && (
            <>
              <Title icon={<Bell size={18} />} sub={tr("Everything can be changed later in Control Panel.")}>{tr("Services and alerts")}</Title>
              <div className="space-y-4">
                <Switch checked={svc.maintenance} onChange={(v) => setSvc({ ...svc, maintenance: v })} label={tr("Recommended maintenance plan")} hint={tr("Monthly array and volume checks, SMART tests, daily snapshots")} />
                <Switch checked={svc.webdav} onChange={(v) => setSvc({ ...svc, webdav: v })} label={tr("WebDAV")} hint={tr("Network drive over HTTPS, handy from outside your network or on a phone")} />
                <Switch checked={svc.sftp} onChange={(v) => setSvc({ ...svc, sftp: v })} label="SFTP" hint={tr("Encrypted transfers with FileZilla, WinSCP, Cyberduck")} />
                <div className="space-y-3 rounded-xl border border-line p-4">
                  <div className="text-[13px] font-medium">{tr("Phone notifications (optional)")}</div>
                  <Field label={tr("Push notifications (ntfy)")} hint={tr("Install the ntfy app and subscribe to this topic")}>
                    <div className="flex gap-2">
                      <Input className="mono" value={svc.ntfyUrl} onChange={(e) => setSvc({ ...svc, ntfyUrl: e.target.value })} placeholder="https://ntfy.sh/…" />
                      <Button type="button" onClick={() => setSvc({ ...svc, ntfyUrl: `https://ntfy.sh/strata-${Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(36).padStart(2, "0")).join("").slice(0, 20)}` })}>{tr("Generate")}</Button>
                    </div>
                  </Field>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label={tr("Telegram bot token")}><Input type="password" autoComplete="off" value={svc.telegramToken} onChange={(e) => setSvc({ ...svc, telegramToken: e.target.value })} /></Field>
                    <Field label={tr("Chat ID")} hint={tr("Can be found later in Settings")}><Input className="mono" value={svc.telegramChatId} onChange={(e) => setSvc({ ...svc, telegramChatId: e.target.value })} /></Field>
                  </div>
                </div>
              </div>
              {progress && <div className="text-muted mt-4 flex items-center gap-2 text-[13px]"><Loader2 size={15} className="animate-spin" />{progress}</div>}
              <ErrorBox error={err} />
              {nav(false, <Button variant="primary" icon={<ArrowRight size={15} />} loading={busy} onClick={applyServices}>{tr("Apply")}</Button>)}
            </>
          )}

          {step === 5 && me && (
            <>
              <Title icon={<CheckCircle2 size={18} />} sub={tr("Your NAS is ready.")}>{tr("All done!")}</Title>
              <ul className="space-y-2 text-[13px]">
                <li className="flex items-center gap-2"><Check size={15} className="text-ok" />{tr("Administrator account {0}", me.username)}</li>
                {done.map((d) => <li key={d} className="flex items-center gap-2"><Check size={15} className="text-ok" />{d}</li>)}
              </ul>
              <div className="mt-4"><Notice>{tr("From a Windows PC, open \\\\{0} in File Explorer; on a Mac, Finder → Go → smb://{0}.", f.hostname)}</Notice></div>
              {nav(false, <Button variant="primary" icon={<ArrowRight size={15} />} onClick={() => onDone(me)}>{tr("Open StrataOS")}</Button>)}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
