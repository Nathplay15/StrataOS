// Interface language. Texts are written in French in the code and wrapped in
// tr("…"); the English dictionary maps them (missing entries stay in French).
import en from "../i18n/en.json";

export type Lang = "fr" | "en";

function initial(): Lang {
  try {
    const saved = localStorage.getItem("strata-lang");
    if (saved === "fr" || saved === "en") return saved;
  } catch {
    /* private mode */
  }
  return (navigator.language || "en").toLowerCase().startsWith("fr") ? "fr" : "en";
}

export const lang: Lang = initial();
export const locale = lang === "en" ? "en-GB" : "fr-FR";
document.documentElement.lang = lang;

const dict = en as Record<string, string>;

/** Translates a French UI text; {0}, {1}… are replaced by the arguments. */
export function tr(fr: string, ...args: (string | number | null | undefined)[]): string {
  let s = lang === "en" ? (dict[fr] ?? fr) : fr;
  args.forEach((a, i) => { s = s.split(`{${i}}`).join(String(a ?? "")); });
  return s;
}

/** Changes the language (the interface reloads to apply it everywhere). */
export function setLang(l: Lang) {
  try {
    localStorage.setItem("strata-lang", l);
  } catch {
    /* ignore */
  }
  location.reload();
}

// Background job titles come from the server, partly in French, partly in English.
const jobPrefixes: [string, string][] = [
  ["Certificat Let's Encrypt pour ", "Let's Encrypt certificate for "],
  ["Mise à jour de StrataOS", "StrataOS update"],
  ["Mise à jour du système", "System update"],
  ["Recherche de mises à jour", "Checking for updates"],
  ["Sauvegarde : ", "Backup: "],
  ["Synchronisation cloud : ", "Cloud sync: "],
  ["Jonction à l'annuaire", "Joining the directory"],
  ["Activer les quotas sur ", "Enable quotas on "],
  ["Restauration : ", "Restore: "],
  ["Tâche planifiée : ", "Scheduled task: "],
  ["Création de la VM ", "Creating VM "],
  ["Installation de ", "Install "],
  ["Création du conteneur ", "Create container "],
  ["Téléchargement de l'image ", "Pull "],
  ["Création de la grappe RAID ", "Create RAID "],
  ["Déploiement de la stack ", "Deploy stack "],
  ["Création du volume ", "Create volume "],
  ["Création du pool ZFS ", "Create ZFS pool "],
  ["Équilibrage ", "Balance "],
];

/** Translates a job title sent by the server. */
export function jobTitle(title: string): string {
  for (const [fr, en] of jobPrefixes) {
    const [from, to] = lang === "en" ? [fr, en] : [en, fr];
    if (title.startsWith(from)) return to + title.slice(from.length);
  }
  return title;
}
