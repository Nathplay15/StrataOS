import { useId } from "react";

/** Minimal responsive area chart for monitoring series. */
export function AreaChart({ series, height = 120, max, format }: { series: { values: number[]; color: string; label: string }[]; height?: number; max?: number; format?: (v: number) => string }) {
  const id = useId().replace(/:/g, "");
  const w = 600;
  const n = Math.max(...series.map((s) => s.values.length), 2);
  const top = max ?? Math.max(1, ...series.flatMap((s) => s.values)) * 1.15;
  const pts = (vals: number[]) =>
    vals.map((v, i) => `${((i + (n - vals.length)) / (n - 1)) * w},${height - (Math.min(v, top) / top) * (height - 4) - 2}`).join(" ");
  return (
    <div>
      <svg viewBox={`0 0 ${w} ${height}`} preserveAspectRatio="none" className="w-full" style={{ height }}>
        {[0.25, 0.5, 0.75].map((f) => (
          <line key={f} x1="0" x2={w} y1={height * f} y2={height * f} stroke="var(--border)" strokeDasharray="3 4" vectorEffect="non-scaling-stroke" />
        ))}
        {series.map((s, k) => {
          if (s.values.length < 2) return null;
          const p = pts(s.values);
          const first = p.split(" ")[0].split(",")[0];
          return (
            <g key={k}>
              <defs>
                <linearGradient id={`${id}-${k}`} x1="0" x2="0" y1="0" y2="1">
                  <stop offset="0" stopColor={s.color} stopOpacity="0.28" />
                  <stop offset="1" stopColor={s.color} stopOpacity="0" />
                </linearGradient>
              </defs>
              <polygon points={`${first},${height} ${p} ${w},${height}`} fill={`url(#${id}-${k})`} />
              <polyline points={p} fill="none" stroke={s.color} strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
            </g>
          );
        })}
      </svg>
      <div className="mt-2 flex flex-wrap gap-4 text-[12px] text-muted">
        {series.map((s) => (
          <span key={s.label} className="flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: s.color }} />
            {s.label}
            {s.values.length > 0 && <b className="text-fg font-medium">{format ? format(s.values[s.values.length - 1]) : s.values[s.values.length - 1].toFixed(1)}</b>}
          </span>
        ))}
      </div>
    </div>
  );
}

/** Circular gauge used on the dashboard. */
export function Ring({ value, size = 92, label, sub, color = "var(--accent)" }: { value: number; size?: number; label: string; sub?: string; color?: string }) {
  const r = (size - 10) / 2;
  const c = 2 * Math.PI * r;
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className="flex flex-col items-center gap-1">
      <div className="relative" style={{ width: size, height: size }}>
        <svg width={size} height={size} className="-rotate-90">
          <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--border)" strokeWidth="8" />
          <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke={color} strokeWidth="8" strokeLinecap="round" strokeDasharray={c} strokeDashoffset={c - (v / 100) * c} style={{ transition: "stroke-dashoffset .6s" }} />
        </svg>
        <div className="absolute inset-0 flex items-center justify-center text-[17px] font-semibold">{Math.round(v)}%</div>
      </div>
      <div className="text-[13px] font-medium">{label}</div>
      {sub && <div className="text-[11.5px] text-muted">{sub}</div>}
    </div>
  );
}
