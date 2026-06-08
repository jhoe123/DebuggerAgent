import { useState } from "react";
import { useSettings, type ThemeMode } from "../context/SettingsContext";
import { useAppData } from "../context/AppDataContext";
import { useToast } from "../context/ToastContext";

const THEMES: ThemeMode[] = ["light", "dark", "system"];
const STAGES = ["apply", "test", "build", "deploy"] as const;

export function SettingsPage() {
  const { theme, setTheme, backendUrl, setBackendUrl, autonomy, setAutonomy } = useSettings();
  const { consoleAvailable, mock, testStatus } = useAppData();
  const toast = useToast();
  const [url, setUrl] = useState(backendUrl);

  function saveUrl() {
    const v = url.trim();
    setBackendUrl(v);
    toast.success(v ? `Backend set to ${v}` : "Backend reset to default");
  }

  return (
    <>
      <h2 className="page-title">Settings</h2>
      <p className="page-sub">Preferences are stored in your browser.</p>

      <section className="settings-card">
        <h3>Appearance</h3>
        <div className="seg">
          {THEMES.map((t) => (
            <button key={t} className={`seg-btn${theme === t ? " active" : ""}`} onClick={() => setTheme(t)}>
              {t}
            </button>
          ))}
        </div>
      </section>

      <section className="settings-card">
        <h3>Backend</h3>
        <p className="muted">
          Point the UI at a different DebuggerAgent backend. Leave blank to use the default.
        </p>
        <div className="qa-input">
          <input
            value={url}
            placeholder="http://localhost:8080"
            onChange={(e) => setUrl(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && saveUrl()}
            aria-label="Backend URL"
          />
          <button onClick={saveUrl}>Save</button>
        </div>
      </section>

      <section className="settings-card">
        <h3>Autonomy defaults</h3>
        <p className="muted">Default pipeline stages for auto-remediation and instrument-apply.</p>
        <div className="pipeline-opts">
          {STAGES.map((k) => (
            <label key={k} className="stage-toggle">
              <input
                type="checkbox"
                checked={autonomy[k]}
                onChange={() => setAutonomy({ ...autonomy, [k]: !autonomy[k] })}
              />{" "}
              {k}
            </label>
          ))}
        </div>
      </section>

      <section className="settings-card">
        <h3>Status</h3>
        <div className="tc-status">
          <span className={`chip ${mock ? "chip-warn" : "chip-ok"}`}>
            {mock ? "mock data" : "backend connected"}
          </span>
          <span className={`chip ${consoleAvailable ? "chip-ok" : "chip-info"}`}>
            Test Console: {consoleAvailable ? "on" : "off"}
          </span>
          {testStatus && <span className="chip">demo: {testStatus.reachable ? "reachable" : "down"}</span>}
        </div>
      </section>
    </>
  );
}
