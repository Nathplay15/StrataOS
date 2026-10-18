import { Fragment, type ReactNode } from "react";
import { LifeBuoy } from "lucide-react";
import guideEn from "../../../docs/GUIDE.md?raw";
import guideFr from "../../../docs/GUIDE.fr.md?raw";
import { Card, PageHeader } from "../components/ui";
import { lang, tr } from "../lib/i18n";

// The guide exists in English and French.
const guide = lang === "fr" ? guideFr : guideEn;

// Inline Markdown: **bold**, `code`, [text](url).
function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)]+\))/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index! > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (t.startsWith("**")) out.push(<b key={out.length}>{t.slice(2, -2)}</b>);
    else if (t.startsWith("`")) out.push(<code key={out.length} className="mono rounded bg-panel-2 px-1 text-[12px]">{t.slice(1, -1)}</code>);
    else {
      const [, label, href] = /\[([^\]]+)\]\(([^)]+)\)/.exec(t)!;
      out.push(<a key={out.length} href={href} target="_blank" rel="noreferrer" className="text-accent hover:underline">{label}</a>);
    }
    last = m.index! + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/** Renders the user guide (headings, paragraphs, lists). */
function Markdown({ src }: { src: string }) {
  const blocks: ReactNode[] = [];
  let list: { ordered: boolean; items: string[] } | null = null;
  const flush = () => {
    if (!list) return;
    const Tag = list.ordered ? "ol" : "ul";
    blocks.push(<Tag key={blocks.length} className={`${list.ordered ? "list-decimal" : "list-disc"} space-y-1 pl-6`}>{list.items.map((it, i) => <li key={i}>{inline(it)}</li>)}</Tag>);
    list = null;
  };
  for (const raw of src.split("\n")) {
    const line = raw.trimEnd();
    const item = /^\s*(-|\d+\.)\s+(.*)$/.exec(line);
    if (item) {
      const ordered = item[1] !== "-";
      if (!list || list.ordered !== ordered) { flush(); list = { ordered, items: [] }; }
      list.items.push(item[2]);
      continue;
    }
    flush();
    if (!line) continue;
    if (line.startsWith("# ")) continue; // the page header shows the title
    if (line.startsWith("## ")) blocks.push(<h2 key={blocks.length} className="mt-6 text-[16px] font-semibold">{inline(line.slice(3))}</h2>);
    else blocks.push(<p key={blocks.length}>{inline(line)}</p>);
  }
  flush();
  return <div className="space-y-3 text-[13.5px] leading-relaxed">{blocks.map((b, i) => <Fragment key={i}>{b}</Fragment>)}</div>;
}

export default function Help() {
  return (
    <div>
      <PageHeader title={tr("Help")} subtitle={tr("StrataOS user guide")} icon={<LifeBuoy size={20} />} />
      <Card><div className="mx-auto max-w-3xl"><Markdown src={guide} /></div></Card>
    </div>
  );
}
