import { useEffect, useState } from "react";
import { useSettings, type ThemeMode } from "../context/SettingsContext";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import {
  connectGitSource,
  getGitSource,
  getPipelineConfig,
  getSlack,
  setGitSourceConfig,
  setPipelineConfig,
  setSlackConfig,
  testSlack,
} from "../api";
import type {
  BuildStrategy,
  DeployTarget,
  GitSourceConfig,
  GitSourceStatus,
  PipelineSettings,
  SlackStatus,
  TestStrategy,
} from "../types";

const THEMES: ThemeMode[] = ["light", "dark", "system"];
const STAGES = ["apply", "test", "build", "deploy"] as const;

type TabKey = "general" | "automation" | "pipeline" | "git" | "notifications";
const TABS: { key: TabKey; label: string; icon: string }[] = [
  { key: "general", label: "General", icon: "⚙" },
  { key: "automation", label: "Automation", icon: "✦" },
  { key: "pipeline", label: "Pipeline & deploy", icon: "⇪" },
  { key: "git", label: "Git source", icon: "⎇" },
  { key: "notifications", label: "Notifications", icon: "✉" },
];

// Maps the GET status (non-secret) back into the editable config form. The token
// is left blank — an empty token tells the backend to keep the existing secret.
function statusToConfig(s: GitSourceStatus): GitSourceConfig {
  return {
    repoUrl: s.repoUrlPreview ?? "",
    workingBranch: s.workingBranch,
    branchPrefix: s.branchPrefix,
    branchPerFix: s.branchPerFix,
    autoMergeOnConfirm: s.autoMergeOnConfirm,
    pushEnabled: s.pushEnabled,
    commitAuthorName: s.commitAuthorName,
    commitAuthorEmail: s.commitAuthorEmail,
  };
}

// Human-friendly labels for the strategy / target selects (values match the backend).
const TEST_STRATEGY_LABELS: Record<TestStrategy, string> = {
  auto: "auto — reuse or generate",
  reuse: "reuse existing only",
  generate: "generate fresh",
  skip: "skip tests",
};
const BUILD_STRATEGY_LABELS: Record<BuildStrategy, string> = {
  auto: "auto — script or go build",
  script: "build script",
  default: "go build",
};
const DEPLOY_TARGET_LABELS: Record<DeployTarget, string> = {
  local: "local process",
  docker: "docker",
  script: "deploy script",
  "cloud-run": "cloud run (cloud build)",
};
// Deploy params shown per target (keys match the backend's DeployParams map).
const PARAM_FIELDS: Record<DeployTarget, string[]> = {
  local: [],
  docker: ["image", "tag", "hostPort"],
  script: ["scriptPath"],
  "cloud-run": ["project", "region", "service", "sourceBucket", "artifactRepo"],
};
const PARAM_LABELS: Record<string, string> = {
  image: "Image",
  tag: "Tag",
  hostPort: "Host port",
  scriptPath: "Script path",
  project: "GCP project",
  region: "Region",
  service: "Service name",
  sourceBucket: "Source bucket",
  artifactRepo: "Artifact repo",
};

export function SettingsPage() {
  const { theme, setTheme, backendUrl, setBackendUrl } = useSettings();
  const { consoleAvailable, mock, testStatus } = useAppData();
  const { config, setConfig, localMode } = useAutopilot();
  const toast = useToast();
  const [tab, setTab] = useState<TabKey>("general");
  const [url, setUrl] = useState(backendUrl);
  const [slack, setSlack] = useState<SlackStatus | null>(null);
  const [hook, setHook] = useState("");
  const [slackBusy, setSlackBusy] = useState(false);
  const [pipe, setPipe] = useState<PipelineSettings | null>(null);
  const [pipeBusy, setPipeBusy] = useState(false);
  const [git, setGit] = useState<GitSourceStatus | null>(null);
  const [gitForm, setGitForm] = useState<GitSourceConfig | null>(null);
  const [gitTok, setGitTok] = useState("");
  const [gitBusy, setGitBusy] = useState(false);

  useEffect(() => {
    getSlack()
      .then(setSlack)
      .catch(() => setSlack(null));
    getPipelineConfig()
      .then(setPipe)
      .catch(() => setPipe(null));
    getGitSource()
      .then((s) => {
        setGit(s);
        setGitForm(statusToConfig(s));
      })
      .catch(() => setGit(null));
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

  async function saveGit() {
    if (!gitForm) return;
    setGitBusy(true);
    try {
      const s = await setGitSourceConfig({ ...gitForm, authToken: gitTok.trim() || undefined });
      setGit(s);
      setGitForm(statusToConfig(s));
      if (gitTok) setGitTok("");
      toast.success("Git source settings saved");
    } catch {
      toast.error("Couldn't save Git source settings");
    } finally {
      setGitBusy(false);
    }
  }
  async function connectGit() {
    setGitBusy(true);
    try {
      const s = await connectGitSource();
      setGit(s);
      setGitForm(statusToConfig(s));
      toast.success(s.connected ? `Connected to ${s.repoUrlPreview ?? "repository"}` : "Connect attempted — check status");
    } catch (e) {
      toast.error(`Connect failed: ${String(e)}`);
    } finally {
      setGitBusy(false);
    }
  }

  function updateParam(k: string, v: string) {
    setPipe((p) => (p ? { ...p, deployParams: { ...p.deployParams, [k]: v } } : p));
  }
  async function savePipe() {
    if (!pipe) return;
    setPipeBusy(true);
    try {
      setPipe(await setPipelineConfig(pipe));
      toast.success("Pipeline settings saved");
    } catch {
      toast.error("Couldn't save pipeline settings");
    } finally {
      setPipeBusy(false);
    }
  }

  return (
    <>
      <h2 className="page-title">Settings</h2>
      <p className="page-sub">Preferences are stored in your browser; pipeline &amp; integrations live on the backend.</p>

      <div className="settings-tabs" role="tablist">
        {TABS.map((t) => (
          <button
            key={t.key}
            role="tab"
            aria-selected={tab === t.key}
            className={`settings-tab${tab === t.key ? " active" : ""}`}
            onClick={() => setTab(t.key)}
          >
            <span className="settings-tab-ico" aria-hidden>
              {t.icon}
            </span>
            {t.label}
          </button>
        ))}
      </div>

      {tab === "general" && (
        <div className="settings-panel">
          <section className="settings-card">
            <h3>Appearance</h3>
            <p className="muted">Theme for the PatchPilot console.</p>
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
            <h3>Status</h3>
            <p className="muted">Live connection and capability of the current backend.</p>
            <div className="tc-status">
              <span className={`chip ${mock ? "chip-warn" : "chip-ok"}`}>
                {mock ? "mock data" : "backend connected"}
              </span>
              <span className={`chip ${consoleAvailable ? "chip-ok" : "chip-info"}`}>
                Test Console: {consoleAvailable ? "on" : "off"}
              </span>
              {testStatus && (
                <span className={`chip ${testStatus.reachable ? "chip-ok" : "chip-warn"}`}>
                  demo: {testStatus.reachable ? "reachable" : "down"}
                </span>
              )}
            </div>
          </section>
        </div>
      )}

      {tab === "automation" && (
        <div className="settings-panel">
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
        </div>
      )}

      {tab === "pipeline" && (
        <div className="settings-panel">
          {pipe ? (
            <>
              <section className="settings-card">
                <h3>
                  Pipeline &amp; deploy <span className="mini-chip">mode: {pipe.mode}</span>
                </h3>
                <p className="muted">
                  Defaults for the auto-remediation pipeline — the base for each run (overridable per run in
                  the Problems pipeline).
                </p>
                <div className="pipe-grid">
                  <label className="pipe-field">
                    <span>Test strategy</span>
                    <select
                      value={pipe.testStrategy}
                      disabled={pipeBusy}
                      onChange={(e) => setPipe({ ...pipe, testStrategy: e.target.value as TestStrategy })}
                    >
                      {(Object.keys(TEST_STRATEGY_LABELS) as TestStrategy[]).map((s) => (
                        <option key={s} value={s}>
                          {TEST_STRATEGY_LABELS[s]}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="pipe-field">
                    <span>Build strategy</span>
                    <select
                      value={pipe.buildStrategy}
                      disabled={pipeBusy}
                      onChange={(e) => setPipe({ ...pipe, buildStrategy: e.target.value as BuildStrategy })}
                    >
                      {(Object.keys(BUILD_STRATEGY_LABELS) as BuildStrategy[]).map((s) => (
                        <option key={s} value={s}>
                          {BUILD_STRATEGY_LABELS[s]}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="pipe-field">
                    <span>Deploy target</span>
                    <select
                      value={pipe.deployTarget}
                      disabled={pipeBusy}
                      onChange={(e) => setPipe({ ...pipe, deployTarget: e.target.value as DeployTarget })}
                    >
                      {(Object.keys(DEPLOY_TARGET_LABELS) as DeployTarget[]).map((s) => (
                        <option key={s} value={s}>
                          {DEPLOY_TARGET_LABELS[s]}
                        </option>
                      ))}
                    </select>
                  </label>
                </div>

                {PARAM_FIELDS[pipe.deployTarget].length > 0 && (
                  <div className="pipe-params">
                    <p className="muted autonomy-label">
                      {DEPLOY_TARGET_LABELS[pipe.deployTarget]} parameters
                    </p>
                    <div className="pipe-grid">
                      {PARAM_FIELDS[pipe.deployTarget].map((k) => (
                        <label key={k} className="pipe-field">
                          <span>{PARAM_LABELS[k] ?? k}</span>
                          <input
                            value={pipe.deployParams[k] ?? ""}
                            disabled={pipeBusy}
                            onChange={(e) => updateParam(k, e.target.value)}
                          />
                        </label>
                      ))}
                    </div>
                  </div>
                )}

                <div className="pipe-actions">
                  <button className="primary-btn" disabled={pipeBusy} onClick={savePipe}>
                    {pipeBusy ? "Saving…" : "Save pipeline settings"}
                  </button>
                </div>
              </section>

              <section className="settings-card">
                <h3>Health check</h3>
                <p className="muted">
                  URL pinged to confirm the demo app is reachable (drives the demo badge in Problems &amp;
                  Status). A full URL or a path on the demo app — leave blank to use its base URL.
                </p>
                <label className="pipe-field">
                  <span>Health check URL</span>
                  <input
                    value={pipe.healthUrl}
                    placeholder="https://…  or  /healthz"
                    disabled={pipeBusy}
                    onChange={(e) => setPipe({ ...pipe, healthUrl: e.target.value })}
                  />
                </label>
                <div className="pipe-actions">
                  {testStatus && (
                    <span className={`chip ${testStatus.reachable ? "chip-ok" : "chip-warn"}`}>
                      currently {testStatus.reachable ? "reachable" : "down"}
                    </span>
                  )}
                  <button className="primary-btn" disabled={pipeBusy} onClick={savePipe}>
                    {pipeBusy ? "Saving…" : "Save health check"}
                  </button>
                </div>
              </section>
            </>
          ) : (
            <section className="settings-card">
              <h3>Pipeline &amp; deploy</h3>
              <p className="muted">Pipeline settings are unavailable — the backend isn't reachable.</p>
            </section>
          )}
        </div>
      )}

      {tab === "git" && (
        <div className="settings-panel">
          {gitForm ? (
            <>
              <section className="settings-card">
                <h3>
                  Git source{" "}
                  <span className="mini-chip">{git?.enabled ? "writes enabled" : "read-only"}</span>
                </h3>
                <p className="muted">
                  Point PatchPilot at a Git repository it manages fixes in: it can branch per fix, push,
                  and merge into the working branch on confirm. The auth token is a secret and is never
                  returned by the API.
                </p>
                <div className="pipe-grid">
                  <label className="pipe-field">
                    <span>Repository URL</span>
                    <input
                      value={gitForm.repoUrl}
                      disabled={gitBusy}
                      placeholder="https://github.com/you/repo.git"
                      onChange={(e) => setGitForm({ ...gitForm, repoUrl: e.target.value })}
                    />
                  </label>
                  <label className="pipe-field">
                    <span>Working branch</span>
                    <input
                      value={gitForm.workingBranch}
                      disabled={gitBusy}
                      placeholder="patchpilot"
                      onChange={(e) => setGitForm({ ...gitForm, workingBranch: e.target.value })}
                    />
                  </label>
                  <label className="pipe-field">
                    <span>Fix branch prefix</span>
                    <input
                      value={gitForm.branchPrefix}
                      disabled={gitBusy}
                      placeholder="patchpilot/fix-"
                      onChange={(e) => setGitForm({ ...gitForm, branchPrefix: e.target.value })}
                    />
                  </label>
                  <label className="pipe-field">
                    <span>Commit author name</span>
                    <input
                      value={gitForm.commitAuthorName}
                      disabled={gitBusy}
                      placeholder="PatchPilot"
                      onChange={(e) => setGitForm({ ...gitForm, commitAuthorName: e.target.value })}
                    />
                  </label>
                  <label className="pipe-field">
                    <span>Commit author email</span>
                    <input
                      value={gitForm.commitAuthorEmail}
                      disabled={gitBusy}
                      placeholder="bot@example.com"
                      onChange={(e) => setGitForm({ ...gitForm, commitAuthorEmail: e.target.value })}
                    />
                  </label>
                </div>

                <p className="muted autonomy-label">Auth token (HTTPS personal access token)</p>
                <div className="qa-input">
                  <input
                    type="password"
                    value={gitTok}
                    placeholder={git?.tokenConfigured ? "Replace token…" : "ghp_… / glpat-…"}
                    disabled={gitBusy}
                    onChange={(e) => setGitTok(e.target.value)}
                    aria-label="Git auth token"
                  />
                </div>

                <label className="switch-row">
                  <input
                    type="checkbox"
                    checked={gitForm.branchPerFix}
                    disabled={gitBusy}
                    onChange={() => setGitForm({ ...gitForm, branchPerFix: !gitForm.branchPerFix })}
                  />
                  <span>
                    <strong>Create a branch per fix</strong> is {gitForm.branchPerFix ? "ON" : "off"}
                  </span>
                </label>
                <label className="switch-row">
                  <input
                    type="checkbox"
                    checked={gitForm.pushEnabled}
                    disabled={gitBusy}
                    onChange={() => setGitForm({ ...gitForm, pushEnabled: !gitForm.pushEnabled })}
                  />
                  <span>
                    <strong>Push to remote</strong> is {gitForm.pushEnabled ? "ON" : "off"} — needs a token
                    with write access
                  </span>
                </label>
                <label className="switch-row">
                  <input
                    type="checkbox"
                    checked={gitForm.autoMergeOnConfirm}
                    disabled={gitBusy}
                    onChange={() =>
                      setGitForm({ ...gitForm, autoMergeOnConfirm: !gitForm.autoMergeOnConfirm })
                    }
                  />
                  <span>
                    <strong>Merge &amp; delete branch on confirm</strong> is{" "}
                    {gitForm.autoMergeOnConfirm ? "ON" : "off"}
                  </span>
                </label>

                <div className="pipe-actions">
                  <button className="primary-btn" disabled={gitBusy} onClick={saveGit}>
                    {gitBusy ? "Saving…" : "Save Git source settings"}
                  </button>
                  <button
                    className="seg-btn"
                    disabled={gitBusy || !gitForm.repoUrl.trim() || !git?.enabled}
                    onClick={connectGit}
                    title={git?.enabled ? "" : "Set ENABLE_GIT_SOURCE=true on the backend to clone/branch/merge"}
                  >
                    {gitBusy ? "Working…" : git?.connected ? "Re-clone / fetch" : "Connect / Clone"}
                  </button>
                </div>
              </section>

              <section className="settings-card">
                <h3>Connection</h3>
                <p className="muted">Live state of the configured Git source.</p>
                <div className="tc-status">
                  <span className={`chip ${git?.connected ? "chip-ok" : "chip-warn"}`}>
                    {git?.connected ? "connected" : "not connected"}
                  </span>
                  {git?.repoUrlPreview && <span className="chip chip-info">{git.repoUrlPreview}</span>}
                  {git?.currentBranch && <span className="chip chip-info">on {git.currentBranch}</span>}
                  {git?.tokenConfigured && <span className="chip chip-ok">token set</span>}
                  {git?.dirty && <span className="chip chip-warn">uncommitted changes</span>}
                  {git?.lastError && <span className="chip chip-warn">{git.lastError}</span>}
                </div>

                {git && git.branches.length > 0 && (
                  <>
                    <p className="muted autonomy-label">Active fix branches</p>
                    <div className="problem-chips">
                      {git.branches.map((b) => (
                        <span key={b.name} className="mini-chip" title={b.problemId ? `Problem ${b.problemId}` : b.name}>
                          ⎇ {b.name}
                        </span>
                      ))}
                    </div>
                  </>
                )}
              </section>
            </>
          ) : (
            <section className="settings-card">
              <h3>Git source</h3>
              <p className="muted">Git source settings are unavailable — the backend isn't reachable.</p>
            </section>
          )}
        </div>
      )}

      {tab === "notifications" && (
        <div className="settings-panel">
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
        </div>
      )}
    </>
  );
}
