// CodeMirror 6 editor. This module is loaded on demand (React.lazy), and each
// language pack is a separate chunk loaded when a file needs it.
import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import { EditorView, keymap } from "@codemirror/view";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import { indentWithTab } from "@codemirror/commands";
import { StreamLanguage } from "@codemirror/language";
import { oneDark } from "@codemirror/theme-one-dark";

async function language(lang: string): Promise<Extension> {
  switch (lang) {
    case "javascript":
    case "jsx":
    case "typescript":
    case "tsx": {
      const { javascript } = await import("@codemirror/lang-javascript");
      return javascript({ jsx: lang === "jsx" || lang === "tsx", typescript: lang === "typescript" || lang === "tsx" });
    }
    case "json": return (await import("@codemirror/lang-json")).json();
    case "yaml": return (await import("@codemirror/lang-yaml")).yaml();
    case "markdown": return (await import("@codemirror/lang-markdown")).markdown();
    case "html": return (await import("@codemirror/lang-html")).html();
    case "css": return (await import("@codemirror/lang-css")).css();
    case "python": return (await import("@codemirror/lang-python")).python();
    case "xml": return (await import("@codemirror/lang-xml")).xml();
    case "sql": return (await import("@codemirror/lang-sql")).sql();
    case "go": return (await import("@codemirror/lang-go")).go();
    case "shell": return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/shell")).shell);
    case "toml": return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/toml")).toml);
    case "ini": return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/properties")).properties);
    case "dockerfile": return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/dockerfile")).dockerFile);
    case "nginx": return StreamLanguage.define((await import("@codemirror/legacy-modes/mode/nginx")).nginx);
  }
  return [];
}

const base = EditorView.theme({
  "&": { height: "100%", fontSize: "13px" },
  ".cm-scroller": { fontFamily: "var(--font-mono)" },
  "&.cm-focused": { outline: "none" },
});
const light = EditorView.theme({
  "&": { backgroundColor: "var(--panel)", color: "var(--text)" },
  ".cm-gutters": { backgroundColor: "var(--panel-2)", color: "var(--muted)", borderRight: "1px solid var(--border)" },
  ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--accent-soft)" },
});

export interface CodeEditorProps {
  value: string;
  lang: string;
  readOnly: boolean;
  onChange: (doc: string) => void;
  onSave: () => void;
  /** Opens at the end (tail of a log). */
  scrollToEnd?: boolean;
}

export default function CodeEditor({ value, lang, readOnly, onChange, onSave, scrollToEnd }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const cb = useRef({ onChange, onSave });
  cb.current = { onChange, onSave };
  const view = useRef<EditorView | null>(null);
  const langSlot = useRef(new Compartment());
  const roSlot = useRef(new Compartment());

  // The document is set once: the parent remounts the editor (key) to reload.
  useEffect(() => {
    const dark = document.documentElement.dataset.theme === "dark";
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          basicSetup,
          keymap.of([indentWithTab, { key: "Mod-s", preventDefault: true, run: () => { cb.current.onSave(); return true; } }]),
          base,
          dark ? oneDark : light,
          langSlot.current.of([]),
          roSlot.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
          EditorView.updateListener.of((u) => { if (u.docChanged) cb.current.onChange(u.state.doc.toString()); }),
        ],
      }),
    });
    view.current = v;
    if (scrollToEnd) v.dispatch({ selection: { anchor: v.state.doc.length }, scrollIntoView: true });
    return () => { v.destroy(); view.current = null; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    let live = true;
    language(lang).then((ext) => { if (live) view.current?.dispatch({ effects: langSlot.current.reconfigure(ext) }); }).catch(() => {});
    return () => { live = false; };
  }, [lang]);

  useEffect(() => {
    view.current?.dispatch({ effects: roSlot.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) });
  }, [readOnly]);

  return <div ref={host} className="h-full min-h-0 overflow-hidden" />;
}
