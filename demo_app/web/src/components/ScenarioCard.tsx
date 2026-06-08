import { useState } from "react";
import type { Scenario } from "../scenarios";
import type { RunResult } from "../api";
import { SignalBadge } from "./SignalBadge";
import { BugCue } from "./BugCue";
import { TraceIdChip } from "./TraceIdChip";

interface Props {
  scenario: Scenario;
  onRun: (s: Scenario) => Promise<void>;
  onReset?: (s: Scenario) => Promise<void>;
  last?: RunResult;
  // From the build self-check: set for issue scenarios whose bug the running binary tracks
  // (checkout/report). true = patched, false = still buggy, undefined = no signal yet.
  fixed?: boolean;
}

function resultClass(r: RunResult): string {
  if (!r.completed || r.status >= 500) return "err";
  if (r.status >= 400) return "warn";
  return "ok";
}

export function ScenarioCard({ scenario, onRun, onReset, last, fixed }: Props) {
  const [busy, setBusy] = useState(false);

  const guard = (fn: () => Promise<void>) => async () => {
    setBusy(true);
    try {
      await fn();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={`scenario-card${scenario.isIssue ? (fixed ? " fixed" : " issue") : ""}`}>
      <div className="scenario-head">
        <h3>{scenario.title}</h3>
        {scenario.isIssue &&
          (fixed ? (
            <span className="fixed-cue" title="The running binary no longer exhibits this issue">
              ✓ fixed
            </span>
          ) : (
            <BugCue />
          ))}
      </div>
      <p className="framing">{scenario.framing}</p>
      <div className="badges">
        {scenario.signals.map((s) => (
          <SignalBadge key={s} kind={s} />
        ))}
      </div>
      <p className="how-to">{scenario.howToTrigger}</p>
      <div className="actions">
        <button className="trigger" onClick={guard(() => onRun(scenario))} disabled={busy}>
          {busy ? "Working…" : scenario.title}
        </button>
        {scenario.resetEndpoint && onReset && (
          <button className="reset" onClick={guard(() => onReset(scenario))} disabled={busy}>
            Reset
          </button>
        )}
      </div>
      {last && (
        <div className={`result ${resultClass(last)}`}>
          <span className="status">{last.completed ? `HTTP ${last.status}` : "no response"}</span>
          <span className="latency">{last.latencyMs} ms</span>
          <TraceIdChip traceId={last.traceId} />
        </div>
      )}
    </div>
  );
}
