import logo from "../logo.svg";
import { tr } from "../lib/i18n";

/** Loading screen: logo and spinner in the colours of the theme (styles in index.html, shown before the scripts load). */
export function Splash({ label }: { label?: string }) {
  return (
    <div className="strata-splash z-[10000] bg-bg" role="status" aria-live="polite" aria-label={label ?? tr("Loading…")}>
      <img src={logo} alt="" />
      <i />
      {label && <span className="text-[13px] text-muted">{label}</span>}
    </div>
  );
}
