import { useEffect, useState } from "react";
import type { Investigation, Problem } from "./types";
import { investigate, listProblems, wasMock } from "./api";
import { ProblemList } from "./components/ProblemList";
import { InvestigationPanel } from "./components/Investigation";

export function App() {
  const [problems, setProblems] = useState<Problem[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [result, setResult] = useState<Investigation | null>(null);
  const [investigating, setInvestigating] = useState(false);
  const [mock, setMock] = useState(false);

  useEffect(() => {
    listProblems().then((p) => {
      setProblems(p);
      setMock(wasMock());
    });
  }, []);

  function onSelect(id: string) {
    setSelectedId(id);
    setResult(null);
  }

  async function onInvestigate() {
    if (!selectedId) return;
    setInvestigating(true);
    try {
      setResult(await investigate(selectedId));
      setMock(wasMock());
    } finally {
      setInvestigating(false);
    }
  }

  return (
    <div className="app">
      <header className="topbar">
        <div>
          <h1>DebuggerAgent</h1>
          <p className="tagline">AI Root-Cause Investigator · Gemini 3 + Dynatrace</p>
        </div>
        {mock && <span className="mock-badge">demo data (mock) — backend not connected</span>}
      </header>

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

          {result && <InvestigationPanel data={result} />}
        </main>
      </div>
    </div>
  );
}
