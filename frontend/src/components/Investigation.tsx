import { useState } from "react";
import type { Investigation } from "../types";
import { ask, confirmFix, stagePatch, unstagePatch } from "../api";
import { downloadMarkdown, toMarkdown } from "../report";
import { useAppData } from "../context/AppDataContext";
import { useLocalStore } from "../context/LocalStoreContext";
import { useToast } from "../context/ToastContext";
import { DiffViewer } from "./DiffViewer";

export function InvestigationPanel({ data, onApproved }: { data: Investigation; onApproved?: () => void }) {
  const { rootCause, confidence, alternatives, proposedPatch, suggestedTest } = data;
  const { staged, refreshPatches, refreshArtifacts, artifactMap } = useAppData();
  const { saveRun } = useLocalStore();
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const toast = useToast();

  const isStaged = staged.some((s) => s.problemId === data.problemId);
  // Confirm-to-merge: available once the fix is deployed on its own branch.
  const art = artifactMap[data.problemId];
  const confirmed = art?.overall === "confirmed";
  const canConfirm = !!art?.fixBranch && art.overall === "deployed";

  async function onConfirm() {
    setBusy(true);
    try {
      const res = await confirmFix(data.problemId);
      await refreshArtifacts();
      saveRun({ problemId: data.problemId, type: "confirm", status: res.merged ? "ok" : "failed" });
      toast.success(
        res.merged ? `Merged ${res.mergedBranch} → ${res.intoBranch}` : "Confirmation recorded",
      );
      onApproved?.();
    } catch (e) {
      toast.error(`Confirm failed: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  }

  async function onStage() {
    setBusy(true);
    try {
      await stagePatch(data.problemId);
      await refreshPatches();
      await refreshArtifacts();
      toast.success("Patch added to the deployment batch");
      onApproved?.();
    } catch (e) {
      toast.error(`Couldn't stage patch: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  }

  async function onUnstage() {
    setBusy(true);
    try {
      await unstagePatch(data.problemId);
      await refreshPatches();
      await refreshArtifacts();
    } catch (e) {
      toast.error(`Couldn't remove patch: ${String(e)}`);
    } finally {
      setBusy(false);
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

      {confirmed ? (
        <p className="approved">✓ Fix confirmed — merged into the working branch and the fix branch was cleaned up.</p>
      ) : canConfirm ? (
        <div className="confirm-fix">
          <button className="approve-btn" onClick={onConfirm} disabled={busy}>
            {busy ? "Merging…" : "Confirm fixed — merge & clean up branch"}
          </button>
          <p className="muted">
            Merges <code>{art?.fixBranch}</code> into the working branch and deletes the fix branch.
          </p>
        </div>
      ) : isStaged ? (
        <p className="approved">
          ✓ Staged — in the deployment batch.{" "}
          <button className="link-btn" onClick={onUnstage} disabled={busy}>
            Remove from batch
          </button>
        </p>
      ) : (
        <button className="approve-btn" onClick={onStage} disabled={busy}>
          {busy ? "Adding…" : "Add to batch"}
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
  const toast = useToast();

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
      toast.error("Follow-up failed — is the backend reachable?");
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
