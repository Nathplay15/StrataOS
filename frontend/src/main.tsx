import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { initI18n } from "./lib/i18n";

// Apply the saved theme before the first paint (loading screens included).
try {
  document.documentElement.dataset.theme = localStorage.getItem("strata-theme") ?? "dark";
} catch {
  document.documentElement.dataset.theme = "dark";
}

// Opened from the "new address" link after a network change: confirm it, then clean the URL.
const netToken = new URLSearchParams(location.search).get("netconfirm");
if (netToken) {
  fetch("/api/system/network/confirm", { method: "POST", headers: { "Content-Type": "application/json", "X-Strata": "1" }, body: JSON.stringify({ token: netToken }) }).finally(() => history.replaceState(null, "", location.pathname));
}

// The dictionaries must be loaded before the interface modules run (some build
// their labels when imported), hence the dynamic import of App.
initI18n().then(async () => {
  const { App } = await import("./App");
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
});
