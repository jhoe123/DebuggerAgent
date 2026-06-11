import { useEffect, useState } from "react";
import { useSettings, type ThemeMode } from "../context/SettingsContext";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import {
  connectGitSourceStream,
  getGitSource,
  getPipelineConfig,
  getSlack,
  setGitSourceConfig,
  setPipelineConfig,
  setSlackConfig,
  testSlack,
  validateGitSource,
} from "../api";
import type {
  BuildStrategy,
  DeployTarget,
  GitSourceConfig,
  GitSourceStatus,
  PipelineSettings,
  SlackStatus,
  Step,
  TestStrategy,
} from "../types";
import { AgentSteps } from "../components/AgentSteps";

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

// Result of probing a repo URL (ls-remote) before cloning: drives the branch chooser.
type GitValidation = {
  status: "idle" | "validating" | "valid" | "invalid";
  branches: string[];
  defaultBranch: string;
  error: string;
};
const GIT_VAL_IDLE: GitValidation = { status: "idle", branches: [], defaultBranch: "", error: "" };

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

// normRepoUrl mirrors the backend's canonRepoURL: embedded userinfo stripped,
// backslashes normalized, trailing "/" and ".git" trimmed, lowercased — so the
// masked preview the form is seeded with never reads as a different repository.
function normRepoUrl(u: string): string {
  let s = u.trim().replace(/\\/g, "/");
  const i = s.indexOf("://");
  if (i >= 0) {
    const at = s.lastIndexOf("@");
    if (at > i) s = s.slice(0, i + 3) + s.slice(at + 1);
  }
  return s.replace(/\/+$/, "").replace(/\.git$/i, "").toLowerCase();
}

// swapKindOf detects a destructive re-target BEFORE applying: the form points at a
// different repository or working branch than the currently connected one. Applying
// such a change stops in-flight automation and (repo change) replaces the workspace.
function swapKindOf(form: GitSourceConfig, status: GitSourceStatus): { repo: boolean; branch: boolean } | null {
  if (!status.connected) return null;
  const repo = !!form.repoUrl.trim() && normRepoUrl(form.repoUrl) !== normRepoUrl(status.repoUrlPreview ?? "");
  const branch = !!form.workingBranch.trim() && form.workingBranch !== status.workingBranch;
  return repo || branch ? { repo, branch } : null;
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
  const { config, setConfig, localMode, activeCount } = useAutopilot();
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
  const [gitVal, setGitVal] = useState<GitValidation>(GIT_VAL_IDLE);
  const [branchMode, setBranchMode] = useState<"existing" | "new">("existing");
  const [gitSteps, setGitSteps] = useState<Step[]>([]);
  const [gitError, setGitError] = useState("");
  const [showAdv, setShowAdv] = useState(false);
  const [confirmSwap, setConfirmSwap] = useState(false);

  // A destructive re-target awaiting confirmation (different repo/branch than connected).
  const gitSwap = git && gitForm ? swapKindOf(gitForm, git) : null;

  // Any edit to the form invalidates a pending replace confirmation.
  useEffect(() => {
    setConfirmSwap(false);
  }, [gitForm]);

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

  // Editing the URL or token invalidates any prior validation (its branch list is stale).
  function resetGitValidation() {
    setGitVal((v) => (v.status === "idle" ? v : GIT_VAL_IDLE));
  }

  // Step 1: probe the repo (ls-remote) without cloning, to confirm reachability and list
  // the remote branches the branch chooser offers.
  async function runValidate() {
    if (!gitForm) return;
    const repoUrl = gitForm.repoUrl.trim();
    if (!repoUrl) {
      setGitVal({ ...GIT_VAL_IDLE, status: "invalid", error: "Enter a repository URL." });
      return;
    }
    setGitVal({ ...GIT_VAL_IDLE, status: "validating" });
    try {
      const res = await validateGitSource(repoUrl, gitTok.trim() || undefined);
      if (!res.valid) {
        setGitVal({ ...GIT_VAL_IDLE, status: "invalid", error: res.error || "Repository is not reachable." });
        return;
      }
      const branches = res.branches ?? [];
      const def = res.defaultBranch || branches[0] || "";
      setGitVal({ status: "valid", branches, defaultBranch: def, error: "" });
      setBranchMode("existing");
      // Seed the working branch: keep the current one if it's a known remote branch, else
      // fall back to the repo's default branch.
      setGitForm((f) => {
        if (!f) return f;
        const keep = !!f.workingBranch && branches.includes(f.workingBranch);
        return { ...f, workingBranch: keep ? f.workingBranch : def, baseBranch: "" };
      });
    } catch (e) {
      setGitVal({ ...GIT_VAL_IDLE, status: "invalid", error: String(e) });
    }
  }

  // Step 2: persist the config, then (when writes are enabled) clone/fetch with a live
  // step timeline. Surfaces validation/clone issues inline. Re-targeting the repo or
  // working branch is destructive (stops runs, replaces the workspace) — it must be
  // confirmed via the warning panel first.
  async function applyGit() {
    if (!gitForm) return;
    if (gitSwap && !confirmSwap) {
      setConfirmSwap(true); // show the replace warning; its danger button re-invokes applyGit
      return;
    }
    setConfirmSwap(false);
    setGitBusy(true);
    setGitError("");
    setGitSteps([]);
    try {
      const saved = await setGitSourceConfig({ ...gitForm, authToken: gitTok.trim() || undefined });
      setGit(saved);
      if (saved.targetChanged) {
        const parts = [
          saved.haltedRuns ? `${saved.haltedRuns} in-flight run${saved.haltedRuns === 1 ? "" : "s"} halted` : "",
          saved.workspaceReset ? "old workspace removed" : "",
          saved.patchesCleared ? "pending patches cleared" : "",
        ].filter(Boolean);
        toast.info(`Previous target replaced${parts.length ? " — " + parts.join(", ") : ""}`);
      }
      if (gitTok) setGitTok("");
      if (!saved.enabled) {
        setGitForm(statusToConfig(saved));
        toast.success("Git source settings saved — set ENABLE_GIT_SOURCE=true to clone.");
        return;
      }
      const final = await connectGitSourceStream((s) => setGitSteps((prev) => [...prev, s]));
      setGit(final);
      setGitForm(statusToConfig(final));
      if (final.lastError) {
        setGitError(final.lastError);
        toast.error(`Connected with issues: ${final.lastError}`);
      } else {
        setGitVal(GIT_VAL_IDLE); // collapse the chooser; the Connection card shows the truth
        toast.success(
          final.connected
            ? `Connected to ${final.repoUrlPreview ?? "repository"} on ${final.currentBranch ?? final.workingBranch}`
            : "Applied — check status",
        );
      }
    } catch (e) {
      setGitError(String(e));
      toast.error(`Apply failed: ${String(e)}`);
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
                <h3>App link &amp; health check</h3>
                <p className="muted">
                  <strong>App URL</strong> is the public address of the deployed app, surfaced as the “Open app”
                  link in the header and after a deploy. Leave blank to auto-detect (local Test Console) or fall
                  back to the health URL below.
                </p>
                <label className="pipe-field">
                  <span>App URL</span>
                  <input
                    value={pipe.appUrl ?? ""}
                    placeholder="https://your-app.example.com"
                    disabled={pipeBusy}
                    onChange={(e) => setPipe({ ...pipe, appUrl: e.target.value })}
                  />
                </label>
                <p className="muted">
                  <strong>Health check URL</strong> is pinged to confirm the app is reachable (drives the demo
                  badge in Problems &amp; Status). A full URL or a path on the app — leave blank to use its base URL.
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
                    {pipeBusy ? "Saving…" : "Save app & health"}
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
              {/* Step 1 — Repository: paste the URL (+ optional token) and validate. */}
              <section className="settings-card">
                <h3>Repository</h3>
                <p className="git-hint">
                  The Git repo PatchPilot clones and manages fixes in. Paste the HTTPS URL — add a
                  personal access token for a private repo — then validate. The token is a secret and is
                  never returned by the API.
                </p>
                <div className="git-stack">
                  <label className="pipe-field">
                    <span>Repository URL</span>
                    <input
                      value={gitForm.repoUrl}
                      disabled={gitBusy}
                      placeholder="https://github.com/you/repo.git"
                      onChange={(e) => {
                        setGitForm({ ...gitForm, repoUrl: e.target.value });
                        resetGitValidation();
                      }}
                    />
                  </label>
                  <label className="pipe-field">
                    <span>
                      Access token <span className="muted">— optional, for private repos</span>
                    </span>
                    <input
                      type="password"
                      value={gitTok}
                      placeholder={git?.tokenConfigured ? "Replace saved token…" : "ghp_… / glpat-…"}
                      disabled={gitBusy}
                      onChange={(e) => {
                        setGitTok(e.target.value);
                        resetGitValidation();
                      }}
                      aria-label="Git auth token"
                    />
                  </label>
                </div>
                <div className="git-status-row">
                  <button
                    className="seg-btn"
                    disabled={gitBusy || gitVal.status === "validating" || !gitForm.repoUrl.trim()}
                    onClick={runValidate}
                  >
                    {gitVal.status === "validating"
                      ? "Validating…"
                      : gitVal.status === "valid"
                        ? "Re-validate"
                        : "Validate"}
                  </button>
                  {gitVal.status === "valid" && (
                    <span className="chip chip-ok">
                      ✓ reachable · {gitVal.branches.length} branch{gitVal.branches.length === 1 ? "" : "es"}
                    </span>
                  )}
                  {gitVal.status === "invalid" && <span className="chip chip-warn">{gitVal.error}</span>}
                  {gitVal.status === "idle" && git?.tokenConfigured && (
                    <span className="chip chip-info">token saved</span>
                  )}
                </div>
              </section>

              {/* Step 2 — Working branch: pick an existing branch or create a new one off a base. */}
              <section className="settings-card">
                <h3>Working branch</h3>
                <p className="git-hint">The branch PatchPilot integrates confirmed fixes into.</p>
                {gitVal.status === "valid" ? (
                  <div className="git-stack">
                    <div className="seg">
                      <button
                        type="button"
                        className={`seg-btn${branchMode === "existing" ? " active" : ""}`}
                        disabled={gitBusy}
                        onClick={() => {
                          setBranchMode("existing");
                          setGitForm({
                            ...gitForm,
                            workingBranch: gitVal.branches.includes(gitForm.workingBranch)
                              ? gitForm.workingBranch
                              : gitVal.defaultBranch,
                            baseBranch: "",
                          });
                        }}
                      >
                        Use existing
                      </button>
                      <button
                        type="button"
                        className={`seg-btn${branchMode === "new" ? " active" : ""}`}
                        disabled={gitBusy}
                        onClick={() => {
                          setBranchMode("new");
                          setGitForm({
                            ...gitForm,
                            workingBranch: "",
                            baseBranch: gitForm.baseBranch || gitVal.defaultBranch,
                          });
                        }}
                      >
                        Create new
                      </button>
                    </div>
                    {branchMode === "existing" ? (
                      <label className="pipe-field">
                        <span>Branch</span>
                        <select
                          value={gitForm.workingBranch}
                          disabled={gitBusy}
                          onChange={(e) => setGitForm({ ...gitForm, workingBranch: e.target.value, baseBranch: "" })}
                        >
                          {gitVal.branches.map((b) => (
                            <option key={b} value={b}>
                              {b}
                              {b === gitVal.defaultBranch ? " (default)" : ""}
                            </option>
                          ))}
                        </select>
                      </label>
                    ) : (
                      <div className="pipe-grid">
                        <label className="pipe-field">
                          <span>New branch name</span>
                          <input
                            value={gitForm.workingBranch}
                            disabled={gitBusy}
                            placeholder="patchpilot"
                            onChange={(e) => setGitForm({ ...gitForm, workingBranch: e.target.value })}
                          />
                        </label>
                        <label className="pipe-field">
                          <span>Created from</span>
                          <select
                            value={gitForm.baseBranch || gitVal.defaultBranch}
                            disabled={gitBusy}
                            onChange={(e) => setGitForm({ ...gitForm, baseBranch: e.target.value })}
                          >
                            {gitVal.branches.map((b) => (
                              <option key={b} value={b}>
                                {b}
                                {b === gitVal.defaultBranch ? " (default)" : ""}
                              </option>
                            ))}
                          </select>
                        </label>
                      </div>
                    )}
                  </div>
                ) : (
                  <p className="git-hint" style={{ marginBottom: 0 }}>
                    {gitForm.workingBranch ? (
                      <>
                        Currently <strong>{gitForm.workingBranch}</strong>.{" "}
                      </>
                    ) : null}
                    Validate the repository above to choose a branch or create a new one.
                  </p>
                )}
              </section>

              {/* Advanced — commit identity, branch naming, push/merge behavior (collapsed by default). */}
              <section className="settings-card">
                <button
                  type="button"
                  className="git-disclosure"
                  aria-expanded={showAdv}
                  onClick={() => setShowAdv((s) => !s)}
                >
                  <span aria-hidden>{showAdv ? "▾" : "▸"}</span> Advanced options
                </button>
                {showAdv && (
                  <>
                    <hr className="git-divider" />
                    <div className="git-stack">
                      <label className="pipe-field">
                        <span>Fix branch prefix</span>
                        <input
                          value={gitForm.branchPrefix}
                          disabled={gitBusy}
                          placeholder="patchpilot/fix-"
                          onChange={(e) => setGitForm({ ...gitForm, branchPrefix: e.target.value })}
                        />
                      </label>
                      <div className="pipe-grid">
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
                    </div>
                    <hr className="git-divider" />
                    <div className="git-toggles">
                      <label className="switch-row">
                        <input
                          type="checkbox"
                          checked={gitForm.branchPerFix}
                          disabled={gitBusy}
                          onChange={() => setGitForm({ ...gitForm, branchPerFix: !gitForm.branchPerFix })}
                        />
                        <span>
                          <strong>Branch per fix</strong>{" "}
                          <span className="muted">— isolate each fix on its own branch</span>
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
                          <strong>Push to remote</strong>{" "}
                          <span className="muted">— needs a token with write access</span>
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
                          <strong>Merge on confirm</strong>{" "}
                          <span className="muted">— merge &amp; delete the fix branch when confirmed</span>
                        </span>
                      </label>
                    </div>
                  </>
                )}
              </section>

              {/* Apply — persist config + clone, then show live connection status. */}
              <section className="settings-card">
                <h3>Apply &amp; connect</h3>
                <p className="git-hint">
                  Saves the settings above and {git?.enabled ? "clones the repository" : "stores them"} —
                  cloning streams its progress below.
                </p>
                {confirmSwap && gitSwap ? (
                  <div className="git-swap-warning" role="alert">
                    <strong>Replace the connected project?</strong>
                    <p>
                      Applying will stop{" "}
                      {activeCount > 0
                        ? `${activeCount} active autopilot run${activeCount === 1 ? "" : "s"}`
                        : "any in-flight automation"}
                      , clear pending patch proposals, and{" "}
                      {gitSwap.repo
                        ? "delete the current workspace clone — unconfirmed local fix branches are lost."
                        : `switch the working branch to ${gitForm.workingBranch}.`}
                    </p>
                    <div className="pipe-actions" style={{ marginTop: 0 }}>
                      <button className="danger-btn" disabled={gitBusy} onClick={applyGit}>
                        {gitBusy ? "Replacing…" : "Stop runs & replace"}
                      </button>
                      <button className="seg-btn" disabled={gitBusy} onClick={() => setConfirmSwap(false)}>
                        Cancel
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="pipe-actions" style={{ marginTop: 0 }}>
                    <button
                      className="primary-btn"
                      disabled={
                        gitBusy ||
                        !gitForm.repoUrl.trim() ||
                        !gitForm.workingBranch.trim() ||
                        !(gitVal.status === "valid" || git?.connected)
                      }
                      onClick={applyGit}
                      title={gitVal.status === "valid" || git?.connected ? "" : "Validate the repository first"}
                    >
                      {gitBusy ? "Applying…" : git?.enabled ? "Apply changes & clone" : "Save settings"}
                    </button>
                    {!git?.enabled && (
                      <span className="chip chip-info">read-only — set ENABLE_GIT_SOURCE=true to clone</span>
                    )}
                  </div>
                )}

                {gitError && (
                  <div className="git-status-row">
                    <span className="chip chip-warn">{gitError}</span>
                  </div>
                )}
                {gitSteps.length > 0 && <AgentSteps steps={gitSteps} title="Clone progress" />}

                <hr className="git-divider" />
                <div className="git-status-row" style={{ marginTop: 0 }}>
                  <span className={`chip ${git?.connected ? "chip-ok" : "chip-warn"}`}>
                    {git?.connected ? "connected" : "not connected"}
                  </span>
                  {git?.repoUrlPreview && <span className="chip chip-info">{git.repoUrlPreview}</span>}
                  {git?.currentBranch && <span className="chip chip-info">on {git.currentBranch}</span>}
                  {git?.dirty && <span className="chip chip-warn">uncommitted changes</span>}
                  {git?.lastError && !gitError && <span className="chip chip-warn">{git.lastError}</span>}
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
