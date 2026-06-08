import { useState } from "react";
import type { BuildStrategy, DeployTarget, PipelineOptions, PipelineResult, Step, TestStrategy } from "../types";
import { remediate } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useToast } from "../context/ToastContext";
import { AgentSteps } from "./AgentSteps";

type StageKey = "apply" | "test" | "build" | "deploy";
const STAGES: StageKey[] = ["apply", "test", "build", "deploy"];

// Configurable auto-remediation pipeline. Streams Apply→Test→Build→Deploy→Verify;
// deploy is gated on tests passing (the backend stops the run if tests fail).
export function Pipeline({
  available,
  problemId,
  onComplete,
  initialResult,
}: {
  available: boolean;
  problemId: string;
  onComplete?: (r: PipelineResult | null) => void;
  initialResult?: PipelineResult | null;
}) {
  const { setStreaming } = useAppData();
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
  const [steps, setSteps] = useState<Step[]>(initialResult?.steps ?? []);
  const [result, setResult] = useState<PipelineResult | null>(initialResult ?? null);
  const [running, setRunning] = useState(false);

  if (!available) return null;

  const scenario: "error" | "performance" = problemId.startsWith("performance:")
    ? "performance"
    : "error";

  async function run() {
    setRunning(true);
    setStreaming(true);
    setSteps([]);
    setResult(null);
    let final: PipelineResult | null = null;
    try {
      final = await remediate(problemId, { ...opts, scenario }, (s) => setSteps((prev) => [...prev, s]));
      setResult(final);
      if (final.success) toast.success("Remediation succeeded");
      else toast.error("Remediation stopped — a gate failed");
    } catch (e) {
      setSteps((prev) => [...prev, { stage: "error", status: "fail", message: String(e) }]);
      toast.error(`Pipeline error: ${String(e)}`);
    } finally {
      setRunning(false);
      setStreaming(false);
      onComplete?.(final);
    }
  }

  return (
    <section className="pipeline">
      <h3>
        Auto-remediation pipeline <span className="mini-chip">{scenario}</span>
      </h3>
      <p className="muted">
        Apply → Test → Build → Deploy → Verify. Deploy is gated on the {scenario} regression test
        passing — autopilot won't ship a broken fix. Requires a proposed patch (run Investigate first).
      </p>
      <div className="pipeline-opts">
        {STAGES.map((k) => (
          <label key={k} className="stage-toggle">
            <input
              type="checkbox"
              checked={opts[k]}
              onChange={() => setOpts((o) => ({ ...o, [k]: !o[k] }))}
              disabled={running}
            />{" "}
            {k}
          </label>
        ))}
        <button className="run-btn" onClick={run} disabled={running}>
          {running ? "Running…" : "Run pipeline"}
        </button>
      </div>
      <div className="pipeline-opts pipeline-strategy">
        <label className="strategy-select">
          Test
          <select
            value={opts.testStrategy}
            onChange={(e) => setOpts((o) => ({ ...o, testStrategy: e.target.value as TestStrategy }))}
            disabled={running}
            title="auto: reuse an existing test or let the agent generate one (lazy gate)"
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
            title="auto: run a detected build script, else go build"
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
            title="where/how to ship the fix"
          >
            <option value="local">local process</option>
            <option value="docker">docker</option>
            <option value="script">deploy script</option>
            <option value="cloud-run">cloud run (cloud build)</option>
          </select>
        </label>
      </div>
      <AgentSteps steps={steps} />
      {result && (
        <p className={result.success ? "approved" : "failed"}>
          {result.success ? "✓ Remediation succeeded — demo_app is fixed" : "✗ Remediation stopped (gate failed)"}
          {result.verify && <span className="muted"> · verify: {result.verify}</span>}
        </p>
      )}
    </section>
  );
}
