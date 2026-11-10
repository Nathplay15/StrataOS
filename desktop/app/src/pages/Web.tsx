// The complete web interface of the NAS in a window of the app, signed in
// with this computer's device token (the agent's proxy, desktop/internal/agent).
import { Globe } from "lucide-react";
import { Card } from "@nas/components/ui";
import { t } from "../lib/i18n";
import { PageHeader, WebButton } from "../components/kit";

const places: [string, string][] = [
  ["/", "Dashboard"], ["/storage/volumes", "Volumes"], ["/storage/disks", "Disks"], ["/shares", "Shared folders"], ["/users", "Users"],
  ["/apps", "App Center"], ["/docker/containers", "Containers"], ["/vms", "Virtual machines"], ["/backup", "Backup"], ["/cloudsync", "Cloud sync"],
  ["/system/updates", "Updates"], ["/system/network", "Network"], ["/system/settings", "Settings"], ["/system/logs", "Logs"],
  ["/system/terminal", "Terminal"], ["/fleet", "Fleet"], ["/surveillance", "Surveillance"], ["/media", "Media"], ["/help", "Help"],
];

export function WebPage() {
  return (
    <>
      <PageHeader title={t("Web interface")} subtitle={t("Everything the NAS offers, in a window of this app, without signing in again.")} />
      <Card>
        <div className="mb-4 flex items-center gap-3 text-[13px] text-muted"><Globe className="text-accent" />{t("The pages open in their own window. Signing this computer out of the NAS ends that session too.")}</div>
        <div className="flex flex-wrap gap-2">
          {places.map(([p, l]) => <WebButton key={p} path={p} label={t(l)} title={t(l)} />)}
        </div>
      </Card>
    </>
  );
}
