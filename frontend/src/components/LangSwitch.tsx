import { Languages } from "lucide-react";
import { lang, languages, setLang, type Lang } from "../lib/i18n";

/** Language picker (the page reloads to apply the choice everywhere). */
export function LangSwitch({ className }: { className?: string }) {
  return (
    <label className={className} title="Language">
      <Languages size={15} className="shrink-0" />
      <select value={lang} onChange={(e) => setLang(e.target.value as Lang)} className="min-w-0 flex-1 cursor-pointer bg-transparent outline-none">
        {languages.map((l) => <option key={l.id} value={l.id} className="bg-panel text-fg">{l.name}</option>)}
      </select>
    </label>
  );
}
