import { useEffect, useState } from "react";
import type { HistoryEntry } from "../types";
import { listHistory } from "../api";
import { DiffViewer } from "./DiffViewer";
import { AgentSteps } from "./AgentSteps";

const STATUS_CLASS: Record<HistoryEntry["status"], string> = {
  proposed: "chip-info",
  written: "chip-ok",
  success: "chip-ok",
  failed: "chip-warn",
};

const KIND_LABEL: Record<HistoryEntry["kind"], string> = {
  proposed: "PROPOSED",
  approved: "APPROVED",
  pipeline: "PIPELINE",
  scan: "SCAN",
  revert: "REVERT",
};

function when(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return new Date(t).toLocaleString();
}

// Patch / change history: every proposed patch, approval, and pipeline run, with
// the files each one touched. Read-only audit view (GET /api/history).
export function History({ reloadKey }: { reloadKey: number }) {
  const [entries, setEntries] = useState<HistoryEntry[]>([]);
  const [loading, setLoading] = useState(false);

  async function refresh() {
    setLoading(true);
    try {
      setEntries(await listHistory());
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    refresh();
  }, [reloadKey]);

  return (
    <section className="history">
      <div className="history-header">
        <p className="muted">
          Every proposed patch, approval, scan, and auto-remediation run — with the files each touched.
        </p>
        <span className="mini-chip">{entries.length}</span>
        <button className="ghost-btn" onClick={refresh} disabled={loading}>
          {loading ? "…" : "Refresh"}
        </button>
      </div>

      <div className="history-body">
          {entries.length === 0 && (
            <p className="muted">No changes yet — investigate a problem to propose a patch.</p>
          )}
          {entries.map((e) => (
            <div key={e.id} className="history-entry">
              <div className="history-top">
                <span className={`chip ${STATUS_CLASS[e.status]}`}>{KIND_LABEL[e.kind]}</span>
                <span className="history-summary">{e.summary}</span>
                <span className="history-when">{when(e.createdAt)}</span>
              </div>
              {e.files.length > 0 && (
                <div className="history-files">
                  {e.files.map((f) => (
                    <code key={f} className="mini-chip">
                      {f}
                    </code>
                  ))}
                  {e.verify && <span className="mini-chip">{e.verify}</span>}
                </div>
              )}
              {(e.diff || (e.steps && e.steps.length > 0)) && (
                <details className="alternatives">
                  <summary>Details</summary>
                  {e.writtenTo && (
                    <p className="muted">
                      Written to <code>{e.writtenTo}</code>
                    </p>
                  )}
                  {e.diff && <DiffViewer diff={e.diff} />}
                  {e.steps && e.steps.length > 0 && <AgentSteps steps={e.steps} />}
                </details>
              )}
            </div>
          ))}
      </div>
    </section>
  );
}
