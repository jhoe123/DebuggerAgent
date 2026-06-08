import { useEffect, useState } from "react";
import { useSettings, type ThemeMode } from "../context/SettingsContext";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import { getSlack, setSlackConfig, testSlack } from "../api";
import type { SlackStatus } from "../types";

const THEMES: ThemeMode[] = ["light", "dark", "system"];
const STAGES = ["apply", "test", "build", "deploy"] as const;

export function SettingsPage() {
  const { theme, setTheme, backendUrl, setBackendUrl } = useSettings();
  const { consoleAvailable, mock, testStatus } = useAppData();
  const { config, setConfig, localMode } = useAutopilot();
  const toast = useToast();
  const [url, setUrl] = useState(backendUrl);
  const [slack, setSlack] = useState<SlackStatus | null>(null);
  const [hook, setHook] = useState("");
  const [slackBusy, setSlackBusy] = useState(false);

  useEffect(() => {
    getSlack()
      .then(setSlack)
      .catch(() => setSlack(null));
  }, []);

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

  async function saveSlack(enabled: boolean, webhookUrl?: string) {
    setSlackBusy(true);
    try {
      const s = await setSlackConfig({ enabled, webhookUrl });
      setSlack(s);
      if (webhookUrl) setHook("");
      toast.success("Slack settings saved");
    } catch {
      toast.error("Couldn't update Slack");
    } finally {
      setSlackBusy(false);
    }
  }
  async function sendSlackTest() {
    setSlackBusy(true);
    try {
      await testSlack();
      toast.success("Test message sent to Slack");
    } catch {
      toast.error("Slack test failed — check the webhook");
    } finally {
      setSlackBusy(false);
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
          Point the UI at a different PatchPilot backend. Leave blank to use the default.
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
        <h3>Slack notifications</h3>
        <p className="muted">
          Post a consolidated digest of active bugs to a Slack Incoming Webhook (re-posted only when
          the bug set changes). Applies to the running backend instance.
        </p>
        <label className="switch-row">
          <input
            type="checkbox"
            checked={slack?.enabled ?? false}
            disabled={slackBusy || slack === null}
            onChange={() => saveSlack(!(slack?.enabled ?? false))}
          />
          <span>
            <strong>Slack digest</strong> is {slack?.enabled ? "ON" : "off"}
            {slack && !slack.configured && " — no webhook set"}
            {slack?.configured && slack.preview ? ` · ${slack.preview}` : ""}
          </span>
        </label>
        <div className="qa-input">
          <input
            type="password"
            value={hook}
            placeholder={slack?.configured ? "Replace webhook…" : "https://hooks.slack.com/services/…"}
            onChange={(e) => setHook(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && hook.trim() && saveSlack(true, hook.trim())}
            aria-label="Slack webhook URL"
          />
          <button disabled={slackBusy || !hook.trim()} onClick={() => saveSlack(true, hook.trim())}>
            Save
          </button>
          <button className="seg-btn" disabled={slackBusy || !slack?.configured} onClick={sendSlackTest}>
            Send test
          </button>
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
