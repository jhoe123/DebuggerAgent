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

	// Classification for performance-vs-error problems.
	Kind   string `json:"kind,omitempty"`   // "error" | "performance"
	Metric string `json:"metric,omitempty"` // e.g. "p95 612 ms" (performance only)
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

	// Layered detail: a plain-language TL;DR anyone can grasp, plus an optional
	// deeper technical explanation for further reading. Both optional/back-compat.
	Summary string `json:"summary,omitempty"`
	Details string `json:"details,omitempty"`
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

// InstrumentationCandidate is one place the agent recommends adding Dynatrace /
// OpenTelemetry telemetry. Kind is one of:
// otel-bootstrap | tracer-init | span | record-error | attributes | metric.
// UnifiedDiff is a small display hunk only — the FULL patched file content is
// generated at apply time (keeps a scan's payload lean for long candidate lists).
type InstrumentationCandidate struct {
	ID          string `json:"id"`                    // stable id assigned by the backend
	File        string `json:"file"`                  // path relative to the source root
	Symbol      string `json:"symbol,omitempty"`      // function/handler the edit targets
	StartLine   int    `json:"startLine"`             // 1-based anchor line
	EndLine     int    `json:"endLine,omitempty"`     // inclusive end of the affected region
	Kind        string `json:"kind"`                  // see above
	Rationale   string `json:"rationale"`             // why this telemetry matters
	Snippet     string `json:"snippet,omitempty"`     // a few lines of context where it lands
	UnifiedDiff string `json:"unifiedDiff,omitempty"` // small display hunk
}

// InstrumentationScan is the result of a (read-only) instrumentation review.
// Truncated flags that the scan was capped (long candidate-list management).
type InstrumentationScan struct {
	Root       string                     `json:"root"`
	Summary    string                     `json:"summary"`
	Candidates []InstrumentationCandidate `json:"candidates"`
	Truncated  bool                       `json:"truncated,omitempty"`
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
	Steps   []Step   `json:"steps"`
	Success bool     `json:"success"`
	Files   []string `json:"files,omitempty"`  // source files the pipeline touched
	Verify  string   `json:"verify,omitempty"` // before→after, e.g. "500 -> 400"
}

// HistoryEntry is one audited change: a proposed patch, an approval, or a
// pipeline run. Surfaced read-only via GET /api/history (hosted-safe).
type HistoryEntry struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // "proposed" | "approved" | "pipeline" | "scan"
	ProblemID string   `json:"problemId,omitempty"`
	Files     []string `json:"files"`               // affected file paths
	Summary   string   `json:"summary"`             // short human-readable line
	Status    string   `json:"status"`              // "proposed" | "written" | "success" | "failed"
	CreatedAt string   `json:"createdAt"`           // RFC3339
	Diff      string   `json:"diff,omitempty"`      // proposed/approved
	Steps     []Step   `json:"steps,omitempty"`     // pipeline
	WrittenTo string   `json:"writtenTo,omitempty"` // approved
	Verify    string   `json:"verify,omitempty"`    // pipeline, e.g. "500 -> 400"
}

// HistoryResponse is the GET /api/history payload (newest first).
type HistoryResponse struct {
	Entries []HistoryEntry `json:"entries"`
}

// AutopilotStages selects which pipeline stages the autopilot runs after a fix is
// proposed (mirrors democtl.Options without the scenario field).
type AutopilotStages struct {
	Apply  bool `json:"apply"`
	Test   bool `json:"test"`
	Build  bool `json:"build"`
	Deploy bool `json:"deploy"`
}

// AutopilotConfig is the live auto-patch configuration (backend is source of truth).
type AutopilotConfig struct {
	Enabled bool            `json:"enabled"`
	Stages  AutopilotStages `json:"stages"`
}

// AutopilotRun is the current automation state for one problem.
// Phase: queued | investigating | proposed | remediating | deployed | failed | halted.
type AutopilotRun struct {
	ProblemID string `json:"problemId"`
	Title     string `json:"title,omitempty"`
	Kind      string `json:"kind,omitempty"` // "error" | "performance"
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"` // latest step, e.g. "testing"
	Steps     []Step `json:"steps,omitempty"`
	Success   *bool  `json:"success,omitempty"` // set on terminal pipeline runs
	UpdatedAt string `json:"updatedAt"`         // RFC3339
}

// AutopilotSnapshot is the GET /api/autopilot payload (runs newest-first).
type AutopilotSnapshot struct {
	Config    AutopilotConfig `json:"config"`
	Runs      []AutopilotRun  `json:"runs"`
	LocalMode bool            `json:"localMode"` // true when apply/build/deploy is available
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

// SlackConfig sets the Slack notifier at runtime (POST /api/slack/config).
// WebhookURL is a secret; an empty value leaves the existing webhook unchanged
// (so toggling Enabled doesn't wipe a configured webhook).
type SlackConfig struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhookUrl,omitempty"`
}

// SlackStatus is the GET /api/slack payload. It never returns the raw webhook —
// only whether one is configured plus a masked preview.
type SlackStatus struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`        // a webhook is set
	Preview    string `json:"preview,omitempty"` // masked webhook for display
}

// PipelineSettings is the runtime-configurable test/build/deploy configuration shown in
// Settings. The backend is the source of truth (seeded from env), so the server-side
// reachability check can read the configured health URL. Mode is read-only (env-controlled).
type PipelineSettings struct {
	Mode          string            `json:"mode"`          // PIPELINE_MODE: "local" | "cloudbuild" (read-only)
	TestStrategy  string            `json:"testStrategy"`  // auto | reuse | generate | skip
	BuildStrategy string            `json:"buildStrategy"` // auto | script | default
	DeployTarget  string            `json:"deployTarget"`  // local | docker | script | cloud-run
	DeployParams  map[string]string `json:"deployParams"`  // image/tag/hostPort · project/region/service/sourceBucket/artifactRepo · scriptPath
	HealthURL     string            `json:"healthUrl"`     // reachability check URL (full URL or path; defaults to the demo app URL)
}
