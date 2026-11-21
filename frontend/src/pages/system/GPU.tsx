import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Clapperboard, MonitorPlay, RefreshCw } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { useJobs } from "../../lib/jobs";
import { dateTime } from "../../lib/format";
import { Badge, Button, Card, Field, Modal, Notice, Select, Switch, useUI } from "../../components/ui";
import { srv, tr } from "../../lib/i18n";

type VA = { driver: string; name: string; decode: string[]; encode: string[]; lowPower?: boolean };
type GPUView = {
  node: string; card?: string; slot?: string; vendor: string; vendorId?: string; deviceId?: string; model?: string; kernel?: string;
  va?: VA; vaError?: string; nvenc?: boolean; group?: string; perm?: string; worldWritable?: boolean;
  hints: { level: string; text: string }[];
};
type AppSettings = { method: "" | "qsv" | "vaapi"; device?: string; decode: string[]; hevc: boolean; lowPower?: boolean };
export type GPUData = {
  gpus: GPUView[]; settings: { accel?: string; device?: string; apps?: string[] };
  status: { encoder: string; device?: string; disabled?: boolean; gpuStreams: number; cpuStreams: number; fallbacks: number; lastReason?: string; lastAt?: string };
  ffmpeg: boolean; vainfo: boolean; nonFree: boolean; renderGroup: boolean;
  apps: { stack: string; app: string; enabled: boolean }[]; appSettings: AppSettings;
};

export const useGPU = () => useQuery({ queryKey: ["gpu"], queryFn: () => api.get<GPUData>("/api/hardware/gpu") });

const vendorName = (v: string) => ({ intel: "Intel", amd: "AMD", nvidia: "NVIDIA" }[v] ?? tr("Other"));

/** Graphics processors and video conversion (Hardware page). */
export function GPUCard() {
  const qc = useQueryClient();
  const { data } = useGPU();
  const refresh = useAction(() => api.get<GPUData>("/api/hardware/gpu?fresh=1").then((d) => qc.setQueryData(["gpu"], d)));
  const save = useAction((v: { accel?: string; device?: string }) => api.put("/api/hardware/gpu", v), { success: tr("Video conversion settings saved") });
  const install = useAction(() => api.post("/api/system/components/photos/install"));
  if (!data) return null;
  const st = data.status;
  const accel = data.settings.accel ?? "";
  return (
    <Card className="@3xl:col-span-2" title={<span className="flex items-center gap-2"><MonitorPlay size={16} />{tr("Graphics and video conversion")}</span>}
      actions={<Button size="sm" variant="ghost" icon={<RefreshCw size={14} />} loading={refresh.isPending} onClick={() => refresh.mutate()}>{tr("Detect again")}</Button>}>
      <div className="space-y-4 text-[13px]">
        {data.gpus.length === 0 && <div className="text-muted">{tr("No graphics processor with video acceleration (no /dev/dri render node). Videos are converted by the processor (CPU).")}</div>}
        {data.gpus.map((g) => (
          <div key={g.node} className="rounded-lg border border-line p-3">
            <div className="flex flex-wrap items-center gap-2">
              <b>{vendorName(g.vendor)}</b><span>{g.model}</span>
              <span className="mono text-muted text-[11.5px]">{g.node}{g.kernel && ` · ${g.kernel}`}{g.slot && ` · ${g.slot}`}</span>
              {g.va && <Badge tone="ok" dot>{tr("VA-API: {0}", g.va.name || g.va.driver)}</Badge>}
              {g.nvenc && <Badge tone="ok" dot>NVENC</Badge>}
              {st.device === g.node && st.encoder && <Badge tone="accent">{tr("Used by the NAS")}</Badge>}
            </div>
            {g.va && <div className="mt-2 grid gap-1 sm:grid-cols-2">
              <div><span className="text-muted">{tr("Decodes")}: </span>{g.va.decode.join(", ") || "—"}</div>
              <div><span className="text-muted">{tr("Encodes")}: </span>{g.va.encode.join(", ") || "—"}{g.va.lowPower && <span className="text-muted"> ({tr("low-power engine")})</span>}</div>
              <div className="text-muted text-[11.5px] sm:col-span-2">{g.va.driver}</div>
            </div>}
            {g.vaError && <div className="mt-1 text-muted text-[12px]">{tr("vainfo")}: {g.vaError}</div>}
            {g.perm && <div className="mt-1 text-muted text-[11.5px]">{tr("Access")}: <span className="mono">{g.perm} {g.group}</span></div>}
            {g.hints.map((h, i) => <div key={i} className="mt-2"><Notice tone={h.level === "warn" ? "warn" : "info"}>{srv(h.text)}</Notice></div>)}
          </div>
        ))}
        {(!data.vainfo || !data.ffmpeg) && data.gpus.length > 0 && (
          <div className="flex flex-wrap items-center gap-3">
            <span className="text-muted">{tr("The media tools (ffmpeg, vainfo and the video drivers) are not installed.")}</span>
            <Button size="sm" loading={install.isPending} onClick={() => install.mutate()}>{tr("Install the media tools")}</Button>
          </div>
        )}
        <div className="grid gap-4 border-t border-line pt-4 sm:grid-cols-2">
          <Field label={tr("Videos played on the NAS (Files, Surveillance)")} hint={tr("Files the browser cannot play are converted on the fly. If the graphics processor fails, the processor (CPU) takes over automatically.")}>
            <Select value={accel === "off" ? "off" : data.settings.device || ""} onChange={(e) => save.mutate(e.target.value === "off" ? { accel: "off" } : { device: e.target.value })}>
              <option value="">{tr("Graphics processor when available (automatic)")}</option>
              {data.gpus.filter((g) => g.va?.encode.includes("H.264") || g.nvenc).map((g) => <option key={g.node} value={g.node}>{vendorName(g.vendor)} {g.node}</option>)}
              <option value="off">{tr("Always the processor (CPU)")}</option>
            </Select>
          </Field>
          <div className="space-y-1 text-[12.5px]">
            <div>{tr("Now")}: <b>{st.encoder === "vaapi" ? tr("graphics processor (VA-API)") : st.encoder === "nvenc" ? tr("graphics processor (NVENC)") : tr("processor (CPU)")}</b>
              {st.disabled && <Badge tone="warn">{tr("Graphics processor off after a failure")}</Badge>}</div>
            <div className="text-muted">{tr("Since start: {0} conversions on the graphics processor, {1} on the CPU, {2} fallbacks", st.gpuStreams, st.cpuStreams, st.fallbacks)}</div>
            {st.lastReason && <div className="text-muted">{tr("Last fallback ({0})", st.lastAt ? dateTime(st.lastAt) : "")}: <span className="mono text-[11.5px]">{st.lastReason}</span></div>}
            {st.disabled && <p className="text-muted">{tr("Saving this setting again tries the graphics processor again.")}</p>}
          </div>
        </div>
        {data.apps.length > 0 && <div className="border-t border-line pt-4">
          <div className="mb-2 flex items-center gap-2 font-semibold"><Clapperboard size={14} />{tr("Media server apps")}</div>
          {data.apps.map((a) => <div key={a.stack} className="flex items-center justify-between gap-2"><span>{a.stack}</span><AppGPUButton stack={a.stack} /></div>)}
        </div>}
      </div>
    </Card>
  );
}

/** "Use the graphics card" of an installed Jellyfin or Plex (App Center card, hardware page). */
export function AppGPUButton({ stack }: { stack: string }) {
  const { data } = useGPU();
  const [open, setOpen] = useState(false);
  const app = data?.apps.find((a) => a.stack === stack);
  if (!data || !app) return null;
  return (
    <>
      <Button size="sm" variant="ghost" icon={<MonitorPlay size={14} className={app.enabled ? "text-ok" : undefined} />} title={tr("Use the graphics card")} onClick={() => setOpen(true)} />
      {open && <AppGPUModal data={data} app={app} onClose={() => setOpen(false)} />}
    </>
  );
}

function AppGPUModal({ data, app, onClose }: { data: GPUData; app: GPUData["apps"][number]; onClose: () => void }) {
  const qc = useQueryClient();
  const { toast } = useUI();
  const { track } = useJobs();
  const [busy, setBusy] = useState(false);
  const s = data.appSettings;
  const toggle = async (on: boolean) => {
    setBusy(true);
    try {
      const r = await api.put<{ job?: unknown }>(`/api/hardware/gpu/apps/${app.stack}`, { enabled: on });
      if (r?.job) track(r.job);
    } catch (e) {
      toast(srv((e as Error).message), "error");
    } finally {
      setBusy(false);
      qc.invalidateQueries({ queryKey: ["gpu"] });
    }
  };
  const row = (k: string, v: string) => <div className="flex gap-2"><span className="text-muted">{k}</span><b className="mono">{v}</b></div>;
  return (
    <Modal open onClose={onClose} title={tr("Use the graphics card")} footer={<Button onClick={onClose}>{tr("Close")}</Button>}>
      <div className="space-y-3 text-[13.5px]">
        <p>{tr("{0} gets the graphics devices (/dev/dri) and the render group; StrataOS restarts it.", app.stack)}</p>
        {!s.method && <Notice tone="warn">{tr("No Intel or AMD graphics processor with a working video driver was found: the app would convert videos on the CPU.")}</Notice>}
        <Switch checked={app.enabled} disabled={busy} onChange={toggle} label={tr("Use the graphics card")} />
        {app.enabled && s.method && <div className="space-y-1.5 rounded-lg border border-line p-3 text-[12.5px]">
          {app.app === "jellyfin" ? <>
            <div className="font-semibold">{tr("Then in Jellyfin: Dashboard › Playback › Transcoding")}</div>
            {row(tr("Hardware acceleration"), s.method === "qsv" ? "Intel QuickSync (QSV)" : "Video Acceleration API (VAAPI)")}
            {row(s.method === "qsv" ? tr("QSV device") : tr("VA-API device"), s.device ?? "")}
            {row(tr("Enable hardware decoding for"), s.decode.join(", ") || "—")}
            {s.hevc && <div>{tr("You may tick \"Allow encoding in HEVC format\".")}</div>}
            {s.lowPower && <div>{tr("Tick \"Enable Intel Low-Power H.264 hardware encoder\" (and HEVC).")}</div>}
            <div className="text-muted">{tr("Jellyfin's image brings its own video drivers: the drivers of the NAS are not needed for it.")}</div>
          </> : <>
            <div className="font-semibold">{tr("Then in Plex: Settings › Transcoder (needs Plex Pass)")}</div>
            <div>{tr("Tick \"Use hardware acceleration when available\" and \"Use hardware-accelerated video encoding\".")}</div>
            {row(tr("Hardware transcoding device"), s.device ?? "")}
          </>}
        </div>}
        <p className="text-[12.5px] text-muted">{tr("NVIDIA cards are not passed to apps: they need the NVIDIA container toolkit.")}</p>
      </div>
    </Modal>
  );
}
