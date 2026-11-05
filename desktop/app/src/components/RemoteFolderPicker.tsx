// Choosing a folder of the NAS that a pair follows: a shared folder, then
// its subfolders (end-to-end encrypted ones are marked).
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, Folder, Lock } from "lucide-react";
import { useState } from "react";
import { Button, Empty, ErrorBox, Spinner } from "@nas/components/ui";
import { agent } from "../lib/agent";
import { tk } from "../lib/i18n";

type Share = { name: string; path: string; writable: boolean; recycleBin: boolean };
type Dir = { name: string; path: string; encrypted?: boolean };

export function RemoteFolderPicker({ account, value, onChange }: { account: string; value: string; onChange: (path: string) => void }) {
  const [path, setPath] = useState("");
  const base = `/v1/accounts/${encodeURIComponent(account)}`;
  const shares = useQuery({ queryKey: ["sync-shares", account], queryFn: () => agent.get<Share[]>(`${base}/remote/shares`) });
  const list = useQuery({
    queryKey: ["sync-list", account, path],
    queryFn: () => agent.get<{ dirs: Dir[]; isShare: boolean }>(`${base}/remote/list?path=${encodeURIComponent(path)}`),
    enabled: !!path,
  });
  const share = shares.data?.find((s) => path === s.path || path.startsWith(s.path + "/"));
  const up = () => {
    if (!share || path === share.path) setPath("");
    else setPath(path.slice(0, path.lastIndexOf("/")));
  };
  const go = (p: string) => {
    setPath(p);
    onChange(p);
  };
  return (
    <div className="rounded-xl border border-line">
      <div className="flex items-center gap-2 border-b border-line px-3 py-2 text-[12.5px]">
        <Button size="sm" variant="ghost" icon={<ChevronLeft size={14} />} disabled={!path} onClick={up} />
        <span className="mono truncate text-muted">{path || tk("folders.remote")}</span>
      </div>
      <div className="max-h-[240px] overflow-y-auto p-1.5">
        {!path ? (
          shares.isLoading ? <Spinner /> : shares.error ? <ErrorBox error={shares.error} /> : !shares.data?.length ? <Empty title={tk("folders.noshares")} /> : shares.data.map((s) => (
            <Row key={s.path} name={s.name} selected={value === s.path} note={s.writable ? "" : tk("folders.readonly")} onClick={() => go(s.path)} />
          ))
        ) : list.isLoading ? <Spinner /> : list.error ? <ErrorBox error={list.error} /> : (
          list.data?.dirs.map((d) => <Row key={d.path} name={d.name} lock={d.encrypted} selected={value === d.path} note={d.encrypted ? tk("folders.encryptedbrowse") : ""} onClick={() => go(d.path)} />)
        )}
      </div>
    </div>
  );
}

function Row({ name, note, lock, selected, onClick }: { name: string; note?: string; lock?: boolean; selected: boolean; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={`flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-[13px] ${selected ? "bg-accent-soft text-accent" : "hover:bg-panel-2"}`}>
      {lock ? <Lock size={15} className="text-ok" /> : <Folder size={15} className="text-accent" />}
      <span className="flex-1 truncate">{name}</span>
      {note && <span className="text-[11.5px] text-muted">{note}</span>}
    </button>
  );
}
