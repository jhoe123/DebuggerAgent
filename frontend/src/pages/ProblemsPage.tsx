import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { Investigation, Step } from "../types";
import { confirmFix, investigateStream } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot, isActivePhase } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import { useLocalStore } from "../context/LocalStoreContext";
import { ProblemList } from "../components/ProblemList";
import { InvestigationPanel } from "../components/Investigation";
import { BatchPanel } from "../components/BatchPanel";
import { StageTracker } from "../components/StageTracker";
import { Skeleton, EmptyState, ErrorState } from "../components/States";

type SortBy = "recent" | "severity" | "impact";
type KindFilter = "all" | "error" | "performance";

// More-severe severities sort first under "severity".
const SEV_RANK: Record<string, number> = { AVAILABILITY: 0, ERROR: 1, RESOURCE: 2, CUSTOM: 3 };

// Problems master-detail. The selected problem comes from the URL (/problems/:id),
// so investigations are deep-linkable. Investigation runs on demand (not auto) so a
// shared link doesn't spend an agent run unexpectedly. Dismissals and prior results
// are persisted in the browser (LocalStoreContext) so they survive a refresh.
export function ProblemsPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const {
    problems,
    problemsLoading,
    problemsError,
    refreshProblems,
    reloadHistory,
    streaming,
    setStreaming,
    artifactMap,
    refreshPatches,
    refreshArtifacts,
    gitSource,
    demoAppUrl,
    demoAppName,
  } = useAppData();
  const toast = useToast();
  const { runs, cancel } = useAutopilot();
  const {
    isDismissed,
    dismiss,
    restore,
    clearDismissed,
    saveRun,
    latestInvestigation,
  } = useLocalStore();

  const [result, setResult] = useState<Investigation | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [investigating, setInvestigating] = useState(false);
  const [halting, setHalting] = useState<Set<string>>(new Set());
  const [mergeBusy, setMergeBusy] = useState(false);
  const [logOpen, setLogOpen] = useState(true);

  async function handleCancel(problemId: string) {
    setHalting((prev) => {
      const next = new Set(prev);
      next.add(problemId);
      return next;
    });
    try {
      await cancel(problemId);
      await refreshProblems();
      await refreshPatches();
      await refreshArtifacts();
      reloadHistory();
      toast.success("Autopilot run halted");
    } catch (e) {
      toast.error(`Halt failed: ${String(e)}`);
    } finally {
      setHalting((prev) => {
        const next = new Set(prev);
        next.delete(problemId);
        return next;
      });
    }
  }


  // List management state.
  const [query, setQuery] = useState("");
  const [severityFilter, setSeverityFilter] = useState("all");
  const [kindFilter, setKindFilter] = useState<KindFilter>("all");
  const [sortBy, setSortBy] = useState<SortBy>("recent");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [selectMode, setSelectMode] = useState(false); // dismiss multi-select mode
  const [showHidden, setShowHidden] = useState(false);
  const [lastDismissed, setLastDismissed] = useState<string[]>([]);

  const selectedId = id ?? null;
  const run = selectedId ? runs[selectedId] : undefined;
  const autoActive = run ? isActivePhase(run.phase) : false;

  const activeProblems = useMemo(() => problems.filter((p) => !isDismissed(p.id, p.startedAt)), [problems, isDismissed]);
  const hiddenProblems = useMemo(() => problems.filter((p) => isDismissed(p.id, p.startedAt)), [problems, isDismissed]);
  const severities = useMemo(
    () => Array.from(new Set(activeProblems.map((p) => p.severity))),
    [activeProblems],
  );

  // Active list after search + filter + sort.
  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = activeProblems.filter((p) => {
      if (severityFilter !== "all" && p.severity !== severityFilter) return false;
      if (kindFilter !== "all" && (p.kind ?? "error") !== kindFilter) return false;
      if (q && !`${p.title} ${p.entity} ${p.id}`.toLowerCase().includes(q)) return false;
      return true;
    });
    // Array.sort is stable, so returning 0 preserves the incoming order. The list arrives
    // already stabilized (newest-appeared first) from AppDataContext, so "recent" — and any
    // tie under "severity" — keep that order instead of reshuffling on the live startedAt.
    return [...list].sort((a, b) => {
      if (sortBy === "impact") return (b.affectedUsers ?? 0) - (a.affectedUsers ?? 0);
      if (sortBy === "severity") return (SEV_RANK[a.severity] ?? 9) - (SEV_RANK[b.severity] ?? 9);
      return 0;
    });
  }, [activeProblems, query, severityFilter, kindFilter, sortBy]);

  // Restore-on-refresh: re-hydrate a previously cached investigation for the
  // selected problem (or clear the detail view when there's nothing cached).
  useEffect(() => {
    const cached = selectedId ? latestInvestigation(selectedId) : undefined;
    if (cached?.investigation) {
      setResult(cached.investigation);
      setSteps(cached.steps ?? []);
    } else {
      setResult(null);
      setSteps([]);
    }
  }, [selectedId, latestInvestigation]);

  async function onInvestigate() {
    if (!selectedId) return;
    setInvestigating(true);
    setStreaming(true);
    setSteps([]);
    setResult(null);
    const collected: Step[] = [];
    try {
      const inv = await investigateStream(selectedId, (s) => {
        collected.push(s);
        setSteps((prev) => [...prev, s]);
      });
      setResult(inv);
      const p = problems.find((x) => x.id === selectedId);
      saveRun({
        problemId: selectedId,
        title: p?.title,
        kind: p?.kind,
        type: "investigation",
        investigation: inv,
        steps: collected,
        status: "ok",
      });
      reloadHistory();
      toast.success("Investigation complete");
    } catch (e) {
      toast.error(`Investigation failed: ${String(e)}`);
    } finally {
      setInvestigating(false);
      setStreaming(false);
    }
  }

  async function handleMerge() {
    if (!selectedId) return;
    setMergeBusy(true);
    try {
      const res = await confirmFix(selectedId);
      await refreshArtifacts();
      saveRun({ problemId: selectedId, type: "confirm", status: res.merged ? "ok" : "failed" });
      toast.success(
        res.merged ? `Merged ${res.mergedBranch} → ${res.intoBranch}` : "Confirmation recorded",
      );
      reloadHistory();
    } catch (e) {
      toast.error(`Confirm failed: ${String(e)}`);
    } finally {
      setMergeBusy(false);
    }
  }

  function toggleSelect(pid: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(pid)) next.delete(pid);
      else next.add(pid);
      return next;
    });
  }

  function handleDismiss(ids: string[]) {
    if (ids.length === 0) return;
    dismiss(ids);
    setLastDismissed(ids);
    setSelected(new Set());
    toast.success(ids.length === 1 ? "Dismissed 1 problem" : `Dismissed ${ids.length} problems`);
  }

  function handleRestore(ids: string[]) {
    restore(ids);
    setLastDismissed((prev) => prev.filter((x) => !ids.includes(x)));
  }

  function clearFilters() {
    setQuery("");
    setSeverityFilter("all");
    setKindFilter("all");
  }

  const selectedProblem = problems.find((p) => p.id === selectedId);
  const listError = problemsError && problems.length === 0;
  const activity = selectedId ? (steps.length > 0 ? steps : run?.steps ?? []) : [];

  return (
    <>
      <h2 className="page-title">Problems</h2>
      <p className="page-sub">
        Live error &amp; performance problems from Dynatrace — select one to investigate.
      </p>

      {/* Consolidated patches + one-click deploy (collapsed bar; hidden until staged). */}
      <BatchPanel />

      <div className="layout">
        <div className="problems-col">
          <aside className="problems">
            <div className="problems-head">
              <h3 style={{ margin: 0, fontSize: "0.95rem", fontWeight: 600 }}>Incidents</h3>
              {!problemsLoading && !listError && (
                <div className="problems-head-actions">
                  <button
                    className={`ghost-btn hidden-toggle${showHidden ? " active" : ""}`}
                    onClick={() => {
                      setShowHidden((s) => !s);
                      setSelected(new Set());
                    }}
                    disabled={streaming || (!showHidden && hiddenProblems.length === 0)}
                    title="Show dismissed problems"
                  >
                    {showHidden ? "← Active" : `Hidden (${hiddenProblems.length})`}
                  </button>
                </div>
              )}
            </div>

            {!showHidden && lastDismissed.length > 0 && (
              <div className="undo-bar">
                <span>Dismissed {lastDismissed.length}.</span>
                <button className="link-btn" onClick={() => handleRestore(lastDismissed)}>
                  Undo
                </button>
              </div>
            )}

            {problemsLoading ? (
              <Skeleton count={3} />
            ) : listError ? (
              <ErrorState message="Couldn't reach the backend." onRetry={refreshProblems} />
            ) : showHidden ? (
              hiddenProblems.length === 0 ? (
                <EmptyState title="Nothing hidden" message="You haven't dismissed any problems." />
              ) : (
                <>
                  <div className="problems-toolbar">
                    <button
                      className="ghost-btn"
                      onClick={() => {
                        clearDismissed();
                        setLastDismissed([]);
                      }}
                      disabled={streaming}
                    >
                      Restore all ({hiddenProblems.length})
                    </button>
                  </div>
                  <ProblemList
                    problems={hiddenProblems}
                    selectedId={selectedId}
                    onSelect={(pid) => navigate(`/problems/${encodeURIComponent(pid)}`)}
                    artifactMap={artifactMap}
                    showHidden
                    selectMode={selectMode}
                    selected={selected}
                    onToggleSelect={toggleSelect}
                    onDismiss={(pid) => handleDismiss([pid])}
                    onRestore={(pid) => handleRestore([pid])}
                    onCancel={handleCancel}
                    halting={halting}
                    streaming={streaming}
                  />
                </>
              )
            ) : activeProblems.length === 0 ? (
              <EmptyState title="No open problems" message="Nothing to investigate right now." />
            ) : (
              <>
                <div className="problems-toolbar">
                  <div className="problems-search-row">
                    <input
                      className="filter-input"
                      type="search"
                      placeholder="Search problems…"
                      value={query}
                      disabled={streaming}
                      onChange={(e) => setQuery(e.target.value)}
                    />
                    <button
                      className={`ghost-btn select-toggle${selectMode ? " active" : ""}`}
                      onClick={() => {
                        setSelectMode((s) => !s);
                        setSelected(new Set());
                      }}
                      disabled={streaming}
                      title="Select multiple problems to dismiss"
                    >
                      {selectMode ? "Done" : "Select"}
                    </button>
                  </div>
                  <div className="problems-filter-row">
                    <select
                      className="filter-select"
                      value={severityFilter}
                      disabled={streaming}
                      onChange={(e) => setSeverityFilter(e.target.value)}
                      aria-label="Filter by severity"
                    >
                      <option value="all">All severities</option>
                      {severities.map((s) => (
                        <option key={s} value={s}>
                          {s}
                        </option>
                      ))}
                    </select>
                    <select
                      className="filter-select"
                      value={kindFilter}
                      disabled={streaming}
                      onChange={(e) => setKindFilter(e.target.value as KindFilter)}
                      aria-label="Filter by kind"
                    >
                      <option value="all">All kinds</option>
                      <option value="error">Errors</option>
                      <option value="performance">Performance</option>
                    </select>
                    <select
                      className="filter-select"
                      value={sortBy}
                      disabled={streaming}
                      onChange={(e) => setSortBy(e.target.value as SortBy)}
                      aria-label="Sort problems"
                    >
                      <option value="recent">Newest</option>
                      <option value="severity">Severity</option>
                      <option value="impact">Most affected</option>
                    </select>
                  </div>
                </div>

                {selectMode && (
                  <div className="bulk-bar">
                    <span>{selected.size} selected</span>
                    <button
                      className="link-btn"
                      onClick={() => handleDismiss([...selected])}
                      disabled={selected.size === 0 || streaming}
                    >
                      Dismiss selected
                    </button>
                    <button
                      className="link-btn"
                      onClick={() => handleDismiss(visible.map((p) => p.id))}
                      disabled={visible.length === 0 || streaming}
                    >
                      Clear all
                    </button>
                  </div>
                )}

                {visible.length === 0 ? (
                  <EmptyState
                    title="No matches"
                    message="No problems match your filters."
                    action={
                      <button className="ghost-btn" onClick={clearFilters} disabled={streaming}>
                        Clear filters
                      </button>
                    }
                  />
                ) : (
                  <ProblemList
                    problems={visible}
                    selectedId={selectedId}
                    onSelect={(pid) => navigate(`/problems/${encodeURIComponent(pid)}`)}
                    artifactMap={artifactMap}
                    showHidden={false}
                    selectMode={selectMode}
                    selected={selected}
                    onToggleSelect={toggleSelect}
                    onDismiss={(pid) => handleDismiss([pid])}
                    onRestore={(pid) => handleRestore([pid])}
                    onCancel={handleCancel}
                    halting={halting}
                    streaming={streaming}
                  />
                )}
              </>
            )}
          </aside>
        </div>

        <main className="content">
          {!selectedId && (
            <div className="detail-group">
              <EmptyState
                title="Select a problem"
                message="Pick a Dynatrace problem on the left to investigate its root cause."
              />
            </div>
          )}

          {/* Still resolving the list for a deep-linked id — don't flash "Unknown problem". */}
          {selectedId && !selectedProblem && !artifactMap[selectedId] && !result && problemsLoading && (
            <div className="detail-group">
              <Skeleton count={2} />
            </div>
          )}

          {/* Genuinely not in a loaded, non-empty list and nothing cached to show for it. */}
          {selectedId &&
            !selectedProblem &&
            !artifactMap[selectedId] &&
            !result &&
            !problemsLoading &&
            problems.length > 0 && (
              <div className="detail-group">
                <EmptyState
                  title="Unknown problem"
                  message={`No problem matches "${selectedId}".`}
                  action={
                    <button className="ghost-btn" onClick={() => navigate("/problems")}>
                      Back to list
                    </button>
                  }
                />
              </div>
            )}

          {/* Group 1: Header and Status */}
          {selectedId && (selectedProblem || artifactMap[selectedId] || run) && (
            <section className="detail-group detail-group-status">
              {selectedProblem && (
                <header className="detail-head" style={{ borderBottom: "none", marginBottom: "1rem", paddingBottom: 0 }}>
                  <div className="detail-head-top">
                    <span className={`badge sev-${selectedProblem.severity.toLowerCase()}`}>
                      {selectedProblem.severity}
                    </span>
                    {selectedProblem.kind === "performance" && (
                      <span className="mini-chip perf" title="Performance problem">⏱ perf</span>
                    )}
                    {selectedProblem.dynatraceUrl && (
                      <a
                        className="mini-chip link detail-dt-link"
                        href={selectedProblem.dynatraceUrl}
                        target="_blank"
                        rel="noreferrer"
                      >
                        Open in Dynatrace ↗
                      </a>
                    )}
                  </div>
                  <h2 className="detail-title" style={{ margin: "0.5rem 0 0" }}>{selectedProblem.title}</h2>
                  <p className="detail-meta">
                    {selectedProblem.entity} ·{" "}
                    {(selectedProblem.occurrences ?? selectedProblem.affectedUsers).toLocaleString()} occurrences
                    {selectedProblem.metric ? ` · ${selectedProblem.metric}` : ""}
                  </p>
                </header>
              )}

              {selectedId && (artifactMap[selectedId] || run || activity.length > 0) && (
                <StageTracker
                  artifact={artifactMap[selectedId]}
                  showMergeButton={
                    gitSource?.enabled &&
                    gitSource?.branchPerFix &&
                    !!gitSource?.workingBranch
                  }
                  onMerge={handleMerge}
                  merging={mergeBusy || autoActive || investigating}
                  demoAppUrl={demoAppUrl}
                  demoAppName={demoAppName}
                  steps={steps}
                  run={run}
                  autoActive={autoActive}
                  investigating={investigating}
                  logOpen={logOpen}
                  onToggleLog={setLogOpen}
                  haltingActive={selectedId ? halting.has(selectedId) : false}
                  onCancel={() => selectedId && handleCancel(selectedId)}
                  streaming={streaming}
                />
              )}

              {selectedId && selectedProblem && !result && !autoActive && (
                <div className="investigate-cta" style={{ margin: "1rem 0 0" }}>
                  <p style={{ margin: "0 0 1rem" }}>
                    {run
                      ? "Take over manually — investigate and propose a fix yourself."
                      : "The agent pulls this problem from Dynatrace, correlates the stack trace to source, and proposes a fix."}
                  </p>
                  <button className="investigate-btn" onClick={onInvestigate} disabled={investigating || streaming}>
                    {investigating ? "Investigating…" : run ? "Investigate manually" : "Investigate with AI"}
                  </button>
                </div>
              )}
            </section>
          )}

          {/* Group 2: The Problem */}
          {selectedId && result && (
            <section className="detail-group detail-group-problem">
              <h3 className="detail-group-title">The Problem</h3>
              
              <div className="problem-diagnostics">
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "1rem" }}>
                  <h4 style={{ margin: 0, fontSize: "0.95rem" }}>Diagnostics &amp; Root Cause</h4>
                  <span className="confidence" title="Agent confidence">
                    {Math.round(result.confidence * 100)}% confidence
                  </span>
                </div>

                {result.rootCause.summary && <p className="rc-summary" style={{ margin: "0 0 1rem" }}>{result.rootCause.summary}</p>}

                <dl className="rc-grid">
                  <dt>What</dt>
                  <dd>{result.rootCause.what}</dd>
                  <dt>Where</dt>
                  <dd>
                    <code>
                      {result.rootCause.where.file}:{result.rootCause.where.line}
                    </code>
                  </dd>
                  <dt>Why</dt>
                  <dd>{result.rootCause.why}</dd>
                  <dt>Impact</dt>
                  <dd>{result.rootCause.impact}</dd>
                </dl>

                {result.rootCause.details && (
                  <details className="collapsible-details further-reading" style={{ marginTop: "1rem" }}>
                    <summary>
                      <span>Further reading — technical detail</span>
                    </summary>
                    <div className="collapsible-details-content">
                      <p style={{ margin: 0, lineHeight: 1.55 }}>{result.rootCause.details}</p>
                    </div>
                  </details>
                )}

                {result.alternatives.length > 0 && (
                  <details className="collapsible-details alternatives-detail" style={{ marginTop: "1rem" }}>
                    <summary>
                      <span>Alternative hypotheses ({result.alternatives.length})</span>
                    </summary>
                    <div className="collapsible-details-content">
                      <ul style={{ margin: 0, paddingLeft: "1.2rem" }}>
                        {result.alternatives.map((a, i) => (
                          <li key={i} style={{ marginBottom: "0.4rem" }}>{a}</li>
                        ))}
                      </ul>
                    </div>
                  </details>
                )}
              </div>
            </section>
          )}

          {/* Group 3: The Solution */}
          {selectedId && result && (
            <section className="detail-group detail-group-solution">
              <h3 className="detail-group-title">The Solution</h3>
              <InvestigationPanel
                data={result}
                onApproved={reloadHistory}
                onReinvestigate={onInvestigate}
                reinvestigating={investigating}
                autoActive={autoActive}
              />
            </section>
          )}
        </main>
      </div>
    </>
  );
}
