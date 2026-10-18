import { SquareTerminal } from "lucide-react";
import { Notice, PageHeader } from "../../components/ui";
import { Terminal } from "../../components/Terminal";
import { tr } from "../../lib/i18n";

export default function TerminalPage() {
  return (
    <div>
      <PageHeader title={tr("Terminal")} subtitle={tr("Root shell on the NAS")} icon={<SquareTerminal size={20} />} />
      <div className="mb-4"><Notice tone="warn">{tr("This terminal has root privileges: a wrong command can destroy your data.")}</Notice></div>
      <Terminal path="/api/system/terminal" height={tr("calc(100vh - 260px)")} />
    </div>
  );
}
