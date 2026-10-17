import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./App";

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

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
