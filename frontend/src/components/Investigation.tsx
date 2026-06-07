import { useState } from "react";
import type { ApproveResult, Investigation } from "../types";
import { approvePatch } from "../api";
import { DiffViewer } from "./DiffViewer";

export function InvestigationPanel({ data }: { data: Investigation }) {
  const { rootCause, confidence, alternatives, proposedPatch, suggestedTest } = data;
  const [approving, setApproving] = useState(false);
  const [result, setResult] = useState<ApproveResult | null>(null);

  async function onApprove() {
    setApproving(true);
    try {
      setResult(await approvePatch(data.problemId));
    } finally {
      setApproving(false);
    }
  }

  return (
    <section className="investigation">
      <div className="rc-header">
        <h2>Root cause</h2>
        <span className="confidence" title="Agent confidence">
          {Math.round(confidence * 100)}% confidence
        </span>
      </div>

      <dl className="rc-grid">
        <dt>What</dt>
        <dd>{rootCause.what}</dd>
        <dt>Where</dt>
        <dd>
          <code>
            {rootCause.where.file}:{rootCause.where.line}
          </code>
        </dd>
        <dt>Why</dt>
        <dd>{rootCause.why}</dd>
        <dt>Impact</dt>
        <dd>{rootCause.impact}</dd>
      </dl>

      {alternatives.length > 0 && (
        <details className="alternatives">
          <summary>Alternative hypotheses ({alternatives.length})</summary>
          <ul>
            {alternatives.map((a, i) => (
              <li key={i}>{a}</li>
            ))}
          </ul>
        </details>
      )}

      <h3>Proposed patch</h3>
      <p className="rationale">{proposedPatch.rationale}</p>
      <DiffViewer diff={proposedPatch.unifiedDiff} />

      {suggestedTest && (
        <div className="suggested-test">
          <h4>Suggested regression test</h4>
          <p>{suggestedTest}</p>
        </div>
      )}

      {result ? (
        <p className="approved">
          ✓ Patch written to <code>{result.writtenTo}</code> — not merged, not deployed.
        </p>
      ) : (
        <button className="approve-btn" onClick={onApprove} disabled={approving}>
          {approving ? "Writing patch…" : "Approve patch"}
        </button>
      )}
    </section>
  );
}
