import type { ArtifactStageKey, ProblemArtifact } from "../types";

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

const OVERALL_LABEL: Record<ProblemArtifact["overall"], string> = {
  investigated: "Investigated",
  staged: "Staged for deploy",
  running: "Running pipeline",
  deployed: "Deployed",
  failed: "Failed",
  confirmed: "Merged & confirmed",
};

// StageTracker renders a problem's durable lifecycle status (server artifact) as a
// row of stage chips, so the UI reflects how far a fix has progressed and survives
// refreshes/restarts.
export function StageTracker({
  artifact,
  onMerge,
  merging = false,
  showMergeButton = false,
}: {
  artifact: ProblemArtifact;
  onMerge?: () => void;
  merging?: boolean;
  showMergeButton?: boolean;
}) {
  return (
    <section className={`stage-tracker overall-${artifact.overall}`}>
      <div className="stage-tracker-head" style={{ display: "flex", alignItems: "center", width: "100%" }}>
        <span className="stage-overall">{OVERALL_LABEL[artifact.overall] ?? artifact.overall}</span>
        {artifact.verify && <span className="muted" style={{ marginLeft: "10px" }}>verify: {artifact.verify}</span>}
        {artifact.fixBranch && artifact.overall !== "confirmed" && (
          <span className="muted" style={{ marginLeft: "10px" }}>branch: {artifact.fixBranch}</span>
        )}
        {showMergeButton && artifact.overall === "deployed" && (
          <button
            className="approve-btn merge-action-btn"
            style={{
              marginLeft: "auto",
              padding: "4px 12px",
              fontSize: "0.85rem",
              cursor: "pointer",
              borderRadius: "4px",
              border: "none",
              background: "var(--color-primary, #4f46e5)",
              color: "white",
              fontWeight: "bold"
            }}
            onClick={onMerge}
            disabled={merging}
          >
            {merging ? "Merging..." : "Merge to working branch"}
          </button>
        )}
      </div>

      <div className="stage-chips">
        {STAGES.map(({ key, label }) => {
          const st = artifact.stages[key];
          const status = st?.status ?? "pending";
          return (
            <span key={key} className={`stage-chip stage-${status}`} title={st?.detail || label}>
              <span className="stage-ico">{ICON[status] ?? "•"}</span> {label}
            </span>
          );
        })}
      </div>
    </section>
  );
}
