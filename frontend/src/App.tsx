import { lazy, Suspense, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { api, ApiError } from "./lib/api";
import type { Me } from "./lib/types";
import { JobsProvider } from "./lib/jobs";
import { Spinner, UIProvider } from "./components/ui";
import { Layout } from "./components/Layout";
import { Login, Setup } from "./pages/Auth";
import { Desktop } from "./desktop/Desktop";
import { useDesktopMode } from "./desktop/mode";

const Dashboard = lazy(() => import("./pages/Dashboard"));
const Disks = lazy(() => import("./pages/storage/Disks"));
const Raid = lazy(() => import("./pages/storage/Raid"));
const Volumes = lazy(() => import("./pages/storage/Volumes"));
const Zfs = lazy(() => import("./pages/storage/Zfs"));
const Lvm = lazy(() => import("./pages/storage/Lvm"));
const Shares = lazy(() => import("./pages/Shares"));
const Users = lazy(() => import("./pages/Users"));
const Files = lazy(() => import("./pages/Files"));
const AppStore = lazy(() => import("./pages/docker/AppStore"));
const Containers = lazy(() => import("./pages/docker/Containers"));
const Stacks = lazy(() => import("./pages/docker/Stacks"));
const Images = lazy(() => import("./pages/docker/Images"));
const Monitor = lazy(() => import("./pages/system/Monitor"));
const Services = lazy(() => import("./pages/system/Services"));
const NetworkPage = lazy(() => import("./pages/system/Network"));
const Updates = lazy(() => import("./pages/system/Updates"));
const Logs = lazy(() => import("./pages/system/Logs"));
const TerminalPage = lazy(() => import("./pages/system/TerminalPage"));
const SettingsPage = lazy(() => import("./pages/system/Settings"));
const Schedules = lazy(() => import("./pages/system/Schedules"));
const Backups = lazy(() => import("./pages/system/Backups"));
const UPSPage = lazy(() => import("./pages/system/UPS"));

const qc = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (n, e) => !(e instanceof ApiError && e.status < 500) && n < 2,
      refetchOnWindowFocus: false,
      staleTime: 3000,
    },
  },
});

function Root() {
  const setup = useQuery({ queryKey: ["setup"], queryFn: () => api.get<{ needed: boolean; hostname: string; version: string }>("/api/setup") });
  const meQ = useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/api/me"), retry: false, enabled: setup.data?.needed === false });
  const [me, setMe] = useState<Me | null>(null);
  const desktop = useDesktopMode();

  useEffect(() => {
    if (meQ.data) setMe(meQ.data);
  }, [meQ.data]);
  useEffect(() => {
    const h = () => setMe(null);
    window.addEventListener("strata:unauthorized", h);
    return () => window.removeEventListener("strata:unauthorized", h);
  }, []);

  if (setup.isLoading || (setup.data?.needed === false && meQ.isLoading)) return <Spinner label="Connexion au NAS…" />;
  if (setup.error) return <div className="p-10 text-center text-danger">Impossible de joindre le serveur StrataOS.</div>;
  const hostname = setup.data?.hostname ?? "NAS";
  if (setup.data?.needed && !me) return <Setup hostname={hostname} onDone={(m) => { setMe(m); qc.invalidateQueries(); }} />;
  if (!me) return <Login hostname={hostname} onLogin={(m) => { setMe(m); qc.invalidateQueries(); }} />;
  if (desktop) {
    return (
      <JobsProvider>
        <Desktop me={me} />
      </JobsProvider>
    );
  }
  const admin = me.role === "admin";
  const A = (el: React.ReactNode) => (admin ? el : <Navigate to="/" replace />);

  return (
    <JobsProvider>
      <Suspense fallback={<Spinner />}>
        <Routes>
          <Route element={<Layout me={me} />}>
            <Route index element={<Dashboard me={me} />} />
            <Route path="storage/disks" element={A(<Disks />)} />
            <Route path="storage/raid" element={A(<Raid />)} />
            <Route path="storage/volumes" element={A(<Volumes />)} />
            <Route path="storage/zfs" element={A(<Zfs />)} />
            <Route path="storage/lvm" element={A(<Lvm />)} />
            <Route path="files" element={<Files />} />
            <Route path="shares" element={A(<Shares />)} />
            <Route path="users" element={A(<Users me={me} />)} />
            <Route path="apps" element={A(<AppStore />)} />
            <Route path="docker/containers" element={A(<Containers />)} />
            <Route path="docker/stacks" element={A(<Stacks />)} />
            <Route path="docker/images" element={A(<Images />)} />
            <Route path="system/monitor" element={A(<Monitor />)} />
            <Route path="system/services" element={A(<Services />)} />
            <Route path="system/network" element={A(<NetworkPage />)} />
            <Route path="system/updates" element={A(<Updates />)} />
            <Route path="system/logs" element={A(<Logs />)} />
            <Route path="system/terminal" element={A(<TerminalPage />)} />
            <Route path="system/settings" element={A(<SettingsPage />)} />
            <Route path="system/schedules" element={A(<Schedules />)} />
            <Route path="backup" element={A(<Backups />)} />
            <Route path="system/ups" element={A(<UPSPage />)} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Routes>
      </Suspense>
    </JobsProvider>
  );
}

export function App() {
  return (
    <QueryClientProvider client={qc}>
      <UIProvider>
        <BrowserRouter>
          <Root />
        </BrowserRouter>
      </UIProvider>
    </QueryClientProvider>
  );
}
