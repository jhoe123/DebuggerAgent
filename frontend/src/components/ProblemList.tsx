import type { ArtifactOverall, AutopilotPhase, Problem, ProblemArtifact } from "../types";
import { useAutopilot, isActivePhase } from "../context/AutopilotContext";

// Per-card status chip derived from the durable server artifact's overall state.
const OVERALL_CHIP: Record<ArtifactOverall, { label: string; cls: string } | null> = {
  investigated: { label: "investigated ✓", cls: "status-investigated" },
  staged: { label: "staged ◷", cls: "status-staged" },
  running: { label: "running ⟳", cls: "status-running" },
  deployed: { label: "deployed ✓", cls: "status-patched" },
  failed: { label: "failed ✗", cls: "status-failed" },
  confirmed: { label: "merged ✓", cls: "status-confirmed" },
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

// Renders the problem cards only — the parent (ProblemsPage) owns the <aside>,
// header, and filter toolbar so it can switch between the active and hidden lists.
//
// A card is deliberately minimal: it surfaces only the issue (title), its type
// (severity + perf), current status (lifecycle / autopilot), and how long ago it
// started. Everything else (root cause, metrics, batch staging, links) lives in the
// detail panel once the card is selected. Controls follow status:
//   • dismiss ✕ / restore ↩ — shown per list (active vs hidden)
//   • Halt — shown only while autopilot is actively working the problem
export function ProblemList({
  problems,
  selectedId,
  onSelect,
  artifactMap,
  showHidden,
  selectMode,
  selected,
  onToggleSelect,
  onDismiss,
  onRestore,
  onCancel,
  halting,
  streaming,
}: {
  problems: Problem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  artifactMap: Record<string, ProblemArtifact>;
  showHidden: boolean;
  selectMode: boolean; // true => the checkbox is a dismiss multi-select
  selected: Set<string>;
  onToggleSelect: (id: string) => void;
  onDismiss: (id: string) => void;
  onRestore: (id: string) => void;
  onCancel?: (id: string) => void;
  halting: Set<string>;
  streaming: boolean;
}) {
  const { runs, cancel } = useAutopilot();
  const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();
  return (
    <div className={`problem-cards${selectMode ? " select-mode" : ""}`}>
      {problems.map((p) => {
        const run = runs[p.id];
        const art = artifactMap[p.id];
        const chip = art ? OVERALL_CHIP[art.overall] : null;

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
              {selectMode && !showHidden && (
                <input
                  type="checkbox"
                  className="problem-check"
                  checked={selected.has(p.id)}
                  onClick={stop}
                  onChange={() => onToggleSelect(p.id)}
                  aria-label={`Select ${p.title}`}
                  title="Select to dismiss"
                />
              )}
              {/* Type of issue */}
              <span className={`badge sev-${p.severity.toLowerCase()}`}>{p.severity}</span>
              {p.kind === "performance" && (
                <span className="mini-chip perf" title="Performance problem">⏱ perf</span>
              )}
              {/* Time */}
              <span className="problem-since">{sinceLabel(p.startedAt)}</span>
              <span className="problem-actions">
                {showHidden ? (
                  <button
                    className="problem-restore"
                    onClick={(e) => {
                      stop(e);
                      onRestore(p.id);
                    }}
                    title="Restore to the active list"
                  >
                    ↩ Restore
                  </button>
                ) : (
                  <button
                    className="problem-dismiss"
                    disabled={!!(run && isActivePhase(run.phase))}
                    onClick={(e) => {
                      stop(e);
                      onDismiss(p.id);
                    }}
                    title={
                      run && isActivePhase(run.phase)
                        ? "Autopilot is working on this problem — halt it first to dismiss"
                        : "Dismiss (hide) this problem"
                    }
                    aria-label={`Dismiss ${p.title}`}
                  >
                    ✕
                  </button>
                )}
              </span>
            </div>

            {/* What the issue is */}
            <div className="problem-title">{p.title}</div>

            {/* Status — durable lifecycle chip and/or live autopilot phase. */}
            {(chip || run) && (
              <div className="problem-tags" style={{ display: "flex", alignItems: "center", gap: "0.4rem", marginTop: "0.5rem", flexWrap: "wrap" }}>
                {chip && (
                  <span className={`mini-chip ${chip.cls}`} title={`Lifecycle: ${art!.overall}`}>
                    {chip.label}
                  </span>
                )}
                {run && (
                  <span className={`autopilot-chip ap-${run.phase}`} title={run.message} style={{ margin: 0 }}>
                    <span className={isActivePhase(run.phase) ? "ap-spin" : ""}>{PHASE[run.phase].icon}</span>{" "}
                    {PHASE[run.phase].label}
                  </span>
                )}
                {run && isActivePhase(run.phase) && (
                  <button
                    className="halt-btn"
                    title={
                      halting.has(p.id)
                        ? "Halting…"
                        : streaming
                          ? "Disabled while a live run is streaming"
                          : "Halt automation and take over manually"
                    }
                    disabled={halting.has(p.id) || streaming}
                    onClick={(e) => {
                      stop(e);
                      if (onCancel) onCancel(p.id);
                      else cancel(p.id);
                    }}
                    style={{ padding: "0.1rem 0.4rem", fontSize: "0.7rem", display: "inline-flex", alignItems: "center", height: "18px" }}
                  >
                    {halting.has(p.id) ? "Halting..." : "Halt"}
                  </button>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
