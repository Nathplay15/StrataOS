// What the file manager does with a file, from its name.

export type FileKind = "text" | "markdown" | "image" | "pdf" | "video" | "audio" | "other";

const ext = (name: string) => (name.includes(".") ? name.split(".").pop()!.toLowerCase() : "");

/** Language of the code editor for each extension (see CodeEditor). */
const langs: Record<string, string> = {
  js: "javascript", mjs: "javascript", cjs: "javascript", jsx: "jsx", ts: "typescript", tsx: "tsx", mts: "typescript",
  json: "json", jsonc: "json", webmanifest: "json",
  yml: "yaml", yaml: "yaml",
  md: "markdown", markdown: "markdown",
  html: "html", htm: "html", vue: "html", svelte: "html",
  css: "css", scss: "css", less: "css",
  py: "python", pyw: "python",
  sh: "shell", bash: "shell", zsh: "shell", ksh: "shell", env: "shell",
  ini: "ini", conf: "ini", cfg: "ini", properties: "ini", service: "ini", timer: "ini", desktop: "ini",
  toml: "toml",
  xml: "xml", svg: "xml", plist: "xml", xsd: "xml", xsl: "xml", rss: "xml", kml: "xml", gpx: "xml",
  sql: "sql",
  go: "go", mod: "go",
};

/** Plain text without highlighting. */
const plain = new Set(["txt", "log", "csv", "tsv", "text", "nfo", "srt", "vtt", "diff", "patch", "gitignore", "lock", "rst", "tex", "c", "h", "cpp", "hpp", "rs", "java", "kt", "rb", "php", "pl", "lua", "r", "swift", "cs", "dart", "ex", "exs", "erl", "hs", "ml", "nix", "tf", "hcl", "gradle", "cmake", "mk", "nginx", "dockerfile", "rules", "list", "sources", "crt", "pem", "pub", "asc", "m3u", "cue"]);
const plainNames = new Set(["dockerfile", "makefile", "readme", "license", "changelog", "authors", "hosts", "fstab", "crontab", "vagrantfile", "jenkinsfile", "procfile"]);

export function fileKind(name: string): FileKind {
  const e = ext(name);
  if (e === "md" || e === "markdown") return "markdown";
  if (/^(jpe?g|png|gif|webp|bmp|avif)$/.test(e)) return "image";
  if (e === "pdf") return "pdf";
  if (/^(mp4|m4v|webm|ogv)$/.test(e)) return "video";
  if (/^(mp3|ogg|oga|opus|wav|m4a|flac)$/.test(e)) return "audio";
  if (langs[e] || plain.has(e) || plainNames.has(name.toLowerCase()) || name.startsWith(".")) return "text";
  return "other";
}

export function editorLang(name: string): string {
  const n = name.toLowerCase();
  if (n === "dockerfile" || n.endsWith(".dockerfile")) return "dockerfile";
  if (n === "makefile" || n === ".bashrc" || n === ".profile") return "shell";
  if (n.endsWith(".nginx") || n === "nginx.conf") return "nginx";
  return langs[ext(name)] ?? "";
}

/** Office document types OnlyOffice opens (sent by the server). */
export interface OfficeStatus {
  enabled: boolean;
  serverUrl: string;
  port: string;
  types: Record<string, string>;
  editable: Record<string, boolean>;
}

export const officeType = (status: OfficeStatus | undefined, name: string) => (status?.enabled ? status.types[ext(name)] : undefined);
export const fileExt = ext;
