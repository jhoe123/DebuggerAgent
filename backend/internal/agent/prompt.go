package agent

import "strings"

// SplitProblemID parses a composite "<kind>:<service>" problem ID. IDs without a
// recognized prefix default to the error scenario (backward compatible).
func SplitProblemID(id string) (kind, svc string) {
	if k, s, ok := strings.Cut(id, ":"); ok && (k == "error" || k == "performance") {
		return k, s
	}
	return "error", id
}

// InvestigatePrompt builds the user prompt for an investigation, branching on the
// problem kind (error vs performance). Shared by the HTTP handler and the autopilot.
func InvestigatePrompt(problemID string) string {
	kind, svc := SplitProblemID(problemID)
	if kind == "performance" {
		return "Investigate the PERFORMANCE problem for the Dynatrace service \"" + svc +
			"\". Find the slowest operation: ONE execute_dql like `fetch spans, from:now()-30d | " +
			"filter service.name == \"" + svc + "\" and span.status_code != \"error\" | summarize " +
			"p95 = percentile(duration, 95), c = count(), by:{span.name} | sort p95 desc | limit 1` " +
			"(duration is in nanoseconds). Read the source for that operation, find the code that makes " +
			"it slow, call propose_patch with an optimization (change ONLY that function; keep the rest of " +
			"the file byte-identical), then return the final JSON object. rootCause.what should name the " +
			"slow operation and the cause; suggestedTest should assert the latency is now under budget."
	}
	return "Investigate the production errors for the Dynatrace service \"" + svc +
		"\". Query its recent error spans, read the offending source file, determine the root cause, " +
		"call propose_patch with a fix, then return the final JSON object."
}
