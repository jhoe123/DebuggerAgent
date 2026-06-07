import type { Problem } from "../types";

function sinceLabel(iso: string): string {
  const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
  if (mins < 60) return `${mins}m ago`;
  return `${Math.round(mins / 60)}h ago`;
}

export function ProblemList({
  problems,
  selectedId,
  onSelect,
}: {
  problems: Problem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <aside className="problems">
      <h2>Dynatrace problems</h2>
      {problems.map((p) => (
        <button
          key={p.id}
          className={`problem-card${p.id === selectedId ? " selected" : ""}`}
          onClick={() => onSelect(p.id)}
        >
          <div className="problem-top">
            <span className={`badge sev-${p.severity.toLowerCase()}`}>{p.severity}</span>
            <span className="problem-since">{sinceLabel(p.startedAt)}</span>
          </div>
          <div className="problem-title">{p.title}</div>
          <div className="problem-meta">
            {p.entity} · {p.affectedUsers.toLocaleString()} users affected
          </div>
        </button>
      ))}
    </aside>
  );
}
