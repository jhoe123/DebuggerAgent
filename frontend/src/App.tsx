// App is the DebuggerAgent UI shell.
//
// Built out in T7: a problem list (GET /api/problems), an "Investigate" action that
// streams the agent's reasoning (POST /api/investigate, SSE), a root-cause panel, a
// diff viewer, and an "Approve" button (POST /api/approve-patch). For now this is a
// placeholder so the frontend scaffold builds and runs.
export function App() {
  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 880, margin: "3rem auto", padding: "0 1rem" }}>
      <h1>DebuggerAgent</h1>
      <p>AI Root-Cause Investigator — Dynatrace track.</p>
      <ol>
        <li>Select a Dynatrace problem</li>
        <li>Agent (Gemini 3) investigates and correlates it to source</li>
        <li>Review the root-cause summary + proposed patch</li>
        <li>Approve to write the patch to a branch/file (never auto-merged)</li>
      </ol>
      <p style={{ color: "#888" }}>UI under construction — see TASKS.md (T7).</p>
    </main>
  );
}
