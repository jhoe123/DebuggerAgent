// Package api defines the JSON shapes exchanged with the React frontend.
// Field names (camelCase json tags) match frontend/src/types.ts.
package api

type Problem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Severity      string `json:"severity"`
	Status        string `json:"status"`
	AffectedUsers int    `json:"affectedUsers"`
	StartedAt     string `json:"startedAt"`
	Entity        string `json:"entity"`

	// Richer Dynatrace context.
	Occurrences       int    `json:"occurrences,omitempty"`
	GrailScannedBytes int64  `json:"grailScannedBytes,omitempty"`
	DynatraceURL      string `json:"dynatraceUrl,omitempty"`
}

type CodeLocation struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type RootCause struct {
	What   string       `json:"what"`
	Where  CodeLocation `json:"where"`
	Why    string       `json:"why"`
	Impact string       `json:"impact"`
}

type ProposedPatch struct {
	File        string `json:"file"`
	UnifiedDiff string `json:"unifiedDiff"`
	Rationale   string `json:"rationale"`
}

type Investigation struct {
	ProblemID     string        `json:"problemId"`
	RootCause     RootCause     `json:"rootCause"`
	Confidence    float64       `json:"confidence"`
	Alternatives  []string      `json:"alternatives"`
	ProposedPatch ProposedPatch `json:"proposedPatch"`
	SuggestedTest string        `json:"suggestedTest,omitempty"`
}

type ApproveResult struct {
	WrittenTo string `json:"writtenTo"`
}

// Step is a single milestone in a live stream (agent reasoning or a pipeline stage).
type Step struct {
	Stage   string `json:"stage"`            // investigate | apply | test | build | deploy | verify
	Status  string `json:"status"`           // running | ok | fail | info
	Message string `json:"message"`          // short human-readable line
	Detail  string `json:"detail,omitempty"` // optional logs/output
}

// PipelineResult is the terminal result of an auto-remediation run.
type PipelineResult struct {
	Steps   []Step `json:"steps"`
	Success bool   `json:"success"`
}

// TestStatus is the Test Console status snapshot (local only).
type TestStatus struct {
	SourceState  string `json:"sourceState"` // "buggy" | "modified"
	Reachable    bool   `json:"reachable"`   // demo_app responding on /healthz
	PendingPatch bool   `json:"pendingPatch"`
	DemoAppURL   string `json:"demoAppUrl"`
}

// TriggerResult reports the outcome of firing demo requests.
type TriggerResult struct {
	Sent  int   `json:"sent"`
	Codes []int `json:"codes"`
}

// AskResult is the answer to a natural-language follow-up question.
type AskResult struct {
	Answer string `json:"answer"`
}
