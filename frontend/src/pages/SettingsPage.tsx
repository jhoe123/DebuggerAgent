import { useState } from "react";
import { useSettings, type ThemeMode } from "../context/SettingsContext";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";

const THEMES: ThemeMode[] = ["light", "dark", "system"];
const STAGES = ["apply", "test", "build", "deploy"] as const;

export function SettingsPage() {
  const { theme, setTheme, backendUrl, setBackendUrl } = useSettings();
  const { consoleAvailable, mock, testStatus } = useAppData();
  const { config, setConfig, localMode } = useAutopilot();
  const toast = useToast();
  const [url, setUrl] = useState(backendUrl);

  function saveUrl() {
    const v = url.trim();
    setBackendUrl(v);
    toast.success(v ? `Backend set to ${v}` : "Backend reset to default");
  }

  async function toggleAutopatch() {
    try {
      await setConfig({ ...config, enabled: !config.enabled });
      toast.success(config.enabled ? "Auto-patch disabled" : "Auto-patch enabled");
    } catch {
      toast.error("Couldn't update auto-patch");
    }
  }
  async function toggleStage(k: (typeof STAGES)[number]) {
    try {
      await setConfig({ ...config, stages: { ...config.stages, [k]: !config.stages[k] } });
    } catch {
      toast.error("Couldn't update stages");
    }
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
        <h3>Auto-patch (autopilot)</h3>
        <p className="muted">
          When on, newly-detected problems are automatically investigated and patched per the stages
          below — no clicks. Each problem shows its live status in Problems, and you can halt any to
          take over manually.
        </p>
        <label className="switch-row">
          <input type="checkbox" checked={config.enabled} onChange={toggleAutopatch} />
          <span>
            <strong>Auto-patch</strong> is {config.enabled ? "ON" : "off"}
          </span>
        </label>

        {config.enabled && (
          <>
            <p className="muted autonomy-label">Pipeline stages autopilot runs after proposing a fix:</p>
            <div className="pipeline-opts">
              {STAGES.map((k) => (
                <label key={k} className="stage-toggle">
                  <input type="checkbox" checked={config.stages[k]} onChange={() => toggleStage(k)} /> {k}
                </label>
              ))}
            </div>
            {!localMode && (
              <p className="muted truncated-note">
                Local Test Console is off — autopilot will auto-investigate &amp; propose only (no
                apply/build/deploy).
              </p>
            )}
          </>
        )}
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
