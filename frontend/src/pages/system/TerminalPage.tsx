import { SquareTerminal } from "lucide-react";
import { Notice, PageHeader } from "../../components/ui";
import { Terminal } from "../../components/Terminal";

export default function TerminalPage() {
  return (
    <div>
      <PageHeader title="Terminal" subtitle="Shell root sur le NAS" icon={<SquareTerminal size={20} />} />
      <div className="mb-4"><Notice tone="warn">Ce terminal dispose des droits root : une mauvaise commande peut détruire vos données.</Notice></div>
      <Terminal path="/api/system/terminal" height="calc(100vh - 260px)" />
    </div>
  );
}
