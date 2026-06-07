import { useState } from "react";
import type { PipelineOptions, PipelineResult, Step } from "../types";
import { remediate } from "../api";
import { AgentSteps } from "./AgentSteps";

const STAGES: (keyof PipelineOptions)[] = ["apply", "test", "build", "deploy"];

// Configurable auto-remediation pipeline. Streams Apply→Test→Build→Deploy→Verify;
// deploy is gated on tests passing (the backend stops the run if tests fail).
export function Pipeline({ available }: { available: boolean }) {
  const [opts, setOpts] = useState<PipelineOptions>({ apply: true, test: true, build: true, deploy: true });
  const [steps, setSteps] = useState<Step[]>([]);
  const [result, setResult] = useState<PipelineResult | null>(null);
  const [running, setRunning] = useState(false);

  if (!available) return null;

  async function run() {
    setRunning(true);
    setSteps([]);
    setResult(null);
    try {
      const r = await remediate(opts, (s) => setSteps((prev) => [...prev, s]));
      setResult(r);
    } catch (e) {
      setSteps((prev) => [...prev, { stage: "error", status: "fail", message: String(e) }]);
    } finally {
      setRunning(false);
    }
  }

  return (
    <section className="pipeline">
      <h3>Auto-remediation pipeline</h3>
      <p className="muted">
        Apply → Test → Build → Deploy → Verify. Deploy is gated on tests passing — autopilot won't
        ship a broken fix. Requires a proposed patch (run Investigate first).
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
      <AgentSteps steps={steps} />
      {result && (
        <p className={result.success ? "approved" : "failed"}>
          {result.success ? "✓ Remediation succeeded — demo_app is fixed" : "✗ Remediation stopped (gate failed)"}
        </p>
      )}
    </section>
  );
}
