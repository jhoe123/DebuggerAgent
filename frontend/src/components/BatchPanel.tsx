import { useState, useEffect } from "react";
import type { ArtifactStageKey, DeployTarget, PipelineOptions, PipelineResult, Step } from "../types";
import { runPipeline, unstagePatch, getPipelineConfig } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useToast } from "../context/ToastContext";
import { AgentSteps } from "./AgentSteps";
import { DiffViewer } from "./DiffViewer";

const OVERALL_CHIP: Record<string, { label: string; cls: string }> = {
  investigated: { label: "investigated ✓", cls: "status-investigated" },
  staged: { label: "staged ◷", cls: "status-staged" },
  running: { label: "running ⟳", cls: "status-running" },
  deployed: { label: "deployed ✓", cls: "status-patched" },
  failed: { label: "failed ✗", cls: "status-failed" },
  confirmed: { label: "merged ✓", cls: "status-confirmed" },
};

// Pipeline subset of the lifecycle, shown per batch row so each problem's stages
// visibly advance while a deploy runs (artifacts poll faster during a stream).
const PIPELINE_STAGES: { key: ArtifactStageKey; label: string }[] = [
  { key: "test", label: "Test" },
  { key: "build", label: "Build" },
  { key: "deploy", label: "Deploy" },
  { key: "verify", label: "Verify" },
];
const STAGE_ICON: Record<string, string> = { ok: "✓", failed: "✗", running: "⟳", pending: "•" };

// BatchPanel is the consolidated-patches + run-pipeline control, mounted at the top
// of the Problems page. Collapsed by default: a slim bar with a "Deploy {N}" button
// and an expand toggle that reveals the staged list, strategy selectors, live steps,
// and result. Hidden until something is staged (or a run is in flight / just finished).
export function BatchPanel() {
  const { staged, consoleAvailable, refreshPatches, refreshArtifacts, reloadHistory, streaming, setStreaming, artifactMap, clearPatches, demoAppUrl, demoAppName } = useAppData();
  const toast = useToast();
  const [opts, setOpts] = useState<PipelineOptions>({
    apply: true,
    test: true,
    build: true,
    deploy: true,
  });
  const [deployTarget, setDeployTarget] = useState<DeployTarget>("local");
  const [runnerAvailable, setRunnerAvailable] = useState(false);
  const [steps, setSteps] = useState<Step[]>([]);
  const [result, setResult] = useState<PipelineResult | null>(null);
  const [running, setRunning] = useState(false);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    getPipelineConfig()
      .then((cfg) => {
        setDeployTarget(cfg.deployTarget);
        setRunnerAvailable(!!cfg.runnerAvailable);
      })
      .catch(console.error);
  }, []);

  // Deploy is usable when a runner is wired (local democtl OR the cloud-build runner),
  // independent of whether the local Test Console (/api/test/status) is mounted — so the
  // hosted cloud-build deployment can deploy without ENABLE_TEST_CONSOLE.
  const canDeploy = consoleAvailable || runnerAvailable;

  const isCurrentlyRunning = staged.some((s) => artifactMap[s.problemId]?.overall === "running");
  const isDeploying = running || isCurrentlyRunning;
  const hasFailed = staged.some((s) => artifactMap[s.problemId]?.overall === "failed");

  // Display states that persist across refreshes
  const persistedSteps = staged.length > 0 ? (artifactMap[staged[0].problemId]?.steps ?? []) : [];
  const activeSteps = steps.length > 0
    ? steps
    : (persistedSteps.length > 0
        ? persistedSteps
        : (isCurrentlyRunning
            ? [{ stage: "deploy", status: "running", message: "Deployment in progress..." } as Step]
            : []
          )
      );
  const hasSucceeded = staged.length > 0 && !isDeploying && staged.every((s) => {
    const status = artifactMap[s.problemId]?.overall;
    return status === "deployed" || status === "confirmed";
  });
  const verifyUrl = staged.map(s => artifactMap[s.problemId]?.verify).find(Boolean);
  const showResult = result || (hasFailed ? { success: false } as PipelineResult : (hasSucceeded ? { success: true, verify: verifyUrl } as PipelineResult : null));

  const n = staged.length;
  // Stay mounted through a run and to show its result even after the batch clears.
  if (n === 0 && !isDeploying && !showResult) return null;

  // Two patches touching the same file collapse to the latest-staged one on deploy.
  const fileList = staged.map((s) => s.file);
  const conflicts = [...new Set(fileList.filter((f, i) => fileList.indexOf(f) !== i))];

  async function remove(problemId: string) {
    try {
      await unstagePatch(problemId);
      await refreshPatches();
    } catch (e) {
      toast.error(`Couldn't remove patch: ${String(e)}`);
    }
  }

  async function clear() {
    try {
      await clearPatches();
      toast.success("Deployment batch cleared");
    } catch (e) {
      toast.error(`Couldn't clear batch: ${String(e)}`);
    }
  }

  async function run() {
    setExpanded(true); // surface progress while deploying
    setRunning(true);
    setStreaming(true);
    setSteps([]);
    setResult(null);
    let final: PipelineResult | null = null;
    try {
      final = await runPipeline(opts, (s) => setSteps((prev) => [...prev, s]));
      setResult(final);
      if (final.success) toast.success("All patches deployed");
      else toast.error("Pipeline stopped — a gate failed");
    } catch (e) {
      setSteps((prev) => [...prev, { stage: "error", status: "fail", message: String(e) }]);
      toast.error(`Pipeline error: ${String(e)}`);
    } finally {
      setRunning(false);
      setStreaming(false);
      await refreshPatches();
      await refreshArtifacts();
      reloadHistory();
    }
  }

  return (
    <section className="batch-panel">
      <div className="batch-bar">
        <button
          className="batch-toggle"
          onClick={() => setExpanded((e) => !e)}
          aria-expanded={expanded}
          title={expanded ? "Hide details" : "Show details"}
        >
          <span className="batch-chevron">{expanded ? "▾" : "▸"}</span>
          Deployment batch <span className="mini-chip">{n}</span>
        </button>
        {n > 0 && (
          <div style={{ display: "flex", gap: "0.5rem", marginLeft: "auto" }}>
            <button
              className="ghost-btn"
              onClick={clear}
              disabled={isDeploying || streaming}
              title={isDeploying || streaming ? "Disabled while a run is in progress" : "Unstage every patch in the batch"}
              style={{ border: "1px solid var(--border)", color: "var(--text)" }}
            >
              Clear batch
            </button>
            <button
              className="run-btn"
              onClick={run}
              disabled={isDeploying || !canDeploy || streaming}
              title={
                !canDeploy
                  ? "Deploy needs the local Test Console or the cloud-build runner"
                  : isDeploying || streaming
                    ? "A run is already in progress"
                    : "Apply, test, build and deploy every staged patch in one pipeline"
              }
            >
              {isDeploying ? "Deploying…" : hasSucceeded ? `Redeploy ${n}` : `Deploy ${n}`}
            </button>
          </div>
        )}
      </div>

      {expanded && (
        <div className="batch-body">
          {n > 0 && (
            <div className="batch-list">
              {staged.map((s) => {
                const art = artifactMap[s.problemId];
                const chip = art ? OVERALL_CHIP[art.overall] : null;
                return (
                  <div key={s.problemId} className="batch-row">
                    <div className="batch-row-top">
                      <code className="mini-chip">{s.file}</code>
                      <span className="batch-row-prob">{s.problemId}</span>
                      {chip && (
                        <span className={`mini-chip ${chip.cls}`} style={{ marginLeft: "0.5rem" }} title={`Lifecycle: ${art!.overall}`}>
                          {chip.label}
                        </span>
                      )}
                      <button
                        className="problem-dismiss batch-remove"
                        title={running || streaming ? "Disabled while a run is in progress" : "Remove from batch"}
                        onClick={() => remove(s.problemId)}
                        disabled={running || streaming}
                      >
                        ✕
                      </button>
                    </div>
                    {art?.stages && PIPELINE_STAGES.some(({ key }) => art.stages[key]) && (
                      <div className="batch-row-stages">
                        {PIPELINE_STAGES.map(({ key, label }) => {
                          const st = art.stages[key];
                          const status = st?.status ?? "pending";
                          return (
                            <span key={key} className={`stage-chip stage-${status}`} title={st?.detail || label}>
                              <span className={status === "running" ? "ap-spin" : ""}>{STAGE_ICON[status] ?? "•"}</span> {label}
                            </span>
                          );
                        })}
                      </div>
                    )}
                    {s.rationale && <div className="batch-row-rationale muted">{s.rationale}</div>}
                    {s.unifiedDiff && (
                      <details className="collapsible-details">
                        <summary>
                          <span>View patch diff</span>
                        </summary>
                        <div className="collapsible-details-content">
                          <DiffViewer diff={s.unifiedDiff} />
                        </div>
                      </details>
                    )}
                  </div>
                );
              })}
            </div>
          )}

          {conflicts.length > 0 && (
            <p className="batch-conflict">
              ⚠ Multiple patches target {conflicts.map((f) => <code key={f}>{f}</code>)} — only the most
              recently staged one will be applied.
            </p>
          )}

          {n > 0 && deployTarget === "cloud-run" && (
            <div style={{ marginTop: "1rem" }}>
              <label className="checkbox-label" style={{ display: "flex", alignItems: "center", gap: "0.5rem", cursor: "pointer" }}>
                <input
                  type="checkbox"
                  checked={opts.forceSync ?? false}
                  onChange={(e) => setOpts((o) => ({ ...o, forceSync: e.target.checked }))}
                  disabled={isDeploying}
                />
                <strong>Force full source upload</strong> (uploads the entire repository instead of a patch overlay)
              </label>
            </div>
          )}

          {!canDeploy && n > 0 && (
            <p className="muted">Deploy is unavailable here (no local/cloud runner) — patches stay staged.</p>
          )}

          <AgentSteps steps={activeSteps} />
          {showResult && (
            <p className={showResult.success ? "approved" : "failed"}>
              {showResult.success ? "✓ All patches deployed" : "✗ Pipeline stopped (a gate failed)"}
              {showResult.verify && <span className="muted"> · verify: {showResult.verify}</span>}
              {showResult.success && demoAppUrl && (
                <>
                  {" · "}
                  <a href={demoAppUrl} target="_blank" rel="noreferrer">
                    Open {demoAppName ?? "the patched app"} ↗
                  </a>
                </>
              )}
            </p>
          )}
        </div>
      )}
    </section>
  );
}
