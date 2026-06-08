import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { Investigation, Step } from "../types";
import { confirmFix, investigateStream, stagePatch, unstagePatch } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot, isActivePhase } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import { useLocalStore } from "../context/LocalStoreContext";
import { ProblemList } from "../components/ProblemList";
import { InvestigationPanel } from "../components/Investigation";
import { AgentSteps } from "../components/AgentSteps";
import { BatchPanel } from "../components/BatchPanel";
import { StageTracker } from "../components/StageTracker";
import { TestConsole } from "../components/TestConsole";
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
    consoleAvailable,
    reloadHistory,
    refreshTestStatus,
    setStreaming,
    artifactMap,
    staged,
    refreshPatches,
    refreshArtifacts,
    gitSource,
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
  const [mergeBusy, setMergeBusy] = useState(false);

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

  // Batch membership: the checkbox stages/unstages (default mode). Only investigated
  // problems (those with an artifact) can be staged.
  const stagedIds = useMemo(() => new Set(staged.map((s) => s.problemId)), [staged]);
  const canStage = (pid: string) => !!artifactMap[pid];
  async function onToggleBatch(pid: string) {
    try {
      if (stagedIds.has(pid)) await unstagePatch(pid);
      else await stagePatch(pid);
      await refreshPatches();
      await refreshArtifacts();
    } catch (e) {
      toast.error(`Couldn't update batch: ${String(e)}`);
    }
  }

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
    return [...list].sort((a, b) => {
      if (sortBy === "impact") return (b.affectedUsers ?? 0) - (a.affectedUsers ?? 0);
      if (sortBy === "severity") {
        const r = (SEV_RANK[a.severity] ?? 9) - (SEV_RANK[b.severity] ?? 9);
        if (r !== 0) return r;
      }
      return new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime();
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

  return (
    <>
      <h2 className="page-title">Problems</h2>
      <p className="page-sub">
        Live error &amp; performance problems from Dynatrace — select one to investigate.
      </p>

      {/* Consolidated patches + one-click deploy (collapsed bar; hidden until staged). */}
      <BatchPanel />

      {consoleAvailable && <TestConsole onChange={refreshTestStatus} />}

      <div className="layout">
        <div className="problems-col">
          <aside className="problems">
            <div className="problems-head">
              <h2>Dynatrace problems</h2>
              {!problemsLoading && !listError && (
                <button
                  className={`ghost-btn hidden-toggle${showHidden ? " active" : ""}`}
                  onClick={() => {
                    setShowHidden((s) => !s);
                    setSelected(new Set());
                  }}
                  disabled={!showHidden && hiddenProblems.length === 0}
                  title="Show dismissed problems"
                >
                  {showHidden ? "← Active" : `Hidden (${hiddenProblems.length})`}
                </button>
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
                    stagedIds={stagedIds}
                    canStage={canStage}
                    onToggleBatch={onToggleBatch}
                    selected={selected}
                    onToggleSelect={toggleSelect}
                    onDismiss={(pid) => handleDismiss([pid])}
                    onRestore={(pid) => handleRestore([pid])}
                  />
                </>
              )
            ) : activeProblems.length === 0 ? (
              <EmptyState title="No open problems" message="Nothing to investigate right now." />
            ) : (
              <>
                <div className="problems-toolbar">
                  <input
                    className="filter-input"
                    type="search"
                    placeholder="Search problems…"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                  />
                  <select
                    className="filter-select"
                    value={severityFilter}
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
                    onChange={(e) => setSortBy(e.target.value as SortBy)}
                    aria-label="Sort problems"
                  >
                    <option value="recent">Newest</option>
                    <option value="severity">Severity</option>
                    <option value="impact">Most affected</option>
                  </select>
                  <button
                    className={`ghost-btn select-toggle clear-all${selectMode ? " active" : ""}`}
                    onClick={() => {
                      setSelectMode((s) => !s);
                      setSelected(new Set());
                    }}
                    title="Select multiple problems to dismiss"
                  >
                    {selectMode ? "Done" : "Select"}
                  </button>
                </div>

                {selectMode && (
                  <div className="bulk-bar">
                    <span>{selected.size} selected</span>
                    <button
                      className="link-btn"
                      onClick={() => handleDismiss([...selected])}
                      disabled={selected.size === 0}
                    >
                      Dismiss selected
                    </button>
                    <button
                      className="link-btn"
                      onClick={() => handleDismiss(visible.map((p) => p.id))}
                      disabled={visible.length === 0}
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
                      <button className="ghost-btn" onClick={clearFilters}>
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
                    stagedIds={stagedIds}
                    canStage={canStage}
                    onToggleBatch={onToggleBatch}
                    selected={selected}
                    onToggleSelect={toggleSelect}
                    onDismiss={(pid) => handleDismiss([pid])}
                    onRestore={(pid) => handleRestore([pid])}
                  />
                )}
              </>
            )}
          </aside>
        </div>

        <main className="content">
          {!selectedId && (
            <EmptyState
              title="Select a problem"
              message="Pick a Dynatrace problem on the left to investigate its root cause."
            />
          )}

          {selectedId && !selectedProblem && problems.length > 0 && (
            <EmptyState
              title="Unknown problem"
              message={`No problem matches "${selectedId}".`}
              action={
                <button className="ghost-btn" onClick={() => navigate("/problems")}>
                  Back to list
                </button>
              }
            />
          )}

          {selectedId && artifactMap[selectedId] && (
            <StageTracker
              artifact={artifactMap[selectedId]}
              showMergeButton={
                gitSource?.enabled &&
                gitSource?.branchPerFix &&
                !!gitSource?.workingBranch
              }
              onMerge={handleMerge}
              merging={mergeBusy}
            />
          )}

          {selectedId && run && (
            <section className={`autopilot-panel ap-${run.phase}`}>
              <div className="autopilot-panel-head">
                <h3>
                  Autopilot {autoActive ? "is handling this" : run.phase === "halted" ? "halted" : "result"}
                </h3>
                {autoActive && (
                  <button className="halt-btn" onClick={() => selectedId && cancel(selectedId)}>
                    Halt &amp; take over
                  </button>
                )}
              </div>
              <p className="muted">{run.message || run.phase}</p>
              {run.steps && run.steps.length > 0 && <AgentSteps steps={run.steps} />}
            </section>
          )}

          {selectedId && (selectedProblem || problems.length === 0) && !result && !autoActive && (
            <div className="investigate-cta">
              <p>
                {run
                  ? "Take over manually — investigate and propose a fix yourself."
                  : "Investigate "}
                {!run && <code>{selectedId}</code>}
                {!run &&
                  " — the agent pulls the problem from Dynatrace, correlates the stack trace to source, and proposes a fix."}
              </p>
              <button className="investigate-btn" onClick={onInvestigate} disabled={investigating}>
                {investigating ? "Investigating…" : "Investigate with AI"}
              </button>
            </div>
          )}

          {steps.length > 0 && <AgentSteps steps={steps} title="Agent activity" />}

          {result && (
            <InvestigationPanel
              data={result}
              onApproved={reloadHistory}
              onReinvestigate={onInvestigate}
              reinvestigating={investigating}
            />
          )}
        </main>
      </div>
    </>
  );
}
