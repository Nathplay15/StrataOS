// Storage of the NAS: volumes, disks with their SMART data, RAID arrays and
// ZFS pools, with the safe everyday actions (SMART tests, scrubs). Creating,
// growing, deleting and replacing open the web interface, which has the
// guided flows and their checks.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { HardDrive, Thermometer } from "lucide-react";
import { Badge, Button, Card, Empty, ErrorBox, Health, Modal, Spinner, Tabs } from "@nas/components/ui";
import { useAction } from "@nas/lib/hooks";
import { bytes, duration } from "@nas/lib/format";
import type { Disk, RaidArray, SmartInfo, Volume, ZPool } from "@nas/lib/types";
import { api, q } from "../lib/nasapi";
import { t } from "../lib/i18n";
import { PageHeader, UsageBar, WebButton } from "../components/kit";

type Tab = "volumes" | "disks" | "raid" | "zfs";

export function StoragePage() {
  const [tab, setTab] = useState<Tab>("volumes");
  return (
    <>
      <PageHeader title={t("Storage")} actions={<WebButton path={`/storage/${tab}`} title={t("Storage")} />} />
      <Tabs value={tab} onChange={setTab} tabs={[
        { id: "volumes", label: t("Volumes") }, { id: "disks", label: t("Disks") }, { id: "raid", label: "RAID" }, { id: "zfs", label: "ZFS" },
      ]} />
      <div className="mt-4">
        {tab === "volumes" ? <Volumes /> : tab === "disks" ? <Disks /> : tab === "raid" ? <Raid /> : <Zfs />}
      </div>
    </>
  );
}

function Volumes() {
  const { data, isLoading, error } = useQuery({ queryKey: ["volumes"], queryFn: () => api.get<Volume[]>("/api/storage/volumes"), refetchInterval: 30_000 });
  const scrub = useAction((id: string) => api.post(`/api/storage/volumes/${id}/btrfs/scrub`, {}), { success: t("Scrub started") });
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.length) return <Card><Empty icon={<HardDrive />} title={t("No volumes")} action={<WebButton path="/storage/volumes" label={t("Create a volume")} />} /></Card>;
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {data.map((v) => (
        <Card key={v.id}>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <span className="font-semibold">{v.name}</span>
            <Badge>{v.fsType}{v.profile ? ` · ${v.profile}` : ""}</Badge>
            {!v.mounted && <Badge tone="warn">{t("Not mounted")}</Badge>}
            <span className="ml-auto"><Health value={v.health} /></span>
          </div>
          <UsageBar used={v.usage?.used ?? 0} total={v.usage?.total ?? 0} />
          <div className="mt-1.5 text-[12px] text-muted">{t("{0} used of {1}", bytes(v.usage?.used), bytes(v.usage?.total))} · {v.devices.join(", ")}</div>
          {v.shares.length > 0 && <div className="mt-1 text-[12px] text-muted">{t("Shared folders")}: {v.shares.join(", ")}</div>}
          <div className="mt-3 flex gap-2">
            {v.fsType === "btrfs" && v.mounted && <Button size="sm" loading={scrub.isPending && scrub.variables === v.id} onClick={() => scrub.mutate(v.id)}>{t("Scrub")}</Button>}
            <WebButton size="sm" path="/storage/volumes" label={t("Manage")} title={v.name} />
          </div>
        </Card>
      ))}
    </div>
  );
}

function Disks() {
  const { data, isLoading, error } = useQuery({ queryKey: ["disks"], queryFn: () => api.get<Disk[]>("/api/storage/disks"), refetchInterval: 60_000 });
  const [smart, setSmart] = useState<Disk | null>(null);
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  return (
    <Card pad={false}>
      <table className="table">
        <thead><tr><th>{t("Disk")}</th><th>{t("Model")}</th><th>{t("Size")}</th><th>{t("Temperature")}</th><th>{t("Health")}</th><th /></tr></thead>
        <tbody>
          {(data ?? []).map((d) => (
            <tr key={d.path}>
              <td className="mono">{d.path}</td>
              <td><div>{d.model || d.vendor}</div><div className="text-[11.5px] text-muted">{d.serial} · {d.rotational ? "HDD" : "SSD"} · {d.transport}</div></td>
              <td>{bytes(d.size)}</td>
              <td>{d.standby ? <span className="text-muted">{t("Standby")}</span> : d.temperature ? <span className="flex items-center gap-1"><Thermometer size={13} />{d.temperature} °C</span> : "—"}</td>
              <td>
                {d.healthy === null ? <Badge>—</Badge> : d.healthy ? <Badge tone={d.warnings?.length ? "warn" : "ok"}>{d.warnings?.length ? t("Watch") : t("Healthy")}</Badge> : <Badge tone="danger">{t("Failing")}</Badge>}
                {d.warnings?.map((w) => <div key={w} className="text-[11.5px] text-warn">{w}</div>)}
              </td>
              <td className="text-right"><Button size="sm" variant="ghost" onClick={() => setSmart(d)}>SMART</Button></td>
            </tr>
          ))}
        </tbody>
      </table>
      {smart && <SmartModal disk={smart} onClose={() => setSmart(null)} />}
    </Card>
  );
}

function SmartModal({ disk, onClose }: { disk: Disk; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({ queryKey: ["smart", disk.path], queryFn: () => api.get<SmartInfo>(`/api/storage/disks/smart${q({ device: disk.path })}`) });
  const test = useAction((type: string) => api.post("/api/storage/disks/smart-test", { device: disk.path, type }), { success: t("SMART test started") });
  return (
    <Modal open wide onClose={onClose} title={`SMART · ${disk.path}`} footer={<>
      <Button loading={test.isPending && test.variables === "short"} onClick={() => test.mutate("short")}>{t("Short test")}</Button>
      <Button loading={test.isPending && test.variables === "long"} onClick={() => test.mutate("long")}>{t("Long test")}</Button>
      <Button variant="primary" onClick={onClose}>{t("Close")}</Button>
    </>}>
      {isLoading ? <Spinner /> : error ? <ErrorBox error={error} /> : data && (
        <>
          <div className="mb-3 grid grid-cols-2 gap-2 text-[13px] sm:grid-cols-4">
            <div><div className="text-muted text-[11.5px]">{t("Result")}</div>{data.passed === null ? "—" : data.passed ? t("Passed") : t("Failed")}</div>
            <div><div className="text-muted text-[11.5px]">{t("Power-on time")}</div>{duration(data.powerOnHours * 3600)}</div>
            <div><div className="text-muted text-[11.5px]">{t("Power cycles")}</div>{data.powerCycles}</div>
            <div><div className="text-muted text-[11.5px]">{t("Firmware")}</div>{data.firmware}</div>
          </div>
          {data.attributes.length > 0 && (
            <table className="table text-[12.5px]">
              <thead><tr><th>ID</th><th>{t("Attribute")}</th><th>{t("Value")}</th><th>{t("Worst")}</th><th>{t("Threshold")}</th><th>{t("Raw")}</th></tr></thead>
              <tbody>{data.attributes.map((a) => (
                <tr key={a.id} className={a.failed ? "text-danger" : ""}><td>{a.id}</td><td>{a.name}</td><td>{a.value}</td><td>{a.worst}</td><td>{a.thresh}</td><td className="mono">{a.raw}</td></tr>
              ))}</tbody>
            </table>
          )}
        </>
      )}
    </Modal>
  );
}

function Raid() {
  const { data, isLoading, error } = useQuery({ queryKey: ["raid"], queryFn: () => api.get<RaidArray[]>("/api/storage/raid"), refetchInterval: 15_000 });
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.length) return <Card><Empty title={t("No RAID arrays")} action={<WebButton path="/storage/raid" label={t("Create a RAID array")} />} /></Card>;
  return (
    <div className="space-y-3">
      {data.map((r) => (
        <Card key={r.name}>
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-semibold">{r.label || r.name}</span>
            <Badge>{r.level.toUpperCase()}</Badge>
            <span className="text-[12.5px] text-muted">{bytes(r.size)} · {r.device}</span>
            <span className="ml-auto"><Health value={r.health} /></span>
          </div>
          <div className="mt-2 text-[12.5px] text-muted">{t("{0} of {1} disks active", r.activeDevices, r.raidDevices)}{r.failedDevices ? ` · ${t("{0} failed", r.failedDevices)}` : ""}{r.spareDevices ? ` · ${t("{0} spare(s)", r.spareDevices)}` : ""}</div>
          <div className="mt-2 flex flex-wrap gap-1.5">{r.members.map((m) => <Badge key={m.device} tone={/faulty|removed/.test(m.state) ? "danger" : "neutral"}>{m.device} · {m.state}</Badge>)}</div>
          {r.health !== "healthy" && <div className="mt-3"><WebButton size="sm" path="/storage/raid" label={t("Repair or replace a disk")} /></div>}
        </Card>
      ))}
    </div>
  );
}

function Zfs() {
  const { data, isLoading, error } = useQuery({ queryKey: ["zfs"], queryFn: () => api.get<{ available: boolean; pools: ZPool[] }>("/api/storage/zfs"), refetchInterval: 15_000 });
  const scrub = useAction((pool: string) => api.post(`/api/storage/zfs/${encodeURIComponent(pool)}/scrub`, {}), { success: t("Scrub started") });
  if (isLoading) return <Spinner />;
  if (error) return <ErrorBox error={error} />;
  if (!data?.available) return <Card><Empty title={t("ZFS is not installed")} action={<WebButton path="/storage/zfs" />} /></Card>;
  if (!data.pools.length) return <Card><Empty title={t("No ZFS pools")} action={<WebButton path="/storage/zfs" label={t("Create a pool")} />} /></Card>;
  return (
    <div className="space-y-3">
      {data.pools.map((p) => (
        <Card key={p.name}>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <span className="font-semibold">{p.name}</span>
            <span className="text-[12.5px] text-muted">{t("Fragmentation")} {p.fragmentation} · dedup {p.dedup}</span>
            <span className="ml-auto"><Health value={p.health} /></span>
          </div>
          <UsageBar used={p.alloc} total={p.size} />
          <div className="mt-1.5 text-[12px] text-muted">{t("{0} used of {1}", bytes(p.alloc), bytes(p.size))}</div>
          {p.scan && <div className="mt-2 text-[12.5px]">{p.scan.kind === "scrub" ? t("Scrub") : t("Resilver")}: {Math.round(p.scan.progress)} %</div>}
          {p.problems?.map((x) => <div key={x.device} className="mt-1 text-[12.5px] text-danger">{x.device}: {x.state}</div>)}
          {p.status && <p className="mt-2 text-[12px] text-muted">{p.status}</p>}
          <div className="mt-3 flex gap-2">
            <Button size="sm" disabled={!!p.scan} loading={scrub.isPending && scrub.variables === p.name} onClick={() => scrub.mutate(p.name)}>{t("Scrub")}</Button>
            <WebButton size="sm" path="/storage/zfs" label={t("Manage")} title={p.name} />
          </div>
        </Card>
      ))}
    </div>
  );
}
