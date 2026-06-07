// Mock data used until the backend (T6) is live. It mirrors the seeded bug in
// demo_app/main.go (/checkout index-out-of-range) so the demo is coherent.
import type { Investigation, Problem } from "./types";

export const mockProblems: Problem[] = [
  {
    id: "P-240608-001",
    title: "Unhandled panic in checkout service (index out of range)",
    severity: "ERROR",
    status: "OPEN",
    affectedUsers: 1284,
    startedAt: new Date(Date.now() - 18 * 60 * 1000).toISOString(),
    entity: "demo_app / checkout",
  },
  {
    id: "P-240608-002",
    title: "Elevated response time on /checkout",
    severity: "RESOURCE",
    status: "OPEN",
    affectedUsers: 312,
    startedAt: new Date(Date.now() - 42 * 60 * 1000).toISOString(),
    entity: "demo_app / checkout",
  },
];

export const mockInvestigation: Investigation = {
  problemId: "P-240608-001",
  rootCause: {
    what: "A panic (runtime error: index out of range) crashes the /checkout request handler.",
    where: { file: "demo_app/main.go", line: 36 },
    why: "checkoutHandler indexes the items slice with an unvalidated `index` query parameter. Any value >= len(items) (or negative) triggers an out-of-range panic instead of a 400 response.",
    impact: "All /checkout calls with an out-of-range index fail; correlated logs show recurring panics affecting ~1.3k users since the spike began.",
  },
  confidence: 0.92,
  alternatives: [
    "Upstream caller sending malformed index values — mitigates symptom but not the missing server-side validation.",
    "Slice mutated concurrently elsewhere — not supported by the trace; items is a local literal.",
  ],
  proposedPatch: {
    file: "demo_app/main.go",
    rationale:
      "Validate the parsed index against the slice bounds and return HTTP 400 for invalid input instead of panicking.",
    unifiedDiff: `--- a/demo_app/main.go
+++ b/demo_app/main.go
@@ func checkoutHandler(w http.ResponseWriter, r *http.Request) {
 	items := []string{"apple", "banana", "cherry"}
 	idx := parseIndex(r.URL.Query().Get("index"))
-	selected := items[idx]
+	if idx < 0 || idx >= len(items) {
+		http.Error(w, "invalid item index", http.StatusBadRequest)
+		return
+	}
+	selected := items[idx]
 	fmt.Fprintf(w, "checked out: %s\\n", selected)`,
  },
  suggestedTest:
    "Add a handler test asserting GET /checkout?index=99 returns 400 (not a panic) and index=1 returns 200 with \"banana\".",
};
