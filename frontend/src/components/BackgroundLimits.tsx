import { tr } from "../lib/i18n";
import { Field, Input, Switch } from "./ui";

// Limits of background work (threads, hours, busy NAS), used by the smart
// photos analysis.

export type Limits = { threads?: number; hours?: string; anyLoad?: boolean };

export const PAUSE_REASONS: Record<string, string> = {
  hours: tr("Waiting for the allowed hours"),
  busy: tr("Paused while the NAS is busy"),
  shutdown: tr("Paused before the scheduled shutdown"),
};

/** Limits of background work (threads, hours, busy NAS). */
export function LimitsFields({ value, onChange }: { value: Limits; onChange: (l: Limits) => void }) {
  const [from, to] = (value.hours ?? "").split("-");
  const setHours = (f: string, t: string) => onChange({ ...value, hours: f && t ? `${f}-${t}` : "" });
  return (
    <div className="space-y-3">
      <Field label={tr("Processor threads")} hint={tr("0 = automatic (a quarter of the processor cores). The analysis always runs at the lowest priority.")}>
        <Input type="number" min={0} max={64} value={value.threads ?? 0} onChange={(e) => onChange({ ...value, threads: Math.max(0, Number(e.target.value) || 0) })} />
      </Field>
      <Field label={tr("Only between")} hint={tr("Leave empty to allow any time, for example 01:00 and 07:00 to work at night only.")}>
        <div className="flex items-center gap-2">
          <Input type="time" value={from ?? ""} onChange={(e) => setHours(e.target.value, to ?? "")} />
          <span className="text-muted">–</span>
          <Input type="time" value={to ?? ""} onChange={(e) => setHours(from ?? "", e.target.value)} />
        </div>
      </Field>
      <Switch checked={!value.anyLoad} onChange={(v) => onChange({ ...value, anyLoad: !v })} label={tr("Pause while the NAS is busy")} />
    </div>
  );
}
