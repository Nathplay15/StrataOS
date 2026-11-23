import { useEffect } from "react";
import { setBearerToken } from "../lib/api";
import { tr } from "../lib/i18n";
import { CinemaTab } from "./media/Cinema";

// Kiosk page opened by the HDMI console (strataos-hdmi.service →
// "strata hdmi") at https://127.0.0.1:7443/hdmi#<kiosk token>: the cinema
// menu full-screen on the TV, scaled up for 10-foot viewing and driven with
// the keyboard or a gamepad (navigation built into CinemaTab). The token in
// the fragment signs the page in read-only (loopback only, see the daemon).
export default function HdmiPage() {
  const token = location.hash.replace(/^#/, "");
  useEffect(() => {
    setBearerToken(token || null);
    document.title = "StrataOS";
    return () => setBearerToken(null);
  }, [token]);
  if (!token) {
    return (
      <div className="flex h-screen items-center justify-center bg-black p-10 text-center text-[22px] text-white/70">
        {tr("No kiosk token: this page is opened by the NAS HDMI console.")}
      </div>
    );
  }
  return (
    <div className="fixed inset-0 overflow-auto bg-black" style={{ zoom: 1.6 }}>
      <div className="mx-auto max-w-[1700px] p-6">
        <CinemaTab admin={false} />
      </div>
    </div>
  );
}
