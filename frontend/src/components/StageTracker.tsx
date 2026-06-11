import type { ArtifactStageKey, ProblemArtifact, Step } from "../types";
import { AgentSteps } from "./AgentSteps";

// Lifecycle order shown in the tracker.
const STAGES: { key: ArtifactStageKey; label: string }[] = [
  { key: "investigation", label: "Investigated" },
  { key: "patch", label: "Staged" },
  { key: "test", label: "Test" },
  { key: "build", label: "Build" },
  { key: "deploy", label: "Deploy" },
  { key: "verify", label: "Verify" },
];

const ICON: Record<string, string> = { ok: "✓", failed: "✗", running: "⟳", pending: "•" };

const OVERALL_LABEL: Record<string, string> = {
  investigated: "Investigated",
  staged: "Staged for deploy",
  running: "Running pipeline",
  deployed: "Deployed",
  failed: "Failed",
  confirmed: "Merged & confirmed",
};

// StageTracker renders a problem's durable lifecycle status (server artifact) as a
// row of stage chips, so the UI reflects how far a fix has progressed and survives
// refreshes/restarts. It also embeds the live/cached autopilot or agent activity log.
export function StageTracker({
  artifact,
  onMerge,
  merging = false,
  showMergeButton = false,
  demoAppUrl,
  demoAppName,
  steps = [],
  run,
  autoActive = false,
  investigating = false,
  logOpen = true,
  onToggleLog,
  haltingActive = false,
  onCancel,
  streaming = false,
}: {
  artifact?: ProblemArtifact;
  onMerge?: () => void;
  merging?: boolean;
  showMergeButton?: boolean;
  demoAppUrl?: string;
  demoAppName?: string;
  steps?: Step[];
  run?: any;
  autoActive?: boolean;
  investigating?: boolean;
  logOpen?: boolean;
  onToggleLog?: (open: boolean) => void;
  haltingActive?: boolean;
  onCancel?: () => void;
  streaming?: boolean;
}) {
  const overall = artifact?.overall ?? (autoActive ? "running" : investigating ? "running" : "investigated");
  const deployed = overall === "deployed" || overall === "confirmed";

  // Use the autopilot log as content when on autopilot, otherwise use agent activity for manual
  const isAutopilot = !!run;
  const activity = isAutopilot ? (run?.steps ?? []) : steps;

  return (
    <details
      className={`stage-tracker stage-tracker-collapse overall-${overall}`}
      open={logOpen || investigating || autoActive}
      onToggle={(e) => onToggleLog?.(e.currentTarget.open)}
    >
      <summary style={{ cursor: "pointer", listStyle: "none" }} className="stage-tracker-summary">
        <div className="stage-tracker-head">
          <span className="stage-overall">
            {OVERALL_LABEL[overall] ?? (overall === "running" ? "Running pipeline" : "Not started")}
          </span>
          {artifact?.fixBranch && overall !== "confirmed" && (
            <span className="muted stage-branch">branch: {artifact.fixBranch}</span>
          )}
          {deployed && demoAppUrl && (
            <a
              className="stage-open-link"
              href={demoAppUrl}
              target="_blank"
              rel="noreferrer"
              title="Open the running demo app to see the fix live"
              onClick={(e) => e.stopPropagation()}
            >
              Open {demoAppName ?? "the patched app"} ↗
            </a>
          )}
          {showMergeButton && overall === "deployed" && (
            <button
              className="approve-btn merge-action-btn"
              onClick={(e) => {
                e.stopPropagation();
                onMerge?.();
              }}
              disabled={merging}
            >
              {merging ? "Merging…" : "Merge to working branch"}
            </button>
          )}
        </div>

        <div className="stage-chips">
          {STAGES.map(({ key, label }) => {
            const st = artifact?.stages?.[key];
            const status = st?.status ?? "pending";
            return (
              <span key={key} className={`stage-chip stage-${status}`} title={st?.detail || label}>
                <span className="stage-ico">{ICON[status] ?? "•"}</span> {label}
              </span>
            );
          })}
        </div>
      </summary>

      {(activity.length > 0 || run) && (
        <div className="collapsible-details-content" style={{ marginTop: "1rem", borderTop: "1px dashed var(--border)", paddingTop: "1rem" }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "0.75rem" }}>
            <span style={{ fontWeight: 600, fontSize: "0.85rem", color: "var(--accent)" }}>
              {isAutopilot ? "Autopilot Log" : "Agent Activity"} ({activity.length})
            </span>
            {run && (
              <span className={`autopilot-chip ap-${run.phase}`} style={{ marginLeft: "0.5rem" }}>
                Autopilot: {run.phase}
              </span>
            )}
            {run && autoActive && onCancel && (
              <button
                className="halt-btn"
                onClick={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  onCancel();
                }}
                disabled={haltingActive || streaming}
                style={{ padding: "0.2rem 0.5rem", fontSize: "0.75rem" }}
              >
                {haltingActive ? "Halting..." : "Halt & take over"}
              </button>
            )}
          </div>
          {isAutopilot && run?.message && (
            <p className="muted" style={{ marginTop: 0, marginBottom: "1rem", fontSize: "0.85rem", fontStyle: "italic" }}>
              Status: {run.message}
            </p>
          )}
          <AgentSteps steps={activity} />
        </div>
      )}
    </details>
  );
}
