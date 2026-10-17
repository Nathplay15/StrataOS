import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode, type ButtonHTMLAttributes, type InputHTMLAttributes, type SelectHTMLAttributes } from "react";
import { createPortal } from "react-dom";
import { AlertTriangle, CheckCircle2, Info, Loader2, X, XCircle } from "lucide-react";
import { cx } from "../lib/format";
import { InWindow } from "../desktop/context";

/* ---------- Buttons ---------- */

type Variant = "primary" | "secondary" | "ghost" | "danger";

export function Button({ variant = "secondary", size = "md", loading, icon, className, children, disabled, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm" | "md"; loading?: boolean; icon?: ReactNode }) {
  const styles: Record<Variant, string> = {
    primary: "bg-accent text-white hover:brightness-110 shadow-sm",
    secondary: "bg-panel-2 border border-line hover:border-accent/50 text-fg",
    ghost: "hover:bg-panel-2 text-fg",
    danger: "bg-danger text-white hover:brightness-110",
  };
  return (
    <button
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-[9px] font-medium transition disabled:opacity-50 disabled:pointer-events-none whitespace-nowrap",
        size === "sm" ? "px-2.5 py-1.5 text-[12.5px]" : "px-3.5 py-2 text-[13.5px]",
        styles[variant],
        className,
      )}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 size={15} className="animate-spin" /> : icon}
      {children}
    </button>
  );
}

export function IconButton({ title, className, children, ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { title: string }) {
  return (
    <button title={title} aria-label={title} className={cx("inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted hover:bg-panel-2 hover:text-fg transition disabled:opacity-40", className)} {...rest}>
      {children}
    </button>
  );
}

/* ---------- Layout primitives ---------- */

export function PageHeader({ title, subtitle, icon, actions }: { title: string; subtitle?: string; icon?: ReactNode; actions?: ReactNode }) {
  // Inside a desktop window the title bar already names the app: keep a compact header.
  const inWindow = useContext(InWindow);
  if (inWindow) {
    return (
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-[16px] font-semibold tracking-tight">{title}</h1>
          {subtitle && <p className="text-muted text-[12.5px]">{subtitle}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
    );
  }
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div className="flex items-center gap-3">
        {icon && <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-accent-soft text-accent">{icon}</div>}
        <div>
          <h1 className="text-[20px] font-semibold tracking-tight">{title}</h1>
          {subtitle && <p className="text-muted text-[13px]">{subtitle}</p>}
        </div>
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function Card({ title, actions, children, className, pad = true }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string; pad?: boolean }) {
  return (
    <section className={cx("card overflow-hidden", className)}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-line px-5 py-3.5">
          <h2 className="font-semibold text-[14px]">{title}</h2>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={pad ? "p-5" : ""}>{children}</div>
    </section>
  );
}

export function Stat({ label, value, sub, icon, tone = "accent" }: { label: string; value: ReactNode; sub?: ReactNode; icon?: ReactNode; tone?: "accent" | "ok" | "warn" | "danger" }) {
  const tones = { accent: "text-accent bg-accent-soft", ok: "text-ok bg-ok/10", warn: "text-warn bg-warn/10", danger: "text-danger bg-danger/10" };
  return (
    <div className="card flex items-center gap-4 p-4">
      {icon && <div className={cx("flex h-11 w-11 shrink-0 items-center justify-center rounded-xl", tones[tone])}>{icon}</div>}
      <div className="min-w-0">
        <div className="text-muted text-[12px]">{label}</div>
        <div className="text-[19px] font-semibold leading-tight">{value}</div>
        {sub && <div className="text-muted text-[12px] truncate">{sub}</div>}
      </div>
    </div>
  );
}

export function Badge({ tone = "neutral", children, dot }: { tone?: "neutral" | "ok" | "warn" | "danger" | "accent"; children: ReactNode; dot?: boolean }) {
  const tones = {
    neutral: "bg-panel-2 text-muted border-line",
    ok: "bg-ok/10 text-ok border-ok/20",
    warn: "bg-warn/10 text-warn border-warn/20",
    danger: "bg-danger/10 text-danger border-danger/20",
    accent: "bg-accent-soft text-accent border-accent/20",
  };
  return (
    <span className={cx("inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11.5px] font-medium whitespace-nowrap", tones[tone])}>
      {dot && <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {children}
    </span>
  );
}

export function healthTone(h: string): "ok" | "warn" | "danger" | "neutral" {
  const s = h.toLowerCase();
  if (["healthy", "online", "running", "active", "passed", "success"].includes(s)) return "ok";
  if (["degraded", "rebuilding", "partial", "unmounted", "paused", "restarting"].includes(s)) return "warn";
  if (["failed", "faulted", "unavail", "removed", "dead", "exited", "error"].includes(s)) return "danger";
  return "neutral";
}

const healthLabels: Record<string, string> = {
  healthy: "Sain", degraded: "Dégradé", rebuilding: "Reconstruction", failed: "En panne", inactive: "Inactif", unmounted: "Non monté",
  running: "En cours", stopped: "Arrêté", partial: "Partiel", exited: "Arrêté", created: "Créé", paused: "En pause", restarting: "Redémarrage",
};

export function Health({ value }: { value: string }) {
  return <Badge tone={healthTone(value)} dot>{healthLabels[value] ?? value}</Badge>;
}

export function Progress({ value, tone, className, indeterminate }: { value: number; tone?: "ok" | "warn" | "danger" | "accent"; className?: string; indeterminate?: boolean }) {
  const t = tone ?? (value > 90 ? "danger" : value > 75 ? "warn" : "accent");
  const colors = { accent: "bg-gradient-to-r from-accent to-accent-2", ok: "bg-ok", warn: "bg-warn", danger: "bg-danger" };
  return (
    <div className={cx("h-2 w-full overflow-hidden rounded-full bg-panel-2 border border-line", className)}>
      <div className={cx("h-full rounded-full transition-all duration-500", colors[t], indeterminate && "animate-pulse")} style={{ width: `${indeterminate ? 100 : Math.max(0, Math.min(100, value))}%` }} />
    </div>
  );
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-muted">
      <Loader2 className="animate-spin" size={18} /> {label ?? "Chargement…"}
    </div>
  );
}

export function Empty({ icon, title, text, action }: { icon?: ReactNode; title: string; text?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-12 text-center">
      {icon && <div className="mb-1 flex h-12 w-12 items-center justify-center rounded-2xl bg-panel-2 text-muted">{icon}</div>}
      <div className="font-medium">{title}</div>
      {text && <div className="max-w-md text-muted text-[13px]">{text}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <div className="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger/10 px-4 py-3 text-danger text-[13px]">
      <XCircle size={16} className="mt-0.5 shrink-0" /> <span className="break-words">{error instanceof Error ? error.message : String(error)}</span>
    </div>
  );
}

export function Notice({ tone = "info", children }: { tone?: "info" | "warn"; children: ReactNode }) {
  return (
    <div className={cx("flex items-start gap-2 rounded-xl border px-4 py-3 text-[13px]", tone === "warn" ? "border-warn/30 bg-warn/10 text-warn" : "border-accent/25 bg-accent-soft text-fg")}>
      {tone === "warn" ? <AlertTriangle size={16} className="mt-0.5 shrink-0" /> : <Info size={16} className="mt-0.5 shrink-0 text-accent" />}
      <div>{children}</div>
    </div>
  );
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: ReactNode }[]; value: T; onChange: (v: T) => void }) {
  return (
    <div className="mb-5 flex gap-1 overflow-x-auto rounded-xl border border-line bg-panel p-1">
      {tabs.map((t) => (
        <button key={t.id} onClick={() => onChange(t.id)} className={cx("rounded-lg px-3.5 py-1.5 text-[13px] font-medium whitespace-nowrap transition", value === t.id ? "bg-accent text-white shadow-sm" : "text-muted hover:text-fg hover:bg-panel-2")}>
          {t.label}
        </button>
      ))}
    </div>
  );
}

/* ---------- Forms ---------- */

export function Field({ label, hint, children, className }: { label: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cx("block", className)}>
      <span className="mb-1.5 block text-[12.5px] font-medium text-muted">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11.5px] text-muted">{hint}</span>}
    </label>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cx("input", props.className)} />;
}

export function Select({ children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select {...props} className={cx("input appearance-none pr-8", props.className)} style={{ backgroundImage: "linear-gradient(45deg,transparent 50%,var(--muted) 50%),linear-gradient(135deg,var(--muted) 50%,transparent 50%)", backgroundPosition: "calc(100% - 15px) 50%, calc(100% - 10px) 50%", backgroundSize: "5px 5px", backgroundRepeat: "no-repeat" }}>
      {children}
    </select>
  );
}

export function Switch({ checked, onChange, label, hint, disabled }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode; hint?: ReactNode; disabled?: boolean }) {
  return (
    <label className={cx("flex items-start gap-3 select-none", disabled ? "opacity-50" : "cursor-pointer")}>
      <button type="button" role="switch" aria-checked={checked} disabled={disabled} onClick={() => onChange(!checked)} className={cx("relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition", checked ? "bg-accent" : "bg-line")}>
        <span className={cx("absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all", checked ? "left-[18px]" : "left-0.5")} />
      </button>
      {(label || hint) && (
        <span>
          {label && <span className="block text-[13.5px]">{label}</span>}
          {hint && <span className="block text-[12px] text-muted">{hint}</span>}
        </span>
      )}
    </label>
  );
}

export function Checkbox({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode; disabled?: boolean }) {
  return (
    <label className={cx("inline-flex items-center gap-2 select-none", disabled ? "opacity-50" : "cursor-pointer")}>
      <input type="checkbox" className="h-4 w-4 accent-[var(--accent)]" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}

/* ---------- Modal ---------- */

export function Modal({ open, onClose, title, children, footer, wide }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean | "xl" }) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
  if (!open) return null;
  // Portal to <body>: inside a desktop window the dialog must cover every window and the taskbar.
  return createPortal(
    <div className="fixed inset-0 z-[9500] flex items-start justify-center overflow-y-auto bg-black/50 p-4 backdrop-blur-sm sm:items-center" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={cx("card my-8 flex max-h-[90vh] w-full flex-col", wide === "xl" ? "max-w-5xl" : wide ? "max-w-3xl" : "max-w-lg")}>
        <header className="flex items-center justify-between border-b border-line px-5 py-3.5">
          <h3 className="font-semibold">{title}</h3>
          <IconButton title="Fermer" onClick={onClose}>
            <X size={17} />
          </IconButton>
        </header>
        <div className="overflow-y-auto p-5">{children}</div>
        {footer && <footer className="flex justify-end gap-2 border-t border-line px-5 py-3.5">{footer}</footer>}
      </div>
    </div>,
    document.body,
  );
}

/* ---------- Toasts & confirmations ---------- */

type Toast = { id: number; tone: "ok" | "error" | "info"; text: string };
type ConfirmOpts = { title: string; text: ReactNode; confirm?: string; danger?: boolean; typeToConfirm?: string };

interface UICtx {
  toast: (text: string, tone?: Toast["tone"]) => void;
  confirm: (o: ConfirmOpts) => Promise<boolean>;
}

const Ctx = createContext<UICtx>({ toast: () => {}, confirm: async () => false });
export const useUI = () => useContext(Ctx);

export function UIProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [conf, setConf] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(null);
  const [typed, setTyped] = useState("");
  const idRef = useRef(0);

  const toast = useCallback((text: string, tone: Toast["tone"] = "ok") => {
    const id = ++idRef.current;
    setToasts((t) => [...t, { id, tone, text }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === "error" ? 8000 : 4000);
  }, []);

  const confirm = useCallback((o: ConfirmOpts) => new Promise<boolean>((resolve) => {
    setTyped("");
    setConf({ ...o, resolve });
  }), []);

  const close = (v: boolean) => {
    conf?.resolve(v);
    setConf(null);
  };

  return (
    <Ctx.Provider value={{ toast, confirm }}>
      {children}
      <div className="fixed bottom-4 right-4 z-[9600] flex w-[360px] max-w-[calc(100vw-2rem)] flex-col gap-2">
        {toasts.map((t) => (
          <div key={t.id} className="card flex items-start gap-2.5 px-4 py-3 text-[13px] animate-[fadeIn_.2s]">
            {t.tone === "ok" ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-ok" /> : t.tone === "error" ? <XCircle size={17} className="mt-0.5 shrink-0 text-danger" /> : <Info size={17} className="mt-0.5 shrink-0 text-accent" />}
            <span className="break-words">{t.text}</span>
          </div>
        ))}
      </div>
      <Modal
        open={!!conf}
        onClose={() => close(false)}
        title={conf?.title}
        footer={
          <>
            <Button onClick={() => close(false)}>Annuler</Button>
            <Button variant={conf?.danger ? "danger" : "primary"} disabled={!!conf?.typeToConfirm && typed !== conf.typeToConfirm} onClick={() => close(true)}>
              {conf?.confirm ?? "Confirmer"}
            </Button>
          </>
        }
      >
        <div className="space-y-4 text-[13.5px]">
          <div>{conf?.text}</div>
          {conf?.typeToConfirm && (
            <Field label={`Tapez « ${conf.typeToConfirm} » pour confirmer`}>
              <Input autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} />
            </Field>
          )}
        </div>
      </Modal>
    </Ctx.Provider>
  );
}
