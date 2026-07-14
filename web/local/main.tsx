import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nProvider } from "../shared/i18n";
import "../shared/styles.css";
import { LocalApp } from "./App";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <I18nProvider><LocalApp /></I18nProvider>
  </StrictMode>,
);
