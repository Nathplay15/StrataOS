import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Check, ImagePlus, Trash2 } from "lucide-react";
import { Button, Modal, useUI } from "./ui";
import { cx } from "../lib/format";
import { tr, srv } from "../lib/i18n";
import { ACCENT_PRESETS, DEFAULT_ACCENT, accentVars } from "../lib/accent";
import { usePrefs, useSavePrefs } from "../lib/appearance";
import { WALLPAPERS, DEFAULT_WALLPAPER, legacyWallpaper, wallpaperCSS } from "../desktop/wallpapers";

const MAX_UPLOAD = 5 << 20;

/**
 * Prepares an image for upload: any format the browser can read, scaled down
 * to 3840 × 2160 at most and turned into a JPEG under 5 MB. The NAS checks
 * and re-encodes it again.
 */
async function prepareImage(file: File): Promise<Blob> {
  let bmp: ImageBitmap;
  try {
    bmp = await createImageBitmap(file);
  } catch {
    throw new Error(tr("This image cannot be read"));
  }
  const k = Math.min(1, 3840 / bmp.width, 2160 / bmp.height);
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(bmp.width * k));
  canvas.height = Math.max(1, Math.round(bmp.height * k));
  canvas.getContext("2d")!.drawImage(bmp, 0, 0, canvas.width, canvas.height);
  bmp.close();
  for (const q of [0.88, 0.75, 0.6]) {
    const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, "image/jpeg", q));
    if (blob && blob.size <= MAX_UPLOAD) return blob;
  }
  throw new Error(tr("The image is too large"));
}

/** Account menu → Appearance: wallpaper of the desktop mode and accent colour, kept on the NAS. */
export function AppearanceModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const prefs = usePrefs();
  const save = useSavePrefs();
  const qc = useQueryClient();
  const { toast } = useUI();
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const p = prefs.data;
  const wall = p?.wallpaper || legacyWallpaper() || DEFAULT_WALLPAPER;
  const accent = p?.accent ?? "";
  const fail = (e: unknown) => toast(srv((e as Error).message), "error");
  const pick = (patch: { wallpaper?: string; accent?: string }) => save(patch).catch(fail);
  // The colour picker sends a value per mouse move: save once it settles.
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const pickLater = (accent: string) => {
    qc.setQueryData(["desktop-prefs"], (old: object | undefined) => (old ? { ...old, accent } : old));
    clearTimeout(timer.current);
    timer.current = setTimeout(() => pick({ accent }), 400);
  };

  const upload = async (file: File) => {
    setBusy(true);
    try {
      const blob = await prepareImage(file);
      const res = await fetch("/api/me/wallpaper", { method: "PUT", headers: { "Content-Type": "image/jpeg", "X-Strata": "1" }, body: blob, credentials: "same-origin" });
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error ?? res.statusText);
      await qc.invalidateQueries({ queryKey: ["desktop-prefs"] });
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const removeImage = async () => {
    setBusy(true);
    try {
      await fetch("/api/me/wallpaper", { method: "DELETE", headers: { "X-Strata": "1" }, credentials: "same-origin" });
      await qc.invalidateQueries({ queryKey: ["desktop-prefs"] });
    } finally {
      setBusy(false);
    }
  };

  const tile = "relative h-20 overflow-hidden rounded-xl border-2 transition";
  return (
    <Modal open={open} onClose={onClose} title={tr("Appearance")} wide footer={<Button variant="primary" onClick={onClose}>{tr("Close")}</Button>}>
      <div className="space-y-6">
        <section>
          <h3 className="mb-1 font-medium">{tr("Wallpaper")}</h3>
          <p className="mb-3 text-muted text-[12.5px]">{tr("Shown behind the desktop mode. Your own image stays on the NAS, in your account only.")}</p>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {Object.entries(WALLPAPERS).map(([id, w]) => (
              <button key={id} type="button" onClick={() => pick({ wallpaper: id })} className={cx(tile, wall === id ? "border-accent" : "border-transparent hover:border-line")} style={{ background: w.css }} aria-pressed={wall === id}>
                <span className="absolute inset-x-0 bottom-0 bg-black/45 px-2 py-1 text-left text-[11.5px] text-white">{w.label}</span>
                {wall === id && <Check size={16} className="absolute right-1.5 top-1.5 rounded-full bg-accent p-0.5 text-white" />}
              </button>
            ))}
            {p?.customWallpaper ? (
              <button type="button" onClick={() => pick({ wallpaper: "custom" })} className={cx(tile, wall === "custom" ? "border-accent" : "border-transparent hover:border-line")} style={{ background: wallpaperCSS("custom", p.customWallpaper) }} aria-pressed={wall === "custom"}>
                <span className="absolute inset-x-0 bottom-0 bg-black/45 px-2 py-1 text-left text-[11.5px] text-white">{tr("My image")}</span>
                {wall === "custom" && <Check size={16} className="absolute right-1.5 top-1.5 rounded-full bg-accent p-0.5 text-white" />}
              </button>
            ) : null}
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <input ref={input} type="file" accept="image/*" className="hidden" onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; if (f) upload(f); }} />
            <Button size="sm" icon={<ImagePlus size={14} />} loading={busy} onClick={() => input.current?.click()}>{p?.customWallpaper ? tr("Replace my image") : tr("Use my own image")}</Button>
            {!!p?.customWallpaper && <Button size="sm" variant="ghost" icon={<Trash2 size={14} />} disabled={busy} onClick={removeImage}>{tr("Remove my image")}</Button>}
            <span className="text-muted text-[12px]">{tr("Any image format; scaled down to 3840 × 2160 and stored as JPEG (5 MB at most).")}</span>
          </div>
        </section>

        <section>
          <h3 className="mb-1 font-medium">{tr("Accent colour")}</h3>
          <p className="mb-3 text-muted text-[12.5px]">{tr("Buttons, links and selections, in the light and dark themes. It is adjusted a little in each theme so text stays readable.")}</p>
          <div className="flex flex-wrap items-center gap-2.5">
            {ACCENT_PRESETS.map((c) => {
              const on = (accent || DEFAULT_ACCENT) === c;
              return (
                <button key={c} type="button" title={c} aria-label={c} aria-pressed={on} onClick={() => pick({ accent: c === DEFAULT_ACCENT ? "" : c })} className={cx("flex h-9 w-9 items-center justify-center rounded-full ring-offset-2 ring-offset-panel transition", on && "ring-2 ring-fg")} style={{ background: c }}>
                  {on && <Check size={16} className="text-white" />}
                </button>
              );
            })}
            <label className="flex items-center gap-2 rounded-lg border border-line px-2.5 py-1.5 text-[12.5px]">
              {tr("Custom")}
              <input type="color" value={accent || DEFAULT_ACCENT} onChange={(e) => pickLater(e.target.value.toLowerCase())} className="h-6 w-8 cursor-pointer border-0 bg-transparent p-0" />
            </label>
            {accent && <Button size="sm" variant="ghost" onClick={() => pick({ accent: "" })}>{tr("Default")}</Button>}
          </div>
          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            {(["light", "dark"] as const).map((t) => {
              const v = accentVars(accent || DEFAULT_ACCENT, t);
              return (
                <div key={t} className="flex items-center gap-3 rounded-xl border border-line p-3" style={{ background: t === "light" ? "#ffffff" : "#121829", color: t === "light" ? "#0f172a" : "#e6e9f2" }}>
                  <span className="rounded-lg px-3 py-1.5 text-[12.5px] font-medium text-white" style={{ background: v.accent }}>{tr("Button")}</span>
                  <span className="text-[12.5px] font-medium" style={{ color: v.accent }}>{tr("Link")}</span>
                  <span className="ml-auto text-[11.5px] opacity-70">{t === "light" ? tr("Light theme") : tr("Dark theme")}</span>
                </div>
              );
            })}
          </div>
        </section>
      </div>
    </Modal>
  );
}
