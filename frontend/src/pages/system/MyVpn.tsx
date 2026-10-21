import { Smartphone } from "lucide-react";
import { PageHeader } from "../../components/ui";
import { tr } from "../../lib/i18n";
import { MyVpnDevices } from "./WireGuard";

export default function MyVpn() {
  return (
    <div>
      <PageHeader title={tr("My VPN devices")} subtitle={tr("Connect your phone or computer to the NAS from anywhere with WireGuard")} icon={<Smartphone size={20} />} />
      <MyVpnDevices />
    </div>
  );
}
