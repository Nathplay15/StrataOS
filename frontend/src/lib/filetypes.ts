// What the file manager does with a file, from its name.

export type FileKind = "text" | "markdown" | "image" | "pdf" | "video" | "audio" | "docx" | "sheet" | "document" | "archive" | "other";

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

/** Pictures the browser shows as they are (SVG through <img> only). */
const nativeImages = /^(jpe?g|jfif|png|gif|webp|bmp|avif|ico|svg)$/;
/** Pictures converted by the NAS (libvips, ffmpeg, exiftool). */
const convertedImages = /^(heic|heif|hif|tiff?|psd|psb|jxl|tga|ppm|pgm|pbm|pnm|exr|jp2|j2k|dds|pcx|sgi|xcf|cr2|cr3|crw|nef|nrw|arw|srf|sr2|dng|orf|rw2|raf|pef|srw|x3f|3fr|kdc|dcr|mrw|erf|mos|iiq|rwl)$/;
const videos = /^(mp4|m4v|webm|ogv|mkv|avi|mov|qt|wmv|asf|flv|f4v|mpg|mpeg|m2v|mpe|m2ts|mts|vob|3gp|3g2|ogm|divx|xvid|rm|rmvb|dv|mxf|y4m)$/;
const audios = /^(mp3|ogg|oga|opus|wav|m4a|m4b|flac|aac|wma|ape|alac|mka|ac3|eac3|dts|aiff?|aifc|amr|wv|tta|au|caf|mpc|spx|dsf|dff)$/;
const archives = /\.(zip|jar|cbz|epub|tar|tgz|tbz2?|txz|tzst|7z|rar|cbr|gz|bz2|xz|zst)$/;

export function fileKind(name: string): FileKind {
  const e = ext(name);
  if (e === "md" || e === "markdown") return "markdown";
  if (nativeImages.test(e) || convertedImages.test(e)) return "image";
  if (e === "pdf") return "pdf";
  if (videos.test(e)) return "video";
  if (audios.test(e)) return "audio";
  if (/^(docx|docm|dotx)$/.test(e)) return "docx";
  if (/^(xlsx|xlsm|xls|ods|csv|tsv|fods)$/.test(e)) return "sheet";
  if (/^(doc|dot|odt|ott|fodt|rtf|wpd|wps|pages|ppt|pptx|pptm|pps|ppsx|odp|otp|fodp|key|odg|vsd|vsdx|xlsb|numbers)$/.test(e)) return "document";
  if (archives.test(name.toLowerCase()) && !/^\.[^.]+$/.test(name)) return "archive";
  if (langs[e] || plain.has(e) || plainNames.has(name.toLowerCase()) || name.startsWith(".")) return "text";
  return "other";
}

/** Pictures the browser cannot show itself: the NAS converts them. */
export const imageNeedsConversion = (name: string) => convertedImages.test(ext(name));

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
