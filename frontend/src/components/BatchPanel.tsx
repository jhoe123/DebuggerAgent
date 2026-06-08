import { useState } from "react";
import type { BuildStrategy, DeployTarget, PipelineOptions, PipelineResult, Step, TestStrategy } from "../types";
import { runPipeline, unstagePatch } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useToast } from "../context/ToastContext";
import { AgentSteps } from "./AgentSteps";
import { DiffViewer } from "./DiffViewer";

// BatchPanel is the consolidated-patches + run-pipeline UI, docked in the Problems
// page. It collects every staged fix and test/build/deploys them in one run — so a
// user reviews all patches, then confirms a single deploy. Hidden when nothing is
// staged. The deploy gate (consoleAvailable) mirrors the inline pipeline it replaces.
export function BatchPanel() {
  const { staged, consoleAvailable, refreshPatches, refreshArtifacts, reloadHistory, setStreaming } = useAppData();
  const toast = useToast();
  const [opts, setOpts] = useState<PipelineOptions>({
    apply: true,
    test: true,
    build: true,
    deploy: true,
    testStrategy: "auto",
    buildStrategy: "auto",
    deployment: { target: "local" },
  });
  const [steps, setSteps] = useState<Step[]>([]);
  const [result, setResult] = useState<PipelineResult | null>(null);
  const [running, setRunning] = useState(false);

  if (staged.length === 0) return null;

  // Two patches touching the same file collapse to the latest-staged one on deploy.
  const files = staged.map((s) => s.file);
  const conflicts = [...new Set(files.filter((f, i) => files.indexOf(f) !== i))];

  async function remove(problemId: string) {
    try {
      await unstagePatch(problemId);
      await refreshPatches();
    } catch (e) {
      toast.error(`Couldn't remove patch: ${String(e)}`);
    }
  }

  async function run() {
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
      <div className="batch-head">
        <h3>
          Deployment batch <span className="mini-chip">{staged.length}</span>
        </h3>
        <p className="muted">
          Consolidate fixes from multiple problems, then test → build → deploy them all in one run.
        </p>
      </div>

      <div className="batch-list">
        {staged.map((s) => (
          <div key={s.problemId} className="batch-row">
            <div className="batch-row-top">
              <code className="mini-chip">{s.file}</code>
              <span className="batch-row-prob">{s.problemId}</span>
              <button className="problem-dismiss batch-remove" title="Remove from batch" onClick={() => remove(s.problemId)} disabled={running}>
                ✕
              </button>
            </div>
            {s.rationale && <div className="batch-row-rationale muted">{s.rationale}</div>}
            {s.unifiedDiff && (
              <details className="alternatives">
                <summary>View diff</summary>
                <DiffViewer diff={s.unifiedDiff} />
              </details>
            )}
          </div>
        ))}
      </div>

      {conflicts.length > 0 && (
        <p className="batch-conflict">
          ⚠ Multiple patches target {conflicts.map((f) => <code key={f}>{f}</code>)} — only the most
          recently staged one will be applied.
        </p>
      )}

      <div className="pipeline-opts pipeline-strategy">
        <label className="strategy-select">
          Test
          <select
            value={opts.testStrategy}
            onChange={(e) => setOpts((o) => ({ ...o, testStrategy: e.target.value as TestStrategy }))}
            disabled={running}
          >
            <option value="auto">auto (reuse or generate)</option>
            <option value="reuse">reuse only</option>
            <option value="generate">generate fresh</option>
            <option value="skip">skip</option>
          </select>
        </label>
        <label className="strategy-select">
          Build
          <select
            value={opts.buildStrategy}
            onChange={(e) => setOpts((o) => ({ ...o, buildStrategy: e.target.value as BuildStrategy }))}
            disabled={running}
          >
            <option value="auto">auto (script or go build)</option>
            <option value="script">build script</option>
            <option value="default">go build</option>
          </select>
        </label>
        <label className="strategy-select">
          Deploy
          <select
            value={opts.deployment?.target ?? "local"}
            onChange={(e) => setOpts((o) => ({ ...o, deployment: { ...o.deployment, target: e.target.value as DeployTarget } }))}
            disabled={running}
          >
            <option value="local">local process</option>
            <option value="docker">docker</option>
            <option value="script">deploy script</option>
            <option value="cloud-run">cloud run (cloud build)</option>
          </select>
        </label>
        <button className="run-btn" onClick={run} disabled={running || !consoleAvailable} title={consoleAvailable ? "" : "Deploy needs the local Test Console or the cloud-build runner"}>
          {running ? "Deploying…" : `Deploy all ${staged.length} patch${staged.length === 1 ? "" : "es"}`}
        </button>
      </div>
      {!consoleAvailable && (
        <p className="muted">Deploy is unavailable here (no local/cloud runner) — patches stay staged.</p>
      )}

      <AgentSteps steps={steps} />
      {result && (
        <p className={result.success ? "approved" : "failed"}>
          {result.success ? "✓ All patches deployed" : "✗ Pipeline stopped (a gate failed)"}
          {result.verify && <span className="muted"> · verify: {result.verify}</span>}
        </p>
      )}
    </section>
  );
}
