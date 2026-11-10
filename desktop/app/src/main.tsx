import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { initTexts } from "./lib/i18n";
import { applyTheme } from "./lib/prefs";

applyTheme();

// The dictionaries load before the interface modules run (some build their
// labels when imported), hence the dynamic import of App.
initTexts().then(async () => {
  const { App } = await import("./App");
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
});
