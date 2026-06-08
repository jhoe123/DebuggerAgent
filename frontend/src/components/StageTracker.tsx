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
export function StageTracker({ artifact }: { artifact: ProblemArtifact }) {
  return (
    <section className={`stage-tracker overall-${artifact.overall}`}>
      <div className="stage-tracker-head">
        <span className="stage-overall">{OVERALL_LABEL[artifact.overall] ?? artifact.overall}</span>
        {artifact.verify && <span className="muted">verify: {artifact.verify}</span>}
        {artifact.fixBranch && artifact.overall !== "confirmed" && (
          <span className="muted">branch: {artifact.fixBranch}</span>
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
