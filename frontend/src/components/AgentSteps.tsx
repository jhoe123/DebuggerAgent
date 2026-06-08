import type { Step } from "../types";

const icon = (s: Step["status"]) =>
  s === "ok" ? "✓" : s === "fail" ? "✗" : s === "running" ? "⋯" : "•";

// Render `backtick`-wrapped segments (file paths, line ranges, queries, DQL) as inline
// monospace. Messages without backticks (pipeline stages, older saved runs) render plain.
function renderMsg(msg: string) {
  return msg
    .split("`")
    .map((part, i) => (i % 2 === 1 ? <code key={i} className="step-ref">{part}</code> : <span key={i}>{part}</span>));
}

// Live timeline of agent/pipeline milestones (SSE).
export function AgentSteps({ steps, title }: { steps: Step[]; title?: string }) {
  if (steps.length === 0) return null;
  return (
    <div className="steps">
      {title && <h4 className="steps-title">{title}</h4>}
      {steps.map((s, i) => (
        <div key={i} className={`step step-${s.status}`}>
          <span className="step-icon">{icon(s.status)}</span>
          <span className="step-msg">{renderMsg(s.message)}</span>
          {s.detail && <pre className="step-detail">{s.detail}</pre>}
        </div>
      ))}
    </div>
  );
}
