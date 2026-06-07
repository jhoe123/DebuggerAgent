import type { Step } from "../types";

const icon = (s: Step["status"]) =>
  s === "ok" ? "✓" : s === "fail" ? "✗" : s === "running" ? "⋯" : "•";

// Live timeline of agent/pipeline milestones (SSE).
export function AgentSteps({ steps, title }: { steps: Step[]; title?: string }) {
  if (steps.length === 0) return null;
  return (
    <div className="steps">
      {title && <h4 className="steps-title">{title}</h4>}
      {steps.map((s, i) => (
        <div key={i} className={`step step-${s.status}`}>
          <span className="step-icon">{icon(s.status)}</span>
          <span className="step-msg">{s.message}</span>
          {s.detail && <pre className="step-detail">{s.detail}</pre>}
        </div>
      ))}
    </div>
  );
}
