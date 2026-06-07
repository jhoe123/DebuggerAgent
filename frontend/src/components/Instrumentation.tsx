import { useMemo, useState } from "react";
import type {
  InstrumentationCandidate,
  InstrumentationKind,
  InstrumentationScan,
  PipelineResult,
  Step,
} from "../types";
import { applyInstrumentation, scanInstrumentation } from "../api";
import { AgentSteps } from "./AgentSteps";
import { DiffViewer } from "./DiffViewer";

const KIND_LABEL: Record<InstrumentationKind, string> = {
  "otel-bootstrap": "bootstrap",
  "tracer-init": "tracer",
  span: "span",
  "record-error": "error",
  attributes: "attrs",
  metric: "metric",
};

// Auto-Instrument panel: scan the service for Dynatrace/OpenTelemetry gaps, pick
// some/all candidates, then apply (local-only) — apply→test→debug→build→deploy→verify
// with rollback. Scanning is read-only and works hosted; apply is gated on `available`.
export function InstrumentationPanel({
  available,
  onComplete,
}: {
  available: boolean;
  onComplete?: () => void;
}) {
  const [scan, setScan] = useState<InstrumentationScan | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [scanning, setScanning] = useState(false);
  const [applying, setApplying] = useState(false);
  const [result, setResult] = useState<PipelineResult | null>(null);

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(new Set());
  const [kindFilter, setKindFilter] = useState<"all" | InstrumentationKind>("all");
  const [text, setText] = useState("");

  const candidates = scan?.candidates ?? [];
  const busy = scanning || applying;

  const filtered = useMemo(() => {
    const q = text.trim().toLowerCase();
    return candidates.filter((c) => {
      if (kindFilter !== "all" && c.kind !== kindFilter) return false;
      if (!q) return true;
      return (
        c.file.toLowerCase().includes(q) ||
        (c.symbol ?? "").toLowerCase().includes(q) ||
        c.rationale.toLowerCase().includes(q)
      );
    });
  }, [candidates, kindFilter, text]);

  // Group the filtered candidates by file for the collapsible long-list view.
  const groups = useMemo(() => {
    const m = new Map<string, InstrumentationCandidate[]>();
    for (const c of filtered) {
      const arr = m.get(c.file) ?? [];
      arr.push(c);
      m.set(c.file, arr);
    }
    return [...m.entries()];
  }, [filtered]);

  const kinds = useMemo(
    () => Array.from(new Set(candidates.map((c) => c.kind))),
    [candidates],
  );

  async function onScan() {
    setScanning(true);
    setSteps([]);
    setResult(null);
    setScan(null);
    setSelected(new Set());
    try {
      const s = await scanInstrumentation((st) => setSteps((prev) => [...prev, st]));
      setScan(s);
      setSelected(new Set(s.candidates.map((c) => c.id))); // default: select all
    } finally {
      setScanning(false);
    }
  }

  async function onApply(ids: string[]) {
    if (!available || ids.length === 0) return;
    setApplying(true);
    setSteps([]);
    setResult(null);
    try {
      const r = await applyInstrumentation(
        ids,
        { apply: true, test: true, build: true, deploy: true },
        (st) => setSteps((prev) => [...prev, st]),
      );
      setResult(r);
    } catch (e) {
      setSteps((prev) => [...prev, { stage: "error", status: "fail", message: String(e) }]);
    } finally {
      setApplying(false);
      onComplete?.();
    }
  }

  function toggle(id: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  function setFileSelection(items: InstrumentationCandidate[], on: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const c of items) {
        if (on) next.add(c.id);
        else next.delete(c.id);
      }
      return next;
    });
  }
  function toggleExpand(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  function toggleFile(file: string) {
    setCollapsedFiles((prev) => {
      const next = new Set(prev);
      if (next.has(file)) next.delete(file);
      else next.add(file);
      return next;
    });
  }

  return (
    <section className="instrument">
      <div className="instrument-header">
        <h3>Auto-Instrument with Dynatrace</h3>
        <span className="mini-chip">OpenTelemetry</span>
      </div>
      <p className="muted">
        Scan the service for telemetry gaps — handlers without spans, missing error recording or
        attributes — then selectively apply. Apply runs test → debug → build → deploy → verify
        locally and rolls back if it can't go green.
      </p>

      <button className="investigate-btn" onClick={onScan} disabled={busy}>
        {scanning ? "Scanning…" : scan ? "Re-scan" : "Scan for Dynatrace instrumentation"}
      </button>

      <AgentSteps
        steps={steps}
        title={applying ? "Apply pipeline" : scanning ? "Scanning" : undefined}
      />

      {scan && candidates.length === 0 && (
        <p className="muted">No instrumentation gaps found — the service looks well covered. 🎉</p>
      )}

      {candidates.length > 0 && (
        <>
          <div className="candidate-toolbar">
            <span className="mini-chip">
              {selected.size} of {candidates.length} selected
            </span>
            <button
              className="ghost-btn"
              onClick={() => setSelected(new Set(candidates.map((c) => c.id)))}
              disabled={busy}
            >
              Select all
            </button>
            <button className="ghost-btn" onClick={() => setSelected(new Set())} disabled={busy}>
              Deselect all
            </button>
            <select
              className="kind-select"
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value as "all" | InstrumentationKind)}
              disabled={busy}
            >
              <option value="all">all kinds</option>
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
            <input
              className="candidate-search"
              placeholder="filter file / symbol / reason…"
              value={text}
              onChange={(e) => setText(e.target.value)}
              disabled={busy}
            />
          </div>

          {scan?.truncated && (
            <p className="muted truncated-note">
              Results were capped — apply in batches, then re-scan for the rest.
            </p>
          )}

          <div className="candidate-list">
            {groups.map(([file, items]) => {
              const collapsed = collapsedFiles.has(file);
              const sel = items.filter((c) => selected.has(c.id)).length;
              return (
                <div key={file} className="candidate-group">
                  <div className="candidate-group-header" onClick={() => toggleFile(file)}>
                    <span className="cg-toggle">{collapsed ? "▸" : "▾"}</span>
                    <code>{file}</code>
                    <span className="mini-chip">
                      {sel}/{items.length}
                    </span>
                    <button
                      className="ghost-btn"
                      onClick={(e) => {
                        e.stopPropagation();
                        setFileSelection(items, sel !== items.length);
                      }}
                      disabled={busy}
                    >
                      {sel === items.length ? "none" : "all"}
                    </button>
                  </div>

                  {!collapsed &&
                    items.map((c) => (
                      <div key={c.id} className="candidate-item">
                        <label className="candidate-row">
                          <input
                            type="checkbox"
                            checked={selected.has(c.id)}
                            onChange={() => toggle(c.id)}
                            disabled={busy}
                          />
                          <span className={`kind-badge kind-${c.kind}`}>{KIND_LABEL[c.kind]}</span>
                          <span className="candidate-loc">
                            {c.symbol || c.file}
                            {c.startLine ? `:${c.startLine}` : ""}
                          </span>
                          <span className="candidate-rationale">{c.rationale}</span>
                        </label>
                        {c.unifiedDiff && (
                          <div className="candidate-diff">
                            <button className="ghost-btn" onClick={() => toggleExpand(c.id)}>
                              {expanded.has(c.id) ? "hide diff" : "show diff"}
                            </button>
                            {expanded.has(c.id) && <DiffViewer diff={c.unifiedDiff} />}
                          </div>
                        )}
                      </div>
                    ))}
                </div>
              );
            })}
          </div>

          <div className="candidate-actions">
            {!available && (
              <span className="muted">
                Apply requires local Test Console mode (ENABLE_TEST_CONSOLE).
              </span>
            )}
            <button
              className="run-btn"
              onClick={() => onApply([...selected])}
              disabled={!available || busy || selected.size === 0}
            >
              {applying ? "Applying…" : `Apply selected (${selected.size})`}
            </button>
            <button
              className="ghost-btn"
              onClick={() => onApply(candidates.map((c) => c.id))}
              disabled={!available || busy || candidates.length === 0}
            >
              Apply all
            </button>
          </div>

          {result && (
            <p className={result.success ? "approved" : "failed"}>
              {result.success
                ? "✓ Instrumentation applied — demo_app rebuilt and healthy"
                : "✗ Apply failed — source rolled back to last committed state"}
              {result.verify && <span className="muted"> · verify: {result.verify}</span>}
            </p>
          )}
        </>
      )}
    </section>
  );
}
