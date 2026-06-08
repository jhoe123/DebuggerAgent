// Legend is the "note somewhere" the user asked for: it explains the bug cue and how to
// read each result, so it's obvious where to expect a problem and what a click does.
export function Legend() {
  return (
    <div className="legend">
      <span className="bug-cue">⚠ triggers an issue</span>
      <span className="legend-text">
        Cards with an amber outline intentionally create a problem you can find in Dynatrace; the rest are healthy
        traffic. Each click is a real request — the result shows HTTP status, round-trip latency, and a copyable trace id.
      </span>
    </div>
  );
}
