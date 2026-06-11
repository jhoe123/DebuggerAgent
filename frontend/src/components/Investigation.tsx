import { useState } from "react";
import type { Investigation } from "../types";
import { ask, confirmFix, stagePatch, unstagePatch } from "../api";
import { downloadMarkdown, toMarkdown } from "../report";
import { useAppData } from "../context/AppDataContext";
import { useLocalStore } from "../context/LocalStoreContext";
import { useToast } from "../context/ToastContext";
import { DiffViewer } from "./DiffViewer";

export function InvestigationPanel({
  data,
  onApproved,
  onReinvestigate,
  reinvestigating = false,
  autoActive = false,
}: {
  data: Investigation;
  onApproved?: () => void;
  onReinvestigate?: () => void;
  reinvestigating?: boolean;
  autoActive?: boolean;
}) {
  const { proposedPatch, suggestedTest } = data;
  const { staged, refreshPatches, refreshArtifacts, artifactMap, streaming } = useAppData();
  const { saveRun } = useLocalStore();
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const toast = useToast();

  const isStaged = staged.some((s) => s.problemId === data.problemId);
  // Confirm-to-merge: available once the fix is deployed on its own branch.
  const art = artifactMap[data.problemId];
  const confirmed = art?.overall === "confirmed";
  const canConfirm = !!art?.fixBranch && art.overall === "deployed";
  const isRunning = art?.overall === "running";
  const isActionDisabled = busy || reinvestigating || autoActive || isRunning || streaming;

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
    <div className="solution-panel">
      <div style={{ display: "flex", justifyContent: "flex-end", gap: "0.5rem", marginBottom: "1rem" }}>
        <button className="ghost-btn" onClick={onCopy}>
          {copied ? "Copied!" : "Copy solution"}
        </button>
        <button className="ghost-btn" onClick={() => downloadMarkdown(`solution-${data.problemId}.md`, toMarkdown(data))}>
          Download solution .md
        </button>
      </div>

      <details className="collapsible-details proposed-patch-card" open={true}>
        <summary>
          <span>Proposed patch</span>
        </summary>
        <div className="collapsible-details-content">
          <p className="rationale" style={{ marginTop: 0 }}>{proposedPatch.rationale}</p>
          <DiffViewer diff={proposedPatch.unifiedDiff} />
        </div>
      </details>

      {suggestedTest && (
        <details className="collapsible-details suggested-test-card" open={true}>
          <summary>
            <span>Suggested regression test</span>
          </summary>
          <div className="collapsible-details-content">
            <p style={{ margin: 0, lineHeight: 1.5 }}>{suggestedTest}</p>
          </div>
        </details>
      )}

      <div className="investigation-actions">
        {confirmed ? (
          <span className="approved">
            ✓ Fix confirmed — merged into the working branch and the fix branch was cleaned up.
          </span>
        ) : canConfirm ? (
          <div className="confirm-fix">
            <button className="approve-btn" onClick={onConfirm} disabled={isActionDisabled}>
              {busy ? "Merging…" : "Confirm fixed — merge & clean up branch"}
            </button>
            <p className="muted">
              Merges <code>{art?.fixBranch}</code> into the working branch and deletes the fix branch.
            </p>
          </div>
        ) : isStaged ? (
          <span className="approved">
            {isRunning ? "⚙ Deploying patch..." : "✓ Staged — in the deployment batch."}{" "}
            {!isRunning && (
              <button className="link-btn" onClick={onUnstage} disabled={isActionDisabled}>
                Remove from batch
              </button>
            )}
          </span>
        ) : (
          <button className="approve-btn" onClick={onStage} disabled={isActionDisabled}>
            {busy ? "Adding…" : "Add to batch"}
          </button>
        )}
        {onReinvestigate && (
          <button
            className="ghost-btn"
            onClick={onReinvestigate}
            disabled={isActionDisabled}
            title="Re-run the agent to refresh the root cause and proposed fix (re-records the proposal so it can be staged)"
          >
            {reinvestigating ? "Re-investigating…" : "Re-investigate"}
          </button>
        )}
      </div>

      <FollowUp problemId={data.problemId} disabled={isActionDisabled} />
    </div>
  );
}

// Natural-language follow-up Q&A about the incident (agent may run DQL).
function FollowUp({ problemId, disabled }: { problemId: string; disabled: boolean }) {
  const [q, setQ] = useState("");
  const [thread, setThread] = useState<{ q: string; a: string }[]>([]);
  const [asking, setAsking] = useState(false);
  const toast = useToast();

  async function onAsk() {
    const question = q.trim();
    if (!question || asking || disabled) return;
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
          disabled={asking || disabled}
        />
        <button onClick={onAsk} disabled={asking || !q.trim() || disabled}>
          {asking ? "Thinking…" : "Ask"}
        </button>
      </div>
    </div>
  );
}
