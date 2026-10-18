import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "../lib/api";
import { tr } from "../lib/i18n";

/** Interactive terminal bound to a StrataOS websocket endpoint (host shell or container exec). */
export function Terminal({ path, height = 480 }: { path: string; height?: number | string }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!ref.current) return;
    const term = new XTerm({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "JetBrains Mono, ui-monospace, Menlo, monospace",
      theme: { background: "#0b0f1c", foreground: "#e6e9f2", cursor: "#6b83ff", selectionBackground: "#2a3557" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(ref.current);
    fit.fit();
    const ws = new WebSocket(wsURL(path));
    ws.binaryType = "arraybuffer";
    const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));
    ws.onopen = () => {
      send({ type: "resize", cols: term.cols, rows: term.rows });
      term.focus();
    };
    ws.onmessage = (e) => term.write(typeof e.data === "string" ? e.data : new Uint8Array(e.data));
    ws.onclose = () => term.write(`\r\n\x1b[90m[${tr("connection closed")}]\x1b[0m\r\n`);
    const d1 = term.onData((data) => send({ type: "input", data }));
    const d2 = term.onResize(({ cols, rows }) => send({ type: "resize", cols, rows }));
    const ro = new ResizeObserver(() => fit.fit());
    ro.observe(ref.current);
    return () => {
      ro.disconnect();
      d1.dispose();
      d2.dispose();
      ws.close();
      term.dispose();
    };
  }, [path]);

  return <div ref={ref} className="overflow-hidden rounded-xl border border-line bg-[#0b0f1c]" style={{ height }} />;
}
