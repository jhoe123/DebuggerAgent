import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { SettingsProvider } from "./context/SettingsContext";
import { ToastProvider } from "./context/ToastContext";
import { AppDataProvider } from "./context/AppDataContext";
import { LocalStoreProvider } from "./context/LocalStoreContext";
import { AutopilotProvider } from "./context/AutopilotContext";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter>
      <SettingsProvider>
        <ToastProvider>
          <AppDataProvider>
            <LocalStoreProvider>
              <AutopilotProvider>
                <App />
              </AutopilotProvider>
            </LocalStoreProvider>
          </AppDataProvider>
        </ToastProvider>
      </SettingsProvider>
    </BrowserRouter>
  </React.StrictMode>,
);
