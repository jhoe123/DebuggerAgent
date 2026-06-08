import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import type { HistoryEntry } from "../types";
import { listHistory } from "../api";
import { useAppData } from "../context/AppDataContext";
import { Skeleton } from "../components/States";

export function Dashboard() {
  const { problems, problemsLoading, consoleAvailable, mock, historyKey } = useAppData();
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const [histLoading, setHistLoading] = useState(true);

  useEffect(() => {
    let active = true;
    setHistLoading(true);
    listHistory()
      .then((h) => active && (setHistory(h), setHistLoading(false)))
      .catch(() => active && setHistLoading(false));
    return () => {
      active = false;
    };
  }, [historyKey]);

  const perf = problems.filter((p) => p.kind === "performance").length;
  const errors = problems.length - perf;

  return (
    <>
      <h2 className="page-title">Overview</h2>
      <p className="page-sub">Your AI SRE at a glance.</p>

      <div className="dash-cards">
        <StatCard label="Open problems" value={problems.length} to="/problems" loading={problemsLoading} />
        <StatCard label="Errors" value={errors} to="/problems" loading={problemsLoading} accent="del" />
        <StatCard label="Performance" value={perf} to="/problems" loading={problemsLoading} accent="warn" />
        <StatCard label="Changes logged" value={history.length} to="/history" loading={histLoading} />
      </div>

      <div className="dash-row">
        <section className="dash-panel">
          <h3>Quick actions</h3>
          <div className="dash-actions">
            <Link className="investigate-btn" to="/problems">
              Investigate a problem
            </Link>
            <Link className="ghost-btn" to="/instrument">
              Scan instrumentation
            </Link>
            <Link className="ghost-btn" to="/settings">
              Settings
            </Link>
          </div>
          <p className="muted dash-status">
            {mock ? "⚠ Showing demo data — backend not connected." : "✓ Backend connected."}{" "}
            {consoleAvailable
              ? "Test Console is ON — local autonomy enabled."
              : "Test Console is off — human-gated."}
          </p>
        </section>

        <section className="dash-panel">
          <h3>Recent activity</h3>
          {histLoading ? (
            <Skeleton count={3} />
          ) : history.length === 0 ? (
            <p className="muted">No changes yet — investigate a problem to propose a fix.</p>
          ) : (
            <ul className="dash-activity">
              {history.slice(0, 5).map((e) => (
                <li key={e.id}>
                  <span className="mini-chip">{e.kind}</span> {e.summary}
                </li>
              ))}
            </ul>
          )}
          <Link className="ghost-btn dash-viewall" to="/history">
            View all →
          </Link>
        </section>
      </div>
    </>
  );
}

function StatCard({
  label,
  value,
  to,
  loading,
  accent,
}: {
  label: string;
  value: number;
  to: string;
  loading?: boolean;
  accent?: "del" | "warn";
}) {
  return (
    <Link className="stat-card" to={to}>
      <span className="stat-label">{label}</span>
      <span className={`stat-value${accent ? ` stat-${accent}` : ""}`}>{loading ? "—" : value}</span>
    </Link>
  );
}
