import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import type { Investigation, Step } from "../types";
import { investigateStream } from "../api";
import { useAppData } from "../context/AppDataContext";
import { useAutopilot, isActivePhase } from "../context/AutopilotContext";
import { useToast } from "../context/ToastContext";
import { ProblemList } from "../components/ProblemList";
import { InvestigationPanel } from "../components/Investigation";
import { AgentSteps } from "../components/AgentSteps";
import { Pipeline } from "../components/Pipeline";
import { TestConsole } from "../components/TestConsole";
import { Skeleton, EmptyState, ErrorState } from "../components/States";

// Problems master-detail. The selected problem comes from the URL (/problems/:id),
// so investigations are deep-linkable. Investigation runs on demand (not auto) so a
// shared link doesn't spend an agent run unexpectedly.
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
  } = useAppData();
  const toast = useToast();
  const { runs, cancel } = useAutopilot();

  const [result, setResult] = useState<Investigation | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [investigating, setInvestigating] = useState(false);

  const selectedId = id ?? null;
  const run = selectedId ? runs[selectedId] : undefined;
  const autoActive = run ? isActivePhase(run.phase) : false;

  // Reset the detail view when the selected problem changes.
  useEffect(() => {
    setResult(null);
    setSteps([]);
  }, [selectedId]);

  async function onInvestigate() {
    if (!selectedId) return;
    setInvestigating(true);
    setStreaming(true);
    setSteps([]);
    setResult(null);
    try {
      const inv = await investigateStream(selectedId, (s) => setSteps((prev) => [...prev, s]));
      setResult(inv);
      reloadHistory();
      toast.success("Investigation complete");
    } catch (e) {
      toast.error(`Investigation failed: ${String(e)}`);
    } finally {
      setInvestigating(false);
      setStreaming(false);
    }
  }

  const selectedProblem = problems.find((p) => p.id === selectedId);

  return (
    <>
      <h2 className="page-title">Problems</h2>
      <p className="page-sub">
        Live error &amp; performance problems from Dynatrace — select one to investigate.
      </p>

      {consoleAvailable && <TestConsole onChange={refreshTestStatus} />}

      <div className="layout">
        <div className="problems-col">
          {problemsLoading ? (
            <aside className="problems">
              <h2>Dynatrace problems</h2>
              <Skeleton count={3} />
            </aside>
          ) : problemsError && problems.length === 0 ? (
            <aside className="problems">
              <h2>Dynatrace problems</h2>
              <ErrorState message="Couldn't reach the backend." onRetry={refreshProblems} />
            </aside>
          ) : problems.length === 0 ? (
            <aside className="problems">
              <h2>Dynatrace problems</h2>
              <EmptyState title="No open problems" message="Nothing to investigate right now." />
            </aside>
          ) : (
            <ProblemList
              problems={problems}
              selectedId={selectedId}
              onSelect={(pid) => navigate(`/problems/${encodeURIComponent(pid)}`)}
            />
          )}
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

          {selectedId &&
            (selectedProblem || problems.length === 0) &&
            !result &&
            !autoActive && (
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
            <>
              <InvestigationPanel data={result} onApproved={reloadHistory} />
              <Pipeline available={consoleAvailable} problemId={result.problemId} onComplete={reloadHistory} />
            </>
          )}
        </main>
      </div>
    </>
  );
}
