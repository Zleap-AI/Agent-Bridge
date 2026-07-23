import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { I18nProvider } from "../shared/i18n";
import "../shared/styles.css";
import { LocalApp } from "./App";
import { ToastProvider } from "./hooks/useToast";
import { ToastContainer } from "./components/Toast";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <I18nProvider>
      <ToastProvider>
        <LocalApp />
        <ToastContainer />
      </ToastProvider>
    </I18nProvider>
  </StrictMode>,
);
