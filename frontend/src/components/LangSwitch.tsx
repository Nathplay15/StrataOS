import { Languages } from "lucide-react";
import { lang, setLang } from "../lib/i18n";

/** Switches the interface between French and English (the page reloads). */
export function LangSwitch({ className }: { className?: string }) {
  const other = lang === "fr" ? "en" : "fr";
  return (
    <button type="button" className={className} onClick={() => setLang(other)} title={other === "en" ? "Switch to English" : "Passer en français"}>
      <Languages size={15} /> {other === "en" ? "English" : "Français"}
    </button>
  );
}
