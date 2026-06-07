import { useEffect, useState } from "react";
import type { Investigation, Problem, Step } from "./types";
import { investigateStream, listProblems, testStatus, wasMock } from "./api";
import { ProblemList } from "./components/ProblemList";
import { InvestigationPanel } from "./components/Investigation";
import { AgentSteps } from "./components/AgentSteps";
import { TestConsole } from "./components/TestConsole";
import { Pipeline } from "./components/Pipeline";
import { History } from "./components/History";

export function App() {
  const [problems, setProblems] = useState<Problem[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [result, setResult] = useState<Investigation | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [investigating, setInvestigating] = useState(false);
  const [mock, setMock] = useState(false);
  const [consoleAvailable, setConsoleAvailable] = useState(false);
  const [historyKey, setHistoryKey] = useState(0);
  const reloadHistory = () => setHistoryKey((k) => k + 1);

  async function refreshProblems() {
    const p = await listProblems();
    setProblems(p);
    setMock(wasMock());
  }

  useEffect(() => {
    refreshProblems();
    testStatus()
      .then(() => setConsoleAvailable(true))
      .catch(() => setConsoleAvailable(false));
  }, []);

  function onSelect(id: string) {
    setSelectedId(id);
    setResult(null);
    setSteps([]);
  }

  async function onInvestigate() {
    if (!selectedId) return;
    setInvestigating(true);
    setSteps([]);
    setResult(null);
    try {
      const inv = await investigateStream(selectedId, (s) => setSteps((prev) => [...prev, s]));
      setResult(inv);
      setMock(wasMock());
      reloadHistory();
    } finally {
      setInvestigating(false);
    }
  }

  return (
    <div className="app">
      <header className="topbar">
        <div>
          <h1>DebuggerAgent</h1>
          <p className="tagline">AI Root-Cause Investigator · Gemini + Dynatrace MCP</p>
        </div>
        {mock && <span className="mock-badge">demo data (mock) — backend not connected</span>}
      </header>

      {consoleAvailable && <TestConsole onChange={refreshProblems} />}

      <div className="layout">
        <ProblemList problems={problems} selectedId={selectedId} onSelect={onSelect} />

        <main className="content">
          {!selectedId && <p className="hint">Select a Dynatrace problem to investigate.</p>}

          {selectedId && !result && (
            <div className="investigate-cta">
              <p>
                Investigate <code>{selectedId}</code> — the agent will pull the problem from
                Dynatrace, correlate the stack trace to source, and propose a fix.
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
              <Pipeline
                available={consoleAvailable}
                problemId={result.problemId}
                onComplete={reloadHistory}
              />
            </>
          )}

          <History reloadKey={historyKey} />
        </main>
      </div>
    </div>
  );
}
