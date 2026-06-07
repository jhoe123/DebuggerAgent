import type { Problem } from "../types";

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
      {problems.map((p) => {
        const occ = p.occurrences ?? p.affectedUsers;
        const scanned = kb(p.grailScannedBytes);
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
              <span className={`badge sev-${p.severity.toLowerCase()}`}>{p.severity}</span>
              <span className="problem-since">{sinceLabel(p.startedAt)}</span>
            </div>
            <div className="problem-title">{p.title}</div>
            <div className="problem-meta">
              {p.entity} · {occ.toLocaleString()} occurrences
            </div>
            <div className="problem-chips">
              {p.kind === "performance" && (
                <span className="mini-chip perf" title="Performance problem">⏱ perf</span>
              )}
              {p.metric && <span className="mini-chip" title="Latency percentile">{p.metric}</span>}
              {scanned && <span className="mini-chip" title="Dynatrace Grail bytes scanned">Grail: {scanned}</span>}
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
    </aside>
  );
}
