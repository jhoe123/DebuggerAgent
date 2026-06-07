import { useState } from "react";
import type { ApproveResult, Investigation } from "../types";
import { approvePatch, ask } from "../api";
import { downloadMarkdown, toMarkdown } from "../report";
import { DiffViewer } from "./DiffViewer";

export function InvestigationPanel({ data, onApproved }: { data: Investigation; onApproved?: () => void }) {
  const { rootCause, confidence, alternatives, proposedPatch, suggestedTest } = data;
  const [approving, setApproving] = useState(false);
  const [result, setResult] = useState<ApproveResult | null>(null);
  const [copied, setCopied] = useState(false);

  async function onApprove() {
    setApproving(true);
    try {
      setResult(await approvePatch(data.problemId));
      onApproved?.();
    } finally {
      setApproving(false);
    }
  }

  async function onCopy() {
    await navigator.clipboard.writeText(toMarkdown(data));
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <section className="investigation">
      <div className="rc-header">
        <h2>Root cause</h2>
        <div className="rc-actions">
          <span className="confidence" title="Agent confidence">
            {Math.round(confidence * 100)}% confidence
          </span>
          <button className="ghost-btn" onClick={onCopy}>
            {copied ? "Copied!" : "Copy report"}
          </button>
          <button className="ghost-btn" onClick={() => downloadMarkdown(`incident-${data.problemId}.md`, toMarkdown(data))}>
            Download .md
          </button>
        </div>
      </div>

      {rootCause.summary && <p className="rc-summary">{rootCause.summary}</p>}

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

      {rootCause.details && (
        <details className="alternatives further-reading">
          <summary>Further reading — technical detail</summary>
          <p>{rootCause.details}</p>
        </details>
      )}

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

      <FollowUp problemId={data.problemId} />
    </section>
  );
}

// Natural-language follow-up Q&A about the incident (agent may run DQL).
function FollowUp({ problemId }: { problemId: string }) {
  const [q, setQ] = useState("");
  const [thread, setThread] = useState<{ q: string; a: string }[]>([]);
  const [asking, setAsking] = useState(false);

  async function onAsk() {
    const question = q.trim();
    if (!question || asking) return;
    setAsking(true);
    setQ("");
    try {
      const { answer } = await ask(problemId, question);
      setThread((t) => [...t, { q: question, a: answer }]);
    } catch (e) {
      setThread((t) => [...t, { q: question, a: `(error: ${String(e)})` }]);
    } finally {
      setAsking(false);
    }
  }

  return (
    <div className="followup">
      <h4>Ask a follow-up</h4>
      {thread.map((t, i) => (
        <div key={i} className="qa">
          <p className="qa-q">{t.q}</p>
          <p className="qa-a">{t.a}</p>
        </div>
      ))}
      <div className="qa-input">
        <input
          value={q}
          placeholder="e.g. how many times did this happen in the last day?"
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && onAsk()}
          disabled={asking}
        />
        <button onClick={onAsk} disabled={asking || !q.trim()}>
          {asking ? "Thinking…" : "Ask"}
        </button>
      </div>
    </div>
  );
}
