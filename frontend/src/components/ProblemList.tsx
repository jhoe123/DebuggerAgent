import type { ArtifactOverall, AutopilotPhase, Problem, ProblemArtifact } from "../types";
import { useAutopilot, isActivePhase } from "../context/AutopilotContext";

// Per-card status chip derived from the durable server artifact's overall state.
const OVERALL_CHIP: Record<ArtifactOverall, { label: string; cls: string } | null> = {
  investigated: { label: "investigated ✓", cls: "status-investigated" },
  staged: { label: "staged ◷", cls: "status-staged" },
  running: { label: "running ⟳", cls: "status-running" },
  deployed: { label: "deployed ✓", cls: "status-patched" },
  failed: { label: "failed ✗", cls: "status-failed" },
};

const PHASE: Record<AutopilotPhase, { label: string; icon: string }> = {
  queued: { label: "Queued", icon: "•" },
  investigating: { label: "Investigating", icon: "⟳" },
  proposed: { label: "Patch proposed", icon: "✎" },
  remediating: { label: "Auto-patching", icon: "⟳" },
  deployed: { label: "Deployed", icon: "✓" },
  failed: { label: "Failed", icon: "✗" },
  halted: { label: "Halted", icon: "⛔" },
};

function sinceLabel(iso: string): string {
  const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (mins < 60) return `${mins}m ago`;
  if (mins < 60 * 24) return `${Math.round(mins / 60)}h ago`;
  return `${Math.round(mins / 1440)}d ago`;
}

function kb(bytes?: number): string | null {
  if (!bytes) return null;
  return bytes > 1024 * 1024 ? `${(bytes / 1024 / 1024).toFixed(1)} MB` : `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

// Renders the problem cards only — the parent (ProblemsPage) owns the <aside>,
// header, and filter toolbar so it can switch between the active and hidden lists.
export function ProblemList({
  problems,
  selectedId,
  onSelect,
  artifactMap,
  showHidden,
  selectMode,
  stagedIds,
  canStage,
  onToggleBatch,
  selected,
  onToggleSelect,
  onDismiss,
  onRestore,
}: {
  problems: Problem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  artifactMap: Record<string, ProblemArtifact>;
  showHidden: boolean;
  selectMode: boolean; // true => checkbox is dismiss multi-select; false => batch membership
  stagedIds: Set<string>;
  canStage: (id: string) => boolean;
  onToggleBatch: (id: string) => void;
  selected: Set<string>;
  onToggleSelect: (id: string) => void;
  onDismiss: (id: string) => void;
  onRestore: (id: string) => void;
}) {
  const { runs, cancel } = useAutopilot();
  return (
    <div className={`problem-cards${selectMode ? " select-mode" : ""}`}>
      {problems.map((p) => {
        const occ = p.occurrences ?? p.affectedUsers;
        const scanned = kb(p.grailScannedBytes);
        const run = runs[p.id];
        const art = artifactMap[p.id];
        const chip = art ? OVERALL_CHIP[art.overall] : null;
        const inBatch = stagedIds.has(p.id);
        const batchDisabled = !inBatch && !canStage(p.id); // can't stage an un-investigated problem
        return (
          <div
            key={p.id}
            role="button"
            tabIndex={0}
            className={`problem-card${p.id === selectedId ? " selected" : ""}`}
            onClick={() => onSelect(p.id)}
            onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onSelect(p.id)}
          >
            <div className="problem-top">
              {!showHidden && (
                <input
                  type="checkbox"
                  className="problem-check"
                  checked={selectMode ? selected.has(p.id) : inBatch}
                  disabled={selectMode ? false : batchDisabled}
                  onClick={(e) => e.stopPropagation()}
                  onChange={() => (selectMode ? onToggleSelect(p.id) : onToggleBatch(p.id))}
                  aria-label={selectMode ? `Select ${p.title}` : `Add ${p.title} to deployment batch`}
                  title={
                    selectMode
                      ? "Select to dismiss"
                      : batchDisabled
                        ? "Investigate first to add to batch"
                        : inBatch
                          ? "In deployment batch — click to remove"
                          : "Add to deployment batch"
                  }
                />
              )}
              <span className={`badge sev-${p.severity.toLowerCase()}`}>{p.severity}</span>
              <span className="problem-since">{sinceLabel(p.startedAt)}</span>
              <span className="problem-actions">
                {showHidden ? (
                  <button
                    className="problem-restore"
                    onClick={(e) => {
                      e.stopPropagation();
                      onRestore(p.id);
                    }}
                    title="Restore to the active list"
                  >
                    ↩ Restore
                  </button>
                ) : (
                  <button
                    className="problem-dismiss"
                    onClick={(e) => {
                      e.stopPropagation();
                      onDismiss(p.id);
                    }}
                    title="Dismiss (hide) this problem"
                    aria-label={`Dismiss ${p.title}`}
                  >
                    ✕
                  </button>
                )}
              </span>
            </div>
            <div className="problem-title">{p.title}</div>
            <div className="problem-meta">
              {p.entity} · {occ.toLocaleString()} occurrences
            </div>
            {run && (
              <div className="autopilot-row" title={run.message}>
                <span className={`autopilot-chip ap-${run.phase}`}>
                  <span className={isActivePhase(run.phase) ? "ap-spin" : ""}>{PHASE[run.phase].icon}</span>{" "}
                  {PHASE[run.phase].label}
                </span>
                {isActivePhase(run.phase) && (
                  <button
                    className="halt-btn"
                    title="Halt automation and take over manually"
                    onClick={(e) => {
                      e.stopPropagation();
                      cancel(p.id);
                    }}
                  >
                    Halt
                  </button>
                )}
              </div>
            )}
            <div className="problem-chips">
              {p.kind === "performance" && (
                <span className="mini-chip perf" title="Performance problem">⏱ perf</span>
              )}
              {p.metric && <span className="mini-chip" title="Latency percentile">{p.metric}</span>}
              {scanned && <span className="mini-chip" title="Dynatrace Grail bytes scanned">Grail: {scanned}</span>}
              {/* Durable lifecycle status from the server artifact (one chip, kept clean). */}
              {chip && (
                <span className={`mini-chip ${chip.cls}`} title={`Lifecycle: ${art!.overall}`}>
                  {chip.label}
                </span>
              )}
              {p.dynatraceUrl && (
                <a
                  className="mini-chip link"
                  href={p.dynatraceUrl}
                  target="_blank"
                  rel="noreferrer"
                  onClick={(e) => e.stopPropagation()}
                >
                  Open in Dynatrace ↗
                </a>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
