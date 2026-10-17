import { SquareTerminal } from "lucide-react";
import { Notice, PageHeader } from "../../components/ui";
import { Terminal } from "../../components/Terminal";
import { tr } from "../../lib/i18n";

export default function TerminalPage() {
  return (
    <div>
      <PageHeader title={tr("Terminal")} subtitle={tr("Shell root sur le NAS")} icon={<SquareTerminal size={20} />} />
      <div className="mb-4"><Notice tone="warn">{tr("Ce terminal dispose des droits root : une mauvaise commande peut détruire vos données.")}</Notice></div>
      <Terminal path="/api/system/terminal" height={tr("calc(100vh - 260px)")} />
    </div>
  );
}
