import type { SignalKind } from "../scenarios";
import { SignalBadge } from "./SignalBadge";
import { TraceIdChip } from "./TraceIdChip";

export interface ConsoleEntry {
  id: number;
  title: string;
  signals: SignalKind[];
  completed: boolean;
  status: number;
  latencyMs: number;
  traceId: string;
  time: string;
  statusClass: string;
}

export function ConsolePanel({ entries, onClear }: { entries: ConsoleEntry[]; onClear: () => void }) {
  return (
    <aside className="console">
      <div className="console-head">
        <h2>Observability console</h2>
        <button className="link" onClick={onClear} disabled={!entries.length}>
          clear
        </button>
      </div>
      <p className="console-hint">
        Every action sends a real request and emits OpenTelemetry to Dynatrace. Copy a trace id to find it there.
      </p>
      {!entries.length && <p className="empty">No activity yet — trigger an action.</p>}
      <ul>
        {entries.map((e) => (
          <li key={e.id} className={e.statusClass}>
            <div className="row1">
              <span className="title">{e.title}</span>
              <span className="time">{e.time}</span>
            </div>
            <div className="row2">
              <span className="status">{e.completed ? `HTTP ${e.status}` : "no response"}</span>
              <span className="latency">{e.latencyMs} ms</span>
              <TraceIdChip traceId={e.traceId} />
            </div>
            <div className="row3">
              {e.signals.map((s) => (
                <SignalBadge key={s} kind={s} />
              ))}
            </div>
          </li>
        ))}
      </ul>
    </aside>
  );
}
