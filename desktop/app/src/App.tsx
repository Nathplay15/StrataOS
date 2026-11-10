import { useEffect, useMemo, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AlertTriangle, ChevronsUpDown, Loader2, Plus } from "lucide-react";
import { PageBoundary, UIProvider, Button } from "@nas/components/ui";
import { JobsProvider } from "@nas/lib/jobs";
import { cx } from "@nas/lib/format";
import { AppProvider, useApp } from "./lib/state";
import { t } from "./lib/i18n";
import { pages, groups, type PageId } from "./pages";
import { PairingPage } from "./pages/Pairing";
import { useShellBridge } from "./lib/shell";
import { NavCtx, useNav, type Nav } from "./lib/nav";
import logo from "@nas/logo.svg";

const rootClient = new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false, retry: 1 } } });

export function App() {
  return (
    <QueryClientProvider client={rootClient}>
      <UIProvider>
        <AppProvider>
          <Shell />
        </AppProvider>
      </UIProvider>
    </QueryClientProvider>
  );
}


function Shell() {
  const { state, error, accountId, isAdmin } = useApp();
  const [nav, setNav] = useState<Nav>({ page: "home" });
  const [adding, setAdding] = useState(false);
  const [pairLink, setPairLink] = useState("");
  const go = (page: PageId, params?: Record<string, string>) => setNav({ page, params });
  useShellBridge({ go, pair: (link) => { setPairLink(link); setAdding(true); } });

  if (!state) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 text-muted">
        {error ? <AlertTriangle className="text-warn" /> : <Loader2 className="animate-spin" />}
        <div>{error ? t("The sync engine does not answer: {0}", String((error as Error).message ?? error)) : t("Starting…")}</div>
      </div>
    );
  }
  if (!accountId || adding) {
    return <PairingPage link={pairLink} onCancel={accountId ? () => { setAdding(false); setPairLink(""); } : undefined} onDone={() => { setAdding(false); setPairLink(""); go("home"); }} />;
  }
  const page = pages.find((p) => p.id === nav.page && (!p.admin || isAdmin)) ?? pages[0];
  const Page = page.component;
  return (
    <NavCtx.Provider value={{ nav, go, addNas: () => setAdding(true) }}>
      <div className="flex h-full">
        <Sidebar current={page.id} />
        <main className="min-w-0 flex-1 overflow-y-auto">
          <AccountScope key={accountId}>
            <div className="mx-auto max-w-[1180px] px-7 py-6">
              <PageBoundary key={page.id + accountId}>
                <Page params={nav.params ?? {}} />
              </PageBoundary>
            </div>
          </AccountScope>
        </main>
      </div>
    </NavCtx.Provider>
  );
}

/** NAS data of one account: its own cache and jobs, cleared when switching. */
function AccountScope({ children }: { children: ReactNode }) {
  const client = useMemo(() => new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false, retry: 1, staleTime: 10_000 } } }), []);
  return (
    <QueryClientProvider client={client}>
      <JobsProvider>{children}</JobsProvider>
    </QueryClientProvider>
  );
}

function Sidebar({ current }: { current: PageId }) {
  const { isAdmin } = useApp();
  const { go } = useNav();
  return (
    <nav className="flex w-[232px] shrink-0 flex-col border-r border-line bg-panel">
      <AccountSwitcher />
      <div className="flex-1 overflow-y-auto px-2.5 pb-3">
        {groups.map((g) => {
          const items = pages.filter((p) => p.group === g.id && !p.hidden && (!p.admin || isAdmin));
          if (!items.length) return null;
          return (
            <div key={g.id} className="mt-3">
              {g.label && <div className="px-2.5 pb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">{g.label()}</div>}
              {items.map((p) => (
                <button key={p.id} onClick={() => go(p.id)}
                  className={cx("flex w-full items-center gap-2.5 rounded-lg px-2.5 py-[7px] text-left text-[13px] transition",
                    p.id === current ? "bg-accent-soft font-medium text-accent" : "text-fg hover:bg-panel-2")}>
                  <p.icon size={16} className={p.id === current ? "text-accent" : "text-muted"} />
                  <span className="truncate">{p.label()}</span>
                </button>
              ))}
            </div>
          );
        })}
      </div>
    </nav>
  );
}

function AccountSwitcher() {
  const { state, account, setAccountId, info } = useApp();
  const { addNas } = useNav();
  const [open, setOpen] = useState(false);
  const paired = (state?.accounts ?? []).filter((a) => a.paired);
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    window.addEventListener("click", close);
    return () => window.removeEventListener("click", close);
  }, [open]);
  const name = (a: typeof account) => a?.config.nasName || a?.config.server?.replace(/^https:\/\//, "") || "NAS";
  return (
    <div className="relative border-b border-line p-2.5">
      <button onClick={(e) => { e.stopPropagation(); setOpen(!open); }} className="flex w-full items-center gap-2.5 rounded-lg px-2 py-2 text-left hover:bg-panel-2">
        <img src={logo} alt="" className="h-8 w-8" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-semibold">{name(account)}</div>
          <div className="truncate text-[11.5px] text-muted">{account?.config.user}{info?.version ? ` · ${info.version}` : ""}</div>
        </div>
        <ChevronsUpDown size={15} className="text-muted" />
      </button>
      {open && (
        <div className="card absolute left-2.5 right-2.5 top-[60px] z-50 p-1.5" onClick={(e) => e.stopPropagation()}>
          {paired.map((a) => (
            <button key={a.id} onClick={() => { setAccountId(a.id); setOpen(false); }}
              className={cx("flex w-full flex-col rounded-md px-2.5 py-1.5 text-left hover:bg-panel-2", a.id === account?.id && "bg-accent-soft")}>
              <span className="truncate text-[13px] font-medium">{name(a)}</span>
              <span className="truncate text-[11.5px] text-muted">{a.config.user} · {a.config.server?.replace(/^https:\/\//, "")}</span>
            </button>
          ))}
          <div className="mt-1 border-t border-line pt-1">
            <Button variant="ghost" size="sm" className="w-full justify-start" icon={<Plus size={14} />} onClick={() => { setOpen(false); addNas(); }}>{t("Add a NAS")}</Button>
          </div>
        </div>
      )}
    </div>
  );
}
